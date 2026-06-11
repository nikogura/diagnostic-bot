package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/google/go-github/v57/github"
	"github.com/nikogura/diagnostic-bot/pkg/apiconfig"
	"github.com/nikogura/diagnostic-bot/pkg/authz"
	"github.com/nikogura/diagnostic-bot/pkg/k8s"
	"golang.org/x/oauth2"
)

// Common schema description strings.
const (
	descEndTime               = "End time as 'now' or RFC3339 timestamp (optional, defaults to now)"
	grafanaInfinityDatasource = "yesoreyeram-infinity-datasource"
)

// Tool name constants.
const (
	toolQueryLoki                      = "query_loki"
	toolWhoisLookup                    = "whois_lookup"
	toolGeneratePDF                    = "generate_pdf"
	toolGitHubGetFile                  = "github_get_file"
	toolGitHubListDirectory            = "github_list_directory"
	toolGitHubSearchCode               = "github_search_code"
	toolECRScanResults                 = "ecr_scan_results"
	toolDatabaseQuery                  = "database_query"
	toolDatabaseList                   = "database_list"
	toolGrafanaListDashboards          = "grafana_list_dashboards"
	toolGrafanaGetDashboard            = "grafana_get_dashboard"
	toolGrafanaCreateDashboard         = "grafana_create_dashboard"
	toolGrafanaUpdateDashboard         = "grafana_update_dashboard"
	toolGrafanaPatchDashboard          = "grafana_patch_dashboard"
	toolGrafanaDeleteDashboard         = "grafana_delete_dashboard"
	toolGrafanaCreateFolder            = "grafana_create_folder"
	toolGrafanaGetDashboardVersion     = "grafana_get_dashboard_version"
	toolGrafanaRestoreDashboardVersion = "grafana_restore_dashboard_version"
	// CloudWatch Logs tools are defined in cloudwatch.go.
	// Prometheus tools are defined in prometheus.go.
)

// Server implements the MCP (Model Context Protocol) server.
type Server struct {
	lokiClient              *k8s.LokiClient
	githubClient            *github.Client
	gitlabClient            *GitLabClient
	dbClients               map[string]*DatabaseClient
	grafanaClient           *GrafanaClient
	graphqlClients          map[string]*GraphQLClient
	prometheusClients       map[string]*PrometheusClient
	tempoClients            map[string]*TempoClient
	cloudWatchClientFactory CloudWatchClientFactory
	apiToolRegistry         *apiconfig.APIToolRegistry
	k8sClusters             map[string]*k8s.Agent
	logger                  *slog.Logger
	companyName             string
	auditUser               string
	readOnly                bool
	maxToolOutputBytes      int
	authorizer              *authz.Policy
}

// NewServer creates a new MCP server.
func NewServer(lokiClient *k8s.LokiClient, githubToken string, apiToolRegistry *apiconfig.APIToolRegistry, logger *slog.Logger) (result *Server) {
	var githubClient *github.Client
	var grafanaClient *GrafanaClient

	if githubToken != "" {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: githubToken},
		)
		tc := oauth2.NewClient(context.Background(), ts)
		githubClient = github.NewClient(tc)
		logger.Info("GitHub client initialized")
	} else {
		logger.Warn("GitHub token not provided - GitHub tools will be unavailable")
	}

	// Initialize database clients from environment variables
	// Supports both legacy DATABASE_URL and multi-database DATABASE_<NAME>_URL patterns
	dbClients, dbErr := LoadDatabaseClients(logger)
	if dbErr != nil {
		logger.Warn("Database client initialization had errors",
			slog.String("error", dbErr.Error()))
	}

	if len(dbClients) > 0 {
		dbNames := make([]string, 0, len(dbClients))
		for name := range dbClients {
			dbNames = append(dbNames, name)
		}
		logger.Info("Database clients initialized",
			slog.Int("count", len(dbClients)),
			slog.Any("databases", dbNames))
	} else {
		logger.Info("No database configuration found - database tools will be unavailable")
	}

	// Initialize Grafana client if configured
	grafanaURL := os.Getenv("GRAFANA_URL")
	grafanaAPIKey := os.Getenv("GRAFANA_API_KEY")
	if grafanaURL != "" && grafanaAPIKey != "" {
		var err error
		grafanaClient, err = NewGrafanaClient(grafanaURL, grafanaAPIKey, logger)
		if err != nil {
			logger.Warn("Grafana client initialization failed - Grafana tools will be unavailable",
				slog.String("error", err.Error()))
		} else {
			logger.Info("Grafana client initialized - Grafana dashboard tools available")
		}
	} else {
		logger.Info("GRAFANA_URL or GRAFANA_API_KEY not provided - Grafana tools will be unavailable")
	}

	// Initialize GraphQL clients from environment variables
	// Supports GRAPHQL_URL (default) and GRAPHQL_<NAME>_URL patterns
	graphqlClients := LoadGraphQLClients(logger)

	// Initialize Prometheus clients from environment variables
	// Supports PROMETHEUS_URL (default) and PROMETHEUS_<NAME>_URL patterns
	prometheusClients := LoadPrometheusClients(logger)

	// Initialize GitLab client if configured
	var gitlabClient *GitLabClient
	glClient, glErr := NewGitLabClient(logger)
	if glErr != nil {
		logger.Info("GitLab not configured - GitLab tools will be unavailable")
	} else {
		gitlabClient = glClient
		logger.Info("GitLab client initialized")
	}

	// Initialize Tempo clients from environment variables
	// Supports TEMPO_URL (default) and TEMPO_<NAME>_URL patterns
	tempoClients := LoadTempoClients(logger)

	// Get company name from environment, default to "Company"
	companyName := os.Getenv("COMPANY_NAME")
	if companyName == "" {
		companyName = "Company"
	}

	result = &Server{
		lokiClient:              lokiClient,
		githubClient:            githubClient,
		gitlabClient:            gitlabClient,
		dbClients:               dbClients,
		grafanaClient:           grafanaClient,
		graphqlClients:          graphqlClients,
		prometheusClients:       prometheusClients,
		tempoClients:            tempoClients,
		cloudWatchClientFactory: defaultCloudWatchClientFactory,
		apiToolRegistry:         apiToolRegistry,
		k8sClusters:             loadK8sClusters(logger),
		logger:                  logger,
		companyName:             companyName,
		auditUser:               resolveAuditUser(logger),
		readOnly:                ReadOnlyEnabled(),
		maxToolOutputBytes:      resolveMaxToolOutputBytes(),
		authorizer:              loadAuthorizer(logger),
	}

	if result.readOnly {
		logger.Info("READ_ONLY mode enabled - all write tools (Grafana dashboard mutations) are disabled")
	}

	return result
}

// ReadOnlyEnabled reports whether the global read-only switch (the READ_ONLY
// env var) is set. When on, every write capability (Grafana dashboard
// create/update/patch/delete/restore and folder creation) is withheld from the
// toolset and rejected at dispatch, across both the Slack and MCP front-ends.
func ReadOnlyEnabled() (enabled bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("READ_ONLY"))) {
	case "1", "true", "yes", "on":
		enabled = true
	}

	return enabled
}

// isWriteTool reports whether a tool name mutates state. Grafana dashboard
// management is the only write surface in the toolset.
func isWriteTool(name string) (isWrite bool) {
	switch name {
	case toolGrafanaCreateDashboard,
		toolGrafanaUpdateDashboard,
		toolGrafanaPatchDashboard,
		toolGrafanaDeleteDashboard,
		toolGrafanaCreateFolder,
		toolGrafanaRestoreDashboardVersion:
		isWrite = true
	}

	return isWrite
}

