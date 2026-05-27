package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nikogura/diagnostic-bot/pkg/k8s"
	"github.com/stretchr/testify/require"
)

func TestGetGitHubTools(t *testing.T) {
	t.Parallel()

	tools := getGitHubTools()

	// Should have 3 GitHub tools
	expectedCount := 3
	if len(tools) != expectedCount {
		t.Fatalf("getGitHubTools() returned %d tools, want %d", len(tools), expectedCount)
	}

	// Check tool names
	expectedTools := map[string]bool{
		"github_get_file":       false,
		"github_list_directory": false,
		"github_search_code":    false,
	}

	for _, tool := range tools {
		if _, exists := expectedTools[tool.Name]; exists {
			expectedTools[tool.Name] = true
		}
	}

	for name, found := range expectedTools {
		if !found {
			t.Errorf("Expected tool %s not found in getGitHubTools()", name)
		}
	}
}

func TestGetLokiTools(t *testing.T) {
	t.Parallel()

	tools := getLokiTools(nil)

	// Should have 1 Loki tool
	if len(tools) != 1 {
		t.Fatalf("getLokiTools() returned %d tools, want 1", len(tools))
	}

	if tools[0].Name != "query_loki" {
		t.Errorf("getLokiTools() tool name = %s, want query_loki", tools[0].Name)
	}
}

// TestGetLokiToolsExposesTenantArgWhenAllowlistSet verifies the query_loki
// schema gains a tenant field and the tool description advertises the
// allowed tenants so the calling LLM can discover valid values without
// guessing.
func TestGetLokiToolsExposesTenantArgWhenAllowlistSet(t *testing.T) {
	t.Parallel()

	tools := getLokiTools([]string{"monitoring", "cloudtrail", "self-monitoring"})
	require.Len(t, tools, 1)

	require.Contains(t, tools[0].Description, "monitoring")
	require.Contains(t, tools[0].Description, "cloudtrail")
	require.Contains(t, tools[0].Description, "self-monitoring")

	props, ok := tools[0].InputSchema["properties"].(map[string]interface{})
	require.True(t, ok, "schema must have properties")
	require.Contains(t, props, "tenant", "tenant arg must appear in schema")

	// tenant must NOT be in the required list — it's optional and defaults
	// to the server-configured default tenant when omitted.
	required, _ := tools[0].InputSchema["required"].([]string)
	require.NotContains(t, required, "tenant")
}

// TestGetLokiToolsOmitsTenantDescriptionWhenNoAllowlist verifies that
// single-tenant deployments (no allowlist) don't get a noisy "Allowed
// tenants:" footer in the tool description.
func TestGetLokiToolsOmitsTenantDescriptionWhenNoAllowlist(t *testing.T) {
	t.Parallel()

	tools := getLokiTools(nil)
	require.Len(t, tools, 1)
	require.NotContains(t, tools[0].Description, "Allowed tenants")
}

