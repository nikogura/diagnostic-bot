package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Tool name constants for Tempo tools.
const (
	toolTempoGetTrace      = "tempo_get_trace"
	toolTempoSearchTraces  = "tempo_search_traces"
	toolTempoListEndpoints = "tempo_list_endpoints"
)

const tempoTimeout = 30 * time.Second

// TempoClient is an HTTP client for querying Grafana Tempo.
type TempoClient struct {
	name       string
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

const tempoEndpointDefault = "default"

// NewTempoClient creates a new Tempo client.
func NewTempoClient(name, baseURL string, logger *slog.Logger) (client *TempoClient) {
	client = &TempoClient{
		name:    name,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: tempoTimeout,
		},
		logger: logger,
	}

	return client
}

// LoadTempoClients loads Tempo clients from environment variables.
// Supports TEMPO_URL (default) and TEMPO_<NAME>_URL patterns.
func LoadTempoClients(logger *slog.Logger) (clients map[string]*TempoClient) {
	clients = make(map[string]*TempoClient)

	// Default endpoint
	if defaultURL := os.Getenv("TEMPO_URL"); defaultURL != "" {
		clients[tempoEndpointDefault] = NewTempoClient(tempoEndpointDefault, defaultURL, logger)
		logger.Info("Tempo client initialized", slog.String("name", tempoEndpointDefault), slog.String("url", defaultURL))
	}

	// Named endpoints (TEMPO_<NAME>_URL)
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "TEMPO_") || !strings.Contains(env, "_URL=") {
			continue
		}

		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}

		key := parts[0]
		url := parts[1]

		// Skip the default TEMPO_URL (already handled)
		if key == "TEMPO_URL" {
			continue
		}

		// Extract name: TEMPO_<NAME>_URL → name
		trimmed := strings.TrimPrefix(key, "TEMPO_")
		trimmed = strings.TrimSuffix(trimmed, "_URL")
		if trimmed == "" || trimmed == key {
			continue
		}

		name := strings.ToLower(trimmed)
		clients[name] = NewTempoClient(name, url, logger)
		logger.Info("Tempo client initialized", slog.String("name", name), slog.String("url", url))
	}

	return clients
}

// getTempoTools returns Tempo-related tool definitions.
func getTempoTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolTempoGetTrace,
			Description: "Fetch a distributed trace by trace ID from Grafana Tempo. Returns all spans with service names, operations, durations, and errors.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"trace_id": map[string]interface{}{
						"type":        "string",
						"description": "Trace ID to look up (hex string, e.g., '2f3e4a5b6c7d8e9f0a1b2c3d4e5f6a7b')",
					},
					"endpoint": map[string]interface{}{
						"type":        "string",
						"description": "Named Tempo endpoint to query. Defaults to 'default'.",
					},
				},
				"required": []string{"trace_id"},
			},
		},
		{
			Name:        toolTempoSearchTraces,
			Description: "Search for traces in Grafana Tempo by tags and time range. Returns matching trace IDs with metadata.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tags": map[string]interface{}{
						"type":        "string",
						"description": "Tag search query (e.g., 'service.name=api-service status.code=error http.method=GET')",
					},
					"start": map[string]interface{}{
						"type":        "string",
						"description": "Start time as relative duration (e.g., '1h', '24h') or Unix epoch seconds",
					},
					"end": map[string]interface{}{
						"type":        "string",
						"description": descEndTime,
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of traces to return (default: 20)",
					},
					"endpoint": map[string]interface{}{
						"type":        "string",
						"description": "Named Tempo endpoint to query. Defaults to 'default'.",
					},
				},
				"required": []string{"tags"},
			},
		},
		{
			Name:        toolTempoListEndpoints,
			Description: "List all configured Tempo endpoints.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}

	return result
}

// executeTempoGetTrace fetches a trace by ID from Tempo.
func (s *Server) executeTempoGetTrace(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var client *TempoClient
	client, err = s.resolveTempoClient(args)
	if err != nil {
		return result, err
	}

	traceID, _ := args["trace_id"].(string)
	if traceID == "" {
		err = errors.New("trace_id parameter is required")
		return result, err
	}

	url := fmt.Sprintf("%s/api/traces/%s", client.baseURL, traceID)

	var body []byte
	body, err = client.makeRequest(ctx, url)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "fetched trace from Tempo",
		slog.String("endpoint", client.name),
		slog.String("trace_id", traceID),
		slog.Int("response_bytes", len(body)))

	result = formatTempoResponse(body)
	return result, err
}

