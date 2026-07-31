package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nikogura/diagnostic-bot/pkg/apiconfig"
)

// apiE2ECapture records what the fake upstream API received, so a dispatch can
// be asserted against the request that actually went out. Guarded because the
// httptest handler runs on its own goroutine (and -race would flag a bare read).
type apiE2ECapture struct {
	mu       sync.Mutex
	seen     bool
	method   string
	path     string
	rawQuery string
	auth     string
	body     string
}

// capturedBody returns the request body the upstream received.
func (c *apiE2ECapture) capturedBody() (body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body = c.body
	return body
}

func (c *apiE2ECapture) snapshot() (seen bool, method, path, rawQuery, auth string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen, method, path, rawQuery, auth = c.seen, c.method, c.path, c.rawQuery, c.auth
	return seen, method, path, rawQuery, auth
}

// newAPIToolE2EServer stands up a fake upstream API and a real *Server wired to
// it through apiconfig.LoadRegistryFromEnv — the exact env → registry → server
// path cmd/bot/main.go uses. The upstream returns respStatus/respBody and
// records the request. tokenValue is set as the config's auth token env
// (pass "" to leave the token unset, which must cause the config to be skipped).
//
// Any env the server reads at construction (READ_ONLY, MCP_AUTHZ, …) must be set
// by the caller before calling this, since it constructs the server.
func newAPIToolE2EServer(t *testing.T, respStatus int, respBody, tokenValue string) (srv *Server, capture *apiE2ECapture, toolName string) {
	t.Helper()

	capture = &apiE2ECapture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.mu.Lock()
		capture.seen = true
		capture.method = r.Method
		capture.path = r.URL.Path
		capture.rawQuery = r.URL.RawQuery
		capture.auth = r.Header.Get("Authorization")
		capture.mu.Unlock()

		w.WriteHeader(respStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	config := fmt.Sprintf(`
name: testapi
description: "Test API"
base_url: %s
auth:
  type: bearer
  token_env: TESTAPI_TOKEN
endpoints:
  - name: get_thing
    description: "Get a thing by id"
    method: GET
    path: /things/{id}
    params:
      - name: id
        type: string
        required: true
        in: path
        validate: "[a-z0-9]+"
      - name: verbose
        type: boolean
        in: query
    redact_fields: ["secret"]
`, upstream.URL)

	writeErr := os.WriteFile(filepath.Join(dir, "testapi.yaml"), []byte(config), 0o644)
	if writeErr != nil {
		t.Fatalf("writing API config: %v", writeErr)
	}

	// Set deterministically (empty means "unset" to LoadConfigs, which then
	// skips the config).
	t.Setenv("TESTAPI_TOKEN", tokenValue)
	t.Setenv("API_CONFIG_DIR", dir)

	logger := slog.New(slog.DiscardHandler)
	registry := apiconfig.LoadRegistryFromEnv(logger)
	srv = NewServer(nil, "", registry, logger)
	toolName = "testapi_get_thing"
	return srv, capture, toolName
}

// apiToolInList reports whether a tool of the given name is in the list.
func apiToolInList(tools []MCPTool, name string) (found bool) {
	for _, tool := range tools {
		if tool.Name == name {
			found = true
			return found
		}
	}
	return found
}

// TestServer_APITool_EndToEnd is the load-bearing proof that the wired feature
// actually works: a real Server, built from an on-disk config via
// LoadRegistryFromEnv, advertises the API tool and dispatches it end to end —
// through authorize → dispatchToolCall → the registry → a live HTTP request —
// with the bearer token applied, path/query params built, and response fields
// redacted. This is the path that was completely dark before the fix.
func TestServer_APITool_EndToEnd(t *testing.T) {
	srv, capture, tool := newAPIToolE2EServer(t, http.StatusOK, `{"name":"thing","secret":"hunter2"}`, "tok-123")

	// (1) Catalog: the tool the model is offered includes the API tool.
	if !apiToolInList(srv.ToolDefinitions(), tool) {
		t.Fatalf("ToolDefinitions() missing %q — server did not surface the API tool", tool)
	}

	// (2) Dispatch through the real server entry point.
	result, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{
		"id":      "abc123",
		"verbose": true,
	})
	if err != nil {
		t.Fatalf("DispatchTool: %v", err)
	}

	// (3) The upstream saw a GET, the path param substituted, the query param,
	// and the bearer token from the env.
	seen, method, path, rawQuery, auth := capture.snapshot()
	if !seen {
		t.Fatal("upstream API was never called")
	}
	if method != http.MethodGet {
		t.Errorf("upstream method = %q, want GET", method)
	}
	if path != "/things/abc123" {
		t.Errorf("upstream path = %q, want /things/abc123", path)
	}
	if !strings.Contains(rawQuery, "verbose=true") {
		t.Errorf("upstream query %q missing verbose=true", rawQuery)
	}
	if auth != "Bearer tok-123" {
		t.Errorf("upstream auth = %q, want 'Bearer tok-123'", auth)
	}

	// (4) The response came back, with the configured field redacted.
	if !strings.Contains(result, "thing") {
		t.Errorf("result missing upstream body: %s", result)
	}
	if strings.Contains(result, "hunter2") {
		t.Errorf("redact_fields did not redact the secret: %s", result)
	}
	if !strings.Contains(result, "[redacted]") {
		t.Errorf("expected [redacted] marker in result: %s", result)
	}
}