// TestExecuteQueryLokiPassesTenantToBackend verifies the executor reads the
// tenant arg from the MCP call and forwards it to the LokiClient, which in
// turn sets X-Scope-OrgID on the outgoing request.
func TestExecuteQueryLokiPassesTenantToBackend(t *testing.T) {
	t.Parallel()

	var capturedTenant string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTenant = r.Header.Get(k8s.LokiTenantHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[],"stats":{}}}`))
	}))
	t.Cleanup(server.Close)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	lokiClient := k8s.NewLokiClient(server.URL, logger)
	configErr := lokiClient.ConfigureTenants("monitoring", []string{"monitoring", "cloudtrail"})
	require.NoError(t, configErr)

	mcpServer := NewServer(lokiClient, "", nil, logger)

	args := map[string]interface{}{
		"query":  `{job="test"}`,
		"start":  "1h",
		"tenant": "cloudtrail",
	}
	_, err := mcpServer.executeQueryLoki(t.Context(), args)
	require.NoError(t, err)
	require.Equal(t, "cloudtrail", capturedTenant)
}

func TestGetUtilityTools(t *testing.T) {
	t.Parallel()

	tools := getUtilityTools()

	// Should have 2 utility tools
	expectedCount := 2
	if len(tools) != expectedCount {
		t.Fatalf("getUtilityTools() returned %d tools, want %d", len(tools), expectedCount)
	}

	expectedTools := map[string]bool{
		"whois_lookup": false,
		"generate_pdf": false,
	}

	for _, tool := range tools {
		if _, exists := expectedTools[tool.Name]; exists {
			expectedTools[tool.Name] = true
		}
	}

	for name, found := range expectedTools {
		if !found {
			t.Errorf("Expected tool %s not found in getUtilityTools()", name)
		}
	}
}

func TestGetToolDefinitionsMinimalServer(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	// Clear env vars that affect tool registration
	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "")
	t.Setenv("CLOUDWATCH_ACCOUNTS", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	// Server with only Loki client, no other services
	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	tools := server.getToolDefinitions()

	// Should have Loki (1) + Utility (2) = 3 tools minimum
	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	// Loki tools should be present (lokiClient is non-nil)
	require.True(t, toolMap["query_loki"], "Loki tool should be present when lokiClient is set")

	// Utility tools should always be present
	require.True(t, toolMap["whois_lookup"], "whois_lookup should always be present")
	require.True(t, toolMap["generate_pdf"], "generate_pdf should always be present")

	// GitHub tools should NOT be present (no token)
	require.False(t, toolMap["github_get_file"], "GitHub tools should not be present without token")

	// Database tools should NOT be present (no DATABASE_URL)
	require.False(t, toolMap["database_query"], "Database tools should not be present without config")
}

func TestGetToolDefinitionsWithGitHub(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	// Clear env vars
	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "")
	t.Setenv("CLOUDWATCH_ACCOUNTS", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "test-token", nil, logger)

	tools := server.getToolDefinitions()

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	// GitHub tools should be present
	require.True(t, toolMap["github_get_file"], "GitHub tools should be present with token")
	require.True(t, toolMap["github_list_directory"], "GitHub list directory should be present")
	require.True(t, toolMap["github_search_code"], "GitHub search should be present")
}

func TestGetToolDefinitionsWithCloudWatch(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "arn:aws:iam::123456789012:role/test")
	t.Setenv("CLOUDWATCH_ACCOUNTS", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	tools := server.getToolDefinitions()

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	require.True(t, toolMap["cloudwatch_logs_query"], "CloudWatch query should be present with CLOUDWATCH_ASSUME_ROLE")
	require.True(t, toolMap["cloudwatch_logs_list_groups"], "CloudWatch list groups should be present")
	require.True(t, toolMap["cloudwatch_logs_get_events"], "CloudWatch get events should be present")
}

func TestGetToolDefinitionsWithCloudWatchAccounts(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	// Set multi-account config, no legacy role
	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "")
	t.Setenv("CLOUDWATCH_ACCOUNTS", `{"dev":"arn:aws:iam::111111111111:role/dev-role","prod":"arn:aws:iam::222222222222:role/prod-role"}`)
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	tools := server.getToolDefinitions()

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	require.True(t, toolMap["cloudwatch_logs_query"], "CloudWatch query should be present with CLOUDWATCH_ACCOUNTS")
	require.True(t, toolMap["cloudwatch_logs_list_groups"], "CloudWatch list groups should be present")
	require.True(t, toolMap["cloudwatch_logs_get_events"], "CloudWatch get events should be present")
}

func TestGetToolDefinitionsWithoutCloudWatch(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "")
	t.Setenv("CLOUDWATCH_ACCOUNTS", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	tools := server.getToolDefinitions()

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	require.False(t, toolMap["cloudwatch_logs_query"], "CloudWatch should not be present without CLOUDWATCH_ASSUME_ROLE")
}

func TestGetToolDefinitionsWithECR(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "")
	t.Setenv("CLOUDWATCH_ACCOUNTS", "")

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	tools := server.getToolDefinitions()

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	require.True(t, toolMap["ecr_scan_results"], "ECR tool should be present with AWS_REGION")
}

func TestGetToolDefinitionsUtilityToolsAlwaysPresent(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	// Clear everything
	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "")
	t.Setenv("CLOUDWATCH_ACCOUNTS", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	// Server with nil lokiClient
	server := &Server{
		logger: logger,
	}

	tools := server.getToolDefinitions()

	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}

	require.True(t, toolMap["whois_lookup"], "whois_lookup should always be present")
	require.True(t, toolMap["generate_pdf"], "generate_pdf should always be present")
	require.Len(t, tools, 2, "Only utility tools should be present when nothing is configured")
}

func TestExecuteGitHubGetFileWithoutToken(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger) // Empty GitHub token

	ctx := context.Background()
	args := map[string]interface{}{
		"owner": "your-org",
		"repo":  "test-repo",
		"path":  "README.md",
	}

	result, err := server.executeGitHubGetFile(ctx, args)

	// Should return error when GitHub client not configured
	if err == nil {
		t.Error("executeGitHubGetFile() without token should return error, got nil")
	}

	if !strings.Contains(err.Error(), "GitHub access not configured") {
		t.Errorf("executeGitHubGetFile() error = %v, want error containing 'GitHub access not configured'", err)
	}

	if result != "" {
		t.Errorf("executeGitHubGetFile() result = %q, want empty string", result)
	}
}

func TestExecuteGitHubListDirectoryWithoutToken(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	ctx := context.Background()
	args := map[string]interface{}{
		"owner": "your-org",
		"repo":  "test-repo",
		"path":  "db/migrations",
	}

	result, err := server.executeGitHubListDirectory(ctx, args)

	// Should return error when GitHub client not configured
	if err == nil {
		t.Error("executeGitHubListDirectory() without token should return error, got nil")
	}

	if !strings.Contains(err.Error(), "GitHub access not configured") {
		t.Errorf("executeGitHubListDirectory() error = %v, want error containing 'GitHub access not configured'", err)
	}

	if result != "" {
		t.Errorf("executeGitHubListDirectory() result = %q, want empty string", result)
	}
}

func TestExecuteGitHubSearchCodeWithoutToken(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	ctx := context.Background()
	args := map[string]interface{}{
		"query": "table users repo:your-org/test-repo",
	}

	result, err := server.executeGitHubSearchCode(ctx, args)

	// Should return error when GitHub client not configured
	if err == nil {
		t.Error("executeGitHubSearchCode() without token should return error, got nil")
	}

	if !strings.Contains(err.Error(), "GitHub access not configured") {
		t.Errorf("executeGitHubSearchCode() error = %v, want error containing 'GitHub access not configured'", err)
	}

	if result != "" {
		t.Errorf("executeGitHubSearchCode() result = %q, want empty string", result)
	}
}

func TestNewServerWithGitHubToken(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "test-token", nil, logger)

	require.NotNil(t, server, "NewServer() returned nil")
	require.NotNil(t, server.githubClient, "NewServer() with GitHub token should initialize githubClient")
	require.NotNil(t, server.lokiClient, "NewServer() should have lokiClient")
}

func TestNewServerWithoutGitHubToken(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	lokiClient := k8s.NewLokiClient("http://dummy:3100", logger)
	server := NewServer(lokiClient, "", nil, logger)

	require.NotNil(t, server, "NewServer() returned nil")
	require.Nil(t, server.githubClient, "NewServer() without GitHub token should not initialize githubClient")
	require.NotNil(t, server.lokiClient, "NewServer() should have lokiClient")
}

// verifyRequiredFields checks that all required fields exist in the properties map.
func verifyRequiredFields(t *testing.T, toolName string, propsMap map[string]interface{}, fields []string) {
	t.Helper()

	for _, field := range fields {
		if _, exists := propsMap[field]; !exists {
			t.Errorf("Tool %s missing required field %s in properties", toolName, field)
		}
	}
}

// validateToolSchema checks basic schema structure for a tool.
// Returns the properties map and a boolean indicating if validation passed.
func validateToolSchema(t *testing.T, tool MCPTool) (result map[string]interface{}, valid bool) {
	t.Helper()

	schema := tool.InputSchema

	// Check for required fields
	schemaType, hasType := schema["type"]
	if !hasType || schemaType != "object" {
		t.Errorf("Tool %s InputSchema missing type=object", tool.Name)
		valid = false
		return result, valid
	}

	properties, hasProperties := schema["properties"]
	if !hasProperties {
		t.Errorf("Tool %s InputSchema missing properties", tool.Name)
		valid = false
		return result, valid
	}

	propsMap, ok := properties.(map[string]interface{})
	if !ok {
		t.Errorf("Tool %s InputSchema properties is not a map", tool.Name)
		valid = false
		return result, valid
	}

	result = propsMap
	valid = true
	return result, valid
}

func TestGitHubToolInputSchemas(t *testing.T) {
	t.Parallel()

	tools := getGitHubTools()

	for _, tool := range tools {
		propsMap, valid := validateToolSchema(t, tool)
		if !valid {
			continue
		}

		// Verify tool-specific required fields
		switch tool.Name {
		case "github_get_file", "github_list_directory":
			verifyRequiredFields(t, tool.Name, propsMap, []string{"owner", "repo", "path"})

		case "github_search_code":
			verifyRequiredFields(t, tool.Name, propsMap, []string{"query"})
		}
	}
}

func TestGrafanaCreateDashboardToolSchemaIncludesInfinity(t *testing.T) {
	t.Parallel()

	tool := getGrafanaCreateDashboardTool()

	require.Equal(t, toolGrafanaCreateDashboard, tool.Name)

	// Navigate to the panels items -> properties -> datasourceType -> enum
	panels, ok := tool.InputSchema["properties"].(map[string]interface{})["panels"].(map[string]interface{})
	require.True(t, ok, "panels property should exist")

	items, ok := panels["items"].(map[string]interface{})
	require.True(t, ok, "panels items should exist")

	props, ok := items["properties"].(map[string]interface{})
	require.True(t, ok, "panel properties should exist")

	dsType, ok := props["datasourceType"].(map[string]interface{})
	require.True(t, ok, "datasourceType property should exist")

	enumValues, ok := dsType["enum"].([]string)
	require.True(t, ok, "datasourceType enum should be []string")
	require.Contains(t, enumValues, "yesoreyeram-infinity-datasource",
		"datasourceType enum should include yesoreyeram-infinity-datasource")

	// Verify Infinity-specific properties exist
	infinityFields := []string{
		"infinityQueryType", "infinityParser", "infinitySource",
		"infinityUrl", "infinityMethod", "infinityBody",
		"infinityRootSelector", "infinityColumns",
	}
	for _, field := range infinityFields {
		_, exists := props[field]
		require.True(t, exists, "panel properties should include %s", field)
	}
}

func TestParseSinglePanelConfigInfinity(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	server := &Server{logger: logger}

	panelMap := map[string]interface{}{
		"title":                "Wiz Critical Vulns",
		"panelType":            "table",
		"datasourceType":       "yesoreyeram-infinity-datasource",
		"datasourceUID":        "wiz-infinity",
		"infinityQueryType":    "graphql",
		"infinityParser":       "backend",
		"infinitySource":       "url",
		"infinityUrl":          "https://api.wiz.io/graphql",
		"infinityMethod":       "POST",
		"infinityBody":         `{"query": "{ issues { id severity } }"}`,
		"infinityRootSelector": "data.issues",
		"infinityColumns": []interface{}{
			map[string]interface{}{
				"selector": "id",
				"text":     "Issue ID",
				"type":     "string",
			},
			map[string]interface{}{
				"selector": "severity",
				"text":     "Severity",
				"type":     "string",
			},
		},
	}

	panel := server.parseSinglePanelConfig(panelMap)

	require.Equal(t, "Wiz Critical Vulns", panel.Title)
	require.Equal(t, "table", panel.PanelType)
	require.Equal(t, "yesoreyeram-infinity-datasource", panel.DatasourceType)
	require.Equal(t, "wiz-infinity", panel.DatasourceUID)
	require.Equal(t, "graphql", panel.InfinityQueryType)
	require.Equal(t, "backend", panel.InfinityParser)
	require.Equal(t, "url", panel.InfinitySource)
	require.Equal(t, "https://api.wiz.io/graphql", panel.InfinityURL)
	require.Equal(t, "POST", panel.InfinityMethod)
	require.JSONEq(t, `{"query": "{ issues { id severity } }"}`, panel.InfinityBody)
	require.Equal(t, "data.issues", panel.InfinityRootSelector)
	require.Len(t, panel.InfinityColumns, 2)
	require.Equal(t, "id", panel.InfinityColumns[0].Selector)
	require.Equal(t, "Issue ID", panel.InfinityColumns[0].Text)
	require.Equal(t, "string", panel.InfinityColumns[0].Type)
	require.Equal(t, "severity", panel.InfinityColumns[1].Selector)
}

// TestParsePanelConfigsRequiresDatasourceUID verifies that panels missing
// an explicit datasourceUID are rejected. Previously the schema advertised
// a "postgres-main" default that the parser never applied, so non-Postgres
// panels silently shipped with datasource.uid="" — Grafana cannot route
// queries without a UID.
func TestParsePanelConfigsRequiresDatasourceUID(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	server := &Server{logger: logger}

	panelsRaw := []interface{}{
		map[string]interface{}{
			"title":          "Prom Panel With No UID",
			"panelType":      "timeseries",
			"datasourceType": "prometheus",
			"query":          "up",
		},
	}

	_, err := server.parsePanelConfigs(panelsRaw)
	require.Error(t, err, "panels without datasourceUID must be rejected")
	require.Contains(t, err.Error(), "datasourceUID")
}

// TestGetGrafanaToolsIncludesCreateFolder verifies the create folder tool is
// registered alongside the other Grafana write tools.
func TestGetGrafanaToolsIncludesCreateFolder(t *testing.T) {
	t.Parallel()

	tools := getGrafanaTools()

	var createFolder *MCPTool
	for i := range tools {
		if tools[i].Name == toolGrafanaCreateFolder {
			createFolder = &tools[i]
			break
		}
	}
	require.NotNil(t, createFolder, "grafana_create_folder tool must be registered")

	props, ok := createFolder.InputSchema["properties"].(map[string]interface{})
	require.True(t, ok, "create_folder schema must have properties")
	require.Contains(t, props, "title")
	require.Contains(t, props, "uid")
	require.Contains(t, props, "parentUid")

	required, ok := createFolder.InputSchema["required"].([]string)
	require.True(t, ok, "create_folder schema must declare required fields as []string")
	require.Contains(t, required, "title")
}

func TestValidatePDFEngine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		engine  string
		wantErr bool
	}{
		{"pdflatex allowed", "pdflatex", false},
		{"xelatex allowed", "xelatex", false},
		{"lualatex allowed", "lualatex", false},
		{"tectonic allowed", "tectonic", false},
		{"wkhtmltopdf allowed", "wkhtmltopdf", false},
		{"weasyprint allowed", "weasyprint", false},
		{"prince allowed", "prince", false},
		{"context allowed", "context", false},
		{"bash rejected", "bash", true},
		{"empty rejected", "", true},
		{"path injection rejected", "/bin/sh", true},
		{"command injection rejected", "pdflatex; rm -rf /", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePDFEngine(tt.engine)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidatePDFTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"app path allowed", "/app/latex-templates/company.latex", false},
		{"etc path allowed", "/etc/pandoc/template.latex", false},
		{"tmp path allowed", "/tmp/report-template.latex", false},
		{"home path rejected", "/home/user/evil.latex", true},
		{"relative path rejected", "../../../etc/passwd", true},
		{"root path rejected", "/root/template.latex", true},
		{"empty path rejected", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePDFTemplate(tt.path)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetPDFEngineDefault(t *testing.T) {
	t.Setenv("PDF_ENGINE", "")
	require.Equal(t, "pdflatex", getPDFEngine())
}

func TestGetPDFEngineOverride(t *testing.T) {
	t.Setenv("PDF_ENGINE", "xelatex")
	require.Equal(t, "xelatex", getPDFEngine())
}

func TestGetPDFTemplateDefault(t *testing.T) {
	t.Setenv("PDF_TEMPLATE", "")
	require.Equal(t, "/app/latex-templates/company-template.latex", getPDFTemplate())
}

func TestGetPDFTemplateOverride(t *testing.T) {
	t.Setenv("PDF_TEMPLATE", "/app/custom/nxdoc.latex")
	require.Equal(t, "/app/custom/nxdoc.latex", getPDFTemplate())
}

// TestResolveAuditUserHonorsEnvVar verifies MCP_AUDIT_USER overrides the OS user.
// This is the explicit-override path — used in containers, CI, or any deployment
// where the process owner isn't the right identity to stamp on Grafana writes.
func TestResolveAuditUserHonorsEnvVar(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	t.Setenv("MCP_AUDIT_USER", "alice@example.com")
	require.Equal(t, "alice@example.com", resolveAuditUser(logger))
}

// TestResolveAuditUserFallsBackToOSUser verifies that with no env override
// the local OS user is used. For Claude Code running the MCP server over
// stdio, that resolves to the developer's username automatically.
func TestResolveAuditUserFallsBackToOSUser(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	t.Setenv("MCP_AUDIT_USER", "")
	got := resolveAuditUser(logger)
	require.NotEmpty(t, got)
	require.NotEqual(t, "mcp-server", got, "must resolve to actual OS user, not the fallback")
}

// TestComposeVersionNoteWithIntention verifies the Grafana version-history
// string is "<user>: <intention>" so audit trails surface both who and why.
func TestComposeVersionNoteWithIntention(t *testing.T) {
	require.Equal(t, "alice: adding ECS dashboard", composeVersionNote("alice", "adding ECS dashboard", "fallback"))
}

// TestComposeVersionNoteFallsBackToDefaultIntention verifies that when the
// LLM omits a message, a tool-specific default still names the user.
func TestComposeVersionNoteFallsBackToDefaultIntention(t *testing.T) {
	require.Equal(t, "alice: created via mcp", composeVersionNote("alice", "", "created via mcp"))
}

// TestWithAuditUserContextOverridesServerDefault verifies the bot path:
// the bot wraps each investigation's ctx with the Slack user, the MCP handler
// reads that for the audit identity instead of the server-startup default.
func TestWithAuditUserContextOverridesServerDefault(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	t.Setenv("MCP_AUDIT_USER", "server-default")
	server := NewServer(nil, "", nil, logger)

	require.Equal(t, "server-default", server.auditUserFromContext(context.Background()))

	ctx := WithAuditUser(context.Background(), "slack-user-bob")
	require.Equal(t, "slack-user-bob", server.auditUserFromContext(ctx))
}

// TestExecuteGrafanaCreateDashboardStampsAuditUserAndIntention verifies a
// create operation lands in Grafana's version history with both who (audit
// user) and why (LLM-supplied message) composed into the Message field.
func TestExecuteGrafanaCreateDashboardStampsAuditUserAndIntention(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var captured DashboardSaveRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"id":1,"uid":"dash-uid","url":"/d/dash-uid/test","status":"success","version":1}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)

	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	args := map[string]interface{}{
		"title": "ECS Dashboard",
		"panels": []interface{}{
			map[string]interface{}{
				"title":          "CPU",
				"panelType":      "timeseries",
				"datasourceType": "prometheus",
				"datasourceUID":  "prom-prod",
				"query":          "rate(cpu_seconds_total[5m])",
			},
		},
		"message": "adding ECS CPU dashboard for new service",
	}

	_, err = mcpServer.executeGrafanaCreateDashboard(t.Context(), args)
	require.NoError(t, err)
	require.Equal(t, "alice: adding ECS CPU dashboard for new service", captured.Message)
}

// TestExecuteGrafanaCreateDashboardUsesDefaultIntentionWhenOmitted verifies
// the LLM can omit the message and the version note still names the user
// with a sensible default — the old hardcoded "Auto-generated dashboard: …"
// dropped the human-context entirely; this preserves attribution.
func TestExecuteGrafanaCreateDashboardUsesDefaultIntentionWhenOmitted(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var captured DashboardSaveRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"id":1,"uid":"dash-uid","url":"/d/dash-uid/test","status":"success","version":1}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)

	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	args := map[string]interface{}{
		"title": "ECS Dashboard",
		"panels": []interface{}{
			map[string]interface{}{
				"title":          "CPU",
				"panelType":      "timeseries",
				"datasourceType": "prometheus",
				"datasourceUID":  "prom-prod",
				"query":          "up",
			},
		},
	}

	_, err = mcpServer.executeGrafanaCreateDashboard(t.Context(), args)
	require.NoError(t, err)
	require.Contains(t, captured.Message, "alice", "audit user must always appear in the version note")
	require.NotContains(t, captured.Message, "Auto-generated dashboard:", "the old hardcoded message must not appear")
}

// TestExecuteGrafanaUpdateDashboardStampsAuditUserAndIntention verifies the
// update path composes the same "<user>: <intention>" format. The previous
// default "Updated via MCP" left no attribution.
func TestExecuteGrafanaUpdateDashboardStampsAuditUserAndIntention(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var captured DashboardSaveRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the POST to /api/dashboards/db carries the save payload we care about.
		if r.Method == http.MethodPost && r.URL.Path == "/api/dashboards/db" {
			_ = json.NewDecoder(r.Body).Decode(&captured)
		}
		_, _ = w.Write([]byte(`{"id":1,"uid":"dash-uid","url":"/d/dash-uid/test","status":"success","version":2}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)

	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	args := map[string]interface{}{
		"uid": "dash-uid",
		"dashboard": map[string]interface{}{
			"uid":   "dash-uid",
			"title": "New",
		},
		"message":   "renamed for clarity",
		"folderUid": "some-folder",
	}

	_, err = mcpServer.executeGrafanaUpdateDashboard(t.Context(), args)
	require.NoError(t, err)
	require.Equal(t, "alice: renamed for clarity", captured.Message)
}