// executeTempoSearchTraces searches for traces in Tempo.
func (s *Server) executeTempoSearchTraces(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var client *TempoClient
	client, err = s.resolveTempoClient(args)
	if err != nil {
		return result, err
	}

	tags, _ := args["tags"].(string)
	if tags == "" {
		err = errors.New("tags parameter is required")
		return result, err
	}

	// Build query parameters
	params := fmt.Sprintf("tags=%s", tags)

	if start, ok := args["start"].(string); ok && start != "" {
		params += "&start=" + start
	}

	if end, ok := args["end"].(string); ok && end != "" {
		params += "&end=" + end
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	params += fmt.Sprintf("&limit=%d", limit)

	url := fmt.Sprintf("%s/api/search?%s", client.baseURL, params)

	var body []byte
	body, err = client.makeRequest(ctx, url)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "searched traces in Tempo",
		slog.String("endpoint", client.name),
		slog.String("tags", tags),
		slog.Int("response_bytes", len(body)))

	result = formatTempoResponse(body)
	return result, err
}

// executeTempoListEndpoints lists configured Tempo endpoints.
func (s *Server) executeTempoListEndpoints(_ context.Context, _ map[string]interface{}) (result string, err error) {
	if len(s.tempoClients) == 0 {
		err = errors.New("no Tempo endpoints configured. Set TEMPO_URL or TEMPO_<NAME>_URL environment variables")
		return result, err
	}

	var endpoints []string
	for name, client := range s.tempoClients {
		endpoints = append(endpoints, fmt.Sprintf("  %s: %s", name, client.baseURL))
	}

	result = fmt.Sprintf("Configured Tempo endpoints (%d):\n%s", len(endpoints), strings.Join(endpoints, "\n"))
	return result, err
}

// resolveTempoClient resolves which Tempo client to use based on the endpoint arg.
func (s *Server) resolveTempoClient(args map[string]interface{}) (client *TempoClient, err error) {
	if len(s.tempoClients) == 0 {
		err = errors.New("no Tempo endpoints configured. Set TEMPO_URL or TEMPO_<NAME>_URL environment variables")
		return client, err
	}

	endpointName := tempoEndpointDefault
	if name, ok := args["endpoint"].(string); ok && name != "" {
		endpointName = strings.ToLower(name)
	}

	var exists bool
	client, exists = s.tempoClients[endpointName]
	if !exists {
		available := make([]string, 0, len(s.tempoClients))
		for name := range s.tempoClients {
			available = append(available, name)
		}

		err = fmt.Errorf("tempo endpoint %q not configured. Available: %v", endpointName, available)
		return client, err
	}

	return client, err
}

// makeRequest performs an HTTP GET request and returns the response body.
func (c *TempoClient) makeRequest(ctx context.Context, url string) (body []byte, err error) {
	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		err = fmt.Errorf("creating request: %w", err)
		return body, err
	}

	req.Header.Set("Accept", "application/json")

	var resp *http.Response
	resp, err = c.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("tempo request failed: %w", err)
		return body, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("tempo returned status %d", resp.StatusCode)
		return body, err
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("reading response: %w", err)
		return body, err
	}

	return body, err
}

const tempoMaxResponseBytes = 50 * 1024

// formatTempoResponse formats and optionally truncates a Tempo JSON response.
func formatTempoResponse(body []byte) (result string) {
	// Pretty-print JSON
	var parsed interface{}

	prettyErr := json.Unmarshal(body, &parsed)
	if prettyErr != nil {
		result = truncateTempoResponse(body)
		return result
	}

	pretty, marshalErr := json.MarshalIndent(parsed, "", "  ")
	if marshalErr != nil {
		result = truncateTempoResponse(body)
		return result
	}

	result = truncateTempoResponse(pretty)
	return result
}

func truncateTempoResponse(body []byte) (result string) {
	if len(body) <= tempoMaxResponseBytes {
		result = string(body)
		return result
	}

	result = string(body[:tempoMaxResponseBytes]) + "\n\n[Response truncated - exceeded 50KB limit]"
	return result
}