// getLokiTools returns Loki-related tool definitions. When allowedTenants
// is non-empty (multi-tenant Loki, auth_enabled: true), the list is
// appended to the tool description so the calling LLM can discover which
// tenants are queryable, and the schema gains an optional tenant arg.
func getLokiTools(allowedTenants []string) (result []MCPTool) {
	description := "Query Loki log aggregation system for ModSecurity WAF logs. Returns JSON log entries with transaction details, blocked IPs, rule IDs, etc."

	properties := map[string]interface{}{
		"query": map[string]interface{}{
			"type":        "string",
			"description": "LogQL query string. Example: '{realm=\"prod\", namespace=\"ingress-nginx\"} |~ \"ModSecurity\" | json | transaction_response_http_code=\"403\"'",
		},
		"start": map[string]interface{}{
			"type":        "string",
			"description": "Start time as relative duration (e.g., '1h', '24h') or RFC3339 timestamp",
		},
		"end": map[string]interface{}{
			"type":        "string",
			"description": descEndTime,
		},
		"limit": map[string]interface{}{
			"type":        "integer",
			"description": "Maximum number of log entries to return (default: 100, recommended max: 500 to avoid token limits)",
		},
	}

	if len(allowedTenants) > 0 {
		description = fmt.Sprintf("%s Allowed tenants: %s.", description, strings.Join(allowedTenants, ", "))
		properties["tenant"] = map[string]interface{}{
			"type":        "string",
			"description": "Loki tenant (X-Scope-OrgID) for this query. Pipe-delimited values request a multi-tenant read (e.g. 'monitoring|cloudtrail'). Omit to use the server's default tenant. Allowed values: " + strings.Join(allowedTenants, ", ") + ".",
		}
	}

	result = []MCPTool{
		{
			Name:        toolQueryLoki,
			Description: description,
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": properties,
				"required":   []string{"query", "start"},
			},
		},
	}

	return result
}

// getUtilityTools returns utility tool definitions (whois, PDF generation).
func getUtilityTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolWhoisLookup,
			Description: "Perform whois lookup on an IP address to determine geolocation, ISP, ASN, and organization. Useful for analyzing blocked IPs to determine if they're VPNs, cloud providers, or suspicious sources.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ip_address": map[string]interface{}{
						"type":        "string",
						"description": "IP address to look up (IPv4 format, e.g., '192.168.1.1')",
					},
				},
				"required": []string{"ip_address"},
			},
		},
		{
			Name:        toolGeneratePDF,
			Description: "Generate a PDF report from Markdown content (headings, bold/italic, lists, code blocks, and tables are supported). The PDF will be saved to /tmp/ and automatically uploaded to Slack. ALWAYS use this tool for report generation to provide downloadable reports.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"markdown_content": map[string]interface{}{
						"type":        "string",
						"description": "Markdown content to convert to PDF. Use standard Markdown formatting (headers, tables, lists, bold, italic, code blocks). Tables are supported.",
					},
					"filename": map[string]interface{}{
						"type":        "string",
						"description": "Output filename (without path, .pdf extension will be added if missing). Example: 'modsecurity_report_2025-01-10'",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": "Report title for PDF metadata",
					},
				},
				"required": []string{"markdown_content", "filename"},
			},
		},
	}

	return result
}

// getGitHubTools returns GitHub-related tool definitions.
func getGitHubTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolGitHubGetFile,
			Description: "Fetch a file from a GitHub repository. Useful for reading database schema files, migration files, or configuration. Requires GITHUB_TOKEN to be configured.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"owner": map[string]interface{}{
						"type":        "string",
						"description": "Repository owner (e.g., 'your-org')",
					},
					"repo": map[string]interface{}{
						"type":        "string",
						"description": "Repository name (e.g., 'example-api')",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path within repository (e.g., 'db/schema.hcl' or 'db/migrations/20250101_add_users.sql')",
					},
					"ref": map[string]interface{}{
						"type":        "string",
						"description": "Git ref (branch, tag, or commit SHA). Defaults to 'main' if not provided",
					},
				},
				"required": []string{"owner", "repo", "path"},
			},
		},
		{
			Name:        toolGitHubListDirectory,
			Description: "List files in a GitHub repository directory. Useful for discovering migration files or schema versions. Requires GITHUB_TOKEN to be configured.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"owner": map[string]interface{}{
						"type":        "string",
						"description": "Repository owner (e.g., 'your-org')",
					},
					"repo": map[string]interface{}{
						"type":        "string",
						"description": "Repository name (e.g., 'example-api')",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory path (e.g., 'db/migrations')",
					},
					"ref": map[string]interface{}{
						"type":        "string",
						"description": "Git ref (branch, tag, or commit SHA). Defaults to 'main' if not provided",
					},
				},
				"required": []string{"owner", "repo", "path"},
			},
		},
		{
			Name:        toolGitHubSearchCode,
			Description: "Search for code across GitHub repositories. Useful for finding migration patterns or schema references. Requires GITHUB_TOKEN to be configured.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search query using GitHub code search syntax (e.g., 'table users repo:your-org/example-api path:db/migrations')",
					},
				},
				"required": []string{"query"},
			},
		},
	}

	return result
}

// getECRTools returns ECR-related tool definitions.
func getECRTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolECRScanResults,
			Description: "Query AWS ECR for container image vulnerability scan results across multiple accounts. Returns vulnerability findings with severity levels (CRITICAL, HIGH, MEDIUM, LOW), CVE IDs, affected packages, and remediation guidance.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"accounts": map[string]interface{}{
						"type":        "array",
						"description": "AWS account IDs to query. Example: ['123456789012', '210987654321']",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
					"regions": map[string]interface{}{
						"type":        "array",
						"description": "AWS regions to query ECR repositories. Defaults to ['us-east-1'] if not specified",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
					"max_age_days": map[string]interface{}{
						"type":        "integer",
						"description": "Only include images pushed within the last N days. Default: 30",
					},
					"min_severity": map[string]interface{}{
						"type":        "string",
						"description": "Minimum severity to report (CRITICAL, HIGH, MEDIUM, LOW, INFORMATIONAL). Default: all severities",
						"enum":        []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "INFORMATIONAL"},
					},
					"repositories": map[string]interface{}{
						"type":        "array",
						"description": "Specific repository names to scan (optional, defaults to all repositories)",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"required": []string{"accounts"},
			},
		},
	}

	return result
}

// getDatabaseTools returns database-related tool definitions.
func getDatabaseTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolDatabaseQuery,
			Description: "Execute a read-only SQL query against a database. Supports PostgreSQL, MySQL, and SQLite. Only SELECT, WITH, SHOW, DESCRIBE, and EXPLAIN queries are allowed.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "SQL query to execute (SELECT, WITH, SHOW, DESCRIBE, or EXPLAIN only)",
					},
					"database": map[string]interface{}{
						"type":        "string",
						"description": "Database name to query (e.g., 'terrace', 'mds'). Use 'database_list' tool to see available databases. Defaults to 'default' if not specified.",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        toolDatabaseList,
			Description: "List all available databases that can be queried. Returns the names of configured databases.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}

	return result
}

// getGrafanaTools returns Grafana dashboard management tools.
func getGrafanaTools() (result []MCPTool) {
	result = append(result, getGrafanaReadTools()...)
	result = append(result, getGrafanaWriteTools()...)
	return result
}

// getGrafanaReadTools returns Grafana tools for reading/listing dashboards.
func getGrafanaReadTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolGrafanaListDashboards,
			Description: "List all Grafana dashboards the user has access to",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        toolGrafanaGetDashboard,
			Description: "Get a specific Grafana dashboard by UID",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"uid": map[string]interface{}{
						"type":        "string",
						"description": "Dashboard UID",
					},
				},
				"required": []string{"uid"},
			},
		},
		{
			Name:        toolGrafanaGetDashboardVersion,
			Description: "Fetch dashboard version history. With only `uid`, lists all versions for that dashboard. With `uid` and `version`, fetches that specific version's full payload (metadata plus the saved dashboard `data`). Useful for diffing against the current state, picking a version to restore, or recovering content from a previous save.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"uid": map[string]interface{}{
						"type":        "string",
						"description": "Dashboard UID",
					},
					"version": map[string]interface{}{
						"type":        "integer",
						"description": "Version number (user-facing, from a previous list result). Omit to list all versions instead.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Optional list mode only: cap on number of versions returned (Grafana default: 1000).",
					},
					"start": map[string]interface{}{
						"type":        "integer",
						"description": "Optional list mode only: version number to start from (for pagination).",
					},
				},
				"required": []string{"uid"},
			},
		},
	}
	return result
}