// TestExecuteGrafanaDeleteDashboardLogsAuditUserAndIntention verifies the
// delete path — Grafana's DELETE has no audit body, so slog is the only
// place attribution lands. The bot's stdout collector ships these into Loki.
func TestExecuteGrafanaDeleteDashboardLogsAuditUserAndIntention(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"Dashboard deleted","id":1}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)

	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	args := map[string]interface{}{
		"uid":     "dash-uid",
		"message": "obsolete after migration",
	}

	_, err = mcpServer.executeGrafanaDeleteDashboard(t.Context(), args)
	require.NoError(t, err)

	logOutput := buf.String()
	require.Contains(t, logOutput, `"audit_user":"alice"`)
	require.Contains(t, logOutput, `"message":"alice: obsolete after migration"`, "logged message is the composed version note that would have landed in Grafana")
	require.Contains(t, logOutput, `"uid":"dash-uid"`)
	require.Contains(t, logOutput, `"tool":"grafana_delete_dashboard"`)
	require.Contains(t, logOutput, `"audit_source_ip":"stdio"`, "no HTTP request → stdio default")
}

// TestExecuteGrafanaCreateFolderLogsAuditUserAndIntention verifies the
// folder path. Grafana's folder API doesn't persist a version note, so slog
// is the audit surface here too.
func TestExecuteGrafanaCreateFolderLogsAuditUserAndIntention(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":12,"uid":"ops-uid","title":"Operations","url":"/dashboards/f/ops-uid/operations","version":1}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)

	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	args := map[string]interface{}{
		"title":   "Operations",
		"message": "grouping ops dashboards",
	}

	_, err = mcpServer.executeGrafanaCreateFolder(t.Context(), args)
	require.NoError(t, err)

	logOutput := buf.String()
	require.Contains(t, logOutput, `"audit_user":"alice"`)
	require.Contains(t, logOutput, `"message":"alice: grouping ops dashboards"`)
	require.Contains(t, logOutput, `"tool":"grafana_create_folder"`)
	require.Contains(t, logOutput, `"audit_source_ip":"stdio"`)
}

