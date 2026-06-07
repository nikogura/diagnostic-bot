package bot

import (
	"os"
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/apiconfig"
	"github.com/nikogura/diagnostic-bot/pkg/mcp"
)

// ToolConfig captures which tool categories are available based on environment configuration.
// This drives both the prompt tool list and ensures Claude knows which tools it can use.
type ToolConfig struct {
	LokiAvailable       bool
	CloudWatchAvailable bool
	PrometheusAvailable bool
	GrafanaAvailable    bool
	GitHubAvailable     bool
	DatabaseAvailable   bool
	ECRAvailable        bool
	ReadOnly            bool
	APIToolRegistry     *apiconfig.APIToolRegistry
}

// NewToolConfig checks environment variables to determine which tool categories
// are available. The checks mirror the client initialization logic in pkg/mcp/server.go.
func NewToolConfig() (config ToolConfig) {
	config = ToolConfig{
		LokiAvailable:       os.Getenv("LOKI_ENDPOINT") != "",
		CloudWatchAvailable: os.Getenv("CLOUDWATCH_ASSUME_ROLE") != "" || os.Getenv("CLOUDWATCH_ACCOUNTS") != "",
		PrometheusAvailable: hasPrometheusConfig(),
		GrafanaAvailable:    os.Getenv("GRAFANA_URL") != "" && os.Getenv("GRAFANA_API_KEY") != "",
		GitHubAvailable:     os.Getenv("GITHUB_TOKEN") != "",
		DatabaseAvailable:   hasDatabaseConfig(),
		ECRAvailable:        os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "",
		ReadOnly:            mcp.ReadOnlyEnabled(),
	}

	return config
}

// WriteToolUsage writes the available tool sections to the builder based on configuration.
func (tc ToolConfig) WriteToolUsage(builder *strings.Builder) {
	builder.WriteString("# Available Tools\n\n")
	builder.WriteString("You have access to these MCP tools:\n\n")

	if tc.LokiAvailable {
		writeLokiToolUsage(builder)
	}

	if tc.CloudWatchAvailable {
		writeCloudWatchToolUsage(builder)
	}

	if tc.PrometheusAvailable {
		writePrometheusToolUsage(builder)
	}

	if tc.GrafanaAvailable {
		writeGrafanaToolUsage(builder, tc.ReadOnly)
	}

	if tc.DatabaseAvailable {
		writeDatabaseToolUsage(builder)
	}

	if tc.GitHubAvailable {
		writeGitHubToolUsage(builder)
	}

	if tc.ECRAvailable {
		writeECRToolUsage(builder)
	}

	// Third-party API tools
	if tc.APIToolRegistry != nil && tc.APIToolRegistry.HasTools() {
		tc.APIToolRegistry.WriteToolUsage(builder)
	}

	// Utility tools are always available
	writeUtilityToolUsage(builder)

	builder.WriteString("Use the appropriate tools to gather data for your investigation. ")
	builder.WriteString("Match the tool to what the user is asking about.\n\n")
}

// hasPrometheusConfig checks if any Prometheus endpoint is configured.
func hasPrometheusConfig() (available bool) {
	if os.Getenv("PROMETHEUS_URL") != "" {
		available = true
		return available
	}

	// Check for PROMETHEUS_<NAME>_URL patterns
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}

		key := parts[0]
		if strings.HasPrefix(key, "PROMETHEUS_") && strings.HasSuffix(key, "_URL") && key != "PROMETHEUS_URL" {
			available = true
			return available
		}
	}

	return available
}

// hasDatabaseConfig checks if any database is configured.
func hasDatabaseConfig() (available bool) {
	if os.Getenv("DATABASE_URL") != "" {
		available = true
		return available
	}

	// Check for DATABASE_<NAME>_URL patterns
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}

		key := parts[0]
		if strings.HasPrefix(key, "DATABASE_") && strings.HasSuffix(key, "_URL") && key != "DATABASE_URL" {
			available = true
			return available
		}
	}

	return available
}

func writeLokiToolUsage(builder *strings.Builder) {
	builder.WriteString("**Logging (Loki):**\n")
	builder.WriteString("- `query_loki`: Query Loki for cluster logs (ModSecurity, application logs). Use LogQL syntax.\n")
	builder.WriteString("  Example: `{realm=\"prod\", namespace=\"ingress-nginx\"} |~ \"ModSecurity\" | json | transaction_response_http_code=\"403\"`\n\n")
}

