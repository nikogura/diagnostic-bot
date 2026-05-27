package auth

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGoogleUserInfoFake stands up an httptest server that mimics Google's
// userinfo endpoint. Returns the server (caller closes it), the body it
// will return on 200 paths, and a hit counter so tests can assert
// cache behavior.
func newGoogleUserInfoFake(t *testing.T, statusFn func(token string) int, bodyFn func(token string) string) (server *httptest.Server, hitCount *atomic.Int64) {
	t.Helper()
	hitCount = &atomic.Int64{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		token := ""
		const prefix = "Bearer "
		if v := r.Header.Get("Authorization"); len(v) > len(prefix) && v[:len(prefix)] == prefix {
			token = v[len(prefix):]
		}
		status := statusFn(token)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = fmt.Fprint(w, bodyFn(token))
		}
	}))
	return server, hitCount
}

func newTestGoogleAuth(t *testing.T, server *httptest.Server, allowedDomains, allowedEmails []string) (auth *GoogleAuth) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := NewGoogleAuth(GoogleConfig{
		ClientID:             "test-client.apps.googleusercontent.com",
		AllowedHostedDomains: allowedDomains,
		AllowedEmails:        allowedEmails,
		UserInfoURL:          server.URL,
	}, logger)
	require.NoError(t, err)
	auth = a
	return auth
}

// Named-return helpers so the inline status/body closures the tests pass
// to newGoogleUserInfoFake don't each need their own named-return dance.
func okStatus(string) (status int) { status = http.StatusOK; return status }

func unauthStatus(string) (status int) { status = http.StatusUnauthorized; return status }

func emptyBody(string) (body string) { return body }

func newReqWithBearer(token string) (r *http.Request) {
	r = httptest.NewRequest(http.MethodGet, "https://bot.example.com/mcp", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// TestGoogleAuthRejectsMissingBearer verifies the no-header path. Returning
// an error here is what triggers the middleware's WWW-Authenticate challenge
// upstream — the browser-pop UX hinges on this.
func TestGoogleAuthRejectsMissingBearer(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		okStatus,
		emptyBody)
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, nil, nil)

	_, err := auth.Authenticate(httptest.NewRequest(http.MethodGet, "https://bot/mcp", nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing Authorization header")
}

// TestGoogleAuthRejectsMalformedBearer verifies Authorization header that
// isn't "Bearer <token>" shape.
func TestGoogleAuthRejectsMalformedBearer(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		okStatus,
		emptyBody)
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, nil, nil)

	r := httptest.NewRequest(http.MethodGet, "https://bot/mcp", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	_, err := auth.Authenticate(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid Authorization header")
}

// TestGoogleAuthCallsUserInfoWithBearer verifies the token is forwarded to
// Google's userinfo endpoint via the Authorization header — the validation
// model here is "ask Google who this token belongs to," not JWT signature
// checking. Claude Code presents opaque OAuth access tokens, not ID tokens.
func TestGoogleAuthCallsUserInfoWithBearer(t *testing.T) {
	t.Parallel()
	var capturedAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `{"sub":"123","email":"alice@katn.io","email_verified":true,"hd":"katn.io"}`)
	}))
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, nil, nil)

	_, err := auth.Authenticate(newReqWithBearer("opaque-google-access-token"))
	require.NoError(t, err)
	require.Equal(t, "Bearer opaque-google-access-token", capturedAuthHeader)
}

// TestGoogleAuthReturnsIdentityOnSuccess verifies the auth Result carries
// the Google identity claims the audit layer needs to attribute writes.
func TestGoogleAuthReturnsIdentityOnSuccess(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		okStatus,
		func(string) (body string) {
			body = `{"sub":"google-123","email":"alice@katn.io","email_verified":true,"hd":"katn.io","name":"Alice"}`
			return body
		})
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, nil, nil)

	result, err := auth.Authenticate(newReqWithBearer("tok"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Authenticated)
	require.Equal(t, "alice@katn.io", result.Email)
	require.Equal(t, "google-123", result.Subject)
	require.Equal(t, "google-oauth", result.Method)
}

// TestGoogleAuthRejectsUserInfo401 verifies that an expired/revoked token
// (userinfo returns 401) fails authentication. Token expiry is enforced
// by Google here — we don't track expiry ourselves.
func TestGoogleAuthRejectsUserInfo401(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		unauthStatus,
		emptyBody)
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, nil, nil)

	_, err := auth.Authenticate(newReqWithBearer("expired-tok"))
	require.Error(t, err)
}

// TestGoogleAuthRejectsWrongHostedDomain verifies hd-claim enforcement when
// AllowedHostedDomains is configured. This is the Workspace-domain filter
// that auto-enrolls Workspace members and auto-revokes ex-members.
func TestGoogleAuthRejectsWrongHostedDomain(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		okStatus,
		func(string) (body string) {
			body = `{"sub":"123","email":"mallory@evil.example","hd":"evil.example"}`
			return body
		})
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, []string{"katn.io"}, nil)

	_, err := auth.Authenticate(newReqWithBearer("tok"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "hosted domain")
}

