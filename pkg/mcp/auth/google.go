package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"
)

// GoogleUserInfoURL is Google's standard OIDC userinfo endpoint. Tests
// override this via GoogleConfig.UserInfoURL.
const GoogleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

// googleCacheTTLDefault is the upper bound on how long a validated token
// is trusted without re-checking with Google. Google access tokens typically
// expire after one hour; this default trades freshness for fewer userinfo
// calls. Token expiry beyond this is enforced by Google returning 401 on
// the next userinfo call.
const googleCacheTTLDefault = 5 * time.Minute

// GoogleConfig configures Google OAuth/OIDC validation against Google's
// userinfo endpoint. The bot is a resource server: it validates tokens
// that clients (Claude Code) acquired from Google directly via the OAuth
// flow. The bot does not host a callback URI and does not need a client
// secret.
type GoogleConfig struct {
	// ClientID is the Google OAuth client ID this server expects tokens
	// to have been issued for. Required.
	ClientID string

	// AllowedHostedDomains is the Workspace-domain filter applied to the
	// `hd` claim returned by userinfo. Empty means no domain restriction
	// (any Google account allowed) — useful for development but unlikely
	// to be the right setting in production.
	AllowedHostedDomains []string

	// AllowedEmails is an explicit per-user allowlist applied on top of
	// the hosted-domain filter. Empty means no per-email restriction.
	AllowedEmails []string

	// Allowed*File fields, when non-empty, point at a file mounted from
	// a Kubernetes ConfigMap whose contents replace the matching static
	// slice. Same semantics as the OIDC equivalents (see OIDCConfig):
	// stat-on-every-call + mtime cache + fail-closed on unreadable +
	// empty-file = no restriction + file wins over static when both
	// are set. File contents go through splitList, so a YAML `|-` block
	// scalar mounted from a ConfigMap parses identically to a
	// comma-separated env var.
	//
	// Hot-reload only works correctly because Authenticate now applies
	// the allowlist on every request (against either a cached or fresh
	// userinfo identity) instead of caching the final authorized result.
	// A user revoked from the file is denied on their next request, not
	// after the userinfo cache TTL elapses.
	AllowedHostedDomainsFile string
	AllowedEmailsFile        string

	// UserInfoURL is the endpoint hit to validate tokens. Defaults to
	// Google's standard endpoint; tests override.
	UserInfoURL string

	// CacheTTL bounds how long a validated token is trusted without
	// re-checking with Google. Defaults to googleCacheTTLDefault.
	CacheTTL time.Duration

	// HTTPClient is the client used for userinfo calls. Defaults to a
	// timeout-bounded client with an isolated transport (see
	// pkg/mcp/tempo.go for the rationale on per-client transports).
	HTTPClient *http.Client
}

// googleUserInfo is the slice of Google's userinfo response we read.
// Other fields (name, picture, etc.) are ignored.
type googleUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HostedDomain  string `json:"hd"`
	Name          string `json:"name"`
}

// cachedIdentity holds the Google userinfo response for a validated
// token plus the deadline at which the token must be re-checked. We
// cache the identity (the expensive thing: a network roundtrip to
// Google) and re-run the allowlist check on every Authenticate, so a
// user removed from a file-backed allowlist is denied on their next
// request rather than waiting out the userinfo cache TTL.
type cachedIdentity struct {
	info    googleUserInfo
	expires time.Time
}

// GoogleAuth implements Method against Google's OIDC userinfo endpoint.
// Validation model is "ask Google who this token belongs to" — Claude
// Code presents opaque OAuth access tokens, not JWT ID tokens, so JWT
// signature verification doesn't apply on this path. Existing OIDCAuth
// (JWT-vs-JWKS) is preserved for any non-Google IdP that issues ID-token
// bearers; the two paths coexist.
type GoogleAuth struct {
	config GoogleConfig
	logger *slog.Logger

	// cache memoizes Google userinfo lookups by token hash. The cached
	// value is the identity (subject, email, hd) — not an authorized
	// Result — so allowlist enforcement runs on every Authenticate.
	cacheMu sync.Mutex
	cache   map[string]cachedIdentity

	// listSource is the file-backed allowlist cache shared with
	// OIDCAuth. Embedded so its `current` method is callable directly
	// and counters surface as promoted fields for tests. See
	// listsource.go.
	listSource
}

// NewGoogleAuth validates the config and returns a ready Method.
func NewGoogleAuth(config GoogleConfig, logger *slog.Logger) (auth *GoogleAuth, err error) {
	if config.ClientID == "" {
		err = errors.New("GoogleConfig.ClientID is required")
		return auth, err
	}

	if config.UserInfoURL == "" {
		config.UserInfoURL = GoogleUserInfoURL
	}
	if config.CacheTTL == 0 {
		config.CacheTTL = googleCacheTTLDefault
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{
			Timeout: 10 * time.Second,
			// Per-client Transport — see pkg/mcp/tempo.go.
			Transport: &http.Transport{},
		}
	}

	auth = &GoogleAuth{
		config: config,
		logger: logger,
		cache:  map[string]cachedIdentity{},
	}
	auth.listCache = map[string]listCacheEntry{}
	return auth, err
}

