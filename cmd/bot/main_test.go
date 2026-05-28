package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSelectAuthProviderNoneConfigured verifies the no-auth default —
// current production behavior is preserved unless an env var is set.
func TestSelectAuthProviderNoneConfigured(t *testing.T) {
	t.Parallel()
	provider, err := selectAuthProvider("", "")
	require.NoError(t, err)
	require.Equal(t, authProviderNone, provider)
}

// TestSelectAuthProviderOIDCOnly verifies the OIDC path is selected when
// only MCP_OIDC_ISSUER is set.
func TestSelectAuthProviderOIDCOnly(t *testing.T) {
	t.Parallel()
	provider, err := selectAuthProvider("https://dex.example.com", "")
	require.NoError(t, err)
	require.Equal(t, authProviderOIDC, provider)
}

// TestSelectAuthProviderGoogleOnly verifies the Google path is selected
// when only GOOGLE_OAUTH_CLIENT_ID is set.
func TestSelectAuthProviderGoogleOnly(t *testing.T) {
	t.Parallel()
	provider, err := selectAuthProvider("", "client-id.apps.googleusercontent.com")
	require.NoError(t, err)
	require.Equal(t, authProviderGoogle, provider)
}

// TestSelectAuthProviderBothSetIsError verifies the spec invariant: pick
// exactly one. The caller (main) turns this error into os.Exit.
func TestSelectAuthProviderBothSetIsError(t *testing.T) {
	t.Parallel()
	_, err := selectAuthProvider("https://dex.example.com", "client-id.apps.googleusercontent.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "MCP_OIDC_ISSUER")
	require.Contains(t, err.Error(), "GOOGLE_OAUTH_CLIENT_ID")
}

// TestOAuthMetadataConfigForOIDCAdvertisesIssuerAndGroupsScope verifies
// the OIDC path's PRM document: authorization server is the Dex issuer,
// scopes include "groups" (required for Dex groups-claim emission).
func TestOAuthMetadataConfigForOIDCAdvertisesIssuerAndGroupsScope(t *testing.T) {
	t.Parallel()
	authServer, scopes, ok := oauthMetadataConfig(authProviderOIDC, "https://dex.tools.nxteam.dev", "")
	require.True(t, ok)
	require.Equal(t, "https://dex.tools.nxteam.dev", authServer)
	require.Contains(t, scopes, "groups")
	require.Contains(t, scopes, "openid")
}

// TestOAuthMetadataConfigForGoogleAdvertisesGoogleAndNoGroupsScope
// verifies the Google path still advertises accounts.google.com and the
// original scope set — no "groups" because Google ID tokens don't carry
// group membership.
func TestOAuthMetadataConfigForGoogleAdvertisesGoogleAndNoGroupsScope(t *testing.T) {
	t.Parallel()
	authServer, scopes, ok := oauthMetadataConfig(authProviderGoogle, "", "client-id.apps.googleusercontent.com")
	require.True(t, ok)
	require.Equal(t, "https://accounts.google.com", authServer)
	require.NotContains(t, scopes, "groups")
	require.Contains(t, scopes, "openid")
	require.Contains(t, scopes, "email")
}

// TestOAuthMetadataConfigNoAuthIsNotOK verifies that with no auth selected,
// no metadata route should be registered — the helper says so.
func TestOAuthMetadataConfigNoAuthIsNotOK(t *testing.T) {
	t.Parallel()
	_, _, ok := oauthMetadataConfig(authProviderNone, "", "")
	require.False(t, ok)
}

// TestMaybeWrapOIDCAuthPassesThroughWhenIssuerUnset verifies the no-op path:
// no MCP_OIDC_ISSUER → handlers returned exactly as passed in. Current
// no-auth behavior preserved by default.
func TestMaybeWrapOIDCAuthPassesThroughWhenIssuerUnset(t *testing.T) {
	t.Setenv("MCP_OIDC_ISSUER", "")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	original := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	mcpH, sseH, err := buildOIDCHandlers(context.Background(), original, original, logger)
	require.NoError(t, err)

	for _, h := range []http.Handler{mcpH, sseH} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
		require.Equal(t, http.StatusTeapot, rec.Code, "no OIDC config → original handler must run unwrapped")
	}
}

