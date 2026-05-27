package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// GraphQL tool name constants.
const (
	toolGraphQLQuery         = "graphql_query"
	toolGraphQLListEndpoints = "graphql_list_endpoints"
)

// GraphQL configuration constants.
const (
	graphqlEnvPrefix       = "GRAPHQL_"
	graphqlURLSuffix       = "_URL"
	graphqlAuthURLSuffix   = "_AUTH_URL"
	graphqlTokenSuffix     = "_TOKEN"
	graphqlHeaderPrefix    = "_HEADER_"
	graphqlTimeout         = 30 * time.Second
	graphqlEndpointDefault = "default"

	// graphqlMaxResponseBytes is the maximum response size before truncation (50KB).
	graphqlMaxResponseBytes = 50 * 1024
)

// GraphQLClient handles interactions with a GraphQL endpoint.
type GraphQLClient struct {
	name         string
	url          string
	httpClient   *http.Client
	headers      map[string]string
	logger       *slog.Logger
	oauth2Config *GraphQLOAuth2Config
	tokenMu      sync.Mutex
	tokenCache   *graphqlTokenCache
}

// GraphQLClientConfig holds intermediate configuration for a GraphQL endpoint.
type GraphQLClientConfig struct {
	Name    string
	URL     string
	Headers map[string]string
}

// GraphQLEndpointInfo contains metadata about a configured GraphQL endpoint.
type GraphQLEndpointInfo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// GraphQLRequestBody represents the JSON body sent to a GraphQL endpoint.
type GraphQLRequestBody struct {
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
	OperationName string                 `json:"operationName,omitempty"`
}

// GraphQLResponse represents the top-level GraphQL response envelope.
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data,omitempty"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError represents a single error in a GraphQL response.
type GraphQLError struct {
	Message string `json:"message"`
}

// NewGraphQLClient creates a new GraphQL API client.
func NewGraphQLClient(name, url string, headers map[string]string, oauth2Config *GraphQLOAuth2Config, logger *slog.Logger) (client *GraphQLClient, err error) {
	if url == "" {
		err = fmt.Errorf("graphql URL is required for endpoint %q", name)
		return client, err
	}

	// Trim trailing slash for consistency.
	url = strings.TrimRight(url, "/")

	if headers == nil {
		headers = make(map[string]string)
	}

	client = &GraphQLClient{
		name: name,
		url:  url,
		httpClient: &http.Client{
			Timeout: graphqlTimeout,
			// Per-client Transport isolates the connection pool from
			// http.DefaultTransport (see pkg/mcp/tempo.go for context).
			Transport: &http.Transport{},
		},
		headers:      headers,
		logger:       logger,
		oauth2Config: oauth2Config,
	}

	return client, err
}

// LoadGraphQLClients scans environment variables and creates GraphQL clients.
// It looks for GRAPHQL_URL (default) and GRAPHQL_<NAME>_URL patterns.
func LoadGraphQLClients(logger *slog.Logger) (clients map[string]*GraphQLClient) {
	clients = make(map[string]*GraphQLClient)

	// Try to load the default GRAPHQL_URL.
	defaultURL := os.Getenv("GRAPHQL_URL")
	if defaultURL != "" {
		headers := collectGraphQLHeaders("", logger)
		oauth2Config := collectGraphQLOAuth2Config("", logger)
		client, clientErr := NewGraphQLClient(graphqlEndpointDefault, defaultURL, headers, oauth2Config, logger)
		if clientErr != nil {
			logger.Warn("Failed to initialize default GraphQL client",
				slog.String("error", clientErr.Error()))
		} else {
			clients[graphqlEndpointDefault] = client
		}
	}

	// Scan for GRAPHQL_<NAME>_URL patterns.
	namedClients := scanGraphQLEnvVars(logger)
	for name, client := range namedClients {
		if name == graphqlEndpointDefault {
			continue
		}
		clients[name] = client
	}

	if len(clients) == 0 {
		logger.Info("No GraphQL endpoints configured")
	} else {
		names := make([]string, 0, len(clients))
		for name := range clients {
			names = append(names, name)
		}
		logger.Info("GraphQL clients loaded",
			slog.Int("count", len(clients)),
			slog.Any("endpoints", names))
	}

	return clients
}

// scanGraphQLEnvVars scans environment variables for GRAPHQL_<NAME>_URL patterns.
func scanGraphQLEnvVars(logger *slog.Logger) (clients map[string]*GraphQLClient) {
	clients = make(map[string]*GraphQLClient)

	urls := collectGraphQLURLs(logger)

	for name, envURL := range urls {
		headers := collectGraphQLHeaders(name, logger)
		oauth2Config := collectGraphQLOAuth2Config(name, logger)

		client, clientErr := NewGraphQLClient(name, envURL, headers, oauth2Config, logger)
		if clientErr != nil {
			logger.Warn("Failed to initialize GraphQL client",
				slog.String("name", name),
				slog.String("error", clientErr.Error()))
			continue
		}

		clients[name] = client
	}

	return clients
}

