package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"strings"
)

// auditUserKey is the private context key used to override the server's
// default audit user on a per-request basis. The Slack bot uses this to
// stamp the resolved Slack username onto Grafana writes initiated by a
// specific investigation, instead of the process owner.
type auditUserKey struct{}

// auditUserFallback is the last-resort audit identity when neither the
// MCP_AUDIT_USER env var nor user.Current() yields anything usable. It
// only surfaces in misconfigured deployments; a warning is logged so the
// operator can fix it.
const auditUserFallback = "mcp-server"

// resolveAuditUser determines the audit identity at MCP server startup.
// Priority:
//  1. MCP_AUDIT_USER env var — explicit override. Use this in containers,
//     CI, or anywhere the OS user isn't the right name to stamp on writes.
//  2. user.Current() — for local Claude Code over stdio, this is the
//     developer running Claude Code. Automatic, true.
//  3. auditUserFallback constant — should rarely hit; warns if it does.
//
// This is the *default* identity; per-request overrides via WithAuditUser
// take precedence and are how the Slack bot attributes writes to the
// specific human who started an investigation.
func resolveAuditUser(logger *slog.Logger) (auditUser string) {
	auditUser = os.Getenv("MCP_AUDIT_USER")
	if auditUser != "" {
		return auditUser
	}

	u, err := user.Current()
	if err == nil && u.Username != "" {
		auditUser = u.Username
		return auditUser
	}

	logger.Warn("could not resolve audit user from MCP_AUDIT_USER or user.Current — falling back",
		slog.String("audit_user", auditUserFallback))
	auditUser = auditUserFallback
	return auditUser
}

// WithAuditUser returns a derived context that overrides the audit user
// for any Grafana write performed under this context. Callers (notably
// the Slack bot path) use this to stamp the actual Slack user onto
// version-history notes rather than the bot process owner.
func WithAuditUser(ctx context.Context, auditUser string) (newCtx context.Context) {
	newCtx = context.WithValue(ctx, auditUserKey{}, auditUser)
	return newCtx
}

// auditUserFromContext returns the audit user for the current request:
// the context-set override when present and non-empty, otherwise the
// server's default resolved at construction.
func (s *Server) auditUserFromContext(ctx context.Context) (auditUser string) {
	v, ok := ctx.Value(auditUserKey{}).(string)
	if ok && v != "" {
		auditUser = v
		return auditUser
	}
	auditUser = s.auditUser
	return auditUser
}

// composeVersionNote builds the Grafana version-history string. Format is
// "<auditUser>: <intention>". If the LLM-supplied intention is empty, a
// tool-specific default replaces it so the user's name still lands.
func composeVersionNote(auditUser, intention, defaultIntention string) (note string) {
	if intention == "" {
		intention = defaultIntention
	}
	note = fmt.Sprintf("%s: %s", auditUser, intention)
	return note
}

// auditSourceIPKey is the private context key the HTTP/SSE middleware
// uses to thread the resolved client IP down to MCP tool handlers.
type auditSourceIPKey struct{}

// auditSourceStdio is the value reported when no IP has been injected —
// the local Slack-bot path uses stdio transport and has no network peer
// to attribute to. Keeping this as an explicit constant rather than the
// empty string keeps audit log fields uniform across transports.
const auditSourceStdio = "stdio"

// WithAuditSourceIP returns a context that carries the client IP for the
// current request. The HTTP/SSE middleware sets this once per request;
// MCP tool handlers read it via auditSourceFromContext when emitting
// audit slog lines for Grafana writes.
func WithAuditSourceIP(ctx context.Context, ip string) (newCtx context.Context) {
	newCtx = context.WithValue(ctx, auditSourceIPKey{}, ip)
	return newCtx
}

// auditSourceFromContext returns the audit source for the current request:
// the IP injected by the HTTP/SSE middleware, or auditSourceStdio when
// none is present (the Slack-bot stdio path).
func auditSourceFromContext(ctx context.Context) (source string) {
	v, ok := ctx.Value(auditSourceIPKey{}).(string)
	if ok && v != "" {
		source = v
		return source
	}
	source = auditSourceStdio
	return source
}

// extractClientIP resolves the originating client IP from an HTTP request.
// Priority:
//  1. X-Forwarded-For — leftmost (originating) hop. The bot's MCP HTTP
//     server runs behind a VPC-gated ingress that always sets this; the
//     ingress's own IP is not what we want to log.
//  2. X-Real-IP — alternative single-IP header set by some ingresses.
//  3. r.RemoteAddr with the port stripped.
//
// This trusts the proxy chain. That's acceptable for the bot's VPC-gated
// deployment; an internet-facing deployment would need a configurable
// trusted-proxy allowlist to prevent header spoofing.
func extractClientIP(r *http.Request) (ip string) {
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		idx := strings.IndexByte(xff, ',')
		if idx > 0 {
			ip = strings.TrimSpace(xff[:idx])
			return ip
		}
		ip = strings.TrimSpace(xff)
		return ip
	}

	realIP := r.Header.Get("X-Real-IP")
	if realIP != "" {
		ip = strings.TrimSpace(realIP)
		return ip
	}

	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr == nil {
		ip = host
		return ip
	}
	ip = r.RemoteAddr
	return ip
}

// WithAuditSourceMiddleware wraps an http.Handler so each request's
// resolved client IP is attached to its context. Wrap the MCP HTTP and
// SSE handlers with this; the IP then flows down to Grafana write
// handlers and lands in the audit slog line as audit_source_ip.
func WithAuditSourceMiddleware(next http.Handler) (wrapped http.Handler) {
	wrapped = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithAuditSourceIP(r.Context(), extractClientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
	return wrapped
}
