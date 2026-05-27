package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// resultContextKey is the private key under which a verified *Result is
// stored in the request context. Downstream MCP handlers (audit, Grafana
// writes) read it back via ResultFromContext.
type resultContextKey struct{}

// WithAuth wraps an http.Handler so each request is authenticated against
// the supplied Method before downstream code runs. On failure the
// response is 401 with WWW-Authenticate carrying the protected-resource
// metadata URL — that's the trigger that makes Claude Code (and any
// other MCP client implementing the 2025-03-26 spec) discover the
// authorization server and pop a browser for the OAuth flow.
//
// resourceMetadataURL must be the fully-qualified, externally reachable
// URL of /.well-known/oauth-protected-resource on this server. Internal
// pod URLs won't work — Claude Code follows the link from outside.
func WithAuth(method Method, resourceMetadataURL string, logger *slog.Logger) (wrapper func(http.Handler) http.Handler) {
	wrapper = func(next http.Handler) (handler http.Handler) {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result, err := method.Authenticate(r)
			if err != nil || result == nil || !result.Authenticated {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q`, resourceMetadataURL))
				w.WriteHeader(http.StatusUnauthorized)
				if err != nil {
					logger.InfoContext(r.Context(), "auth rejected",
						slog.String("method", method.Name()),
						slog.String("error", err.Error()),
						slog.String("path", r.URL.Path),
					)
				}
				return
			}
			ctx := context.WithValue(r.Context(), resultContextKey{}, result)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
		return handler
	}
	return wrapper
}

// ResultFromContext returns the verified auth Result associated with the
// request, or nil when the request didn't pass through WithAuth (notably
// the slackbot's stdio path, which has no HTTP context).
func ResultFromContext(ctx context.Context) (result *Result) {
	v, ok := ctx.Value(resultContextKey{}).(*Result)
	if !ok {
		return result
	}
	result = v
	return result
}

// ProtectedResourceMetadata is the RFC 9728 document shape that MCP
// clients fetch to discover the authorization server. Field names match
// the spec exactly — clients are picky about this.
type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
}

// ProtectedResourceMetadataHandler serves the well-known endpoint that
// Claude Code reads to discover which OAuth authorization server to
// point the browser at. This endpoint must NOT be protected by WithAuth
// — clients have to be able to fetch it before they have a token.
func ProtectedResourceMetadataHandler(resourceURL, authServerURL string, scopes []string) (handler http.Handler) {
	body, err := json.Marshal(ProtectedResourceMetadata{
		Resource:               resourceURL,
		AuthorizationServers:   []string{authServerURL},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        scopes,
	})
	if err != nil {
		// Constants in, no way to fail; panic surfaces the misconfig at startup.
		panic(fmt.Sprintf("marshaling protected resource metadata: %v", err))
	}

	handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
	})
	return handler
}