// getGrafanaWriteTools returns Grafana tools for creating/updating/deleting dashboards.
func getGrafanaWriteTools() (result []MCPTool) {
	result = append(result, getGrafanaCreateDashboardTool())
	result = append(result, getGrafanaModifyTools()...)
	return result
}

// getPanelSchemaProperties returns the combined panel properties for the create dashboard tool schema.
func getPanelSchemaProperties() (result map[string]interface{}) {
	result = map[string]interface{}{
		"title": map[string]interface{}{
			"type":        "string",
			"description": "Panel title",
		},
		"query": map[string]interface{}{
			"type":        "string",
			"description": "Query for the panel (SQL, PromQL, or CloudWatch query)",
		},
		"sql": map[string]interface{}{
			"type":        "string",
			"description": "SQL query (deprecated, use 'query' instead)",
		},
		"panelType": map[string]interface{}{
			"type":        "string",
			"description": "Panel visualization type",
			"enum":        []string{"stat", "timeseries", "table", "piechart", "bargauge", "gauge", "heatmap"},
		},
		"datasourceType": map[string]interface{}{
			"type":        "string",
			"description": "Type of datasource (postgres, mysql, prometheus, cloudwatch, yesoreyeram-infinity-datasource)",
			"enum":        []string{"postgres", "mysql", "prometheus", "cloudwatch", grafanaInfinityDatasource},
			"default":     "postgres",
		},
		"datasourceUID": map[string]interface{}{
			"type":        "string",
			"description": "UID of the specific datasource (required — must match datasourceType; no default is applied)",
		},
		"description": map[string]interface{}{
			"type":        "string",
			"description": "Optional panel description",
		},
		"legend": map[string]interface{}{
			"type":        "string",
			"description": "Legend format for Prometheus queries (e.g., '{{instance}}')",
		},
		"region": map[string]interface{}{
			"type":        "string",
			"description": "AWS region for CloudWatch metrics (e.g., 'us-east-1')",
		},
		"namespace": map[string]interface{}{
			"type":        "string",
			"description": "CloudWatch namespace (e.g., 'AWS/EC2', 'AWS/RDS')",
		},
		"metricName": map[string]interface{}{
			"type":        "string",
			"description": "CloudWatch metric name (e.g., 'CPUUtilization')",
		},
		"statistics": map[string]interface{}{
			"type":        "array",
			"description": "CloudWatch statistics to fetch (e.g., ['Average', 'Maximum'])",
			"items": map[string]interface{}{
				"type": "string",
				"enum": []string{"Average", "Sum", "Maximum", "Minimum", "SampleCount"},
			},
		},
		"dimensions": map[string]interface{}{
			"type":        "object",
			"description": "CloudWatch dimensions as key-value pairs (e.g., {'InstanceId': 'i-123'})",
			"additionalProperties": map[string]interface{}{
				"type": "string",
			},
		},
	}

	// Add Infinity datasource properties
	addInfinitySchemaProperties(result)

	return result
}

// addInfinitySchemaProperties adds Infinity datasource-specific properties to the panel schema.
func addInfinitySchemaProperties(props map[string]interface{}) {
	props["infinityQueryType"] = map[string]interface{}{
		"type":        "string",
		"description": "Infinity query type: json, graphql, csv, xml (default: json)",
	}
	props["infinityParser"] = map[string]interface{}{
		"type":        "string",
		"description": "Infinity parser: simple, backend, uql, groq (default: backend)",
	}
	props["infinitySource"] = map[string]interface{}{
		"type":        "string",
		"description": "Infinity data source: url, inline (default: url)",
	}
	props["infinityUrl"] = map[string]interface{}{
		"type":        "string",
		"description": "Override URL for Infinity request (empty = use datasource default)",
	}
	props["infinityMethod"] = map[string]interface{}{
		"type":        "string",
		"description": "HTTP method for Infinity request: GET, POST (default: GET, POST for graphql)",
	}
	props["infinityBody"] = map[string]interface{}{
		"type":        "string",
		"description": "Request body for Infinity (GraphQL query string, JSON payload, etc.)",
	}
	props["infinityRootSelector"] = map[string]interface{}{
		"type":        "string",
		"description": "JSONPath root selector for Infinity response parsing",
	}
	props["infinityColumns"] = map[string]interface{}{
		"type":        "array",
		"description": "Column definitions for Infinity response mapping",
		"items": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "JSONPath selector for the column value",
				},
				"text": map[string]interface{}{
					"type":        "string",
					"description": "Display name for the column",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "Column data type: string, number, timestamp, timestamp_epoch",
				},
			},
		},
	}
}

// getGrafanaCreateDashboardTool returns the tool definition for creating dashboards.
func getGrafanaCreateDashboardTool() (tool MCPTool) {
	tool = MCPTool{
		Name:        toolGrafanaCreateDashboard,
		Description: "Create a new Grafana dashboard. Two modes: (1) typed builder via `panels` — convenient for SQL/Prometheus/CloudWatch/Infinity dashboards where the bot composes the JSON from PanelQueryConfig; (2) raw `dashboard` JSON — pass the full dashboard object verbatim, useful for Elasticsearch/OpenSearch (metrics, bucketAggs) or any datasource whose target fields the typed model doesn't cover. Supply exactly one of `panels` or `dashboard`.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title": map[string]interface{}{
					"type":        "string",
					"description": "Dashboard title (required for builder mode; for raw `dashboard` mode the title inside the object is what Grafana stores, but this top-level value is still required for audit logging).",
				},
				"panels": map[string]interface{}{
					"type":        "array",
					"description": "Builder mode: array of panel configurations. Mutually exclusive with `dashboard`.",
					"items": map[string]interface{}{
						"type":       "object",
						"properties": getPanelSchemaProperties(),
						"required":   []string{"title", "panelType"},
					},
				},
				"dashboard": map[string]interface{}{
					"type":        "object",
					"description": "Raw mode: complete dashboard JSON object, POSTed verbatim. Use this when panel targets contain datasource-specific fields the typed builder doesn't model (Elasticsearch metrics/bucketAggs, OpenSearch, etc.). Mutually exclusive with `panels`.",
				},
				"folderUid": map[string]interface{}{
					"type":        "string",
					"description": "Folder UID to create the dashboard in. If omitted, creates in the General folder.",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "Short description of why this dashboard is being created (the intention). Lands in Grafana's version history alongside the audit user.",
				},
			},
			"required": []string{"title"},
		},
	}
	return tool
}

