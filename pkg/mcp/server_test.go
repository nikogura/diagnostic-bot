package mcp

import (
	"context"
	"log/slog"
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

	tools := getLokiTools()

	// Should have 1 Loki tool
	if len(tools) != 1 {
		t.Fatalf("getLokiTools() returned %d tools, want 1", len(tools))
	}

	if tools[0].Name != "query_loki" {
		t.Errorf("getLokiTools() tool name = %s, want query_loki", tools[0].Name)
	}
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