// TestMaybeWrapOIDCAuthErrorsWhenAudienceMissing verifies the spec's
// audience-binding requirement: refuse to run a JWT validator without
// an audience claim to check against.
func TestMaybeWrapOIDCAuthErrorsWhenAudienceMissing(t *testing.T) {
	t.Setenv("MCP_OIDC_ISSUER", "https://dex.example.com")
	t.Setenv("MCP_OIDC_AUDIENCE", "")
	t.Setenv("MCP_PUBLIC_URL", "https://bot.example.com")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	_, _, err := buildOIDCHandlers(context.Background(), noop, noop, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "MCP_OIDC_AUDIENCE")
}

// TestMaybeWrapOIDCAuthErrorsWhenPublicURLMissing verifies the spec's
// other config-shape requirement: the resource_metadata URL must be
// constructable, otherwise Claude Code can't discover the auth server.
func TestMaybeWrapOIDCAuthErrorsWhenPublicURLMissing(t *testing.T) {
	t.Setenv("MCP_OIDC_ISSUER", "https://dex.example.com")
	t.Setenv("MCP_OIDC_AUDIENCE", "dex-client-id")
	t.Setenv("MCP_PUBLIC_URL", "")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	_, _, err := buildOIDCHandlers(context.Background(), noop, noop, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "MCP_PUBLIC_URL")
}

// TestMaybeWrapOIDCAuthWrapsWhenFullyConfigured verifies the wrapping
// happens end-to-end: with full config, a request to the wrapped handler
// without a bearer token gets 401 + WWW-Authenticate carrying the
// resource_metadata pointer. JWKS isn't fetched at wrap time (lazy on
// first authenticated request), so this works with a fake issuer URL.
func TestMaybeWrapOIDCAuthWrapsWhenFullyConfigured(t *testing.T) {
	t.Setenv("MCP_OIDC_ISSUER", "https://dex.example.com")
	t.Setenv("MCP_OIDC_AUDIENCE", "dex-client-id")
	t.Setenv("MCP_PUBLIC_URL", "https://bot.example.com")
	t.Setenv("MCP_OIDC_ALLOWED_GROUPS", "sre,platform")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	teapot := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	mcpH, sseH, err := buildOIDCHandlers(context.Background(), teapot, teapot, logger)
	require.NoError(t, err)

	for _, h := range []http.Handler{mcpH, sseH} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "missing token must be rejected before downstream")
		require.Contains(t, rec.Header().Get("WWW-Authenticate"), "Bearer")
		require.Contains(t, rec.Header().Get("WWW-Authenticate"), "https://bot.example.com/.well-known/oauth-protected-resource")
	}
}

// TestProtectedResourceMetadataAdvertisesOIDCWhenActive verifies the
// well-known endpoint reflects the active method's auth server + scopes.
func TestProtectedResourceMetadataAdvertisesOIDCWhenActive(t *testing.T) {
	t.Setenv("MCP_OIDC_ISSUER", "https://dex.tools.nxteam.dev")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("MCP_PUBLIC_URL", "https://bot.example.com")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mux := http.NewServeMux()
	registerOAuthMetadataRoute(mux, logger)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var meta map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &meta))
	servers, _ := meta["authorization_servers"].([]interface{})
	require.Contains(t, servers, "https://dex.tools.nxteam.dev")
	scopes, _ := meta["scopes_supported"].([]interface{})
	require.Contains(t, scopes, "groups")
}

