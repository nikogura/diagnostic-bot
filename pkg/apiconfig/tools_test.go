package apiconfig

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// permitAll is a WriteToolUsage permits predicate that allows every tool.
func permitAll(string) (ok bool) {
	ok = true
	return ok
}

func TestAPIToolRegistry_GetToolDefinitions(t *testing.T) {
	t.Parallel()

	configs := []*APIConfig{
		{
			Name:    "testapi",
			BaseURL: "https://example.com",
			Endpoints: []Endpoint{
				{Name: "list_items", Description: "List items", Path: "/items"},
				{Name: "get_item", Description: "Get item", Path: "/items/{id}", Params: []Param{
					{Name: "id", Type: "string", Required: true, In: "path"},
				}},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	registry := NewAPIToolRegistry(configs, nil, logger)

	tools := registry.GetToolDefinitions()

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	if tools[0].Name != "testapi_list_items" {
		t.Errorf("expected tool name 'testapi_list_items', got %q", tools[0].Name)
	}

	if tools[1].Name != "testapi_get_item" {
		t.Errorf("expected tool name 'testapi_get_item', got %q", tools[1].Name)
	}

	// Check that required param is in schema
	schema := tools[1].InputSchema
	requiredRaw, ok := schema["required"]
	if !ok {
		t.Fatal("expected 'required' in schema")
	}

	required, castOk := requiredRaw.([]string)
	if !castOk {
		t.Fatal("expected required to be []string")
	}

	if len(required) != 1 || required[0] != "id" {
		t.Errorf("expected required=['id'], got %v", required)
	}
}

func TestAPIToolRegistry_DispatchToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	t.Setenv("DISPATCH_TOKEN", "tok")

	configs := []*APIConfig{
		{
			Name:    "myapi",
			BaseURL: server.URL,
			Auth:    AuthConfig{Type: AuthTypeBearer, TokenEnv: "DISPATCH_TOKEN"},
			Endpoints: []Endpoint{
				{Name: "list", Method: "GET", Path: "/items"},
			},
			RateLimit: RateLimitConfig{MaxConcurrent: 5, MaxRetries: 1},
			Defaults:  DefaultsConfig{Limit: 25, MaxLimit: 100},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	registry := NewAPIToolRegistry(configs, nil, logger)

	result, handled, err := registry.DispatchToolCall(context.Background(), "myapi_list", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !handled {
		t.Error("expected handled=true")
	}

	if result != `{"items":[]}` {
		t.Errorf("unexpected result: %s", result)
	}

	// Unknown tool should not be handled
	_, handled, _ = registry.DispatchToolCall(context.Background(), "unknown_tool", map[string]interface{}{})
	if handled {
		t.Error("expected handled=false for unknown tool")
	}
}

func TestAPIToolRegistry_WriteToolUsage(t *testing.T) {
	t.Parallel()

	configs := []*APIConfig{
		{
			Name: "bitgo",
			Endpoints: []Endpoint{
				{Name: "list_wallets", Description: "List wallets"},
				{Name: "get_wallet", Description: "Get wallet details"},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	registry := NewAPIToolRegistry(configs, nil, logger)

	var builder strings.Builder
	registry.WriteToolUsage(&builder, permitAll)

	output := builder.String()

	if !strings.Contains(output, "bitgo API") {
		t.Errorf("expected 'bitgo API' in output, got: %s", output)
	}

	if !strings.Contains(output, "bitgo_list_wallets") {
		t.Errorf("expected 'bitgo_list_wallets' in output, got: %s", output)
	}

	if !strings.Contains(output, "bitgo_get_wallet") {
		t.Errorf("expected 'bitgo_get_wallet' in output, got: %s", output)
	}
}

// TestAPIToolRegistry_WriteToolUsage_Permits verifies the prose honors the
// permits gate — a denied endpoint is omitted, and an API whose every endpoint
// is denied prints no header at all — so the prose can never describe a tool the
// authz-filtered catalog withholds.
func TestAPIToolRegistry_WriteToolUsage_Permits(t *testing.T) {
	t.Parallel()

	configs := []*APIConfig{
		{
			Name: "bitgo",
			Endpoints: []Endpoint{
				{Name: "list_wallets", Description: "List wallets"},
				{Name: "get_wallet", Description: "Get wallet details"},
			},
		},
		{
			Name: "jira",
			Endpoints: []Endpoint{
				{Name: "get_issue", Description: "Get an issue"},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	registry := NewAPIToolRegistry(configs, nil, logger)

	// Permit only one bitgo endpoint; deny the other and all of jira.
	permits := func(name string) (ok bool) {
		ok = name == "bitgo_list_wallets"
		return ok
	}

	var builder strings.Builder
	registry.WriteToolUsage(&builder, permits)
	output := builder.String()

	if !strings.Contains(output, "bitgo_list_wallets") {
		t.Errorf("expected permitted tool 'bitgo_list_wallets' in output, got: %s", output)
	}
	if strings.Contains(output, "bitgo_get_wallet") {
		t.Errorf("denied tool 'bitgo_get_wallet' must not appear, got: %s", output)
	}
	// jira has no permitted endpoints, so its header must not appear.
	if strings.Contains(output, "jira API") {
		t.Errorf("fully-denied API 'jira' must print no header, got: %s", output)
	}
}

// TestLoadRegistryFromEnv is the regression test for the wiring bug: it proves
// LoadRegistryFromEnv reads API_CONFIG_DIR and produces a registry whose tools
// are present. Before the fix, apiconfig had no env-driven entry point at all.
func TestLoadRegistryFromEnv(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	t.Run("valid config yields tools", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `
name: testapi
description: "Test API"
base_url: https://api.example.com
auth:
  type: bearer
  token_env: LOADREG_TEST_TOKEN
endpoints:
  - name: list_items
    description: "List items"
    method: GET
    path: /api/v1/items
`
		writeErr := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(yamlContent), 0o644)
		if writeErr != nil {
			t.Fatalf("writing test config: %v", writeErr)
		}

		t.Setenv("LOADREG_TEST_TOKEN", "tok")
		t.Setenv("API_CONFIG_DIR", dir)

		registry := LoadRegistryFromEnv(logger)

		if !registry.HasTools() {
			t.Fatal("expected HasTools()=true for a dir with one valid config")
		}
		if len(registry.GetToolDefinitions()) == 0 {
			t.Fatal("expected non-empty tool definitions")
		}
	})

	t.Run("missing dir degrades to empty registry", func(t *testing.T) {
		t.Setenv("API_CONFIG_DIR", filepath.Join(t.TempDir(), "does-not-exist"))

		registry := LoadRegistryFromEnv(logger)

		if registry == nil {
			t.Fatal("expected a non-nil (empty) registry, got nil")
		}
		if registry.HasTools() {
			t.Error("expected HasTools()=false when the config dir is absent")
		}
	})
}

func TestAPIToolRegistry_HasTools(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	emptyRegistry := NewAPIToolRegistry(nil, nil, logger)
	if emptyRegistry.HasTools() {
		t.Error("expected HasTools()=false for empty registry")
	}

	withConfigs := NewAPIToolRegistry([]*APIConfig{{Name: "test", Endpoints: []Endpoint{{Name: "a", Path: "/a"}}}}, nil, logger)
	if !withConfigs.HasTools() {
		t.Error("expected HasTools()=true with configs")
	}
}

// toolNamesContain reports whether a tool of the given name is in the list.
func toolNamesContain(tools []MCPTool, name string) (found bool) {
	for _, tool := range tools {
		if tool.Name == name {
			found = true
			return found
		}
	}
	return found
}

func TestParseAllowedMethods(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want []string
		deny []string
	}{
		{name: "empty is GET only", raw: "", want: []string{"GET"}, deny: []string{"POST", "DELETE"}},
		{name: "GET always present", raw: "POST", want: []string{"GET", "POST"}, deny: []string{"DELETE"}},
		{name: "comma separated", raw: "POST,PATCH", want: []string{"GET", "POST", "PATCH"}, deny: []string{"DELETE"}},
		{name: "newline block scalar", raw: "POST\nPATCH\nDELETE\n", want: []string{"GET", "POST", "PATCH", "DELETE"}},
		{name: "lowercase normalized", raw: "post", want: []string{"GET", "POST"}, deny: []string{"DELETE"}},
		{name: "mixed commas and whitespace", raw: "post,  put \n patch", want: []string{"GET", "POST", "PUT", "PATCH"}, deny: []string{"DELETE"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			allowed := parseAllowedMethods(tc.raw)
			for _, m := range tc.want {
				if !allowed[m] {
					t.Errorf("method %q should be allowed for input %q", m, tc.raw)
				}
			}
			for _, m := range tc.deny {
				if allowed[m] {
					t.Errorf("method %q should NOT be allowed for input %q", m, tc.raw)
				}
			}
		})
	}
}

func TestAPIToolRegistry_MethodAllowlist(t *testing.T) {
	t.Parallel()

	configs := []*APIConfig{
		{
			Name: "jira",
			Endpoints: []Endpoint{
				{Name: "get_issue", Method: "GET", Path: "/issue/{id}"},
				{Name: "add_comment", Method: "POST", Path: "/issue/{id}/comment"},
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// GET-only (nil allowlist): the POST tool is withheld and classified write.
	getOnly := NewAPIToolRegistry(configs, nil, logger)
	if !toolNamesContain(getOnly.GetToolDefinitions(), "jira_get_issue") {
		t.Error("GET tool must be present under GET-only")
	}
	if toolNamesContain(getOnly.GetToolDefinitions(), "jira_add_comment") {
		t.Error("POST tool must be withheld under GET-only")
	}
	if !getOnly.IsWriteTool("jira_add_comment") {
		t.Error("POST tool must classify as a write")
	}
	if getOnly.IsWriteTool("jira_get_issue") {
		t.Error("GET tool must not classify as a write")
	}

	// Dispatch of a withheld-method tool is rejected (defense in depth).
	_, handled, err := getOnly.DispatchToolCall(context.Background(), "jira_add_comment", map[string]interface{}{"id": "PROJ-1"})
	if !handled || err == nil {
		t.Errorf("withheld POST dispatch: handled=%v err=%v; want handled=true, err!=nil", handled, err)
	}

	// POST allowed: the tool now appears.
	withPost := NewAPIToolRegistry(configs, map[string]bool{http.MethodGet: true, http.MethodPost: true}, logger)
	if !toolNamesContain(withPost.GetToolDefinitions(), "jira_add_comment") {
		t.Error("POST tool must be present when POST is allowed")
	}
}