// collectGraphQLURLs scans environment variables for GRAPHQL_<NAME>_URL patterns.
// It returns a map of lowercase endpoint names to URLs, excluding the default GRAPHQL_URL.
func collectGraphQLURLs(logger *slog.Logger) (urls map[string]string) {
	urls = make(map[string]string)

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]

		if !strings.HasPrefix(key, graphqlEnvPrefix) || !strings.HasSuffix(key, graphqlURLSuffix) {
			continue
		}

		// Skip the default GRAPHQL_URL (handled separately).
		if key == "GRAPHQL_URL" {
			continue
		}

		// Exclude keys that contain _HEADER_ (those are header env vars).
		if strings.Contains(key, graphqlHeaderPrefix) {
			continue
		}

		// Exclude OAuth2 auth URL keys (GRAPHQL_<NAME>_AUTH_URL).
		if strings.HasSuffix(key, graphqlAuthURLSuffix) {
			continue
		}

		// Extract the name: GRAPHQL_<NAME>_URL -> <NAME>.
		name := key[len(graphqlEnvPrefix) : len(key)-len(graphqlURLSuffix)]
		if name == "" {
			continue
		}

		// Skip if this looks like a token env var (GRAPHQL_TOKEN -> name would be "TOKEN").
		if name == "TOKEN" {
			continue
		}

		name = strings.ToLower(name)

		envURL := os.Getenv(key)
		if envURL == "" {
			continue
		}

		logger.Debug("Found GraphQL endpoint configuration",
			slog.String("name", name),
			slog.String("url_key", key))

		urls[name] = envURL
	}

	return urls
}

// collectGraphQLHeaders builds the headers map for a named GraphQL endpoint.
// For the default endpoint (name=""), it looks for GRAPHQL_TOKEN and GRAPHQL_HEADER_* vars.
// For named endpoints, it looks for GRAPHQL_<NAME>_TOKEN and GRAPHQL_<NAME>_HEADER_* vars.
func collectGraphQLHeaders(name string, logger *slog.Logger) (headers map[string]string) {
	headers = make(map[string]string)

	var tokenKey string
	var headerPrefix string

	if name == "" {
		tokenKey = "GRAPHQL_TOKEN"
		headerPrefix = "GRAPHQL_HEADER_"
	} else {
		upperName := strings.ToUpper(name)
		tokenKey = fmt.Sprintf("GRAPHQL_%s_TOKEN", upperName)
		headerPrefix = fmt.Sprintf("GRAPHQL_%s_HEADER_", upperName)
	}

	// Check for bearer token.
	token := os.Getenv(tokenKey)
	if token != "" {
		headers["Authorization"] = "Bearer " + token
		logger.Debug("Found GraphQL bearer token",
			slog.String("token_key", tokenKey))
	}

	// Scan for custom headers: GRAPHQL_<NAME>_HEADER_<KEY>.
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		if !strings.HasPrefix(key, headerPrefix) {
			continue
		}

		// Extract the header key name and convert underscores to hyphens.
		headerKey := key[len(headerPrefix):]
		if headerKey == "" {
			continue
		}

		headerKey = strings.ReplaceAll(headerKey, "_", "-")
		headerKey = strings.ToLower(headerKey)

		headerValue := os.Getenv(key)
		if headerValue == "" {
			continue
		}

		headers[headerKey] = headerValue
		logger.Debug("Found GraphQL custom header",
			slog.String("header", headerKey))
	}

	return headers
}

// Query executes a GraphQL query against the endpoint.
func (c *GraphQLClient) Query(ctx context.Context, query string, variables map[string]interface{}, operationName string) (result string, err error) {
	err = c.ensureAuth(ctx)
	if err != nil {
		return result, err
	}

	reqBody := GraphQLRequestBody{
		Query:         query,
		Variables:     variables,
		OperationName: operationName,
	}

	var bodyBytes []byte
	bodyBytes, err = json.Marshal(reqBody)
	if err != nil {
		err = fmt.Errorf("marshaling graphql request: %w", err)
		return result, err
	}

	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(bodyBytes))
	if err != nil {
		err = fmt.Errorf("creating graphql request: %w", err)
		return result, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Apply configured headers.
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}

	c.logger.DebugContext(ctx, "making GraphQL request",
		"endpoint", c.name,
		"url", c.url)

	var resp *http.Response
	resp, err = c.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("executing graphql request: %w", err)
		return result, err
	}
	defer resp.Body.Close()

	var respBody []byte
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("reading graphql response: %w", err)
		return result, err
	}

	if resp.StatusCode >= 400 {
		err = fmt.Errorf("graphql HTTP error (status %d): %s", resp.StatusCode, truncateResponse(respBody))
		return result, err
	}

	result = formatGraphQLResponse(respBody)
	return result, err
}