// getGrafanaPatchDashboardTool returns the schema for the patch tool.
// Split out so getGrafanaModifyTools stays within funlen.
func getGrafanaPatchDashboardTool() (tool MCPTool) {
	tool = MCPTool{
		Name:        toolGrafanaPatchDashboard,
		Description: "Patch a Grafana dashboard server-side without round-tripping the full dashboard JSON. Useful for changing one or a few nested fields (e.g. time.from) on large dashboards. The bot fetches the dashboard losslessly, applies the patch in-memory, and POSTs the result back — the caller sends only the diff. Supply exactly one of `merge` (RFC 7386 JSON merge-patch) or `patches` (RFC 6902 JSON Patch operation list).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"uid": map[string]interface{}{
					"type":        "string",
					"description": "Dashboard UID to patch",
				},
				"merge": map[string]interface{}{
					"type":        "object",
					"description": "RFC 7386 JSON merge-patch object. Recursively merges into the dashboard; null values delete keys. Example: {\"time\": {\"from\": \"now-1h\"}} changes only time.from. Cannot replace one element of an array — use `patches` for surgical array ops. Mutually exclusive with `patches`.",
				},
				"patches": map[string]interface{}{
					"type":        "array",
					"description": "RFC 6902 JSON Patch operation list. Each op is {op, path, value?, from?}. Supported ops: add, remove, replace, move, copy, test. The `test` op aborts the write on mismatch — use it for optimistic locking. Example: [{\"op\":\"replace\",\"path\":\"/time/from\",\"value\":\"now-1h\"}]. Mutually exclusive with `merge`.",
					"items": map[string]interface{}{
						"type": "object",
					},
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "Short description of why this patch is being applied. Composed with the audit user into Grafana's version history note.",
				},
			},
			"required": []string{"uid"},
		},
	}
	return tool
}

// getGrafanaModifyTools returns Grafana tools for updating/deleting dashboards.
func getGrafanaModifyTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolGrafanaUpdateDashboard,
			Description: "Update an existing Grafana dashboard",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"uid": map[string]interface{}{
						"type":        "string",
						"description": "Dashboard UID to update",
					},
					"dashboard": map[string]interface{}{
						"type":        "object",
						"description": "Complete dashboard JSON object",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Update message/reason",
					},
					"folderUid": map[string]interface{}{
						"type":        "string",
						"description": "Target folder UID. If omitted, the dashboard stays in its current folder.",
					},
				},
				"required": []string{"uid", "dashboard"},
			},
		},
		getGrafanaPatchDashboardTool(),
		{
			Name:        toolGrafanaDeleteDashboard,
			Description: "Delete a Grafana dashboard",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"uid": map[string]interface{}{
						"type":        "string",
						"description": "Dashboard UID to delete",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Short description of why this dashboard is being deleted. Grafana's DELETE leaves no version history; the deletion is recorded in the bot's slog with the audit user and this reason.",
					},
				},
				"required": []string{"uid"},
			},
		},
		{
			Name:        toolGrafanaRestoreDashboardVersion,
			Description: "Restore a Grafana dashboard to a previous version. Grafana itself stamps the new version with 'Restored from version N' — the audit invariant we apply to other writes is intentionally not applied here, since by definition a restore reverts to a known prior state and the version history alone is the audit trail. The bot still records the restore in slog with audit_user and audit_source_ip for forensic purposes.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"uid": map[string]interface{}{
						"type":        "string",
						"description": "Dashboard UID",
					},
					"version": map[string]interface{}{
						"type":        "integer",
						"description": "Version number to restore (from grafana_get_dashboard_version's list output).",
					},
				},
				"required": []string{"uid", "version"},
			},
		},
		{
			Name:        toolGrafanaCreateFolder,
			Description: "Create a Grafana folder (directory) used to group dashboards. Returns the new folder's UID, which dashboards reference via folderUid.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{
						"type":        "string",
						"description": "Folder display name (required)",
					},
					"uid": map[string]interface{}{
						"type":        "string",
						"description": "Optional folder UID. If omitted, Grafana auto-generates one.",
					},
					"parentUid": map[string]interface{}{
						"type":        "string",
						"description": "Optional parent folder UID for nested folders. Omit to create at the top level.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Short description of why this folder is being created. Recorded in the bot's slog with the audit user (Grafana's folder API has no version history).",
					},
				},
				"required": []string{"title"},
			},
		},
	}

	return result
}

