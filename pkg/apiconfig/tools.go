package apiconfig

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"unicode"
)

// defaultAPIConfigDir is where operator-supplied third-party API tool configs
// live when API_CONFIG_DIR is unset. Matches the documented default (README)
// and the sibling INVESTIGATION_DIR convention; the container image overrides
// it to an absolute path via the environment, same as INVESTIGATION_DIR.
const defaultAPIConfigDir = "./apis"

// LoadRegistryFromEnv builds the third-party API tool registry from the
// directory named by API_CONFIG_DIR (default ./apis). The
// feature is opt-in: a missing directory or a config whose auth env var is
// unset degrades cleanly to an empty registry (no tools), never an error, so
// an unconfigured deployment simply advertises and dispatches no API tools.
//
// It is called independently by each front-end that needs the registry (the
// MCP tool server and the Slack agent's ToolConfig), mirroring how those
// surfaces already re-derive tool availability from the environment. Building
// twice is cheap and side-effect-free — NewAPIClient opens no connections.
func LoadRegistryFromEnv(logger *slog.Logger) (registry *APIToolRegistry) {
	dir := os.Getenv("API_CONFIG_DIR")
	if dir == "" {
		dir = defaultAPIConfigDir
	}

	configs, err := LoadConfigs(dir, logger)
	if err != nil {
		logger.Warn("failed to load third-party API tool configs; API tools unavailable",
			slog.String("dir", dir),
			slog.String("error", err.Error()))
	}

	allowedMethods := parseAllowedMethods(os.Getenv("API_ALLOWED_METHODS"))
	registry = NewAPIToolRegistry(configs, allowedMethods, logger)
	return registry
}

// parseAllowedMethods parses API_ALLOWED_METHODS into the set of HTTP methods
// the generic API client may use. GET is always included — the allowlist only
// adds write verbs (POST/PUT/PATCH/DELETE) on top of reads, so a fat-fingered
// value can never disable reads. Accepts commas AND whitespace/newlines as
// separators, so the value can be a readable YAML block scalar (one method per
// line), consistent with every other list-valued env var.
func parseAllowedMethods(raw string) (allowed map[string]bool) {
	allowed = map[string]bool{http.MethodGet: true}

	for _, method := range strings.FieldsFunc(raw, isMethodSeparator) {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method != "" {
			allowed[method] = true
		}
	}

	return allowed
}

// isMethodSeparator reports whether r is a boundary between API_ALLOWED_METHODS
// entries: a literal comma or any Unicode whitespace. Extracted to a top-level
// function so the namedreturns linter doesn't flag the predicate's bool return
// (per the project's nested-closure guidance).
func isMethodSeparator(r rune) (isSep bool) {
	isSep = r == ',' || unicode.IsSpace(r)
	return isSep
}

// MCPTool matches the MCP tool definition structure from pkg/mcp/types.go.
// Duplicated here to avoid circular imports.
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// APIToolRegistry holds loaded API configs and their clients.
type APIToolRegistry struct {
	configs        []*APIConfig
	clients        map[string]*APIClient
	allowedMethods map[string]bool
	logger         *slog.Logger
}

// NewAPIToolRegistry creates a registry from loaded configs. allowedMethods is
// the set of HTTP methods this deployment permits (see parseAllowedMethods); a
// nil or empty set means GET-only (read-only), the safe default. Endpoints whose
// method is not permitted are withheld from the toolset — and logged here, once,
// so an operator who wrote `method: POST` can see why the tool did not appear.
func NewAPIToolRegistry(configs []*APIConfig, allowedMethods map[string]bool, logger *slog.Logger) (registry *APIToolRegistry) {
	clients := make(map[string]*APIClient, len(configs))

	for _, config := range configs {
		clients[config.Name] = NewAPIClient(config, logger)
	}

	registry = &APIToolRegistry{
		configs:        configs,
		clients:        clients,
		allowedMethods: allowedMethods,
		logger:         logger,
	}

	for _, config := range configs {
		for _, endpoint := range config.Endpoints {
			if !registry.methodAllowed(endpoint.Method) {
				logger.Warn("API tool withheld: HTTP method not permitted (set API_ALLOWED_METHODS to enable)",
					slog.String("tool", config.Name+"_"+endpoint.Name),
					slog.String("method", strings.ToUpper(endpoint.Method)))
			}
		}
	}

	return registry
}

// methodAllowed reports whether an endpoint's HTTP method is permitted by this
// deployment's allowlist. GET is always allowed; every other verb must be in
// allowedMethods.
func (r *APIToolRegistry) methodAllowed(method string) (ok bool) {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" || m == http.MethodGet {
		ok = true
		return ok
	}

	ok = r.allowedMethods[m]
	return ok
}