// TestExecuteGrafanaGetDashboardEmitsUnmodeledFields is the regression
// guard for the lossy-typed-unmarshal bug: Grafana panel/target fields
// the bot's structs don't model (expression, period, accountId,
// matchExact, legendFormat, etc.) must survive a GET intact. Before the
// fix, GetDashboard unmarshaled into a closed Dashboard{} and dropped
// everything unmodeled, which silently degraded any non-trivial panel
// on every grafana_update_dashboard cycle.
func TestExecuteGrafanaGetDashboardEmitsUnmodeledFields(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Fixture contains a CloudWatch math-expression target with fields the
	// Target struct doesn't model. The test fails closed: any of these
	// strings missing from the executor output means data loss.
	fixture := `{
		"dashboard": {
			"uid": "dash-uid",
			"title": "Services",
			"panels": [{
				"id": 4,
				"type": "timeseries",
				"title": "Request Rate",
				"targets": [{
					"refId": "A",
					"datasource": {"type": "cloudwatch", "uid": "cw-prod"},
					"namespace": "AWS/ApplicationELB",
					"metricName": "RequestCount",
					"statistic": "Sum",
					"period": "60",
					"accountId": "111122223333",
					"matchExact": false,
					"dimensions": {"LoadBalancer": "app/prod"},
					"id": "m1"
				}, {
					"refId": "B",
					"datasource": {"type": "cloudwatch", "uid": "cw-prod"},
					"expression": "m1/60",
					"label": "RPS",
					"id": "rps"
				}]
			}],
			"annotations": {"list": [{"name": "deploys"}]},
			"refresh": "30s"
		},
		"meta": {"folderUid": "ops", "version": 7}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	out, err := mcpServer.executeGrafanaGetDashboard(t.Context(), map[string]interface{}{"uid": "dash-uid"})
	require.NoError(t, err)

	// Every one of these is unmodeled in the typed structs. Their absence
	// from the executor's output is the bug.
	for _, marker := range []string{
		`"expression"`,
		`"period"`,
		`"accountId"`,
		`"matchExact"`,
		`"label"`,
		`"id": "m1"`,
		`"id": "rps"`,
		`"annotations"`,
		`"refresh"`,
		`"m1/60"`,
	} {
		require.Contains(t, out, marker, "Grafana field %s must survive get round-trip", marker)
	}
}

// TestExecuteGrafanaUpdateDashboardPreservesUnmodeledFields is the other
// half of the regression guard: the same fields the get path must
// preserve on the way in, the update path must preserve on the way out.
// Captures the POST body Grafana would have received.
func TestExecuteGrafanaUpdateDashboardPreservesUnmodeledFields(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/dashboards/db" {
			b, _ := io.ReadAll(r.Body)
			capturedBody = string(b)
		}
		_, _ = w.Write([]byte(`{"id":1,"uid":"dash-uid","status":"success","version":2}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	// Dashboard payload with fields the typed structs would silently
	// strip. The pre-fix code unmarshaled this into Dashboard{} and
	// re-marshaled it, dropping every entry below.
	args := map[string]interface{}{
		"uid":       "dash-uid",
		"folderUid": "ops",
		"message":   "rate normalization",
		"dashboard": map[string]interface{}{
			"title": "Services",
			"panels": []interface{}{
				map[string]interface{}{
					"id":    4,
					"type":  "timeseries",
					"title": "Request Rate",
					"targets": []interface{}{
						map[string]interface{}{
							"refId":      "A",
							"datasource": map[string]interface{}{"type": "cloudwatch", "uid": "cw-prod"},
							"namespace":  "AWS/ApplicationELB",
							"metricName": "RequestCount",
							"statistic":  "Sum",
							"period":     "60",
							"accountId":  "111122223333",
							"matchExact": false,
							"id":         "m1",
						},
						map[string]interface{}{
							"refId":      "B",
							"expression": "m1/60",
							"label":      "RPS",
							"id":         "rps",
						},
					},
				},
			},
			"annotations": map[string]interface{}{"list": []interface{}{map[string]interface{}{"name": "deploys"}}},
			"refresh":     "30s",
		},
	}

	_, err = mcpServer.executeGrafanaUpdateDashboard(t.Context(), args)
	require.NoError(t, err)

	for _, marker := range []string{
		`"expression":"m1/60"`,
		`"period":"60"`,
		`"accountId":"111122223333"`,
		`"matchExact":false`,
		`"label":"RPS"`,
		`"id":"m1"`,
		`"id":"rps"`,
		`"annotations"`,
		`"refresh":"30s"`,
	} {
		require.Contains(t, capturedBody, marker, "Grafana field %s must reach the server verbatim", marker)
	}

	require.Contains(t, capturedBody, `"message":"alice: rate normalization"`, "audit attribution must still land in the version note")
	require.Contains(t, capturedBody, `"overwrite":true`, "update path must always overwrite")
}