// getToolDefinitions returns the list of available MCP tool definitions
// based on which backing services are configured.
func (s *Server) getToolDefinitions() (result []MCPTool) {
	if s.lokiClient != nil {
		result = append(result, getLokiTools(s.lokiClient.AllowedTenants())...)
	}

	// Utility tools (whois, PDF) are always available
	result = append(result, getUtilityTools()...)

	if s.githubClient != nil {
		result = append(result, getGitHubTools()...)
	}

	// ECR uses the default AWS credential chain; include if AWS is configured
	if os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "" {
		result = append(result, getECRTools()...)
	}

	if len(s.dbClients) > 0 {
		result = append(result, getDatabaseTools()...)
	}

	if len(s.k8sClusters) > 0 {
		result = append(result, getK8sTools()...)
	}

	if s.grafanaClient != nil {
		result = append(result, getGrafanaReadTools()...)

		// Grafana dashboard mutation is the only write capability in the
		// toolset, and it is withheld entirely in read-only mode.
		if !s.readOnly {
			result = append(result, getGrafanaWriteTools()...)
		}
	}

	if isCloudWatchConfigured() {
		result = append(result, getCloudWatchTools()...)
	}

	if len(s.prometheusClients) > 0 {
		result = append(result, getPrometheusTools()...)
	}

	if len(s.graphqlClients) > 0 {
		result = append(result, getGraphQLTools()...)
	}

	// Add dynamically loaded third-party API tools
	if s.apiToolRegistry != nil && s.apiToolRegistry.HasTools() {
		for _, tool := range s.apiToolRegistry.GetToolDefinitions() {
			result = append(result, MCPTool{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
		}
	}

	return result
}

// ToolDefinitions returns the gated list of tools the server advertises,
// for in-process callers (the Slack agent loop) that drive the same tool
// surface as the MCP transports. The set is gated by which backends are
// configured, identical to what the MCP transports expose.
func (s *Server) ToolDefinitions() (tools []MCPTool) {
	tools = s.getToolDefinitions()
	return tools
}

// dispatchToolCall routes a tool call to the appropriate handler.
func (s *Server) dispatchToolCall(ctx context.Context, toolName string, args map[string]interface{}) (result string, err error) {
	// Defense in depth: even though read-only mode withholds write tools from
	// the advertised list, reject any write call that still arrives.
	if s.readOnly && isWriteTool(toolName) {
		err = fmt.Errorf("tool %q is disabled: server is in read-only mode", toolName)
		return result, err
	}

	switch toolName {
	case toolQueryLoki:
		result, err = s.executeQueryLoki(ctx, args)
	case toolWhoisLookup:
		result, err = s.executeWhoisLookup(ctx, args)
	case toolGeneratePDF:
		result, err = s.executeGeneratePDF(ctx, args)
	case toolGitHubGetFile:
		result, err = s.executeGitHubGetFile(ctx, args)
	case toolGitHubListDirectory:
		result, err = s.executeGitHubListDirectory(ctx, args)
	case toolGitHubSearchCode:
		result, err = s.executeGitHubSearchCode(ctx, args)
	case toolECRScanResults:
		result, err = s.executeECRScanResults(ctx, args)
	case toolDatabaseQuery:
		result, err = s.executeDatabaseQuery(ctx, args)
	case toolDatabaseList:
		result, err = s.executeDatabaseList(ctx, args)
	case toolGrafanaListDashboards:
		result, err = s.executeGrafanaListDashboards(ctx, args)
	case toolGrafanaGetDashboard:
		result, err = s.executeGrafanaGetDashboard(ctx, args)
	case toolGrafanaGetDashboardVersion:
		result, err = s.executeGrafanaGetDashboardVersion(ctx, args)
	case toolGrafanaRestoreDashboardVersion:
		result, err = s.executeGrafanaRestoreDashboardVersion(ctx, args)
	case toolGrafanaCreateDashboard:
		result, err = s.executeGrafanaCreateDashboard(ctx, args)
	case toolGrafanaUpdateDashboard:
		result, err = s.executeGrafanaUpdateDashboard(ctx, args)
	case toolGrafanaPatchDashboard:
		result, err = s.executeGrafanaPatchDashboard(ctx, args)
	case toolGrafanaDeleteDashboard:
		result, err = s.executeGrafanaDeleteDashboard(ctx, args)
	case toolGrafanaCreateFolder:
		result, err = s.executeGrafanaCreateFolder(ctx, args)
	default:
		result, err = s.dispatchExtendedToolCall(ctx, toolName, args)
	}

	return result, err
}

// DispatchTool executes a tool by name in-process and returns its textual
// result. It is the exact dispatch path used by the MCP HTTP transport,
// exposed for in-process callers (the Slack agent loop) so that every
// front-end drives one identical, gated tool surface.
func (s *Server) DispatchTool(ctx context.Context, name string, args map[string]interface{}) (result string, err error) {
	err = s.authorize(ctx, name)
	if err != nil {
		return result, err
	}

	result, err = s.dispatchToolCall(ctx, name, args)
	if err == nil {
		result = capToolResult(result, s.maxToolOutputBytes)
	}

	return result, err
}

// dispatchExtendedToolCall handles CloudWatch, Prometheus, and GraphQL tool dispatch.
func (s *Server) dispatchExtendedToolCall(ctx context.Context, toolName string, args map[string]interface{}) (result string, err error) {
	switch toolName {
	case toolCloudWatchLogsQuery:
		result, err = s.executeCloudWatchLogsQuery(ctx, args)
	case toolCloudWatchLogsListGroups:
		result, err = s.executeCloudWatchLogsListGroups(ctx, args)
	case toolCloudWatchLogsGetEvents:
		result, err = s.executeCloudWatchLogsGetEvents(ctx, args)
	case toolPrometheusQuery:
		result, err = s.executePrometheusQuery(ctx, args)
	case toolPrometheusQueryRange:
		result, err = s.executePrometheusQueryRange(ctx, args)
	case toolPrometheusSeries:
		result, err = s.executePrometheusSeries(ctx, args)
	case toolPrometheusLabelValues:
		result, err = s.executePrometheusLabelValues(ctx, args)
	case toolPrometheusListEndpoints:
		result, err = s.executePrometheusListEndpoints(ctx, args)
	case toolGraphQLQuery:
		result, err = s.executeGraphQLQuery(ctx, args)
	case toolGraphQLListEndpoints:
		result, err = s.executeGraphQLListEndpoints(ctx, args)
	case toolK8sGetResource:
		result, err = s.executeK8sGetResource(ctx, args)
	case toolK8sListResources:
		result, err = s.executeK8sListResources(ctx, args)
	case toolK8sPodLogs:
		result, err = s.executeK8sPodLogs(ctx, args)
	case toolK8sListPods:
		result, err = s.executeK8sListPods(ctx, args)
	case toolK8sGetEvents:
		result, err = s.executeK8sGetEvents(ctx, args)
	default:
		// Try dynamically loaded API tools
		if s.apiToolRegistry != nil {
			var handled bool
			result, handled, err = s.apiToolRegistry.DispatchToolCall(ctx, toolName, args)
			if handled {
				return result, err
			}
		}
		err = fmt.Errorf("unknown tool: %s", toolName)
	}

	return result, err
}

// executeQueryLoki executes a Loki query.
func (s *Server) executeQueryLoki(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var queryResult k8s.QueryResult

	query, _ := args["query"].(string)
	start, _ := args["start"].(string)
	end, _ := args["end"].(string)

	limit := 100
	if limitFloat, ok := args["limit"].(float64); ok {
		limit = int(limitFloat)
	}

	// Cap at 500 to avoid overwhelming Claude Code with data
	if limit > 500 {
		limit = 500
	}

	tenant, _ := args["tenant"].(string)

	queryResult, err = s.lokiClient.Query(ctx, k8s.QueryRequest{
		Query:  query,
		Start:  start,
		End:    end,
		Limit:  limit,
		Tenant: tenant,
	})
	if err != nil {
		return result, err
	}

	result = queryResult.FormatResultAsText()
	return result, err
}

// executeWhoisLookup performs a whois lookup.
func (s *Server) executeWhoisLookup(ctx context.Context, args map[string]interface{}) (result string, err error) {
	ipAddress, _ := args["ip_address"].(string)

	result, err = k8s.NewWhoisClient(s.logger).Lookup(ctx, ipAddress)
	return result, err
}

// executeGeneratePDF renders Markdown content to a PDF in /tmp using a pure-Go
// pipeline (see renderMarkdownPDF). The bot's PDF scanner picks it up and
// uploads it to Slack.
func (s *Server) executeGeneratePDF(ctx context.Context, args map[string]interface{}) (result string, err error) {
	markdownContent, _ := args["markdown_content"].(string)
	filename, _ := args["filename"].(string)
	title, _ := args["title"].(string)

	if markdownContent == "" {
		err = errors.New("markdown_content is required")
		return result, err
	}

	if filename == "" {
		err = errors.New("filename is required")
		return result, err
	}

	// Ensure .pdf extension and strip any directory components so the model
	// cannot direct the write outside /tmp via the filename.
	if !strings.HasSuffix(filename, ".pdf") {
		filename += ".pdf"
	}
	filename = filepath.Base(filename)
	outputPath := filepath.Join("/tmp", filename)

	var data []byte

	data, err = renderMarkdownPDF(ctx, markdownContent, title, s.companyName)
	if err != nil {
		return result, err
	}

	err = os.WriteFile(outputPath, data, 0o600)
	if err != nil {
		err = fmt.Errorf("writing PDF: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "PDF generated successfully",
		slog.String("path", outputPath),
		slog.Int("size_bytes", len(data)),
		slog.String("filename", filename))

	result = fmt.Sprintf("PDF generated successfully at %s (%.2f KB). The bot will automatically scan /tmp and upload this file to Slack.", outputPath, float64(len(data))/1024.0)
	return result, err
}

// executeGitHubGetFile fetches a file from a GitHub repository.
func (s *Server) executeGitHubGetFile(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.githubClient == nil {
		err = errors.New("GitHub access not configured (GITHUB_TOKEN not set)")
		return result, err
	}

	owner, _ := args["owner"].(string)
	repo, _ := args["repo"].(string)
	path, _ := args["path"].(string)
	ref, _ := args["ref"].(string)

	if ref == "" {
		ref = "main"
	}

	opts := &github.RepositoryContentGetOptions{Ref: ref}
	fileContent, _, _, getErr := s.githubClient.Repositories.GetContents(ctx, owner, repo, path, opts)
	if getErr != nil {
		err = fmt.Errorf("fetching file from GitHub: %w", getErr)
		return result, err
	}

	if fileContent == nil {
		err = errors.New("file not found")
		return result, err
	}

	content, decodeErr := fileContent.GetContent()
	if decodeErr != nil {
		err = fmt.Errorf("decoding file content: %w", decodeErr)
		return result, err
	}

	s.logger.InfoContext(ctx, "fetched file from GitHub",
		slog.String("repo", fmt.Sprintf("%s/%s", owner, repo)),
		slog.String("path", path),
		slog.String("ref", ref),
		slog.Int("size", len(content)))

	result = fmt.Sprintf("File: %s/%s/%s (ref: %s)\nSize: %d bytes\n\n%s", owner, repo, path, ref, len(content), content)
	return result, err
}

// executeGitHubListDirectory lists files in a GitHub repository directory.
func (s *Server) executeGitHubListDirectory(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.githubClient == nil {
		err = errors.New("GitHub access not configured (GITHUB_TOKEN not set)")
		return result, err
	}

	owner, _ := args["owner"].(string)
	repo, _ := args["repo"].(string)
	path, _ := args["path"].(string)
	ref, _ := args["ref"].(string)

	if ref == "" {
		ref = "main"
	}

	opts := &github.RepositoryContentGetOptions{Ref: ref}
	_, dirContents, _, listErr := s.githubClient.Repositories.GetContents(ctx, owner, repo, path, opts)
	if listErr != nil {
		err = fmt.Errorf("listing directory: %w", listErr)
		return result, err
	}

	var files []string

	for _, content := range dirContents {
		files = append(files, fmt.Sprintf("  %s  %-8s  %8d bytes",
			content.GetName(), content.GetType(), content.GetSize()))
	}

	s.logger.InfoContext(ctx, "listed directory from GitHub",
		slog.String("repo", fmt.Sprintf("%s/%s", owner, repo)),
		slog.String("path", path),
		slog.String("ref", ref),
		slog.Int("file_count", len(files)))

	result = fmt.Sprintf("Directory: %s/%s/%s (ref: %s)\nFiles: %d\n\n%s",
		owner, repo, path, ref, len(files), strings.Join(files, "\n"))
	return result, err
}

// executeGitHubSearchCode searches for code in GitHub repositories.
func (s *Server) executeGitHubSearchCode(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.githubClient == nil {
		err = errors.New("GitHub access not configured (GITHUB_TOKEN not set)")
		return result, err
	}

	query, _ := args["query"].(string)

	opts := &github.SearchOptions{ListOptions: github.ListOptions{PerPage: 10}}
	searchResult, _, searchErr := s.githubClient.Search.Code(ctx, query, opts)
	if searchErr != nil {
		err = fmt.Errorf("searching code: %w", searchErr)
		return result, err
	}

	var results []string

	for _, codeResult := range searchResult.CodeResults {
		results = append(results, fmt.Sprintf("  %s:%s\n    URL: %s",
			codeResult.Repository.GetFullName(),
			codeResult.GetPath(),
			codeResult.GetHTMLURL()))
	}

	s.logger.InfoContext(ctx, "searched GitHub code",
		slog.String("query", query),
		slog.Int("total_count", searchResult.GetTotal()),
		slog.Int("returned", len(results)))

	result = fmt.Sprintf("Found %d results for: %s\n\n%s",
		searchResult.GetTotal(), query, strings.Join(results, "\n\n"))
	return result, err
}

// executeDatabaseQuery executes a read-only database query.
func (s *Server) executeDatabaseQuery(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if len(s.dbClients) == 0 {
		err = errors.New("no database connections configured. Set DATABASE_URL or DATABASE_<NAME>_URL environment variables")
		return result, err
	}

	query, _ := args["query"].(string)
	if query == "" {
		err = errors.New("query parameter is required")
		return result, err
	}

	// Get the database name, default to "default"
	dbName := "default"
	if name, ok := args["database"].(string); ok && name != "" {
		dbName = strings.ToLower(name)
	}

	// Look up the database client
	client, exists := s.dbClients[dbName]
	if !exists {
		availableDBs := make([]string, 0, len(s.dbClients))
		for name := range s.dbClients {
			availableDBs = append(availableDBs, name)
		}
		err = fmt.Errorf("database %q not configured. Available databases: %v", dbName, availableDBs)
		return result, err
	}

	s.logger.InfoContext(ctx, "Executing database query",
		slog.String("database", dbName),
		slog.String("query", query))

	// Execute the read-only query
	var queryResult QueryResult
	queryResult, err = client.ExecuteReadOnlyQuery(ctx, query)
	if err != nil {
		return result, err
	}

	// Format the result as JSON for Claude to parse
	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(queryResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting query result: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executeDatabaseList returns a list of available databases.
func (s *Server) executeDatabaseList(_ context.Context, _ map[string]interface{}) (result string, err error) {
	if len(s.dbClients) == 0 {
		err = errors.New("no database connections configured. Set DATABASE_URL or DATABASE_<NAME>_URL environment variables")
		return result, err
	}

	databases := GetAvailableDatabases(s.dbClients)

	// Format the result as JSON
	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(databases, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting database list: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executeGrafanaListDashboards lists all Grafana dashboards.
func (s *Server) executeGrafanaListDashboards(ctx context.Context, _ map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	var dashboards []DashboardSearchResponse
	dashboards, err = s.grafanaClient.ListDashboards(ctx)
	if err != nil {
		return result, err
	}

	// Format the result as JSON
	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(dashboards, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting dashboard list: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executeGrafanaGetDashboard retrieves a specific dashboard.
func (s *Server) executeGrafanaGetDashboard(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	uid, _ := args["uid"].(string)
	if uid == "" {
		err = errors.New("uid parameter is required")
		return result, err
	}

	var resp *DashboardGetResponse
	resp, err = s.grafanaClient.GetDashboard(ctx, uid)
	if err != nil {
		return result, err
	}

	// Format the result as JSON, including folder metadata
	output := map[string]interface{}{
		"dashboard": resp.Dashboard,
		"folderUid": resp.FolderUID,
	}

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(output, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting dashboard: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executeGrafanaGetDashboardVersion is a two-mode tool: with only `uid`,
// it lists all versions; with `uid` and `version`, it fetches that
// version's full payload. Both modes return the raw Grafana response so
// no unmodeled fields are stripped — same contract as grafana_get_dashboard.
func (s *Server) executeGrafanaGetDashboardVersion(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	uid, _ := args["uid"].(string)
	if uid == "" {
		err = errors.New("uid parameter is required")
		return result, err
	}

	var body json.RawMessage

	versionFloat, hasVersion := args["version"].(float64)
	if hasVersion {
		body, err = s.grafanaClient.GetDashboardVersion(ctx, uid, int(versionFloat))
		if err != nil {
			return result, err
		}
		result = string(body)
		return result, err
	}

	// List mode.
	limit := 0
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	start := 0
	if s, ok := args["start"].(float64); ok {
		start = int(s)
	}

	body, err = s.grafanaClient.ListDashboardVersions(ctx, uid, limit, start)
	if err != nil {
		return result, err
	}
	result = string(body)
	return result, err
}

// executeGrafanaRestoreDashboardVersion restores a dashboard to a previous
// version. Grafana stamps the new version with "Restored from version N";
// we don't override that (restore IS the audit). For our own forensic
// trail, we still emit a slog INFO with audit_user and audit_source_ip.
func (s *Server) executeGrafanaRestoreDashboardVersion(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	uid, _ := args["uid"].(string)
	if uid == "" {
		err = errors.New("uid parameter is required")
		return result, err
	}

	versionFloat, hasVersion := args["version"].(float64)
	if !hasVersion {
		err = errors.New("version parameter is required (the version number to restore, from grafana_get_dashboard_version's list output)")
		return result, err
	}
	version := int(versionFloat)

	var response json.RawMessage
	response, err = s.grafanaClient.RestoreDashboardVersion(ctx, uid, version)
	if err != nil {
		return result, err
	}

	auditUser := s.auditUserFromContext(ctx)
	s.logger.InfoContext(ctx, "grafana write",
		slog.String("tool", toolGrafanaRestoreDashboardVersion),
		slog.String("uid", uid),
		slog.Int("restored_version", version),
		slog.String("audit_user", auditUser),
		slog.String("audit_source_ip", auditSourceFromContext(ctx)),
	)

	result = fmt.Sprintf("Successfully restored dashboard %s to version %d.\n\n%s", uid, version, string(response))
	return result, err
}

// parsePanelConfigs parses raw panel data into PanelQueryConfig structs.
func (s *Server) parsePanelConfigs(panelsRaw []interface{}) (panels []PanelQueryConfig, err error) {
	for i, panelRaw := range panelsRaw {
		panelMap, panelOk := panelRaw.(map[string]interface{})
		if !panelOk {
			err = errors.New("each panel must be an object")
			return panels, err
		}

		panel := s.parseSinglePanelConfig(panelMap)
		if panel.DatasourceUID == "" {
			err = fmt.Errorf("panel %d (%q): datasourceUID is required — specify the UID of the datasource that should run this panel's query", i, panel.Title)
			return panels, err
		}
		panels = append(panels, panel)
	}
	return panels, err
}

// parseSinglePanelConfig parses a single panel configuration.
func (s *Server) parseSinglePanelConfig(panelMap map[string]interface{}) (panel PanelQueryConfig) {
	panel.Title, _ = panelMap["title"].(string)
	panel.PanelType, _ = panelMap["panelType"].(string)
	panel.DatasourceUID, _ = panelMap["datasourceUID"].(string)

	// Support both 'query' and 'sql' fields for backward compatibility
	panel.Query, _ = panelMap["query"].(string)
	if panel.Query == "" {
		panel.Query, _ = panelMap["sql"].(string) // Fallback to old field
	}

	// Determine datasource type (default to postgres for backward compatibility)
	panel.DatasourceType, _ = panelMap["datasourceType"].(string)
	if panel.DatasourceType == "" && panel.Query != "" {
		panel.DatasourceType = "postgres" // Default for backward compatibility
	}

	// Parse optional fields for different datasource types
	panel.Legend, _ = panelMap["legend"].(string)
	panel.Region, _ = panelMap["region"].(string)
	panel.Namespace, _ = panelMap["namespace"].(string)
	panel.MetricName, _ = panelMap["metricName"].(string)

	// Parse statistics array for CloudWatch
	if statsRaw, statsOk := panelMap["statistics"].([]interface{}); statsOk {
		for _, stat := range statsRaw {
			if statStr, statOk := stat.(string); statOk {
				panel.Statistics = append(panel.Statistics, statStr)
			}
		}
	}

	// Parse dimensions map for CloudWatch
	panel.Dimensions = make(map[string]string)
	if dimRaw, dimOk := panelMap["dimensions"].(map[string]interface{}); dimOk {
		for k, v := range dimRaw {
			if vStr, vOk := v.(string); vOk {
				panel.Dimensions[k] = vStr
			}
		}
	}

	// Parse Infinity datasource fields
	parseInfinityPanelFields(&panel, panelMap)

	return panel
}

// parseInfinityPanelFields extracts Infinity datasource fields from a raw panel map.
func parseInfinityPanelFields(panel *PanelQueryConfig, panelMap map[string]interface{}) {
	panel.InfinityQueryType, _ = panelMap["infinityQueryType"].(string)
	panel.InfinityParser, _ = panelMap["infinityParser"].(string)
	panel.InfinitySource, _ = panelMap["infinitySource"].(string)
	panel.InfinityURL, _ = panelMap["infinityUrl"].(string)
	panel.InfinityMethod, _ = panelMap["infinityMethod"].(string)
	panel.InfinityBody, _ = panelMap["infinityBody"].(string)
	panel.InfinityRootSelector, _ = panelMap["infinityRootSelector"].(string)

	// Parse Infinity columns array
	colsRaw, colsOk := panelMap["infinityColumns"].([]interface{})
	if !colsOk {
		return
	}

	for _, colRaw := range colsRaw {
		colMap, colOk := colRaw.(map[string]interface{})
		if !colOk {
			continue
		}

		col := InfinityColumn{}
		col.Selector, _ = colMap["selector"].(string)
		col.Text, _ = colMap["text"].(string)
		col.Type, _ = colMap["type"].(string)
		panel.InfinityColumns = append(panel.InfinityColumns, col)
	}
}

// executeGrafanaCreateDashboard creates a new dashboard from queries (SQL, PromQL, CloudWatch).
func (s *Server) executeGrafanaCreateDashboard(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	title, _ := args["title"].(string)
	if title == "" {
		err = errors.New("title parameter is required")
		return result, err
	}

	// Validate mode selection: exactly one of `panels` (typed builder)
	// or `dashboard` (raw bytes). Raw mode exists for datasource families
	// the typed model doesn't cover (Elasticsearch metrics/bucketAggs, etc.).
	panelsRaw, hasPanels := args["panels"].([]interface{})
	dashboardRaw, hasDashboard := args["dashboard"].(map[string]interface{})
	if !hasPanels && !hasDashboard {
		err = errors.New("must supply exactly one of `panels` (typed builder) or `dashboard` (raw JSON object)")
		return result, err
	}
	if hasPanels && hasDashboard {
		err = errors.New("must supply exactly one of `panels` or `dashboard`, not both")
		return result, err
	}

	folderUID, _ := args["folderUid"].(string)
	intention, _ := args["message"].(string)
	auditUser := s.auditUserFromContext(ctx)
	versionNote := composeVersionNote(auditUser, intention, "created via mcp")

	var uid string
	if hasDashboard {
		uid, err = s.createDashboardRawPath(ctx, dashboardRaw, folderUID, versionNote)
	} else {
		uid, err = s.createDashboardBuilderPath(ctx, panelsRaw, title, folderUID, versionNote)
	}
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "grafana write",
		slog.String("tool", toolGrafanaCreateDashboard),
		slog.String("uid", uid),
		slog.String("title", title),
		slog.String("audit_user", auditUser),
		slog.String("audit_source_ip", auditSourceFromContext(ctx)),
		slog.String("message", versionNote),
	)

	result = fmt.Sprintf("Successfully created dashboard '%s' with UID: %s\n\nDashboard URL: %s/d/%s/%s",
		title, uid, s.grafanaClient.baseURL, uid, strings.ReplaceAll(strings.ToLower(title), " ", "-"))
	return result, err
}

// createDashboardRawPath POSTs the caller's dashboard payload verbatim.
// Used when the LLM supplies a raw `dashboard` JSON object on create —
// the path Elasticsearch/OpenSearch dashboards must take since the typed
// builder doesn't model metrics/bucketAggs (issue #17).
func (s *Server) createDashboardRawPath(ctx context.Context, dashboardRaw map[string]interface{}, folderUID, versionNote string) (uid string, err error) {
	var dashboardBytes []byte
	dashboardBytes, err = json.Marshal(dashboardRaw)
	if err != nil {
		err = fmt.Errorf("marshaling dashboard: %w", err)
		return uid, err
	}
	uid, err = s.grafanaClient.CreateDashboardRaw(ctx, dashboardBytes, folderUID, versionNote)
	return uid, err
}

// createDashboardBuilderPath runs the typed builder. Used when the LLM
// supplies `panels` as configuration that the bot composes into a
// Dashboard{}. Convenient for SQL/Prometheus/CloudWatch/Infinity; cannot
// express datasource types whose targets aren't modeled.
func (s *Server) createDashboardBuilderPath(ctx context.Context, panelsRaw []interface{}, title, folderUID, versionNote string) (uid string, err error) {
	if len(panelsRaw) == 0 {
		err = errors.New("panels parameter must be a non-empty array")
		return uid, err
	}
	var panels []PanelQueryConfig
	panels, err = s.parsePanelConfigs(panelsRaw)
	if err != nil {
		return uid, err
	}
	uid, err = s.grafanaClient.CreateDashboardFromQueries(ctx, title, panels, folderUID, versionNote)
	return uid, err
}

// executeGrafanaUpdateDashboard updates an existing dashboard.
func (s *Server) executeGrafanaUpdateDashboard(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	uid, _ := args["uid"].(string)
	if uid == "" {
		err = errors.New("uid parameter is required")
		return result, err
	}

	dashboardRaw, ok := args["dashboard"].(map[string]interface{})
	if !ok {
		err = errors.New("dashboard parameter is required and must be an object")
		return result, err
	}

	intention, _ := args["message"].(string)
	auditUser := s.auditUserFromContext(ctx)
	versionNote := composeVersionNote(auditUser, intention, "updated via mcp")

	// Determine target folder: use explicit folderUid if provided,
	// otherwise fetch the existing dashboard's folder to preserve placement.
	folderUID, _ := args["folderUid"].(string)
	if folderUID == "" {
		var existing *DashboardGetResponse
		existing, err = s.grafanaClient.GetDashboard(ctx, uid)
		if err != nil {
			err = fmt.Errorf("fetching existing dashboard to preserve folder: %w", err)
			return result, err
		}
		folderUID = existing.FolderUID
	}

	// args.uid is the source of truth for which dashboard to update — it
	// trumps whatever (or no) uid the LLM put into the dashboard payload.
	dashboardRaw["uid"] = uid
	title, _ := dashboardRaw["title"].(string)

	// Marshal the raw map directly. We deliberately do NOT round-trip
	// through the typed Dashboard{}: every field the model doesn't cover
	// (panel expression/period/accountId, target legendFormat,
	// annotations, refresh, variables, etc.) would silently disappear,
	// degrading the panel on every edit.
	var dashboardBytes []byte
	dashboardBytes, err = json.Marshal(dashboardRaw)
	if err != nil {
		err = fmt.Errorf("marshaling dashboard: %w", err)
		return result, err
	}

	err = s.grafanaClient.UpdateDashboard(ctx, dashboardBytes, folderUID, versionNote)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "grafana write",
		slog.String("tool", toolGrafanaUpdateDashboard),
		slog.String("uid", uid),
		slog.String("title", title),
		slog.String("audit_user", auditUser),
		slog.String("audit_source_ip", auditSourceFromContext(ctx)),
		slog.String("message", versionNote),
	)

	result = fmt.Sprintf("Successfully updated dashboard with UID: %s", uid)
	return result, err
}

// executeGrafanaPatchDashboard patches a dashboard server-side without
// requiring the caller to ship the full model. The bot fetches the
// dashboard losslessly (preserves every Grafana field the typed structs
// don't model), applies a merge-patch or JSON Patch in-memory, and POSTs
// the result back via the same UpdateDashboard path used by the full-
// update tool. The LLM sends only the diff — a single-field edit on a
// 52KB dashboard becomes a ~30-byte input.
func (s *Server) executeGrafanaPatchDashboard(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	uid, _ := args["uid"].(string)
	if uid == "" {
		err = errors.New("uid parameter is required")
		return result, err
	}

	mergeRaw, hasMerge := args["merge"]
	patchesRaw, hasPatches := args["patches"]
	if !hasMerge && !hasPatches {
		err = errors.New("must supply exactly one of `merge` (RFC 7386 merge-patch object) or `patches` (RFC 6902 JSON Patch op list)")
		return result, err
	}
	if hasMerge && hasPatches {
		err = errors.New("must supply exactly one of `merge` or `patches`, not both")
		return result, err
	}

	// Lossless GET — relies on the json.RawMessage representation so every
	// unmodeled Grafana field survives into the patch input.
	var existing *DashboardGetResponse
	existing, err = s.grafanaClient.GetDashboard(ctx, uid)
	if err != nil {
		err = fmt.Errorf("fetching dashboard %s for patch: %w", uid, err)
		return result, err
	}

	var patched []byte
	patched, err = applyDashboardPatch(existing.Dashboard, mergeRaw, patchesRaw, hasMerge)
	if err != nil {
		return result, err
	}

	intention, _ := args["message"].(string)
	auditUser := s.auditUserFromContext(ctx)
	versionNote := composeVersionNote(auditUser, intention, "patched via mcp")

	err = s.grafanaClient.UpdateDashboard(ctx, patched, existing.FolderUID, versionNote)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "grafana write",
		slog.String("tool", toolGrafanaPatchDashboard),
		slog.String("uid", uid),
		slog.String("title", existing.Title),
		slog.String("audit_user", auditUser),
		slog.String("audit_source_ip", auditSourceFromContext(ctx)),
		slog.String("message", versionNote),
	)

	result = fmt.Sprintf("Successfully patched dashboard with UID: %s", uid)
	return result, err
}

// applyDashboardPatch runs either a merge-patch or a JSON Patch against
// the dashboard bytes. Caller is responsible for ensuring exactly one of
// the two inputs is present (the executor validates).
func applyDashboardPatch(dashboard []byte, mergeRaw, patchesRaw interface{}, useMerge bool) (patched []byte, err error) {
	if useMerge {
		var mergeBytes []byte
		mergeBytes, err = json.Marshal(mergeRaw)
		if err != nil {
			err = fmt.Errorf("marshaling merge patch: %w", err)
			return patched, err
		}
		patched, err = jsonpatch.MergePatch(dashboard, mergeBytes)
		if err != nil {
			err = fmt.Errorf("applying merge patch: %w", err)
			return patched, err
		}
		return patched, err
	}

	var patchesBytes []byte
	patchesBytes, err = json.Marshal(patchesRaw)
	if err != nil {
		err = fmt.Errorf("marshaling patch list: %w", err)
		return patched, err
	}

	var patch jsonpatch.Patch
	patch, err = jsonpatch.DecodePatch(patchesBytes)
	if err != nil {
		err = fmt.Errorf("decoding JSON patch: %w", err)
		return patched, err
	}

	patched, err = patch.Apply(dashboard)
	if err != nil {
		err = fmt.Errorf("applying JSON patch: %w", err)
		return patched, err
	}
	return patched, err
}

// executeGrafanaCreateFolder creates a Grafana folder (directory).
func (s *Server) executeGrafanaCreateFolder(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	title, _ := args["title"].(string)
	if title == "" {
		err = errors.New("title parameter is required")
		return result, err
	}

	uid, _ := args["uid"].(string)
	parentUID, _ := args["parentUid"].(string)

	intention, _ := args["message"].(string)
	auditUser := s.auditUserFromContext(ctx)
	versionNote := composeVersionNote(auditUser, intention, "created via mcp")

	var folder Folder
	folder, err = s.grafanaClient.CreateFolder(ctx, CreateFolderRequest{
		Title:     title,
		UID:       uid,
		ParentUID: parentUID,
	})
	if err != nil {
		return result, err
	}

	// Grafana folders carry no version history; slog is the audit surface.
	s.logger.InfoContext(ctx, "grafana write",
		slog.String("tool", toolGrafanaCreateFolder),
		slog.String("uid", folder.UID),
		slog.String("title", folder.Title),
		slog.String("parent_uid", folder.ParentUID),
		slog.String("audit_user", auditUser),
		slog.String("audit_source_ip", auditSourceFromContext(ctx)),
		slog.String("message", versionNote),
	)

	result = fmt.Sprintf("Successfully created folder '%s' with UID: %s", folder.Title, folder.UID)
	if folder.ParentUID != "" {
		result = fmt.Sprintf("%s (nested under parent UID: %s)", result, folder.ParentUID)
	}
	return result, err
}

// executeGrafanaDeleteDashboard deletes a dashboard.
func (s *Server) executeGrafanaDeleteDashboard(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.grafanaClient == nil {
		err = errors.New("grafana access not configured (GRAFANA_URL or GRAFANA_API_KEY not set)")
		return result, err
	}

	uid, _ := args["uid"].(string)
	if uid == "" {
		err = errors.New("uid parameter is required")
		return result, err
	}

	intention, _ := args["message"].(string)
	auditUser := s.auditUserFromContext(ctx)
	versionNote := composeVersionNote(auditUser, intention, "deleted via mcp")

	err = s.grafanaClient.DeleteDashboard(ctx, uid)
	if err != nil {
		return result, err
	}

	// Grafana's DELETE leaves no version history, so slog is the only audit
	// surface for deletes. The bot ships stdout to Loki.
	s.logger.InfoContext(ctx, "grafana write",
		slog.String("tool", toolGrafanaDeleteDashboard),
		slog.String("uid", uid),
		slog.String("audit_user", auditUser),
		slog.String("audit_source_ip", auditSourceFromContext(ctx)),
		slog.String("message", versionNote),
	)

	result = fmt.Sprintf("Successfully deleted dashboard with UID: %s", uid)
	return result, err
}