// TestProtectedResourceMetadataAdvertisesGoogleWhenActive is the
// regression guard for the existing Google path — generalizing the
// metadata route must not break it.
func TestProtectedResourceMetadataAdvertisesGoogleWhenActive(t *testing.T) {
	t.Setenv("MCP_OIDC_ISSUER", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id.apps.googleusercontent.com")
	t.Setenv("MCP_PUBLIC_URL", "https://bot.example.com")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mux := http.NewServeMux()
	registerOAuthMetadataRoute(mux, logger)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var meta map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &meta))
	servers, _ := meta["authorization_servers"].([]interface{})
	require.Contains(t, servers, "https://accounts.google.com")
	scopes, _ := meta["scopes_supported"].([]interface{})
	require.NotContains(t, scopes, "groups")
}

// TestProtectedResourceMetadataOmittedWhenNoAuthActive verifies that with
// no auth provider configured, the well-known endpoint isn't served —
// pointing clients at nothing would be a worse experience than 404.
// TestSplitCSVAcceptsCommasAndWhitespace is the regression guard for the
// "make MCP_OIDC_ALLOWED_EMAILS bearable to maintain" change. A single
// flat env var holding comma-separated emails is fine for two or three
// entries; once you have a dozen, the YAML deployment file becomes
// unreadable. The fix is to accept both commas AND whitespace (notably
// newlines) as separators, so operators can write a multi-line YAML
// block scalar and get one entry per line.
func TestSplitCSVAcceptsCommasAndWhitespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "only_whitespace",
			in:   "   \n\t  ",
			want: nil,
		},
		{
			name: "single_value",
			in:   "alice@katn-solutions.io",
			want: []string{"alice@katn-solutions.io"},
		},
		{
			name: "comma_separated_legacy",
			in:   "alice@katn-solutions.io,bob@katn-solutions.io,carol@katn-solutions.io",
			want: []string{"alice@katn-solutions.io", "bob@katn-solutions.io", "carol@katn-solutions.io"},
		},
		{
			name: "comma_with_spaces",
			in:   "alice@katn-solutions.io, bob@katn-solutions.io ,carol@katn-solutions.io",
			want: []string{"alice@katn-solutions.io", "bob@katn-solutions.io", "carol@katn-solutions.io"},
		},
		{
			name: "newline_separated_block_scalar",
			// What a YAML `value: |-` block scalar produces when each
			// entry is on its own line. The whole point of this change.
			in:   "alice@katn-solutions.io\nbob@katn-solutions.io\ncarol@katn-solutions.io",
			want: []string{"alice@katn-solutions.io", "bob@katn-solutions.io", "carol@katn-solutions.io"},
		},
		{
			name: "mixed_commas_and_newlines",
			in:   "alice@katn-solutions.io,bob@katn-solutions.io\ncarol@katn-solutions.io",
			want: []string{"alice@katn-solutions.io", "bob@katn-solutions.io", "carol@katn-solutions.io"},
		},
		{
			name: "indented_yaml_block_scalar",
			// YAML block scalars strip common indentation but mismatched
			// indents leave leading whitespace on some lines. The
			// whitespace-as-separator rule means that's harmless.
			in:   "alice@katn-solutions.io\n  bob@katn-solutions.io\n\tcarol@katn-solutions.io",
			want: []string{"alice@katn-solutions.io", "bob@katn-solutions.io", "carol@katn-solutions.io"},
		},
		{
			name: "consecutive_separators_dropped",
			in:   "alice@katn-solutions.io,,,\n\n\nbob@katn-solutions.io",
			want: []string{"alice@katn-solutions.io", "bob@katn-solutions.io"},
		},
		{
			name: "trailing_newline_from_pipe_block",
			// `value: |` (without the `-`) preserves the trailing newline.
			in:   "alice@katn-solutions.io\nbob@katn-solutions.io\n",
			want: []string{"alice@katn-solutions.io", "bob@katn-solutions.io"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := splitCSV(tt.in)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestProtectedResourceMetadataOmittedWhenNoAuthActive(t *testing.T) {
	t.Setenv("MCP_OIDC_ISSUER", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mux := http.NewServeMux()
	registerOAuthMetadataRoute(mux, logger)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, "no auth → no metadata route")
}
