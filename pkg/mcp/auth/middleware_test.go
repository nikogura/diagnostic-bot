package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubMethod is a Method impl that returns whatever Result/error it's
// configured with. Lets the middleware tests exercise their branches
// without a real GoogleAuth.
type stubMethod struct {
	result *Result
	err    error
	called int
}

func (s *stubMethod) Name() (name string) {
	name = "stub"
	return name
}

func (s *stubMethod) Authenticate(_ *http.Request) (result *Result, err error) {
	s.called++
	result = s.result
	err = s.err
	return result, err
}

func newTestLogger() (logger *slog.Logger) {
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	return logger
}

const testResourceMetadataURL = "https://bot.example.com/.well-known/oauth-protected-resource"

// TestWithAuthReturns401AndWWWAuthenticateOnMissingToken verifies the
// 401 + WWW-Authenticate-with-resource_metadata pattern that triggers
// Claude Code's browser-pop OAuth flow. This is the load-bearing
// regression guard for the UX the user asked for.
func TestWithAuthReturns401AndWWWAuthenticateOnMissingToken(t *testing.T) {
	t.Parallel()
	stub := &stubMethod{err: errors.New("missing Authorization header")}
	downstreamCalled := false
	wrapped := WithAuth(stub, testResourceMetadataURL, newTestLogger())(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		downstreamCalled = true
	}))

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), `Bearer`)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), `resource_metadata=`)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), testResourceMetadataURL)
	require.False(t, downstreamCalled, "downstream handler must not run on auth failure")
}

// TestWithAuthAllowsAuthenticatedRequest verifies the happy path: token
// validates, downstream runs.
func TestWithAuthAllowsAuthenticatedRequest(t *testing.T) {
	t.Parallel()
	stub := &stubMethod{result: &Result{Authenticated: true, Email: "alice@katn-solutions.io", Method: "google-oauth"}}
	downstreamCalled := false
	wrapped := WithAuth(stub, testResourceMetadataURL, newTestLogger())(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		downstreamCalled = true
	}))

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, downstreamCalled, "downstream handler must run on auth success")
}

// TestWithAuthInjectsResultIntoContext verifies the auth.Result is
// retrievable downstream — this is how the audit-identity bridge picks
// up the verified email so Grafana version notes carry the real human.
func TestWithAuthInjectsResultIntoContext(t *testing.T) {
	t.Parallel()
	want := &Result{Authenticated: true, Email: "alice@katn-solutions.io", Method: "google-oauth"}
	stub := &stubMethod{result: want}
	var seen *Result
	wrapped := WithAuth(stub, testResourceMetadataURL, newTestLogger())(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = ResultFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	require.NotNil(t, seen)
	require.Equal(t, "alice@katn-solutions.io", seen.Email)
	require.Equal(t, "google-oauth", seen.Method)
}

// TestResultFromContextReturnsNilWhenAbsent verifies the getter is safe
// to call on un-wrapped contexts (the in-process Slack-bot path doesn't go
// through WithAuth and must not crash when audit code asks for a Result).
func TestResultFromContextReturnsNilWhenAbsent(t *testing.T) {
	t.Parallel()
	require.Nil(t, ResultFromContext(httptest.NewRequest(http.MethodGet, "/mcp", nil).Context()))
}

// TestProtectedResourceMetadataHandlerReturnsRFC9728JSON verifies the
// well-known endpoint Claude Code reads to discover the auth server.
// Document shape must match RFC 9728.
func TestProtectedResourceMetadataHandlerReturnsRFC9728JSON(t *testing.T) {
	t.Parallel()
	handler, err := ProtectedResourceMetadataHandler(
		"https://bot.example.com/mcp",
		"https://accounts.google.com",
		[]string{"openid", "email", "profile"},
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var meta map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &meta))
	require.Equal(t, "https://bot.example.com/mcp", meta["resource"])

	servers, ok := meta["authorization_servers"].([]interface{})
	require.True(t, ok, "authorization_servers must be a JSON array")
	require.Contains(t, servers, "https://accounts.google.com")

	bearerMethods, ok := meta["bearer_methods_supported"].([]interface{})
	require.True(t, ok)
	require.Contains(t, bearerMethods, "header")

	scopes, ok := meta["scopes_supported"].([]interface{})
	require.True(t, ok)
	require.Contains(t, scopes, "openid")
	require.Contains(t, scopes, "email")
}