// TestExecuteGrafanaUpdateDashboardInjectsUID verifies the bot still
// honors the contract that args.uid is the source of truth for which
// dashboard gets updated, even when the LLM's dashboard payload lacks a
// uid (or has a stale one). The injected uid must reach the wire.
func TestExecuteGrafanaUpdateDashboardInjectsUID(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/dashboards/db" {
			b, _ := io.ReadAll(r.Body)
			capturedBody = string(b)
		}
		_, _ = w.Write([]byte(`{"id":1,"uid":"target-uid","status":"success","version":2}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	args := map[string]interface{}{
		"uid":       "target-uid",
		"folderUid": "ops",
		"dashboard": map[string]interface{}{
			// Deliberately no uid field. The executor must inject "target-uid".
			"title":  "Services",
			"panels": []interface{}{},
		},
	}
	_, err = mcpServer.executeGrafanaUpdateDashboard(t.Context(), args)
	require.NoError(t, err)
	require.Contains(t, capturedBody, `"uid":"target-uid"`, "args.uid must be injected into the dashboard payload before POST")
}

// TestExecuteGrafanaDeleteDashboardLogsHTTPSourceIP verifies the HTTP/SSE
// transport path: when the request context carries a client IP (injected
// by the middleware), it lands in the audit slog line instead of "stdio".
func TestExecuteGrafanaDeleteDashboardLogsHTTPSourceIP(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"Dashboard deleted","id":1}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)

	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	ctx := WithAuditSourceIP(t.Context(), "10.0.1.5")
	_, err = mcpServer.executeGrafanaDeleteDashboard(ctx, map[string]interface{}{
		"uid":     "dash-uid",
		"message": "cleanup",
	})
	require.NoError(t, err)

	require.Contains(t, buf.String(), `"audit_source_ip":"10.0.1.5"`)
}

// patchTestFixture is a faithful Grafana GET response with unmodeled
// CloudWatch fields the patcher must not touch. Shared across the patch
// tool tests below.
const patchTestFixture = `{
	"dashboard": {
		"uid": "dash-uid",
		"title": "Services",
		"panels": [
			{"id": 4, "title": "RPS", "targets": [{
				"refId": "A",
				"namespace": "AWS/ApplicationELB",
				"metricName": "RequestCount",
				"statistic": "Sum",
				"period": "60",
				"accountId": "111122223333",
				"matchExact": false,
				"label": "rps",
				"id": "m1"
			}]},
			{"id": 5, "title": "Errors"}
		],
		"time": {"from": "now-3h", "to": "now"},
		"refresh": "30s"
	},
	"meta": {"folderUid": "ops", "version": 7}
}`

// newPatchTestServer stands up a httptest Grafana that returns the
// fixture on GET and captures the POST body. Returns the server and a
// pointer to the captured request body so callers can assert on it.
func newPatchTestServer(t *testing.T) (server *httptest.Server, captured *string) {
	t.Helper()
	var body string
	captured = &body
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(patchTestFixture))
		case r.Method == http.MethodPost && r.URL.Path == "/api/dashboards/db":
			b, _ := io.ReadAll(r.Body)
			*captured = string(b)
			_, _ = w.Write([]byte(`{"id":1,"uid":"dash-uid","status":"success","version":8}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return server, captured
}

func newPatchTestServerInstance(t *testing.T) (mcpServer *Server, captured *string, cleanup func()) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	var server *httptest.Server
	server, captured = newPatchTestServer(t)
	cleanup = server.Close
	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer = NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient
	return mcpServer, captured, cleanup
}

