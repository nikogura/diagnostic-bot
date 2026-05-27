package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/nikogura/diagnostic-bot/pkg/mcp/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractClientIPPrefersForwardedFor verifies that when X-Forwarded-For
// is set (which the bot's VPC-gated ingress always sets), we use its first
// hop as the originating client IP — that's the actual user, not the
// ingress's address.
func TestExtractClientIPPrefersForwardedFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		xff        string
		xRealIP    string
		remoteAddr string
		want       string
	}{
		{
			name:       "single_xff_hop",
			xff:        "10.0.1.5",
			remoteAddr: "127.0.0.1:54321",
			want:       "10.0.1.5",
		},
		{
			name:       "multi_xff_hops_uses_leftmost",
			xff:        "10.0.1.5, 10.0.2.10, 10.0.3.20",
			remoteAddr: "127.0.0.1:54321",
			want:       "10.0.1.5",
		},
		{
			name:       "xff_with_extra_whitespace_trimmed",
			xff:        "  10.0.1.5  , 10.0.2.10",
			remoteAddr: "127.0.0.1:54321",
			want:       "10.0.1.5",
		},
		{
			name:       "falls_back_to_x_real_ip",
			xRealIP:    "10.0.1.6",
			remoteAddr: "127.0.0.1:54321",
			want:       "10.0.1.6",
		},
		{
			name:       "falls_back_to_remote_addr_strips_port",
			remoteAddr: "10.0.1.7:54321",
			want:       "10.0.1.7",
		},
		{
			name:       "remote_addr_without_port_passes_through",
			remoteAddr: "10.0.1.7",
			want:       "10.0.1.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(http.MethodGet, "http://example.test/mcp", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xRealIP != "" {
				r.Header.Set("X-Real-IP", tt.xRealIP)
			}

			assert.Equal(t, tt.want, extractClientIP(r))
		})
	}
}

// TestAuditSourceMiddlewareInjectsIPIntoContext verifies the middleware
// stuffs the resolved client IP into the request context so downstream MCP
// tool handlers can read it via auditSourceFromContext.
func TestAuditSourceMiddlewareInjectsIPIntoContext(t *testing.T) {
	t.Parallel()

	var captured string
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = auditSourceFromContext(r.Context())
	})

	wrapped := WithAuditSourceMiddleware(handler)

	req := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", nil)
	req.Header.Set("X-Forwarded-For", "10.0.1.5, 10.0.2.10")
	req.RemoteAddr = "127.0.0.1:54321"

	wrapped.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "10.0.1.5", captured)
}

// TestAuditSourceFromContextDefaultsToStdio verifies the local Slack-bot
// path: when no IP has been injected (stdio transport, no HTTP request),
// the audit source is the literal string "stdio" so audit logs are
// uniform across transports rather than carrying empty fields.
func TestAuditSourceFromContextDefaultsToStdio(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "stdio", auditSourceFromContext(context.Background()))
}

// TestAuditSourceFromContextHonorsInjectedValue verifies the bot path
// override mechanism: WithAuditSourceIP sets the value, the getter returns it.
func TestAuditSourceFromContextHonorsInjectedValue(t *testing.T) {
	t.Parallel()
	ctx := WithAuditSourceIP(context.Background(), "10.0.1.5")
	assert.Equal(t, "10.0.1.5", auditSourceFromContext(ctx))
}

// TestAuditUserFromContextPrefersAuthResultEmail closes the loop on the
// Google OAuth path: when a request has been authenticated by the
// WithAuth middleware, the verified email becomes the audit identity
// stamped on Grafana writes. This is the trusted-source attribution
// that the LLM cannot influence.
func TestAuditUserFromContextPrefersAuthResultEmail(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	t.Setenv("MCP_AUDIT_USER", "server-default")
	server := NewServer(nil, "", nil, logger)

	// No verified identity yet → server default wins.
	require.Equal(t, "server-default", server.auditUserFromContext(context.Background()))

	// Bot path: explicit WithAuditUser override beats server default.
	botCtx := WithAuditUser(context.Background(), "slack-user-bob")
	require.Equal(t, "slack-user-bob", server.auditUserFromContext(botCtx))

	// HTTP/SSE path: verified auth result beats both.
	authCtx := context.WithValue(context.Background(), authResultCtxKeyForTest{}, &auth.Result{Authenticated: true, Email: "alice@katn.io", Method: "google-oauth"})
	_ = authCtx // marker for the comment block; the next line uses the real context value via auth.WithAuth at runtime
	verified := injectAuthResult(context.Background(), &auth.Result{Authenticated: true, Email: "alice@katn.io", Method: "google-oauth"})
	require.Equal(t, "alice@katn.io", server.auditUserFromContext(verified))

	// Verified identity also wins over WithAuditUser — trusted source > LLM-side hint.
	mixed := injectAuthResult(WithAuditUser(context.Background(), "slack-user-bob"), &auth.Result{Authenticated: true, Email: "alice@katn.io"})
	require.Equal(t, "alice@katn.io", server.auditUserFromContext(mixed))
}

// authResultCtxKeyForTest is a no-op alias to make the precedence-order
// comment in the test above visually anchored to where Result lives. The
// real path goes through auth.WithAuth's internal key.
type authResultCtxKeyForTest struct{}

// injectAuthResult lets tests stuff an auth.Result into a context using
// the same path the WithAuth middleware uses, without standing up a
// full HTTP handler chain.
func injectAuthResult(ctx context.Context, r *auth.Result) (newCtx context.Context) {
	// We have to go through auth's exported entry point so the unexported
	// key matches what ResultFromContext reads back. WithAuth itself
	// only uses ServeHTTP — but ResultFromContext lives in the auth
	// package, and the key is unexported. Use a tiny middleware to
	// thread the value through a synthetic request.
	var captured context.Context
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := auth.WithAuth(&fixedResultMethod{result: r}, "https://example/.well-known/oauth-protected-resource", logger)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		captured = req.Context() //nolint:fatcontext // test capture: we deliberately extract the post-WithAuth context to assert on it
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer placeholder")
	handler.ServeHTTP(rec, req)
	newCtx = captured
	return newCtx
}

// fixedResultMethod is an auth.Method that returns a pinned Result/nil-error.
type fixedResultMethod struct {
	result *auth.Result
}

func (f *fixedResultMethod) Name() (name string) {
	name = "fixed-test"
	return name
}

func (f *fixedResultMethod) Authenticate(_ *http.Request) (result *auth.Result, err error) {
	result = f.result
	return result, err
}
