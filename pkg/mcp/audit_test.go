package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
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