// TestExecuteGrafanaPatchDashboardMergePatchTouchesOnlyTargetField verifies
// the merge-patch (RFC 7386) mode changes the targeted nested field and
// leaves every sibling — including the unmodeled CloudWatch target fields
// the lossless-get fix preserves — intact.
func TestExecuteGrafanaPatchDashboardMergePatchTouchesOnlyTargetField(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	mcpServer, captured, cleanup := newPatchTestServerInstance(t)
	t.Cleanup(cleanup)

	_, err := mcpServer.executeGrafanaPatchDashboard(t.Context(), map[string]interface{}{
		"uid":     "dash-uid",
		"message": "narrow default window to 1h",
		"merge": map[string]interface{}{
			"time": map[string]interface{}{"from": "now-1h"},
		},
	})
	require.NoError(t, err)

	// Patched field landed.
	require.Contains(t, *captured, `"from":"now-1h"`)
	// Sibling field in same parent unchanged.
	require.Contains(t, *captured, `"to":"now"`)
	// Unmodeled CloudWatch target fields preserved verbatim.
	for _, marker := range []string{`"period":"60"`, `"accountId":"111122223333"`, `"matchExact":false`, `"label":"rps"`, `"id":"m1"`} {
		require.Contains(t, *captured, marker, "merge-patch must not touch field %s", marker)
	}
	// Audit attribution lands in the version note.
	require.Contains(t, *captured, `"message":"alice: narrow default window to 1h"`)
	// folderUid round-trips from the GET.
	require.Contains(t, *captured, `"folderUid":"ops"`)
	require.Contains(t, *captured, `"overwrite":true`)
}

// TestExecuteGrafanaPatchDashboardJSONPatchReplaceNestedPath verifies the
// RFC 6902 mode with a single replace op against a nested JSON Pointer
// path — the simplest "change one field" case.
func TestExecuteGrafanaPatchDashboardJSONPatchReplaceNestedPath(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	mcpServer, captured, cleanup := newPatchTestServerInstance(t)
	t.Cleanup(cleanup)

	_, err := mcpServer.executeGrafanaPatchDashboard(t.Context(), map[string]interface{}{
		"uid": "dash-uid",
		"patches": []interface{}{
			map[string]interface{}{"op": "replace", "path": "/time/from", "value": "now-1h"},
		},
	})
	require.NoError(t, err)

	require.Contains(t, *captured, `"from":"now-1h"`)
	require.Contains(t, *captured, `"to":"now"`)
	require.Contains(t, *captured, `"period":"60"`, "JSON Patch must not touch unmodeled fields either")
}

// TestExecuteGrafanaPatchDashboardJSONPatchArrayRemove verifies the
// surgical array op JSON Patch gives us that merge-patch cannot — removing
// one element of /panels by index, leaving the rest in place.
func TestExecuteGrafanaPatchDashboardJSONPatchArrayRemove(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	mcpServer, captured, cleanup := newPatchTestServerInstance(t)
	t.Cleanup(cleanup)

	_, err := mcpServer.executeGrafanaPatchDashboard(t.Context(), map[string]interface{}{
		"uid": "dash-uid",
		"patches": []interface{}{
			map[string]interface{}{"op": "remove", "path": "/panels/1"},
		},
	})
	require.NoError(t, err)

	require.NotContains(t, *captured, `"title":"Errors"`, "panel at index 1 must be removed")
	require.Contains(t, *captured, `"title":"RPS"`, "panel at index 0 must remain")
}

// TestExecuteGrafanaPatchDashboardJSONPatchTestFailureAborts verifies the
// optimistic-locking primitive: a failed `test` op means we don't write.
// Captured body remains empty — no POST hit the server.
func TestExecuteGrafanaPatchDashboardJSONPatchTestFailureAborts(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	mcpServer, captured, cleanup := newPatchTestServerInstance(t)
	t.Cleanup(cleanup)

	_, err := mcpServer.executeGrafanaPatchDashboard(t.Context(), map[string]interface{}{
		"uid": "dash-uid",
		"patches": []interface{}{
			map[string]interface{}{"op": "test", "path": "/time/from", "value": "now-99h"},
			map[string]interface{}{"op": "replace", "path": "/time/from", "value": "now-1h"},
		},
	})
	require.Error(t, err)
	require.Empty(t, *captured, "failed test op must abort before POST")
}

// TestExecuteGrafanaPatchDashboardRejectsNeitherInput verifies the schema
// invariant: the LLM must choose merge or patches.
func TestExecuteGrafanaPatchDashboardRejectsNeitherInput(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	mcpServer, captured, cleanup := newPatchTestServerInstance(t)
	t.Cleanup(cleanup)

	_, err := mcpServer.executeGrafanaPatchDashboard(t.Context(), map[string]interface{}{
		"uid": "dash-uid",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge")
	require.Contains(t, err.Error(), "patches")
	require.Empty(t, *captured)
}

// TestExecuteGrafanaPatchDashboardRejectsBothInputs verifies the schema
// invariant the other way: not both at once, since the two operate on the
// same bytes and the resolution order would be unobvious.
func TestExecuteGrafanaPatchDashboardRejectsBothInputs(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	mcpServer, captured, cleanup := newPatchTestServerInstance(t)
	t.Cleanup(cleanup)

	_, err := mcpServer.executeGrafanaPatchDashboard(t.Context(), map[string]interface{}{
		"uid":     "dash-uid",
		"merge":   map[string]interface{}{"time": map[string]interface{}{"from": "now-1h"}},
		"patches": []interface{}{map[string]interface{}{"op": "replace", "path": "/time/from", "value": "now-2h"}},
	})
	require.Error(t, err)
	require.Empty(t, *captured)
}

// TestExecuteGrafanaPatchDashboardLogsAuditFields verifies the slog INFO
// line for patch carries the same audit fields as the other write tools.
func TestExecuteGrafanaPatchDashboardLogsAuditFields(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	server, _ := newPatchTestServer(t)
	t.Cleanup(server.Close)
	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	ctx := WithAuditSourceIP(t.Context(), "10.0.1.5")
	_, err = mcpServer.executeGrafanaPatchDashboard(ctx, map[string]interface{}{
		"uid":     "dash-uid",
		"message": "cleanup",
		"merge":   map[string]interface{}{"time": map[string]interface{}{"from": "now-1h"}},
	})
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, `"tool":"grafana_patch_dashboard"`)
	require.Contains(t, out, `"audit_user":"alice"`)
	require.Contains(t, out, `"audit_source_ip":"10.0.1.5"`)
	require.Contains(t, out, `"message":"alice: cleanup"`)
}

// TestExecuteGrafanaGetDashboardVersionListsWhenVersionOmitted verifies the
// list mode: no `version` arg → bot hits /versions, returns the raw array.
func TestExecuteGrafanaGetDashboardVersionListsWhenVersionOmitted(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	listBody := `[
		{"id": 11, "version": 7, "created": "2026-05-26T12:00:00Z", "createdBy": "alice", "message": "alice: rate normalization"},
		{"id": 10, "version": 6, "created": "2026-05-20T08:00:00Z", "createdBy": "bob", "message": "bob: cleanup"}
	]`

	var capturedPath, capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(listBody))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	out, err := mcpServer.executeGrafanaGetDashboardVersion(t.Context(), map[string]interface{}{
		"uid": "dash-uid",
	})
	require.NoError(t, err)
	require.Equal(t, "/api/dashboards/uid/dash-uid/versions", capturedPath)
	require.Empty(t, capturedQuery, "no limit/start → no query params")
	// Response is returned verbatim — verify the version metadata round-trips
	// by parsing rather than depending on the fixture's whitespace.
	var parsed []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Len(t, parsed, 2)
	require.EqualValues(t, 7, parsed[0]["version"])
	require.Equal(t, "alice", parsed[0]["createdBy"])
	require.EqualValues(t, 6, parsed[1]["version"])
}