// TestGoogleAuthAcceptsMatchingHostedDomain is the positive of the prior test.
func TestGoogleAuthAcceptsMatchingHostedDomain(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		func(string) (status int) { status = http.StatusOK; return status },
		func(string) (body string) {
			body = `{"sub":"123","email":"alice@katn.io","hd":"katn.io"}`
			return body
		})
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, []string{"katn.io"}, nil)

	result, err := auth.Authenticate(newReqWithBearer("tok"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Authenticated)
}

// TestGoogleAuthAcceptsAnyHostedDomainWhenAllowlistEmpty verifies that an
// empty AllowedHostedDomains means "no domain restriction" — the
// auth-anyone-with-a-Google-account path. Strange in production but the
// right default behavior for the config knob.
func TestGoogleAuthAcceptsAnyHostedDomainWhenAllowlistEmpty(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		okStatus,
		func(string) (body string) {
			body = `{"sub":"123","email":"alice@gmail.com"}`
			return body
		})
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, nil, nil)

	_, err := auth.Authenticate(newReqWithBearer("tok"))
	require.NoError(t, err)
}

// TestGoogleAuthEnforcesEmailAllowlistWhenSet verifies that an explicit
// email allowlist takes effect when configured. Useful for the "two of us
// can write, the rest of Workspace is read-only" pattern when combined
// with the hosted-domain filter.
func TestGoogleAuthEnforcesEmailAllowlistWhenSet(t *testing.T) {
	t.Parallel()
	server, _ := newGoogleUserInfoFake(t,
		okStatus,
		func(string) (body string) {
			body = `{"sub":"123","email":"bob@katn.io","hd":"katn.io"}`
			return body
		})
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, []string{"katn.io"}, []string{"alice@katn.io"})

	_, err := auth.Authenticate(newReqWithBearer("tok"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "email")
}

// TestGoogleAuthCachesValidToken verifies the second Authenticate call for
// the same token does NOT hit Google's userinfo endpoint — that would be
// ~one network round-trip per MCP request, which is unacceptable.
func TestGoogleAuthCachesValidToken(t *testing.T) {
	t.Parallel()
	server, hits := newGoogleUserInfoFake(t,
		func(string) (status int) { status = http.StatusOK; return status },
		func(string) (body string) {
			body = `{"sub":"123","email":"alice@katn.io","hd":"katn.io"}`
			return body
		})
	t.Cleanup(server.Close)
	auth := newTestGoogleAuth(t, server, nil, nil)

	_, err := auth.Authenticate(newReqWithBearer("same-token"))
	require.NoError(t, err)
	_, err = auth.Authenticate(newReqWithBearer("same-token"))
	require.NoError(t, err)
	_, err = auth.Authenticate(newReqWithBearer("same-token"))
	require.NoError(t, err)

	require.Equal(t, int64(1), hits.Load(), "userinfo should be hit exactly once for three identical-token requests")
}

// TestGoogleAuthCacheExpires verifies the cache entry is honored only up
// to its configured TTL, after which a fresh userinfo call is made. Token
// expiry beyond our knowledge is handled by Google returning 401.
func TestGoogleAuthCacheExpires(t *testing.T) {
	t.Parallel()
	server, hits := newGoogleUserInfoFake(t,
		func(string) (status int) { status = http.StatusOK; return status },
		func(string) (body string) {
			body = `{"sub":"123","email":"alice@katn.io","hd":"katn.io"}`
			return body
		})
	t.Cleanup(server.Close)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	auth, err := NewGoogleAuth(GoogleConfig{
		ClientID:    "test-client.apps.googleusercontent.com",
		UserInfoURL: server.URL,
		CacheTTL:    50 * time.Millisecond,
	}, logger)
	require.NoError(t, err)

	_, err = auth.Authenticate(newReqWithBearer("tok"))
	require.NoError(t, err)
	require.Equal(t, int64(1), hits.Load())

	time.Sleep(80 * time.Millisecond)

	_, err = auth.Authenticate(newReqWithBearer("tok"))
	require.NoError(t, err)
	require.Equal(t, int64(2), hits.Load(), "userinfo should be hit again after the cache TTL has expired")
}

// TestGoogleAuthName documents the Method name surfaced to log/audit lines.
func TestGoogleAuthName(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	auth, err := NewGoogleAuth(GoogleConfig{
		ClientID:    "test-client.apps.googleusercontent.com",
		UserInfoURL: "https://example.invalid/userinfo",
	}, logger)
	require.NoError(t, err)
	assert.Equal(t, "google-oauth", auth.Name())
}

// TestNewGoogleAuthRequiresClientID verifies config validation at construction.
func TestNewGoogleAuthRequiresClientID(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	_, err := NewGoogleAuth(GoogleConfig{
		ClientID: "",
	}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ClientID")
}
