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

// cachedAuth holds a validated token's identity plus the deadline at
// which it must be re-checked with Google.
type cachedAuth struct {
	result  *Result
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

	cacheMu sync.Mutex
	cache   map[string]cachedAuth
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
		cache:  map[string]cachedAuth{},
	}
	return auth, err
}

// Name returns the human-readable auth method name surfaced in audit logs.
func (g *GoogleAuth) Name() (name string) {
	name = "google-oauth"
	return name
}

// Authenticate validates the request's bearer token against Google's
// userinfo endpoint and applies the configured hd-domain / email allowlists.
func (g *GoogleAuth) Authenticate(r *http.Request) (result *Result, err error) {
	var token string
	token, err = extractBearerToken(r)
	if err != nil {
		return result, err
	}

	// Cache hit short-circuit: same token, still inside TTL.
	cacheKey := hashToken(token)
	cached, found := g.cacheGet(cacheKey)
	if found {
		result = cached
		return result, err
	}

	var info googleUserInfo
	info, err = g.callUserInfo(r.Context(), token)
	if err != nil {
		return result, err
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
	g.cachePut(cacheKey, result)
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
// Empty allowlist means "no restriction" for that axis.
func (g *GoogleAuth) enforceAllowLists(info googleUserInfo) (err error) {
	if len(g.config.AllowedHostedDomains) > 0 {
		if !slices.Contains(g.config.AllowedHostedDomains, info.HostedDomain) {
			err = fmt.Errorf("hosted domain %q is not in the allowed list", info.HostedDomain)
			return err
		}
	}
	if len(g.config.AllowedEmails) > 0 {
		if !slices.Contains(g.config.AllowedEmails, info.Email) {
			err = fmt.Errorf("email %q is not in the allowed list", info.Email)
			return err
		}
	}
	return err
}

// cacheGet returns a cached Result if the entry exists and hasn't expired.
func (g *GoogleAuth) cacheGet(key string) (result *Result, found bool) {
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	entry, ok := g.cache[key]
	if !ok {
		return result, found
	}
	if time.Now().After(entry.expires) {
		delete(g.cache, key)
		return result, found
	}
	result = entry.result
	found = true
	return result, found
}

// cachePut stores a Result against a hashed token key with a TTL deadline.
func (g *GoogleAuth) cachePut(key string, result *Result) {
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	g.cache[key] = cachedAuth{
		result:  result,
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