// TestExecuteGrafanaGetDashboardVersionForwardsLimitAndStart verifies the
// optional pagination params are forwarded to Grafana.
func TestExecuteGrafanaGetDashboardVersionForwardsLimitAndStart(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	_, err = mcpServer.executeGrafanaGetDashboardVersion(t.Context(), map[string]interface{}{
		"uid":   "dash-uid",
		"limit": float64(50),
		"start": float64(10),
	})
	require.NoError(t, err)
	require.Contains(t, capturedQuery, "limit=50")
	require.Contains(t, capturedQuery, "start=10")
}

// TestExecuteGrafanaGetDashboardVersionFetchesSpecificVersion verifies the
// single-version mode preserves the raw response (including the full
// dashboard `data` payload) so the LLM gets every Grafana field intact —
// same lossless contract as grafana_get_dashboard.
func TestExecuteGrafanaGetDashboardVersionFetchesSpecificVersion(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Includes unmodeled CloudWatch target fields the typed structs would strip.
	versionBody := `{
		"id": 11,
		"dashboardId": 1,
		"version": 7,
		"message": "alice: rate normalization",
		"data": {
			"uid": "dash-uid",
			"title": "Services",
			"panels": [{"id": 4, "targets": [{"refId": "A", "expression": "m1/60", "period": "60", "accountId": "111122223333"}]}]
		}
	}`

	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_, _ = w.Write([]byte(versionBody))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	out, err := mcpServer.executeGrafanaGetDashboardVersion(t.Context(), map[string]interface{}{
		"uid":     "dash-uid",
		"version": float64(7),
	})
	require.NoError(t, err)
	require.Equal(t, "/api/dashboards/uid/dash-uid/versions/7", capturedPath)
	for _, marker := range []string{`"expression"`, `"m1/60"`, `"period"`, `"accountId"`, `"111122223333"`} {
		require.Contains(t, out, marker, "single-version fetch must preserve unmodeled field %s", marker)
	}
}