// formatGraphQLResponse checks for GraphQL errors in the response and formats the output.
func formatGraphQLResponse(body []byte) (result string) {
	var gqlResp GraphQLResponse

	err := json.Unmarshal(body, &gqlResp)
	if err != nil {
		// Not valid JSON; return truncated raw body.
		result = truncateResponse(body)
		return result
	}

	// If there are GraphQL errors, include them in the output.
	if len(gqlResp.Errors) > 0 {
		errorMsgs := make([]string, 0, len(gqlResp.Errors))
		for _, gqlErr := range gqlResp.Errors {
			errorMsgs = append(errorMsgs, gqlErr.Message)
		}
		errStr := "GraphQL errors: " + strings.Join(errorMsgs, "; ")

		if gqlResp.Data != nil {
			result = errStr + "\n\nData:\n" + truncateResponse(body)
		} else {
			result = errStr
		}

		return result
	}

	result = truncateResponse(body)
	return result
}

// truncateResponse truncates a response body if it exceeds the maximum size.
func truncateResponse(body []byte) (result string) {
	if len(body) <= graphqlMaxResponseBytes {
		result = string(body)
		return result
	}

	result = string(body[:graphqlMaxResponseBytes]) + "\n\n[Response truncated - exceeded 50KB limit]"
	return result
}

// getGraphQLTools returns GraphQL-related tool definitions.
func getGraphQLTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolGraphQLQuery,
			Description: "Execute a GraphQL query against a configured endpoint. Supports any GraphQL API (Wiz, Hasura, GitHub, etc.). Use graphql_list_endpoints to see available endpoints.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "GraphQL query string",
					},
					"variables": map[string]interface{}{
						"type":        "object",
						"description": "GraphQL variables as key-value pairs (optional)",
					},
					"endpoint": map[string]interface{}{
						"type":        "string",
						"description": "Named GraphQL endpoint to query (e.g., 'wiz', 'hasura'). Use graphql_list_endpoints to see available endpoints. Defaults to 'default'.",
					},
					"operation_name": map[string]interface{}{
						"type":        "string",
						"description": "GraphQL operation name (optional, for multi-operation documents)",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        toolGraphQLListEndpoints,
			Description: "List all configured GraphQL endpoints. Shows named endpoints available for querying.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}

	return result
}

// resolveGraphQLClient resolves the GraphQL client for the given endpoint name.
func (s *Server) resolveGraphQLClient(args map[string]interface{}) (client *GraphQLClient, err error) {
	if len(s.graphqlClients) == 0 {
		err = errors.New("no GraphQL endpoints configured. Set GRAPHQL_URL or GRAPHQL_<NAME>_URL environment variables")
		return client, err
	}

	endpointName := graphqlEndpointDefault
	if name, ok := args["endpoint"].(string); ok && name != "" {
		endpointName = strings.ToLower(name)
	}

	var exists bool
	client, exists = s.graphqlClients[endpointName]
	if !exists {
		available := make([]string, 0, len(s.graphqlClients))
		for name := range s.graphqlClients {
			available = append(available, name)
		}
		err = fmt.Errorf("graphql endpoint %q not configured. Available endpoints: %v", endpointName, available)
		return client, err
	}

	return client, err
}

// executeGraphQLQuery executes a GraphQL query via the MCP tool interface.
func (s *Server) executeGraphQLQuery(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var client *GraphQLClient
	client, err = s.resolveGraphQLClient(args)
	if err != nil {
		return result, err
	}

	query, _ := args["query"].(string)
	if query == "" {
		err = errors.New("query parameter is required")
		return result, err
	}

	// Parse optional variables.
	var variables map[string]interface{}
	if varsRaw, ok := args["variables"].(map[string]interface{}); ok {
		variables = varsRaw
	}

	operationName, _ := args["operation_name"].(string)

	s.logger.InfoContext(ctx, "executing GraphQL query",
		"endpoint", client.name,
		"url", client.url,
		"has_variables", variables != nil,
		"operation_name", operationName)

	result, err = client.Query(ctx, query, variables, operationName)
	return result, err
}

// executeGraphQLListEndpoints returns the list of configured GraphQL endpoints.
func (s *Server) executeGraphQLListEndpoints(_ context.Context, _ map[string]interface{}) (result string, err error) {
	if len(s.graphqlClients) == 0 {
		err = errors.New("no GraphQL endpoints configured. Set GRAPHQL_URL or GRAPHQL_<NAME>_URL environment variables")
		return result, err
	}

	endpoints := make([]GraphQLEndpointInfo, 0, len(s.graphqlClients))
	for name, client := range s.graphqlClients {
		endpoints = append(endpoints, GraphQLEndpointInfo{
			Name: name,
			URL:  client.url,
		})
	}

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(endpoints, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting endpoints list: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}