func writeCloudWatchToolUsage(builder *strings.Builder) {
	builder.WriteString("**CloudWatch Logs:**\n")
	builder.WriteString("- `cloudwatch_logs_query`: Execute CloudWatch Logs Insights queries across AWS log groups\n")
	builder.WriteString("- `cloudwatch_logs_list_groups`: List available CloudWatch log groups in an AWS region\n")
	builder.WriteString("- `cloudwatch_logs_get_events`: Get log events from a specific CloudWatch log stream\n\n")
}

func writePrometheusToolUsage(builder *strings.Builder) {
	builder.WriteString("**Prometheus/Metrics:**\n")
	builder.WriteString("- `prometheus_query`: Execute an instant PromQL query\n")
	builder.WriteString("- `prometheus_query_range`: Execute a range PromQL query for trend analysis\n")
	builder.WriteString("- `prometheus_series`: Find time series matching label selectors\n")
	builder.WriteString("- `prometheus_label_values`: Get all values for a given label name\n")
	builder.WriteString("- `prometheus_list_endpoints`: List configured Prometheus endpoints\n\n")
}

func writeGrafanaToolUsage(builder *strings.Builder, readOnly bool) {
	builder.WriteString("**Grafana:**\n")
	builder.WriteString("- `grafana_list_dashboards`: List all Grafana dashboards\n")
	builder.WriteString("- `grafana_get_dashboard`: Get a specific Grafana dashboard by UID\n")
	builder.WriteString("- `grafana_get_dashboard_version`: List a dashboard's version history (omit `version`) or fetch a specific saved version with full payload (supply `version`)\n")

	// Write tools are advertised only when writes are permitted; in read-only
	// mode they are neither described nor available.
	if readOnly {
		builder.WriteString("\n(Read-only mode: Grafana dashboards can be inspected but not created, modified, or deleted.)\n\n")
		return
	}

	builder.WriteString("- `grafana_create_dashboard`: Create a new Grafana dashboard (supports postgres, mysql, prometheus, cloudwatch, and infinity datasources)\n")
	builder.WriteString("- `grafana_update_dashboard`: Update an existing Grafana dashboard (replaces the full model)\n")
	builder.WriteString("- `grafana_patch_dashboard`: Patch a Grafana dashboard server-side (RFC 7386 merge-patch or RFC 6902 JSON Patch); avoids round-tripping the full model for small edits\n")
	builder.WriteString("- `grafana_restore_dashboard_version`: Restore a dashboard to a previous version\n")
	builder.WriteString("- `grafana_delete_dashboard`: Delete a Grafana dashboard\n")
	builder.WriteString("- `grafana_create_folder`: Create a Grafana folder (directory) for grouping dashboards. Accepts title (required), uid (optional), parentUid (optional, for nested folders).\n\n")
}

func writeDatabaseToolUsage(builder *strings.Builder) {
	builder.WriteString("**Database:**\n")
	builder.WriteString("- `database_query`: Execute read-only SQL queries (SELECT, SHOW, DESCRIBE, EXPLAIN)\n")
	builder.WriteString("- `database_list`: List available databases\n\n")
}

func writeGitHubToolUsage(builder *strings.Builder) {
	builder.WriteString("**GitHub:**\n")
	builder.WriteString("- `github_get_file`: Fetch a file from a GitHub repository\n")
	builder.WriteString("- `github_list_directory`: List files in a GitHub repository directory\n")
	builder.WriteString("- `github_search_code`: Search for code across GitHub repositories\n\n")
}

func writeECRToolUsage(builder *strings.Builder) {
	builder.WriteString("**ECR (Container Security):**\n")
	builder.WriteString("- `ecr_scan_results`: Query AWS ECR for container image vulnerability scan results\n\n")
}

func writeUtilityToolUsage(builder *strings.Builder) {
	builder.WriteString("**Utilities:**\n")
	builder.WriteString("- `whois_lookup`: Look up IP address geolocation, ISP, ASN\n")
	builder.WriteString("- `generate_pdf`: Generate a PDF report from Markdown content\n\n")
}
