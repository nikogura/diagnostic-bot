package apiconfig

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigs_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	yamlContent := `
name: testapi
description: "Test API"
base_url: https://api.example.com
auth:
  type: bearer
  token_env: TEST_API_TOKEN
endpoints:
  - name: list_items
    description: "List items"
    method: GET
    path: /api/v1/items
    params:
      - name: page
        type: integer
        description: "Page number"
`

	err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(yamlContent), 0o644)
	if err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	t.Setenv("TEST_API_TOKEN", "test-token-value")

	configs, loadErr := LoadConfigs(dir, logger)
	if loadErr != nil {
		t.Fatalf("LoadConfigs() error: %v", loadErr)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	config := configs[0]
	if config.Name != "testapi" {
		t.Errorf("expected name 'testapi', got %q", config.Name)
	}

	if config.BaseURL != "https://api.example.com" {
		t.Errorf("expected base_url 'https://api.example.com', got %q", config.BaseURL)
	}

	if len(config.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(config.Endpoints))
	}

	if config.Endpoints[0].Method != http.MethodGet {
		t.Errorf("expected method GET, got %q", config.Endpoints[0].Method)
	}
}

func TestLoadConfigs_MissingTokenSkips(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	yamlContent := `
name: skippedapi
base_url: https://api.example.com
auth:
  type: bearer
  token_env: MISSING_TOKEN_VAR
endpoints:
  - name: test
    path: /test
`

	err := os.WriteFile(filepath.Join(dir, "skipped.yaml"), []byte(yamlContent), 0o644)
	if err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	configs, loadErr := LoadConfigs(dir, logger)
	if loadErr != nil {
		t.Fatalf("LoadConfigs() error: %v", loadErr)
	}

	if len(configs) != 0 {
		t.Errorf("expected 0 configs (token not set), got %d", len(configs))
	}
}

func TestLoadConfigs_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	configs, err := LoadConfigs(dir, logger)
	if err != nil {
		t.Fatalf("LoadConfigs() error: %v", err)
	}

	if len(configs) != 0 {
		t.Errorf("expected 0 configs from empty dir, got %d", len(configs))
	}
}

func TestLoadConfigs_NonexistentDir(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	configs, err := LoadConfigs("/nonexistent/path", logger)
	if err != nil {
		t.Fatalf("LoadConfigs() should not error on missing dir: %v", err)
	}

	if len(configs) != 0 {
		t.Errorf("expected 0 configs, got %d", len(configs))
	}
}

func TestApplyDefaults(t *testing.T) {
	t.Parallel()

	config := &APIConfig{
		Endpoints: []Endpoint{
			{Name: "test", Path: "/test", Params: []Param{{Name: "id"}}},
		},
	}

	applyDefaults(config)

	if config.RateLimit.MaxConcurrent != 5 {
		t.Errorf("expected MaxConcurrent=5, got %d", config.RateLimit.MaxConcurrent)
	}

	if config.RateLimit.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", config.RateLimit.MaxRetries)
	}

	if config.Defaults.Limit != 25 {
		t.Errorf("expected Limit=25, got %d", config.Defaults.Limit)
	}

	if config.Endpoints[0].Method != http.MethodGet {
		t.Errorf("expected default method GET, got %q", config.Endpoints[0].Method)
	}

	if config.Endpoints[0].Params[0].In != "query" {
		t.Errorf("expected default param.In=query, got %q", config.Endpoints[0].Params[0].In)
	}

	if config.Endpoints[0].Params[0].Type != "string" {
		t.Errorf("expected default param.Type=string, got %q", config.Endpoints[0].Params[0].Type)
	}
}

func TestValidateConfig_MissingName(t *testing.T) {
	t.Parallel()

	config := &APIConfig{BaseURL: "https://example.com", Endpoints: []Endpoint{{Name: "a", Path: "/a"}}}
	err := validateConfig(config)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestValidateConfig_MissingBaseURL(t *testing.T) {
	t.Parallel()

	config := &APIConfig{Name: "test", Endpoints: []Endpoint{{Name: "a", Path: "/a"}}}
	err := validateConfig(config)
	if err == nil {
		t.Error("expected error for missing base_url")
	}
}

func TestValidateConfig_NoEndpoints(t *testing.T) {
	t.Parallel()

	config := &APIConfig{Name: "test", BaseURL: "https://example.com"}
	err := validateConfig(config)
	if err == nil {
		t.Error("expected error for no endpoints")
	}
}

func TestValidateConfig_RejectsUnknownMethod(t *testing.T) {
	config := &APIConfig{
		Name:    "bad",
		BaseURL: "https://api.example.com",
		Endpoints: []Endpoint{
			{Name: "weird", Path: "/w", Method: "FROBNICATE"},
		},
	}

	err := validateConfig(config)
	if err == nil {
		t.Error("expected validateConfig to reject an unsupported HTTP method")
	}
}

func TestApplyDefaults_UppercasesMethod(t *testing.T) {
	config := &APIConfig{
		Name:    "api",
		BaseURL: "https://api.example.com",
		Endpoints: []Endpoint{
			{Name: "a", Path: "/a", Method: "post"},
			{Name: "b", Path: "/b"}, // empty -> GET
		},
	}

	applyDefaults(config)

	if config.Endpoints[0].Method != http.MethodPost {
		t.Errorf("method not normalized to upper: %q", config.Endpoints[0].Method)
	}
	if config.Endpoints[1].Method != http.MethodGet {
		t.Errorf("empty method not defaulted to GET: %q", config.Endpoints[1].Method)
	}
}
