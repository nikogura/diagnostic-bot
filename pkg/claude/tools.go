package claude

import (
	anthropic "github.com/liushuangls/go-anthropic/v2"
)

// ToolName constants for Claude tool use.
const (
	ToolLokiQuery      = "loki_query"
	ToolGetK8sPodLogs  = "get_k8s_pod_logs"
	ToolGetK8sResource = "get_k8s_resource"
	ToolListK8sPods    = "list_k8s_pods"
	ToolGetK8sEvents   = "get_k8s_events"
	ToolWhoisLookup    = "whois_lookup"
)

// GetInvestigationTools returns the tool definitions for Claude to use during investigations.
//
//nolint:funlen // Tool definitions are verbose but must be kept together
func GetInvestigationTools() (result []anthropic.ToolDefinition) {
	result = []anthropic.ToolDefinition{
		{
			Name:        ToolLokiQuery,
			Description: "Query Loki log aggregation system for ModSecurity WAF logs and other application logs. Use LogQL query syntax. Returns JSON log entries.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "LogQL query string (e.g., '{realm=\"prod\", namespace=\"ingress-nginx\"} |~ \"ModSecurity\" | json | transaction_response_http_code=\"403\"')",
					},
					"start": map[string]interface{}{
						"type":        "string",
						"description": "Start time in RFC3339 format or relative duration (e.g., '1h', '24h', '2025-10-30T10:00:00Z')",
					},
					"end": map[string]interface{}{
						"type":        "string",
						"description": "End time in RFC3339 format or 'now' (optional, defaults to current time)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of log entries to return (default: 100, max: 1000)",
					},
				},
				"required": []string{"query", "start"},
			},
		},
		{
			Name:        ToolGetK8sPodLogs,
			Description: "Fetch logs from Kubernetes pods by namespace and label selector. Returns pod logs with optional grep filtering.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": map[string]interface{}{
						"type":        "string",
						"description": "Kubernetes namespace (e.g., 'ingress-nginx', 'flux-system')",
					},
					"label_selector": map[string]interface{}{
						"type":        "string",
						"description": "Label selector to filter pods (e.g., 'app.kubernetes.io/name=ingress-nginx')",
					},
					"container": map[string]interface{}{
						"type":        "string",
						"description": "Container name (optional, uses first container if not specified)",
					},
					"since": map[string]interface{}{
						"type":        "string",
						"description": "Fetch logs since duration (e.g., '1h', '30m', default: '1h')",
					},
					"tail_lines": map[string]interface{}{
						"type":        "integer",
						"description": "Number of lines to tail (default: 100, max: 1000)",
					},
					"grep": map[string]interface{}{
						"type":        "string",
						"description": "Optional grep pattern to filter logs (case-insensitive)",
					},
				},
				"required": []string{"namespace"},
			},
		},
		{
			Name:        ToolGetK8sResource,
			Description: "Get Kubernetes resource configuration (ConfigMap, Deployment, Service, GitRepository, Kustomization, AtlasMigration). Returns YAML or JSON representation.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"resource_type": map[string]interface{}{
						"type":        "string",
						"description": "Resource type (configmap, deployment, service, gitrepository, kustomization, atlasmigration)",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Resource name",
					},
					"namespace": map[string]interface{}{
						"type":        "string",
						"description": "Kubernetes namespace",
					},
					"output_format": map[string]interface{}{
						"type":        "string",
						"description": "Output format: 'yaml' or 'json' (default: 'yaml')",
					},
				},
				"required": []string{"resource_type", "name", "namespace"},
			},
		},
		{
			Name:        ToolListK8sPods,
			Description: "List Kubernetes pods in a namespace with optional label selector. Returns pod names, status, and basic info.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": map[string]interface{}{
						"type":        "string",
						"description": "Kubernetes namespace",
					},
					"label_selector": map[string]interface{}{
						"type":        "string",
						"description": "Label selector to filter pods (optional)",
					},
				},
				"required": []string{"namespace"},
			},
		},
		{
			Name:        ToolGetK8sEvents,
			Description: "Get Kubernetes events for troubleshooting. Can filter by namespace, field selector, or specific resource.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"namespace": map[string]interface{}{
						"type":        "string",
						"description": "Kubernetes namespace (optional, gets cluster-wide events if not specified)",
					},
					"field_selector": map[string]interface{}{
						"type":        "string",
						"description": "Field selector to filter events (e.g., 'involvedObject.name=my-pod')",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of events to return (default: 50)",
					},
				},
				"required": []string{},
			},
		},
		{
			Name:        ToolWhoisLookup,
			Description: "Perform whois lookup on an IP address to determine geolocation, ISP, and organization. Useful for analyzing blocked IPs in WAF investigations.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ip_address": map[string]interface{}{
						"type":        "string",
						"description": "IP address to look up (e.g., '192.168.1.1')",
					},
				},
				"required": []string{"ip_address"},
			},
		},
	}

	return result
}
