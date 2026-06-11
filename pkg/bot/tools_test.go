package bot

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// clearToolEnvVars clears all env vars that affect ToolConfig.
func clearToolEnvVars(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"LOKI_ENDPOINT",
		"CLOUDWATCH_ASSUME_ROLE",
		"CLOUDWATCH_ACCOUNTS",
		"PROMETHEUS_URL",
		"GRAFANA_URL",
		"GRAFANA_API_KEY",
		"GITHUB_TOKEN",
		"DATABASE_URL",
		"AWS_REGION",
		"AWS_DEFAULT_REGION",
	} {
		t.Setenv(key, "")
	}
}

func TestNewToolConfigNoEnvVars(t *testing.T) {
	clearToolEnvVars(t)

	config := NewToolConfig()

	assert.False(t, config.LokiAvailable, "Loki should not be available without LOKI_ENDPOINT")
	assert.False(t, config.CloudWatchAvailable, "CloudWatch should not be available without CLOUDWATCH_ASSUME_ROLE or CLOUDWATCH_ACCOUNTS")
	assert.False(t, config.PrometheusAvailable, "Prometheus should not be available without PROMETHEUS_URL")
	assert.False(t, config.GrafanaAvailable, "Grafana should not be available without GRAFANA_URL+GRAFANA_API_KEY")
	assert.False(t, config.GitHubAvailable, "GitHub should not be available without GITHUB_TOKEN")
	assert.False(t, config.DatabaseAvailable, "Database should not be available without DATABASE_URL")
	assert.False(t, config.ECRAvailable, "ECR should not be available without AWS_REGION")
}

func TestNewToolConfigWithLoki(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("LOKI_ENDPOINT", "http://loki:3100")

	config := NewToolConfig()

	assert.True(t, config.LokiAvailable, "Loki should be available with LOKI_ENDPOINT set")
	assert.False(t, config.CloudWatchAvailable, "CloudWatch should not be available")
}

func TestNewToolConfigWithCloudWatch(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "arn:aws:iam::123456789012:role/test")

	config := NewToolConfig()

	assert.True(t, config.CloudWatchAvailable, "CloudWatch should be available with CLOUDWATCH_ASSUME_ROLE set")
	assert.False(t, config.LokiAvailable, "Loki should not be available")
}

func TestNewToolConfigWithCloudWatchAccounts(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("CLOUDWATCH_ACCOUNTS", `{"dev":"arn:aws:iam::111111111111:role/dev","prod":"arn:aws:iam::222222222222:role/prod"}`)

	config := NewToolConfig()

	assert.True(t, config.CloudWatchAvailable, "CloudWatch should be available with CLOUDWATCH_ACCOUNTS set")
	assert.False(t, config.LokiAvailable, "Loki should not be available")
}

func TestNewToolConfigWithPrometheus(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("PROMETHEUS_URL", "http://prometheus:9090")

	config := NewToolConfig()

	assert.True(t, config.PrometheusAvailable, "Prometheus should be available with PROMETHEUS_URL set")
}

func TestNewToolConfigWithNamedPrometheus(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("PROMETHEUS_PROD_URL", "http://prom-prod:9090")

	config := NewToolConfig()

	assert.True(t, config.PrometheusAvailable, "Prometheus should be available with PROMETHEUS_PROD_URL set")
}

func TestNewToolConfigWithGrafana(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("GRAFANA_URL", "http://grafana:3000")
	t.Setenv("GRAFANA_API_KEY", "test-key")

	config := NewToolConfig()

	assert.True(t, config.GrafanaAvailable, "Grafana should be available with both env vars set")
}

func TestNewToolConfigGrafanaRequiresBothVars(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("GRAFANA_URL", "http://grafana:3000")

	config := NewToolConfig()

	assert.False(t, config.GrafanaAvailable, "Grafana should not be available with only GRAFANA_URL")
}

func TestNewToolConfigWithGitHub(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("GITHUB_TOKEN", "ghp_testtoken")

	config := NewToolConfig()

	assert.True(t, config.GitHubAvailable, "GitHub should be available with GITHUB_TOKEN set")
}

func TestNewToolConfigWithDatabase(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/test")

	config := NewToolConfig()

	assert.True(t, config.DatabaseAvailable, "Database should be available with DATABASE_URL set")
}

func TestNewToolConfigWithNamedDatabase(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("DATABASE_TERRACE_URL", "postgres://localhost:5432/terrace")

	config := NewToolConfig()

	assert.True(t, config.DatabaseAvailable, "Database should be available with DATABASE_TERRACE_URL set")
}

func TestNewToolConfigWithECR(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("AWS_REGION", "us-east-1")

	config := NewToolConfig()

	assert.True(t, config.ECRAvailable, "ECR should be available with AWS_REGION set")
}

func TestNewToolConfigWithDefaultAWSRegion(t *testing.T) {
	clearToolEnvVars(t)
	t.Setenv("AWS_DEFAULT_REGION", "us-west-2")

	config := NewToolConfig()

	assert.True(t, config.ECRAvailable, "ECR should be available with AWS_DEFAULT_REGION set")
}