// TestExecuteGrafanaRestoreDashboardVersionPostsCorrectBody verifies the
// restore tool hits POST /restore with the version int in the body — and
// only the version int, since Grafana's restore endpoint doesn't accept a
// custom message (it stamps "Restored from version N" itself).
func TestExecuteGrafanaRestoreDashboardVersionPostsCorrectBody(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var capturedPath, capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		capturedBody = string(b)
		_, _ = w.Write([]byte(`{"slug":"services","status":"success","version":8,"uid":"dash-uid"}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	out, err := mcpServer.executeGrafanaRestoreDashboardVersion(t.Context(), map[string]interface{}{
		"uid":     "dash-uid",
		"version": float64(7),
	})
	require.NoError(t, err)
	require.Equal(t, "/api/dashboards/uid/dash-uid/restore", capturedPath)
	require.JSONEq(t, `{"version":7}`, capturedBody)
	require.Contains(t, out, "dash-uid")
}

// TestExecuteGrafanaRestoreDashboardVersionLogsForensicLine verifies that
// even though Grafana's own version-history note is "Restored from version
// N" (we don't override it), the bot still emits a slog INFO with
// audit_user/audit_source_ip so the bot-side audit trail captures who
// initiated the restore.
func TestExecuteGrafanaRestoreDashboardVersionLogsForensicLine(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"slug":"services","status":"success","version":8,"uid":"dash-uid"}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	ctx := WithAuditSourceIP(t.Context(), "10.0.1.5")
	_, err = mcpServer.executeGrafanaRestoreDashboardVersion(ctx, map[string]interface{}{
		"uid":     "dash-uid",
		"version": float64(7),
	})
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, `"tool":"grafana_restore_dashboard_version"`)
	require.Contains(t, out, `"audit_user":"alice"`)
	require.Contains(t, out, `"audit_source_ip":"10.0.1.5"`)
	require.Contains(t, out, `"restored_version":7`)
}

// TestExecuteGrafanaCreateDashboardRawPreservesElasticsearchFields is the
// regression guard for issue #17: an Elasticsearch dashboard created via
// the raw `dashboard` arg (alternative to `panels`) must reach Grafana
// with metrics/bucketAggs/timeField/alias intact. The typed builder path
// can't express those fields, so the raw mode is the supported way.
func TestExecuteGrafanaCreateDashboardRawPreservesElasticsearchFields(t *testing.T) {
	t.Setenv("MCP_AUDIT_USER", "alice")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/dashboards/db" {
			b, _ := io.ReadAll(r.Body)
			capturedBody = string(b)
		}
		_, _ = w.Write([]byte(`{"id":1,"uid":"es-dash","status":"success","version":1,"url":"/d/es-dash/elasticsearch-health"}`))
	}))
	t.Cleanup(server.Close)

	grafanaClient, err := NewGrafanaClient(server.URL, "test-key", logger)
	require.NoError(t, err)
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = grafanaClient

	args := map[string]interface{}{
		"title":   "Elasticsearch Health",
		"message": "initial create with ES target",
		"dashboard": map[string]interface{}{
			"title": "Elasticsearch Health",
			"panels": []interface{}{
				map[string]interface{}{
					"id":    1,
					"type":  "timeseries",
					"title": "Errors by service",
					"targets": []interface{}{
						map[string]interface{}{
							"refId":     "A",
							"timeField": "@timestamp",
							"alias":     "{{service}}",
							"metrics": []interface{}{
								map[string]interface{}{"id": "1", "type": "count"},
							},
							"bucketAggs": []interface{}{
								map[string]interface{}{
									"id":       "2",
									"type":     "terms",
									"field":    "service",
									"settings": map[string]interface{}{"size": "10"},
								},
								map[string]interface{}{
									"id":       "3",
									"type":     "date_histogram",
									"field":    "@timestamp",
									"settings": map[string]interface{}{"interval": "auto"},
								},
							},
						},
					},
				},
			},
		},
	}

	_, err = mcpServer.executeGrafanaCreateDashboard(t.Context(), args)
	require.NoError(t, err)

	for _, marker := range []string{
		`"metrics"`,
		`"bucketAggs"`,
		`"timeField":"@timestamp"`,
		`"alias":"{{service}}"`,
		`"type":"date_histogram"`,
		`"type":"terms"`,
		`"interval":"auto"`,
	} {
		require.Contains(t, capturedBody, marker, "ES field %s must survive raw create", marker)
	}
	require.Contains(t, capturedBody, `"message":"alice: initial create with ES target"`, "audit attribution must land in version note for the create-raw path")
	require.NotContains(t, capturedBody, `"Auto-generated dashboard:"`)
}

// TestExecuteGrafanaCreateDashboardRejectsBothPanelsAndDashboard verifies
// the schema invariant: the LLM picks exactly one creation mode.
func TestExecuteGrafanaCreateDashboardRejectsBothPanelsAndDashboard(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = &GrafanaClient{logger: logger}

	_, err := mcpServer.executeGrafanaCreateDashboard(t.Context(), map[string]interface{}{
		"title":     "ambiguous",
		"panels":    []interface{}{map[string]interface{}{"title": "p", "panelType": "stat", "datasourceUID": "ds"}},
		"dashboard": map[string]interface{}{"title": "different"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "panels")
	require.Contains(t, err.Error(), "dashboard")
}

// TestExecuteGrafanaCreateDashboardRequiresOneCreationMode verifies that
// at least one of panels or dashboard must be present.
func TestExecuteGrafanaCreateDashboardRequiresOneCreationMode(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = &GrafanaClient{logger: logger}

	_, err := mcpServer.executeGrafanaCreateDashboard(t.Context(), map[string]interface{}{
		"title": "nothing to build",
	})
	require.Error(t, err)
}

// TestExecuteGrafanaRestoreDashboardVersionRequiresVersion verifies the
// schema invariant: restore is destructive (creates a new version, can't
// be UNdone except by another restore), so version is required.
func TestExecuteGrafanaRestoreDashboardVersionRequiresVersion(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mcpServer := NewServer(nil, "", nil, logger)
	mcpServer.grafanaClient = &GrafanaClient{logger: logger}

	_, err := mcpServer.executeGrafanaRestoreDashboardVersion(t.Context(), map[string]interface{}{
		"uid": "dash-uid",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "version")
}

// TestBuildPandocArgsDisablesRawTeX is the Layer-1 regression guard for
// the markdown→LaTeX raw_tex extension. With raw_tex enabled (pandoc's
// default for the markdown reader), attacker-controlled markdown can
// inject \input{/proc/self/environ} which xelatex resolves and typesets
// into the PDF — a file-read/secret-exfil primitive. Disabling the
// extension makes raw LaTeX render as literal text.
func TestBuildPandocArgsDisablesRawTeX(t *testing.T) {
	t.Parallel()
	args := buildPandocArgs("/tmp/out.pdf", "xelatex", "/app/latex-templates/nx.latex", "My Report", "Nx", "/tmp/in.md")

	// Must contain "-f markdown-raw_tex"; must NOT contain bare "markdown".
	require.Contains(t, args, "-f")
	idx := indexOfString(args, "-f")
	require.Greater(t, len(args), idx+1)
	require.Equal(t, "markdown-raw_tex", args[idx+1], "must explicitly disable raw_tex; bare 'markdown' lets injected LaTeX reach xelatex")
}

// TestBuildPandocArgsCarriesThroughTrustedFields verifies the helper
// still hands pandoc the rest of what executeGeneratePDF needs — output
// path, engine, template, the report's title, the company-name macro,
// and the input file at the end.
func TestBuildPandocArgsCarriesThroughTrustedFields(t *testing.T) {
	t.Parallel()
	args := buildPandocArgs("/tmp/out.pdf", "xelatex", "/app/latex-templates/nx.latex", "My Report", "Nx", "/tmp/in.md")
	require.Contains(t, args, "--pdf-engine=xelatex")
	require.Contains(t, args, "--template=/app/latex-templates/nx.latex")
	require.Contains(t, args, "-o")
	require.Contains(t, args, "/tmp/out.pdf")
	require.Contains(t, args, "title=My Report")
	require.Contains(t, args, "companyname=Nx")
	require.Equal(t, "/tmp/in.md", args[len(args)-1], "input path must be the final positional arg")
}

// TestBuildPandocArgsOmitsTitleWhenEmpty verifies that an empty title
// doesn't produce an empty -M title= meta var.
func TestBuildPandocArgsOmitsTitleWhenEmpty(t *testing.T) {
	t.Parallel()
	args := buildPandocArgs("/tmp/out.pdf", "xelatex", "/app/latex-templates/nx.latex", "", "Nx", "/tmp/in.md")
	for _, a := range args {
		require.NotEqual(t, "title=", a, "empty title must not be passed as a meta var")
	}
}

// TestBuildPandocEnvExcludesSecrets is the Layer-2 guard: even with
// pandoc/xelatex executing, the renderer process must not see app
// secrets. Set every known credential env var in the test environment;
// the returned env must contain none of them.
func TestBuildPandocEnvExcludesSecrets(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "GRAFANA_API_KEY", "SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "GITHUB_TOKEN", "DATABASE_URL"} {
		t.Setenv(k, "secret-"+k)
	}

	env := buildPandocEnv("/app/latex-templates")
	for _, e := range env {
		for _, secretKey := range []string{"ANTHROPIC_API_KEY", "GRAFANA_API_KEY", "SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "GITHUB_TOKEN", "DATABASE_URL"} {
			require.False(t, strings.HasPrefix(e, secretKey+"="), "secret %s must not appear in pandoc env (got %q)", secretKey, e)
		}
	}
}

// TestBuildPandocEnvForwardsTexInputs verifies the legitimate
// TEXINPUTS path is still threaded so xelatex finds .cls files.
func TestBuildPandocEnvForwardsTexInputs(t *testing.T) {
	t.Parallel()
	env := buildPandocEnv("/app/latex-templates")
	require.Contains(t, env, "TEXINPUTS=.:/app/latex-templates//:")
}

// TestBuildPandocEnvAppliesTexLiveHardening verifies the belt-and-suspenders
// TeX Live env-var guards that block absolute/dotfile reads and writes,
// even if a future raw-LaTeX leak slips past Layer 1.
func TestBuildPandocEnvAppliesTexLiveHardening(t *testing.T) {
	t.Parallel()
	env := buildPandocEnv("/app/latex-templates")
	require.Contains(t, env, "openin_any=p")
	require.Contains(t, env, "openout_any=p")
	require.Contains(t, env, "shell_escape=f")
}

// TestBuildPandocEnvCarriesPathAndLocale verifies that the variables
// pandoc/xelatex/fontconfig genuinely need (PATH, HOME, locale) survive
// the allowlist — otherwise pandoc can't find binaries or fonts.
func TestBuildPandocEnvCarriesPathAndLocale(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("HOME", "/home/bot")
	t.Setenv("LANG", "en_US.UTF-8")
	env := buildPandocEnv("/app/latex-templates")
	require.Contains(t, env, "PATH=/usr/local/bin:/usr/bin")
	require.Contains(t, env, "HOME=/home/bot")
	require.Contains(t, env, "LANG=en_US.UTF-8")
}

// indexOfString is a test helper for finding a flag's position so we can
// assert on the value that follows it (e.g. "-f" → "markdown-raw_tex").
func indexOfString(haystack []string, needle string) (idx int) {
	for i, s := range haystack {
		if s == needle {
			idx = i
			return idx
		}
	}
	idx = -1
	return idx
}