// TestServer_APITool_ReadOnlyStillDispatchesGET proves READ_ONLY does not
// withhold read (GET) API tools — they are not writes, so a read-only
// deployment (the bot's default posture) still gets them.
func TestServer_APITool_ReadOnlyStillDispatchesGET(t *testing.T) {
	t.Setenv("READ_ONLY", "true")
	srv, capture, tool := newAPIToolE2EServer(t, http.StatusOK, `{"ok":true}`, "tok")

	if !apiToolInList(srv.ToolDefinitions(), tool) {
		t.Fatal("READ_ONLY must not withhold a GET (read) API tool")
	}

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{"id": "x1"})
	if err != nil {
		t.Fatalf("READ_ONLY blocked a GET API tool: %v", err)
	}

	seen, _, _, _, _ := capture.snapshot()
	if !seen {
		t.Fatal("upstream not called under READ_ONLY")
	}
}

// TestServer_APITool_AuthzDenied proves API tools are subject to the same
// authorization as every other tool: a deny-by-default policy filters the tool
// out of the model's allowed catalog and rejects it at dispatch.
func TestServer_APITool_AuthzDenied(t *testing.T) {
	t.Setenv("MCP_AUTHZ", "authz:\n  default: deny\n")
	srv, capture, tool := newAPIToolE2EServer(t, http.StatusOK, `{"ok":true}`, "tok")

	allowed := srv.AllowedTools(context.Background(), srv.ToolDefinitions())
	if apiToolInList(allowed, tool) {
		t.Error("deny-by-default policy must filter the API tool out of AllowedTools")
	}

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{"id": "x1"})
	if err == nil {
		t.Error("expected permission denied dispatching a denied API tool")
	}

	seen, _, _, _, _ := capture.snapshot()
	if seen {
		t.Error("a denied tool must never reach the upstream API")
	}
}

// TestServer_APITool_AuthzAllowed proves the allow path: a default-allow policy
// leaves the API tool available and dispatchable.
func TestServer_APITool_AuthzAllowed(t *testing.T) {
	t.Setenv("MCP_AUTHZ", "authz:\n  default: allow\n")
	srv, _, tool := newAPIToolE2EServer(t, http.StatusOK, `{"ok":true}`, "tok")

	allowed := srv.AllowedTools(context.Background(), srv.ToolDefinitions())
	if !apiToolInList(allowed, tool) {
		t.Fatal("default-allow policy must keep the API tool available")
	}

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{"id": "x1"})
	if err != nil {
		t.Fatalf("default-allow policy blocked the API tool: %v", err)
	}
}

// TestServer_APITool_UnsetTokenSkipped proves a config whose auth token env is
// unset is skipped — its tool never appears, so a half-provisioned integration
// fails safe rather than dispatching unauthenticated.
func TestServer_APITool_UnsetTokenSkipped(t *testing.T) {
	srv, _, tool := newAPIToolE2EServer(t, http.StatusOK, `{}`, "") // token unset

	if apiToolInList(srv.ToolDefinitions(), tool) {
		t.Error("a config whose token_env is unset must be skipped (tool absent from catalog)")
	}
}

