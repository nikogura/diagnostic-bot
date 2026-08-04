package bot

import (
	"log/slog"
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
	K8sAvailable        bool
	ReadOnly            bool
	APIToolRegistry     *apiconfig.APIToolRegistry
}

// NewToolConfig checks environment variables to determine which tool categories
// are available. The checks mirror the client initialization logic in
// pkg/mcp/server.go, so the Slack prompt's tool prose matches the tool surface
// the shared MCP server advertises and dispatches — including the operator's
// third-party API tools, loaded from the same API_CONFIG_DIR the server reads.
func NewToolConfig(logger *slog.Logger) (config ToolConfig) {
	config = ToolConfig{
		LokiAvailable:       os.Getenv("LOKI_ENDPOINT") != "",
		CloudWatchAvailable: os.Getenv("CLOUDWATCH_ASSUME_ROLE") != "" || os.Getenv("CLOUDWATCH_ACCOUNTS") != "",
		PrometheusAvailable: hasPrometheusConfig(),
		GrafanaAvailable:    os.Getenv("GRAFANA_URL") != "" && os.Getenv("GRAFANA_API_KEY") != "",
		GitHubAvailable:     os.Getenv("GITHUB_TOKEN") != "",
		DatabaseAvailable:   hasDatabaseConfig(),
		ECRAvailable:        os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "",
		K8sAvailable:        hasK8sConfig(),
		ReadOnly:            mcp.ReadOnlyEnabled(),
		APIToolRegistry:     apiconfig.LoadRegistryFromEnv(logger),
	}

	return config
}

// WriteToolUsage writes the available tool sections to the builder. allowed
// gates each tool by name: a tool is described only if allowed[name] — so the
// prose lists exactly what the caller can dispatch, never more. A nil allowed
// map means "everything" (authorization disabled), preserving prior behavior.
func (tc ToolConfig) WriteToolUsage(builder *strings.Builder, allowed map[string]bool) {
	permits := func(name string) (ok bool) {
		ok = allowed == nil || allowed[name]
		return ok
	}

	builder.WriteString("# Available Tools\n\n")
	builder.WriteString("You have access to these MCP tools:\n\n")

	if tc.LokiAvailable {
		writeLokiToolUsage(builder, permits)
	}

	if tc.CloudWatchAvailable {
		writeCloudWatchToolUsage(builder, permits)
	}

	if tc.PrometheusAvailable {
		writePrometheusToolUsage(builder, permits)
	}

	if tc.GrafanaAvailable {
		writeGrafanaToolUsage(builder, permits, tc.ReadOnly)
	}

	if tc.DatabaseAvailable {
		writeDatabaseToolUsage(builder, permits)
	}

	if tc.K8sAvailable {
		writeK8sToolUsage(builder, permits)
	}

	if tc.GitHubAvailable {
		writeGitHubToolUsage(builder, permits)
	}

	if tc.ECRAvailable {
		writeECRToolUsage(builder, permits)
	}

	// Third-party API tools, gated by the same permits check as every other
	// category so the prose can't describe a tool the caller can't dispatch.
	if tc.APIToolRegistry != nil && tc.APIToolRegistry.HasTools() {
		tc.APIToolRegistry.WriteToolUsage(builder, permits)
	}

	// Utility tools are always available
	writeUtilityToolUsage(builder, permits)

	builder.WriteString("Use the appropriate tools to gather data for your investigation. ")
	builder.WriteString("Match the tool to what the user is asking about.\n\n")
}