func TestNewToolConfigAllEnabled(t *testing.T) {
	t.Setenv("LOKI_ENDPOINT", "http://loki:3100")
	t.Setenv("CLOUDWATCH_ASSUME_ROLE", "arn:aws:iam::123456789012:role/test")
	t.Setenv("PROMETHEUS_URL", "http://prometheus:9090")
	t.Setenv("GRAFANA_URL", "http://grafana:3000")
	t.Setenv("GRAFANA_API_KEY", "test-key")
	t.Setenv("GITHUB_TOKEN", "ghp_testtoken")
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/test")
	t.Setenv("AWS_REGION", "us-east-1")

	config := NewToolConfig()

	assert.True(t, config.LokiAvailable, "Loki should be available")
	assert.True(t, config.CloudWatchAvailable, "CloudWatch should be available")
	assert.True(t, config.PrometheusAvailable, "Prometheus should be available")
	assert.True(t, config.GrafanaAvailable, "Grafana should be available")
	assert.True(t, config.GitHubAvailable, "GitHub should be available")
	assert.True(t, config.DatabaseAvailable, "Database should be available")
	assert.True(t, config.ECRAvailable, "ECR should be available")
}

func TestWriteToolUsageOnlyUtilities(t *testing.T) {
	t.Parallel()

	config := ToolConfig{} // All false

	var builder strings.Builder
	config.WriteToolUsage(&builder, nil)
	output := builder.String()

	// Should always include utility tools
	assert.Contains(t, output, "whois_lookup", "Should always include whois_lookup")
	assert.Contains(t, output, "generate_pdf", "Should always include generate_pdf")

	// Should NOT include any optional tools
	assert.NotContains(t, output, "loki_query", "Should not include Loki tools")
	assert.NotContains(t, output, "cloudwatch_logs_query", "Should not include CloudWatch tools")
	assert.NotContains(t, output, "prometheus_query", "Should not include Prometheus tools")
	assert.NotContains(t, output, "grafana_list_dashboards", "Should not include Grafana tools")
	assert.NotContains(t, output, "database_query", "Should not include Database tools")
	assert.NotContains(t, output, "github_get_file", "Should not include GitHub tools")
	assert.NotContains(t, output, "ecr_scan_results", "Should not include ECR tools")
}

func TestWriteToolUsageWithLoki(t *testing.T) {
	t.Parallel()

	config := ToolConfig{LokiAvailable: true}

	var builder strings.Builder
	config.WriteToolUsage(&builder, nil)
	output := builder.String()

	assert.Contains(t, output, "loki_query", "Should include Loki tool")
	assert.Contains(t, output, "whois_lookup", "Should always include utilities")
	assert.NotContains(t, output, "cloudwatch_logs_query", "Should not include CloudWatch")
}

func TestWriteToolUsageWithCloudWatch(t *testing.T) {
	t.Parallel()

	config := ToolConfig{CloudWatchAvailable: true}

	var builder strings.Builder
	config.WriteToolUsage(&builder, nil)
	output := builder.String()

	assert.Contains(t, output, "cloudwatch_logs_query", "Should include CloudWatch query tool")
	assert.Contains(t, output, "cloudwatch_logs_list_groups", "Should include CloudWatch list groups tool")
	assert.Contains(t, output, "cloudwatch_logs_get_events", "Should include CloudWatch get events tool")
	assert.NotContains(t, output, "loki_query", "Should not include Loki")
}

func TestWriteToolUsageAllEnabled(t *testing.T) {
	t.Parallel()

	config := ToolConfig{
		LokiAvailable:       true,
		CloudWatchAvailable: true,
		PrometheusAvailable: true,
		GrafanaAvailable:    true,
		DatabaseAvailable:   true,
		GitHubAvailable:     true,
		ECRAvailable:        true,
	}

	var builder strings.Builder
	config.WriteToolUsage(&builder, nil)
	output := builder.String()

	// All tool categories should be present
	expectedTools := []string{
		"loki_query",
		"cloudwatch_logs_query",
		"prometheus_query",
		"grafana_list_dashboards",
		"database_query",
		"github_get_file",
		"ecr_scan_results",
		"whois_lookup",
		"generate_pdf",
	}

	for _, tool := range expectedTools {
		assert.Contains(t, output, tool, "Should include %s when all enabled", tool)
	}
}

func TestWriteToolUsageGrafanaMentionsInfinity(t *testing.T) {
	t.Parallel()

	config := ToolConfig{GrafanaAvailable: true}

	var builder strings.Builder
	config.WriteToolUsage(&builder, nil)
	output := builder.String()

	assert.Contains(t, output, "grafana_create_dashboard", "Should include grafana_create_dashboard")
	assert.Contains(t, output, "infinity", "Grafana create dashboard description should mention infinity")
}