// TestServer_APITool_UpstreamError proves an upstream failure surfaces as a
// dispatch error rather than a panic or a silently-swallowed empty result.
func TestServer_APITool_UpstreamError(t *testing.T) {
	srv, _, tool := newAPIToolE2EServer(t, http.StatusInternalServerError, `{"error":"boom"}`, "tok")

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{"id": "x1"})
	if err == nil {
		t.Fatal("expected an error when the upstream API returns 500")
	}
}

// TestServer_APITool_PathTraversalBlocked proves parameter validation runs on
// the dispatch path: a path-traversal attempt is rejected before any request is
// sent to the upstream.
func TestServer_APITool_PathTraversalBlocked(t *testing.T) {
	srv, capture, tool := newAPIToolE2EServer(t, http.StatusOK, `{}`, "tok")

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{"id": "../secrets"})
	if err == nil {
		t.Fatal("expected a validation error for path traversal in a path param")
	}

	seen, _, _, _, _ := capture.snapshot()
	if seen {
		t.Error("the upstream must NOT be called when a path param fails validation")
	}
}

// newAPIWriteToolE2EServer is the write-path analogue of newAPIToolE2EServer: a
// real *Server wired to a fake upstream via a config with a POST endpoint that
// takes a JSON body param. The upstream records the method, path, auth, and body
// so a write dispatch can be asserted against the request that went out. Set any
// gating env (API_ALLOWED_METHODS, READ_ONLY) before calling this.
func newAPIWriteToolE2EServer(t *testing.T, tokenValue string) (srv *Server, capture *apiE2ECapture, toolName string) {
	t.Helper()

	capture = &apiE2ECapture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)

		capture.mu.Lock()
		capture.seen = true
		capture.method = r.Method
		capture.path = r.URL.Path
		capture.auth = r.Header.Get("Authorization")
		capture.body = string(bodyBytes)
		capture.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	config := fmt.Sprintf(`
name: jira
description: "Jira"
base_url: %s
auth:
  type: bearer
  token_env: JIRA_TOKEN
endpoints:
  - name: add_comment
    description: "Add a comment to an issue"
    method: POST
    path: /issue/{id}/comment
    params:
      - name: id
        type: string
        required: true
        in: path
        validate: "[A-Z0-9-]+"
      - name: body
        type: string
        required: true
        in: body
`, upstream.URL)

	writeErr := os.WriteFile(filepath.Join(dir, "jira.yaml"), []byte(config), 0o644)
	if writeErr != nil {
		t.Fatalf("writing API config: %v", writeErr)
	}

	t.Setenv("JIRA_TOKEN", tokenValue)
	t.Setenv("API_CONFIG_DIR", dir)

	logger := slog.New(slog.DiscardHandler)
	registry := apiconfig.LoadRegistryFromEnv(logger)
	srv = NewServer(nil, "", registry, logger)
	toolName = "jira_add_comment"
	return srv, capture, toolName
}

// TestServer_APIWriteTool_AllowedDispatches proves a write endpoint works when
// its method is in API_ALLOWED_METHODS: the tool is advertised and a dispatch
// issues a real POST carrying the JSON body built from `in: body` params.
func TestServer_APIWriteTool_AllowedDispatches(t *testing.T) {
	t.Setenv("API_ALLOWED_METHODS", "POST")
	srv, capture, tool := newAPIWriteToolE2EServer(t, "tok")

	if !apiToolInList(srv.ToolDefinitions(), tool) {
		t.Fatal("POST tool missing from catalog when POST is allowed")
	}

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{
		"id":   "PROJ-1",
		"body": "looks good",
	})
	if err != nil {
		t.Fatalf("DispatchTool: %v", err)
	}

	seen, method, path, _, auth := capture.snapshot()
	if !seen {
		t.Fatal("upstream was never called")
	}
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if path != "/issue/PROJ-1/comment" {
		t.Errorf("path = %q, want /issue/PROJ-1/comment", path)
	}
	if auth != "Bearer tok" {
		t.Errorf("auth = %q, want 'Bearer tok'", auth)
	}
	if !strings.Contains(capture.capturedBody(), `"looks good"`) {
		t.Errorf("request body missing the comment: %s", capture.capturedBody())
	}
}