// toolLine writes a "- `name`: description" line only when permits(name).
func toolLine(b *strings.Builder, permits func(string) bool, name, description string) {
	if !permits(name) {
		return
	}
	b.WriteString("- `")
	b.WriteString(name)
	b.WriteString("`: ")
	b.WriteString(description)
	b.WriteString("\n")
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

func writeLokiToolUsage(builder *strings.Builder, permits func(string) bool) {
	if !permits("loki_query") {
		return
	}
	builder.WriteString("**Logging (Loki):**\n")
	builder.WriteString("- `loki_query`: Query Loki for cluster logs (ModSecurity, application logs). Use LogQL syntax.\n")
	builder.WriteString("  Example: `{realm=\"prod\", namespace=\"ingress-nginx\"} |~ \"ModSecurity\" | json | transaction_response_http_code=\"403\"`\n\n")
}

func writeCloudWatchToolUsage(builder *strings.Builder, permits func(string) bool) {
	var lines strings.Builder
	toolLine(&lines, permits, "cloudwatch_logs_query", "Execute CloudWatch Logs Insights queries across AWS log groups")
	toolLine(&lines, permits, "cloudwatch_logs_list_groups", "List available CloudWatch log groups in an AWS region")
	toolLine(&lines, permits, "cloudwatch_logs_get_events", "Get log events from a specific CloudWatch log stream")
	toolLine(&lines, permits, "cloudwatch_metrics_list", "Discover CloudWatch metrics (namespaces, names, dimensions)")
	toolLine(&lines, permits, "cloudwatch_metrics_get_statistics", "Fetch time-series statistics for a single CloudWatch metric")
	toolLine(&lines, permits, "cloudwatch_metrics_query", "Query CloudWatch metric data with optional metric math (GetMetricData)")
	toolLine(&lines, permits, "cloudwatch_alarms_list", "List CloudWatch alarms and their current state (OK/ALARM/INSUFFICIENT_DATA)")
	toolLine(&lines, permits, "cloudwatch_alarms_history", "Retrieve CloudWatch alarm state-transition history for post-incident diagnosis")
	if lines.Len() == 0 {
		return
	}
	builder.WriteString("**CloudWatch Logs, Metrics & Alarms:**\n")
	builder.WriteString(lines.String())
	builder.WriteString("\n")
}

func writePrometheusToolUsage(builder *strings.Builder, permits func(string) bool) {
	var lines strings.Builder
	toolLine(&lines, permits, "prometheus_query", "Execute an instant PromQL query")
	toolLine(&lines, permits, "prometheus_query_range", "Execute a range PromQL query for trend analysis")
	toolLine(&lines, permits, "prometheus_series", "Find time series matching label selectors")
	toolLine(&lines, permits, "prometheus_label_values", "Get all values for a given label name")
	toolLine(&lines, permits, "prometheus_list_endpoints", "List configured Prometheus endpoints")
	if lines.Len() == 0 {
		return
	}
	builder.WriteString("**Prometheus/Metrics:**\n")
	builder.WriteString(lines.String())
	builder.WriteString("\n")
}

func writeGrafanaToolUsage(builder *strings.Builder, permits func(string) bool, readOnly bool) {
	var lines strings.Builder
	toolLine(&lines, permits, "grafana_list_dashboards", "List all Grafana dashboards")
	toolLine(&lines, permits, "grafana_get_dashboard", "Get a specific Grafana dashboard by UID")
	toolLine(&lines, permits, "grafana_get_dashboard_version", "List a dashboard's version history (omit `version`) or fetch a specific saved version with full payload (supply `version`)")

	// Write tools are advertised only when writes are permitted; in read-only
	// mode they are neither described nor available.
	if !readOnly {
		toolLine(&lines, permits, "grafana_create_dashboard", "Create a new Grafana dashboard (supports postgres, mysql, prometheus, cloudwatch, and infinity datasources)")
		toolLine(&lines, permits, "grafana_update_dashboard", "Update an existing Grafana dashboard (replaces the full model)")
		toolLine(&lines, permits, "grafana_patch_dashboard", "Patch a Grafana dashboard server-side (RFC 7386 merge-patch or RFC 6902 JSON Patch); avoids round-tripping the full model for small edits")
		toolLine(&lines, permits, "grafana_restore_dashboard_version", "Restore a dashboard to a previous version")
		toolLine(&lines, permits, "grafana_delete_dashboard", "Delete a Grafana dashboard")
		toolLine(&lines, permits, "grafana_create_folder", "Create a Grafana folder (directory) for grouping dashboards. Accepts title (required), uid (optional), parentUid (optional, for nested folders).")
	}

	if lines.Len() == 0 {
		return
	}
	builder.WriteString("**Grafana:**\n")
	builder.WriteString(lines.String())
	if readOnly {
		builder.WriteString("(Read-only mode: Grafana dashboards can be inspected but not created, modified, or deleted.)\n")
	}
	builder.WriteString("\n")
}

func writeDatabaseToolUsage(builder *strings.Builder, permits func(string) bool) {
	var lines strings.Builder
	toolLine(&lines, permits, "database_query", "Execute read-only SQL queries (SELECT, SHOW, DESCRIBE, EXPLAIN)")
	toolLine(&lines, permits, "database_list", "List available databases")
	if lines.Len() == 0 {
		return
	}
	builder.WriteString("**Database:**\n")
	builder.WriteString(lines.String())
	builder.WriteString("\n")
}

// hasK8sConfig reports whether Kubernetes tools should be advertised: the bot is
// running in a cluster or a KUBECONFIG is set, and k8s access is not disabled.
func hasK8sConfig() (available bool) {
	if strings.EqualFold(os.Getenv("K8S_ENABLED"), "false") {
		return available
	}

	available = os.Getenv("KUBERNETES_SERVICE_HOST") != "" || os.Getenv("KUBECONFIG") != ""
	return available
}

func writeK8sToolUsage(builder *strings.Builder, permits func(string) bool) {
	var lines strings.Builder
	toolLine(&lines, permits, "k8s_get_resource", "Read a configmap, deployment, service, pod, or Flux/Atlas CRD. Secrets cannot be read.")
	toolLine(&lines, permits, "k8s_pod_logs", "Fetch pod logs by pod name or label selector")
	toolLine(&lines, permits, "k8s_list_pods", "List pods with status and restart counts")
	toolLine(&lines, permits, "k8s_get_events", "List Kubernetes events")
	if lines.Len() == 0 {
		return
	}
	builder.WriteString("**Kubernetes (read-only, the bot's own cluster):**\n")
	builder.WriteString(lines.String())
	builder.WriteString("\n")
}

func writeGitHubToolUsage(builder *strings.Builder, permits func(string) bool) {
	var lines strings.Builder
	toolLine(&lines, permits, "github_get_file", "Fetch a file from a GitHub repository")
	toolLine(&lines, permits, "github_list_directory", "List files in a GitHub repository directory")
	toolLine(&lines, permits, "github_search_code", "Search for code across GitHub repositories")
	if lines.Len() == 0 {
		return
	}
	builder.WriteString("**GitHub:**\n")
	builder.WriteString(lines.String())
	builder.WriteString("\n")
}

func writeECRToolUsage(builder *strings.Builder, permits func(string) bool) {
	if !permits("ecr_scan_results") {
		return
	}
	builder.WriteString("**ECR (Container Security):**\n")
	builder.WriteString("- `ecr_scan_results`: Query AWS ECR for container image vulnerability scan results\n\n")
}

func writeUtilityToolUsage(builder *strings.Builder, permits func(string) bool) {
	var lines strings.Builder
	toolLine(&lines, permits, "whois_lookup", "Look up IP address geolocation, ISP, ASN")
	toolLine(&lines, permits, "generate_pdf", "Generate a PDF report from Markdown content")
	toolLine(&lines, permits, "list_my_tools", "List the tools you can use here — the authoritative answer to \"what can I do?\"")
	if lines.Len() == 0 {
		return
	}
	builder.WriteString("**Utilities:**\n")
	builder.WriteString(lines.String())
	builder.WriteString("\n")
}
