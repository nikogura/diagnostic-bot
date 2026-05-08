package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SDKServer wraps the existing Server with the official MCP SDK.
// It provides both stdio and Streamable HTTP transports.
type SDKServer struct {
	mcpServer *sdkmcp.Server
	legacy    *Server
	logger    *slog.Logger
}

// NewSDKServer creates a new MCP SDK-based server from an existing Server.
// It registers all tools from the legacy server with the SDK's tool system.
func NewSDKServer(legacy *Server) (result *SDKServer) {
	mcpServer := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "nikogura.com/diagnostic-bot",
		Version: "0.2.0",
	}, nil)

	result = &SDKServer{
		mcpServer: mcpServer,
		legacy:    legacy,
		logger:    legacy.logger,
	}

	result.registerTools()

	return result
}

// RunStdio starts the server using stdio transport.
func (s *SDKServer) RunStdio(ctx context.Context) (err error) {
	s.logger.InfoContext(ctx, "MCP server started", slog.String("transport", "stdio"))

	err = s.mcpServer.Run(ctx, &sdkmcp.StdioTransport{})

	return err
}

// StreamableHTTPHandler returns an http.Handler for the Streamable HTTP transport.
func (s *SDKServer) StreamableHTTPHandler() (handler http.Handler) {
	handler = sdkmcp.NewStreamableHTTPHandler(s.getServer, nil)

	return handler
}

// SSEHandler returns an http.Handler for the legacy SSE transport.
func (s *SDKServer) SSEHandler() (handler http.Handler) {
	handler = sdkmcp.NewSSEHandler(s.getServer, nil)

	return handler
}

// getServer returns the underlying SDK server for HTTP handler callbacks.
func (s *SDKServer) getServer(_ *http.Request) (server *sdkmcp.Server) {
	server = s.mcpServer
	return server
}

// registerTool registers a single legacy tool with the SDK server.
// It wraps the existing execute* handler to match the SDK's ToolHandler signature.
func (s *SDKServer) registerTool(name, description string, schema map[string]interface{}, handler func(context.Context, map[string]interface{}) (string, error)) {
	tool := &sdkmcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: schema,
	}

	s.mcpServer.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (result *sdkmcp.CallToolResult, err error) {
		// Unmarshal raw arguments to the map format legacy handlers expect
		args := make(map[string]interface{})

		err = json.Unmarshal(req.Params.Arguments, &args)
		if err != nil {
			return result, err
		}

		var text string
		text, err = handler(ctx, args)
		if err != nil {
			return result, err
		}

		result = &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: text},
			},
		}

		return result, err
	})
}

// registerTools registers all available tools with the SDK server.
// Tools are conditionally registered based on which backends are configured.
func (s *SDKServer) registerTools() {
	s.registerLokiTools()
	s.registerUtilityTools()
	s.registerGitHubTools()
	s.registerECRTools()
	s.registerDatabaseTools()
	s.registerGrafanaTools()
	s.registerCloudWatchTools()
	s.registerPrometheusTools()
	s.registerGraphQLTools()
	s.registerGitLabTools()
	s.registerTempoTools()
	s.registerAPITools()

	s.logger.Info("SDK server tools registered")
}

func (s *SDKServer) registerLokiTools() {
	if s.legacy.lokiClient == nil {
		return
	}

	for _, t := range getLokiTools() {
		s.registerTool(t.Name, t.Description, t.InputSchema, s.legacy.executeQueryLoki)
	}
}