// TestServer_APIWriteTool_WithheldByDefault proves that with no
// API_ALLOWED_METHODS (GET-only, the safe default) a POST endpoint is withheld
// from the catalog and rejected at dispatch — the upstream is never called.
func TestServer_APIWriteTool_WithheldByDefault(t *testing.T) {
	srv, capture, tool := newAPIWriteToolE2EServer(t, "tok") // no API_ALLOWED_METHODS

	if apiToolInList(srv.ToolDefinitions(), tool) {
		t.Error("POST tool must be withheld from the catalog by default (GET-only)")
	}

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{"id": "PROJ-1", "body": "x"})
	if err == nil {
		t.Error("POST dispatch must be rejected by default")
	}

	seen, _, _, _, _ := capture.snapshot()
	if seen {
		t.Error("a withheld write tool must never reach the upstream")
	}
}

// TestServer_APIWriteTool_ReadOnlyOverridesAllowlist proves READ_ONLY wins: even
// when the method is in API_ALLOWED_METHODS, a read-only deployment withholds and
// rejects the write tool.
func TestServer_APIWriteTool_ReadOnlyOverridesAllowlist(t *testing.T) {
	t.Setenv("API_ALLOWED_METHODS", "POST")
	t.Setenv("READ_ONLY", "true")
	srv, capture, tool := newAPIWriteToolE2EServer(t, "tok")

	if apiToolInList(srv.ToolDefinitions(), tool) {
		t.Error("READ_ONLY must withhold the write tool even when its method is allowed")
	}

	_, err := srv.DispatchTool(context.Background(), tool, map[string]interface{}{"id": "PROJ-1", "body": "x"})
	if err == nil {
		t.Error("READ_ONLY must reject the write dispatch even when its method is allowed")
	}

	seen, _, _, _, _ := capture.snapshot()
	if seen {
		t.Error("a READ_ONLY-blocked write must never reach the upstream")
	}
}

// TestServer_APIWriteTool_AuditLogged guards the write audit trail: a successful
// API write must emit an "api write" slog line carrying the tool name and the
// resolved audit user. Without this assertion a refactor could silently drop the
// audit log and every other test would still pass.
func TestServer_APIWriteTool_AuditLogged(t *testing.T) {
	t.Setenv("API_ALLOWED_METHODS", "POST")
	t.Setenv("MCP_AUDIT_USER", "alice@example.com")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	config := fmt.Sprintf(`
name: jira
base_url: %s
auth:
  type: bearer
  token_env: JIRA_AUDIT_TOKEN
endpoints:
  - name: add_comment
    description: "Add a comment"
    method: POST
    path: /issue/{id}/comment
    params:
      - name: id
        type: string
        required: true
        in: path
        validate: "[A-Z0-9-]+"
      - name: body
        type: string
        required: true
        in: body
`, upstream.URL)

	writeErr := os.WriteFile(filepath.Join(dir, "jira.yaml"), []byte(config), 0o644)
	if writeErr != nil {
		t.Fatalf("writing API config: %v", writeErr)
	}
	t.Setenv("JIRA_AUDIT_TOKEN", "tok")
	t.Setenv("API_CONFIG_DIR", dir)

	// Capture the server's structured logs so the audit line can be asserted.
	// Every logger write in this test is on this goroutine (construction, then a
	// synchronous dispatch), so an unsynchronized buffer is race-safe.
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	registry := apiconfig.LoadRegistryFromEnv(logger)
	srv := NewServer(nil, "", registry, logger)

	_, err := srv.DispatchTool(context.Background(), "jira_add_comment", map[string]interface{}{
		"id":   "PROJ-1",
		"body": "looks good",
	})
	if err != nil {
		t.Fatalf("DispatchTool: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, `"api write"`) {
		t.Errorf("expected an 'api write' audit line, got: %s", out)
	}
	if !strings.Contains(out, "jira_add_comment") {
		t.Errorf("audit line missing the tool name, got: %s", out)
	}
	if !strings.Contains(out, "alice@example.com") {
		t.Errorf("audit line missing the audit_user, got: %s", out)
	}
}