// endpointFor resolves the config and endpoint backing a "<api>_<endpoint>" tool
// name.
func (r *APIToolRegistry) endpointFor(toolName string) (config *APIConfig, endpoint *Endpoint, found bool) {
	for _, cfg := range r.configs {
		prefix := cfg.Name + "_"
		if !strings.HasPrefix(toolName, prefix) {
			continue
		}

		name := strings.TrimPrefix(toolName, prefix)
		for i := range cfg.Endpoints {
			if cfg.Endpoints[i].Name == name {
				config = cfg
				endpoint = &cfg.Endpoints[i]
				found = true
				return config, endpoint, found
			}
		}
	}

	return config, endpoint, found
}

// IsWriteTool reports whether the named API tool mutates state — any endpoint
// whose method is not GET. The MCP server ORs this into its READ_ONLY gate, so a
// read-only deployment drops API write tools exactly as it drops Grafana writes.
func (r *APIToolRegistry) IsWriteTool(toolName string) (isWrite bool) {
	_, endpoint, found := r.endpointFor(toolName)
	if !found {
		return isWrite
	}

	method := strings.ToUpper(strings.TrimSpace(endpoint.Method))
	isWrite = method != "" && method != http.MethodGet
	return isWrite
}

// GetToolDefinitions returns MCP tool definitions for all loaded API endpoints.
func (r *APIToolRegistry) GetToolDefinitions() (tools []MCPTool) {
	for _, config := range r.configs {
		for _, endpoint := range config.Endpoints {
			if !r.methodAllowed(endpoint.Method) {
				continue
			}
			tool := buildToolDefinition(config, endpoint)
			tools = append(tools, tool)
		}
	}

	return tools
}

// DispatchToolCall routes a tool call to the correct API client and endpoint.
func (r *APIToolRegistry) DispatchToolCall(ctx context.Context, toolName string, args map[string]interface{}) (result string, handled bool, err error) {
	config, endpoint, found := r.endpointFor(toolName)
	if !found {
		return result, handled, err
	}

	// Defense in depth: reject a call whose method the deployment does not
	// permit, even though such tools are already withheld from the catalog.
	if !r.methodAllowed(endpoint.Method) {
		handled = true
		err = fmt.Errorf("tool %q is disabled: HTTP method %s is not permitted (set API_ALLOWED_METHODS)", toolName, strings.ToUpper(endpoint.Method))
		return result, handled, err
	}

	handled = true
	result, err = r.clients[config.Name].Execute(ctx, endpoint.Name, args)
	return result, handled, err
}

// HasTools returns true if any API tools are registered.
func (r *APIToolRegistry) HasTools() (has bool) {
	has = len(r.configs) > 0
	return has
}

// WriteToolUsage writes available API tool descriptions for the Claude prompt.
// permits gates each tool by name — a tool is described only if permits(name) —
// so the prose lists exactly what the caller can dispatch, matching the
// authz-filtered catalog the model is given. An API whose every endpoint is
// denied prints no header at all.
func (r *APIToolRegistry) WriteToolUsage(builder *strings.Builder, permits func(name string) bool) {
	for _, config := range r.configs {
		var lines strings.Builder

		for _, endpoint := range config.Endpoints {
			if !r.methodAllowed(endpoint.Method) {
				continue
			}

			toolName := config.Name + "_" + endpoint.Name
			if !permits(toolName) {
				continue
			}

			desc := endpoint.Description
			if desc == "" {
				desc = endpoint.Name
			}

			fmt.Fprintf(&lines, "- `%s`: %s\n", toolName, desc)
		}

		if lines.Len() == 0 {
			continue
		}

		fmt.Fprintf(builder, "**%s API:**\n", config.Name)
		builder.WriteString(lines.String())
		builder.WriteString("\n")
	}
}

func buildToolDefinition(config *APIConfig, endpoint Endpoint) (tool MCPTool) {
	toolName := config.Name + "_" + endpoint.Name

	description := endpoint.Description
	if description == "" {
		description = fmt.Sprintf("%s: %s %s", endpoint.Name, endpoint.Method, endpoint.Path)
	}

	properties := make(map[string]interface{})
	var required []string

	for _, param := range endpoint.Params {
		prop := map[string]interface{}{
			"type":        mapParamType(param.Type),
			"description": param.Description,
		}
		properties[param.Name] = prop

		if param.Required {
			required = append(required, param.Name)
		}
	}

	// Add common optional params
	properties["pretty"] = map[string]interface{}{
		"type":        "boolean",
		"description": "Pretty-print JSON response (default: false, saves tokens)",
	}

	schema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	tool = MCPTool{
		Name:        toolName,
		Description: description,
		InputSchema: schema,
	}

	return tool
}

func mapParamType(configType string) (jsonType string) {
	switch configType {
	case "integer":
		jsonType = "integer"
	case "boolean":
		jsonType = "boolean"
	case "number":
		jsonType = "number"
	default:
		jsonType = "string"
	}

	return jsonType
}