func (s *SDKServer) registerUtilityTools() {
	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolWhoisLookup: s.legacy.executeWhoisLookup,
		toolGeneratePDF: s.legacy.executeGeneratePDF,
	}

	for _, t := range getUtilityTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerGitHubTools() {
	if s.legacy.githubClient == nil {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolGitHubGetFile:       s.legacy.executeGitHubGetFile,
		toolGitHubListDirectory: s.legacy.executeGitHubListDirectory,
		toolGitHubSearchCode:    s.legacy.executeGitHubSearchCode,
	}

	for _, t := range getGitHubTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerECRTools() {
	if s.legacy.cloudWatchClientFactory == nil {
		return
	}

	for _, t := range getECRTools() {
		s.registerTool(t.Name, t.Description, t.InputSchema, s.legacy.executeECRScanResults)
	}
}

func (s *SDKServer) registerDatabaseTools() {
	if len(s.legacy.dbClients) == 0 {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolDatabaseQuery: s.legacy.executeDatabaseQuery,
		toolDatabaseList:  s.legacy.executeDatabaseList,
	}

	for _, t := range getDatabaseTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerGrafanaTools() {
	if s.legacy.grafanaClient == nil {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolGrafanaListDashboards:  s.legacy.executeGrafanaListDashboards,
		toolGrafanaGetDashboard:    s.legacy.executeGrafanaGetDashboard,
		toolGrafanaCreateDashboard: s.legacy.executeGrafanaCreateDashboard,
		toolGrafanaUpdateDashboard: s.legacy.executeGrafanaUpdateDashboard,
		toolGrafanaDeleteDashboard: s.legacy.executeGrafanaDeleteDashboard,
	}

	for _, t := range getGrafanaTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerCloudWatchTools() {
	if s.legacy.cloudWatchClientFactory == nil {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolCloudWatchLogsQuery:      s.legacy.executeCloudWatchLogsQuery,
		toolCloudWatchLogsListGroups: s.legacy.executeCloudWatchLogsListGroups,
		toolCloudWatchLogsGetEvents:  s.legacy.executeCloudWatchLogsGetEvents,
	}

	for _, t := range getCloudWatchTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerPrometheusTools() {
	if len(s.legacy.prometheusClients) == 0 {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolPrometheusQuery:         s.legacy.executePrometheusQuery,
		toolPrometheusQueryRange:    s.legacy.executePrometheusQueryRange,
		toolPrometheusSeries:        s.legacy.executePrometheusSeries,
		toolPrometheusLabelValues:   s.legacy.executePrometheusLabelValues,
		toolPrometheusListEndpoints: s.legacy.executePrometheusListEndpoints,
	}

	for _, t := range getPrometheusTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerGraphQLTools() {
	if len(s.legacy.graphqlClients) == 0 {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolGraphQLQuery:         s.legacy.executeGraphQLQuery,
		toolGraphQLListEndpoints: s.legacy.executeGraphQLListEndpoints,
	}

	for _, t := range getGraphQLTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerGitLabTools() {
	if s.legacy.gitlabClient == nil {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolGitLabGetFile:       s.legacy.executeGitLabGetFile,
		toolGitLabListDirectory: s.legacy.executeGitLabListDirectory,
		toolGitLabSearchCode:    s.legacy.executeGitLabSearchCode,
	}

	for _, t := range getGitLabTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerTempoTools() {
	if len(s.legacy.tempoClients) == 0 {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolTempoGetTrace:      s.legacy.executeTempoGetTrace,
		toolTempoSearchTraces:  s.legacy.executeTempoSearchTraces,
		toolTempoListEndpoints: s.legacy.executeTempoListEndpoints,
	}

	for _, t := range getTempoTools() {
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}

func (s *SDKServer) registerAPITools() {
	if s.legacy.apiToolRegistry == nil || !s.legacy.apiToolRegistry.HasTools() {
		return
	}

	for _, t := range s.legacy.apiToolRegistry.GetToolDefinitions() {
		toolName := t.Name
		s.registerTool(t.Name, t.Description, t.InputSchema, func(ctx context.Context, args map[string]interface{}) (result string, err error) {
			var handled bool
			result, handled, err = s.legacy.apiToolRegistry.DispatchToolCall(ctx, toolName, args)
			if !handled {
				err = fmt.Errorf("unhandled API tool: %s", toolName)
			}
			return result, err
		})
	}
}