// Name returns the human-readable auth method name surfaced in audit logs.
func (g *GoogleAuth) Name() (name string) {
	name = "google-oauth"
	return name
}

// Authenticate validates the request's bearer token against Google's
// userinfo endpoint and applies the configured hd-domain / email
// allowlists.
//
// Cache shape: we memoize the userinfo identity (subject, email, hd)
// keyed by token hash, not the authorized Result. Allowlist enforcement
// then runs on every call against either the cached identity or a
// freshly-fetched one. This is what makes file-backed allowlists hot-
// reloadable on the Google path — a user removed from the file is
// denied on their very next request, not after the 5-minute userinfo
// cache expires.
func (g *GoogleAuth) Authenticate(r *http.Request) (result *Result, err error) {
	var token string
	token, err = extractBearerToken(r)
	if err != nil {
		return result, err
	}

	cacheKey := hashToken(token)
	info, hit := g.cacheGetIdentity(cacheKey)
	if !hit {
		info, err = g.callUserInfo(r.Context(), token)
		if err != nil {
			return result, err
		}
		g.cachePutIdentity(cacheKey, info)
	}

	err = g.enforceAllowLists(info)
	if err != nil {
		return result, err
	}

	result = &Result{
		Authenticated: true,
		Method:        g.Name(),
		Username:      info.Email,
		Email:         info.Email,
		Subject:       info.Sub,
	}
	return result, err
}

// callUserInfo hits Google's userinfo endpoint with the supplied token.
// A non-200 response is treated as authentication failure — most commonly
// 401 for expired/revoked tokens.
func (g *GoogleAuth) callUserInfo(ctx context.Context, token string) (info googleUserInfo, err error) {
	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, g.config.UserInfoURL, nil)
	if err != nil {
		err = fmt.Errorf("building userinfo request: %w", err)
		return info, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	var resp *http.Response
	resp, err = g.config.HTTPClient.Do(req)
	if err != nil {
		err = fmt.Errorf("calling Google userinfo: %w", err)
		return info, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("google userinfo returned status %d", resp.StatusCode)
		return info, err
	}

	var body []byte
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("reading userinfo body: %w", err)
		return info, err
	}

	err = json.Unmarshal(body, &info)
	if err != nil {
		err = fmt.Errorf("parsing userinfo body: %w", err)
		return info, err
	}
	return info, err
}

// enforceAllowLists applies the hosted-domain and per-email allowlists.
// Empty allowlist means "no restriction" for that axis. File-backed
// allowlists are resolved per-call via the embedded listSource, so a
// ConfigMap edit takes effect on the next request rather than after
// the userinfo cache TTL.
func (g *GoogleAuth) enforceAllowLists(info googleUserInfo) (err error) {
	domains, domainsErr := g.current(g.config.AllowedHostedDomainsFile, g.config.AllowedHostedDomains)
	if domainsErr != nil {
		g.logger.Error("Google allowed-hosted-domains file unreadable; failing closed",
			slog.String("file", g.config.AllowedHostedDomainsFile),
			slog.String("err", domainsErr.Error()))
		err = fmt.Errorf("allowed-hosted-domains file unreadable: %w", domainsErr)
		return err
	}
	if len(domains) > 0 {
		if !slices.Contains(domains, info.HostedDomain) {
			err = fmt.Errorf("hosted domain %q is not in the allowed list", info.HostedDomain)
			return err
		}
	}

	emails, emailsErr := g.current(g.config.AllowedEmailsFile, g.config.AllowedEmails)
	if emailsErr != nil {
		g.logger.Error("Google allowed-emails file unreadable; failing closed",
			slog.String("file", g.config.AllowedEmailsFile),
			slog.String("err", emailsErr.Error()))
		err = fmt.Errorf("allowed-emails file unreadable: %w", emailsErr)
		return err
	}
	if len(emails) > 0 {
		if !slices.Contains(emails, info.Email) {
			err = fmt.Errorf("email %q is not in the allowed list", info.Email)
			return err
		}
	}
	return err
}

// cacheGetIdentity returns a cached userinfo identity if the entry
// exists and hasn't expired.
func (g *GoogleAuth) cacheGetIdentity(key string) (info googleUserInfo, found bool) {
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	entry, ok := g.cache[key]
	if !ok {
		return info, found
	}
	if time.Now().After(entry.expires) {
		delete(g.cache, key)
		return info, found
	}
	info = entry.info
	found = true
	return info, found
}

// cachePutIdentity stores a userinfo identity against a hashed token
// key with a TTL deadline.
func (g *GoogleAuth) cachePutIdentity(key string, info googleUserInfo) {
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	g.cache[key] = cachedIdentity{
		info:    info,
		expires: time.Now().Add(g.config.CacheTTL),
	}
}

// hashToken returns a fixed-length identifier for a bearer token. Keying
// the cache by hash rather than the raw token bounds the time-in-memory
// of a real credential — defense-in-depth against memory dumps.
func hashToken(token string) (h string) {
	sum := sha256.Sum256([]byte(token))
	h = hex.EncodeToString(sum[:])
	return h
}
