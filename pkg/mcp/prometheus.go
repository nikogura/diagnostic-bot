package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Prometheus tool name constants.
const (
	toolPrometheusQuery         = "prometheus_query"
	toolPrometheusQueryRange    = "prometheus_query_range"
	toolPrometheusSeries        = "prometheus_series"
	toolPrometheusLabelValues   = "prometheus_label_values"
	toolPrometheusListEndpoints = "prometheus_list_endpoints"
)

// Default values for Prometheus queries.
const (
	// DefaultPrometheusEndpoint is the default in-cluster Thanos Query endpoint.
	DefaultPrometheusEndpoint = "http://thanos-query.monitoring.svc.cluster.local:9090"

	prometheusEnvPrefix       = "PROMETHEUS_"
	prometheusURLSuffix       = "_URL"
	prometheusTimeout         = 30 * time.Second
	prometheusStatusSuccess   = "success"
	prometheusEndpointDefault = "default"

	// targetDataPoints is the target number of data points for auto-step calculation.
	targetDataPoints = 250
)

// PrometheusClient handles interactions with a Prometheus-compatible HTTP API.
type PrometheusClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

// PrometheusAPIResponse represents the top-level Prometheus HTTP API response.
// The Data field is json.RawMessage because /api/v1/query and /api/v1/query_range
// return {resultType, result}, while /api/v1/series and /api/v1/label/*/values
// return a plain array.
type PrometheusAPIResponse struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data,omitempty"`
	ErrorType string          `json:"errorType,omitempty"`
	Error     string          `json:"error,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// PrometheusData contains the result type and result data from a Prometheus query.
type PrometheusData struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

// PrometheusVectorResult represents a single instant query result (vector).
type PrometheusVectorResult struct {
	Metric map[string]string `json:"metric"`
	Value  [2]interface{}    `json:"value"` // [timestamp, value]
}

// PrometheusMatrixResult represents a single range query result (matrix).
type PrometheusMatrixResult struct {
	Metric map[string]string `json:"metric"`
	Values [][2]interface{}  `json:"values"` // [[timestamp, value], ...]
}

// PrometheusSeriesResult represents a series match result.
type PrometheusSeriesResult []map[string]string

// PrometheusLabelValuesResult represents a label values response.
type PrometheusLabelValuesResult []string

// PrometheusQueryResult is the structured output returned to the caller.
type PrometheusQueryResult struct {
	Status     string      `json:"status"`
	ResultType string      `json:"resultType,omitempty"`
	Result     interface{} `json:"result"`
	Warnings   []string    `json:"warnings,omitempty"`
	Query      string      `json:"query,omitempty"`
	Endpoint   string      `json:"endpoint,omitempty"`
}

// PrometheusEndpointInfo contains metadata about a configured Prometheus endpoint.
type PrometheusEndpointInfo struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
}

// NewPrometheusClient creates a new Prometheus API client.
func NewPrometheusClient(baseURL string, logger *slog.Logger) (client *PrometheusClient, err error) {
	if baseURL == "" {
		err = errors.New("prometheus base URL is required")
		return client, err
	}

	// Trim trailing slash for consistency.
	baseURL = strings.TrimRight(baseURL, "/")

	client = &PrometheusClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: prometheusTimeout,
			// Per-client Transport isolates the connection pool from
			// http.DefaultTransport (see pkg/mcp/tempo.go for context).
			Transport: &http.Transport{},
		},
		logger: logger,
	}

	return client, err
}

// LoadPrometheusClients scans environment variables and creates Prometheus clients.
// It looks for PROMETHEUS_URL (default) and PROMETHEUS_<NAME>_URL patterns.
func LoadPrometheusClients(logger *slog.Logger) (clients map[string]*PrometheusClient) {
	clients = make(map[string]*PrometheusClient)

	// Try to load the default PROMETHEUS_URL.
	defaultURL := os.Getenv("PROMETHEUS_URL")
	if defaultURL != "" {
		client, clientErr := NewPrometheusClient(defaultURL, logger)
		if clientErr != nil {
			logger.Warn("Failed to initialize default Prometheus client",
				slog.String("error", clientErr.Error()))
		} else {
			clients[prometheusEndpointDefault] = client
		}
	}

	// Scan for PROMETHEUS_<NAME>_URL patterns.
	namedClients := scanPrometheusEnvVars(logger)
	for name, client := range namedClients {
		if name == prometheusEndpointDefault {
			continue
		}
		clients[name] = client
	}

	if len(clients) == 0 {
		logger.Info("No Prometheus endpoints configured")
	} else {
		names := make([]string, 0, len(clients))
		for name := range clients {
			names = append(names, name)
		}
		logger.Info("Prometheus clients loaded",
			slog.Int("count", len(clients)),
			slog.Any("endpoints", names))
	}

	return clients
}

// scanPrometheusEnvVars scans environment variables for PROMETHEUS_<NAME>_URL patterns.
func scanPrometheusEnvVars(logger *slog.Logger) (clients map[string]*PrometheusClient) {
	clients = make(map[string]*PrometheusClient)

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]

		if !strings.HasPrefix(key, prometheusEnvPrefix) || !strings.HasSuffix(key, prometheusURLSuffix) {
			continue
		}

		// Skip the default PROMETHEUS_URL (handled separately).
		if key == "PROMETHEUS_URL" {
			continue
		}

		// Extract the name: PROMETHEUS_<NAME>_URL -> <NAME>.
		name := key[len(prometheusEnvPrefix) : len(key)-len(prometheusURLSuffix)]
		if name == "" {
			continue
		}

		name = strings.ToLower(name)

		envURL := os.Getenv(key)
		if envURL == "" {
			continue
		}

		logger.Debug("Found Prometheus endpoint configuration",
			slog.String("name", name),
			slog.String("url_key", key))

		client, clientErr := NewPrometheusClient(envURL, logger)
		if clientErr != nil {
			logger.Warn("Failed to initialize Prometheus client",
				slog.String("name", name),
				slog.String("error", clientErr.Error()))
			continue
		}

		clients[name] = client
	}

	return clients
}

// makeRequest performs an HTTP GET request to the Prometheus API.
func (c *PrometheusClient) makeRequest(ctx context.Context, endpoint string, params url.Values) (body []byte, err error) {
	reqURL := c.baseURL + endpoint
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		err = fmt.Errorf("creating request: %w", err)
		return body, err
	}

	req.Header.Set("Accept", "application/json")

	c.logger.DebugContext(ctx, "making Prometheus API request",
		"endpoint", endpoint)

	var resp *http.Response
	resp, err = c.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("executing request: %w", err)
		return body, err
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("reading response body: %w", err)
		return body, err
	}

	if resp.StatusCode >= 400 {
		err = fmt.Errorf("prometheus API error (status %d): %s", resp.StatusCode, string(body))
		return body, err
	}

	return body, err
}

// Query executes an instant query against /api/v1/query.
func (c *PrometheusClient) Query(ctx context.Context, query string, queryTime *time.Time) (result PrometheusQueryResult, err error) {
	params := url.Values{}
	params.Set("query", query)

	if queryTime != nil {
		params.Set("time", formatPrometheusTime(*queryTime))
	}

	var body []byte
	body, err = c.makeRequest(ctx, "/api/v1/query", params)
	if err != nil {
		return result, err
	}

	result, err = parsePrometheusResponse(body)
	if err != nil {
		return result, err
	}

	result.Query = query

	return result, err
}

// QueryRange executes a range query against /api/v1/query_range.
func (c *PrometheusClient) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (result PrometheusQueryResult, err error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", formatPrometheusTime(start))
	params.Set("end", formatPrometheusTime(end))
	params.Set("step", formatStep(step))

	var body []byte
	body, err = c.makeRequest(ctx, "/api/v1/query_range", params)
	if err != nil {
		return result, err
	}

	result, err = parsePrometheusResponse(body)
	if err != nil {
		return result, err
	}

	result.Query = query

	return result, err
}

// Series queries the /api/v1/series endpoint.
// The series endpoint returns data as a plain array: {"status":"success","data":[...]}.
func (c *PrometheusClient) Series(ctx context.Context, matchers []string, start, end *time.Time) (result PrometheusQueryResult, err error) {
	params := url.Values{}
	for _, m := range matchers {
		params.Add("match[]", m)
	}

	if start != nil {
		params.Set("start", formatPrometheusTime(*start))
	}

	if end != nil {
		params.Set("end", formatPrometheusTime(*end))
	}

	var body []byte
	body, err = c.makeRequest(ctx, "/api/v1/series", params)
	if err != nil {
		return result, err
	}

	var apiResp PrometheusAPIResponse
	err = json.Unmarshal(body, &apiResp)
	if err != nil {
		err = fmt.Errorf("unmarshaling series response: %w", err)
		return result, err
	}

	if apiResp.Status != prometheusStatusSuccess {
		err = fmt.Errorf("prometheus series error: %s: %s", apiResp.ErrorType, apiResp.Error)
		return result, err
	}

	// Series data is a plain array of label sets.
	var series PrometheusSeriesResult
	err = json.Unmarshal(apiResp.Data, &series)
	if err != nil {
		err = fmt.Errorf("unmarshaling series data: %w", err)
		return result, err
	}

	result = PrometheusQueryResult{
		Status:   apiResp.Status,
		Result:   series,
		Warnings: apiResp.Warnings,
	}

	return result, err
}

// LabelValues queries the /api/v1/label/{name}/values endpoint.
// The label values endpoint returns data as a plain array: {"status":"success","data":["val1","val2"]}.
func (c *PrometheusClient) LabelValues(ctx context.Context, labelName string, matchers []string) (result PrometheusQueryResult, err error) {
	params := url.Values{}
	for _, m := range matchers {
		params.Add("match[]", m)
	}

	endpoint := fmt.Sprintf("/api/v1/label/%s/values", url.PathEscape(labelName))

	var body []byte
	body, err = c.makeRequest(ctx, endpoint, params)
	if err != nil {
		return result, err
	}

	var apiResp PrometheusAPIResponse
	err = json.Unmarshal(body, &apiResp)
	if err != nil {
		err = fmt.Errorf("unmarshaling label values response: %w", err)
		return result, err
	}

	if apiResp.Status != prometheusStatusSuccess {
		err = fmt.Errorf("prometheus label values error: %s: %s", apiResp.ErrorType, apiResp.Error)
		return result, err
	}

	// Label values data is a plain string array.
	var values PrometheusLabelValuesResult
	err = json.Unmarshal(apiResp.Data, &values)
	if err != nil {
		err = fmt.Errorf("unmarshaling label values data: %w", err)
		return result, err
	}

	result = PrometheusQueryResult{
		Status:   apiResp.Status,
		Result:   values,
		Warnings: apiResp.Warnings,
	}

	return result, err
}

// parsePrometheusResponse parses the common Prometheus API response for query/query_range.
// These endpoints return data as {resultType, result}.
func parsePrometheusResponse(body []byte) (result PrometheusQueryResult, err error) {
	var apiResp PrometheusAPIResponse
	err = json.Unmarshal(body, &apiResp)
	if err != nil {
		err = fmt.Errorf("unmarshaling prometheus response: %w", err)
		return result, err
	}

	if apiResp.Status != prometheusStatusSuccess {
		err = fmt.Errorf("prometheus query error: %s: %s", apiResp.ErrorType, apiResp.Error)
		return result, err
	}

	// Parse the data field as {resultType, result}.
	var data PrometheusData
	err = json.Unmarshal(apiResp.Data, &data)
	if err != nil {
		err = fmt.Errorf("unmarshaling prometheus data: %w", err)
		return result, err
	}

	result = PrometheusQueryResult{
		Status:     apiResp.Status,
		ResultType: data.ResultType,
		Warnings:   apiResp.Warnings,
	}

	// Parse result based on type.
	switch data.ResultType {
	case "vector":
		var vectorResults []PrometheusVectorResult
		err = json.Unmarshal(data.Result, &vectorResults)
		if err != nil {
			err = fmt.Errorf("unmarshaling vector result: %w", err)
			return result, err
		}
		result.Result = vectorResults

	case "matrix":
		var matrixResults []PrometheusMatrixResult
		err = json.Unmarshal(data.Result, &matrixResults)
		if err != nil {
			err = fmt.Errorf("unmarshaling matrix result: %w", err)
			return result, err
		}
		result.Result = matrixResults

	default:
		// For scalar, string, or unknown types, keep raw JSON.
		result.Result = data.Result
	}

	return result, err
}

// formatPrometheusTime formats a time.Time as a Unix timestamp string for the Prometheus API.
func formatPrometheusTime(t time.Time) (result string) {
	result = strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
	return result
}

// formatStep formats a duration as a Prometheus step string (e.g., "15s", "1m", "1h").
func formatStep(d time.Duration) (result string) {
	seconds := int(d.Seconds())
	if seconds <= 0 {
		result = "15s"
		return result
	}

	result = strconv.Itoa(seconds) + "s"
	return result
}

// calculateAutoStep determines an appropriate step duration based on the time range.
// It targets approximately targetDataPoints data points.
func calculateAutoStep(start, end time.Time) (step time.Duration) {
	rangeDuration := end.Sub(start)
	if rangeDuration <= 0 {
		step = 15 * time.Second
		return step
	}

	stepSeconds := int(rangeDuration.Seconds()) / targetDataPoints
	if stepSeconds < 15 {
		stepSeconds = 15
	}

	step = time.Duration(stepSeconds) * time.Second
	return step
}

// getPrometheusTools returns Prometheus-related tool definitions.
func getPrometheusTools() (result []MCPTool) {
	result = []MCPTool{
		getPrometheusQueryTool(),
		getPrometheusQueryRangeTool(),
	}

	result = append(result, getPrometheusSeriesAndLabelTools()...)

	return result
}

// getPrometheusQueryTool returns the instant query tool definition.
func getPrometheusQueryTool() (result MCPTool) {
	result = MCPTool{
		Name:        toolPrometheusQuery,
		Description: "Execute an instant PromQL query against a Prometheus-compatible endpoint. Returns the current value of a metric expression. Works with Prometheus, Thanos Query, Cortex, Mimir, and VictoriaMetrics.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "PromQL query string. Example: 'up{job=\"prometheus\"}'",
				},
				"time": map[string]interface{}{
					"type":        "string",
					"description": "Evaluation time as RFC3339 timestamp or relative duration (e.g., '5m', '1h'). Defaults to now.",
				},
				"endpoint": map[string]interface{}{
					"type":        "string",
					"description": "Named Prometheus endpoint to query (e.g., 'prod', 'dev'). Use prometheus_list_endpoints to see available endpoints. Defaults to 'default'.",
				},
			},
			"required": []string{"query"},
		},
	}

	return result
}

// getPrometheusQueryRangeTool returns the range query tool definition.
func getPrometheusQueryRangeTool() (result MCPTool) {
	result = MCPTool{
		Name:        toolPrometheusQueryRange,
		Description: "Execute a range PromQL query against a Prometheus-compatible endpoint. Returns metric values over a time range. Ideal for graphing and trend analysis.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "PromQL query string. Example: 'rate(http_requests_total[5m])'",
				},
				"start": map[string]interface{}{
					"type":        "string",
					"description": "Start time as relative duration (e.g., '1h', '24h', '7d') or RFC3339 timestamp",
				},
				"end": map[string]interface{}{
					"type":        "string",
					"description": descEndTime,
				},
				"step": map[string]interface{}{
					"type":        "string",
					"description": "Query resolution step (e.g., '15s', '1m', '5m'). Auto-calculated if omitted to target ~250 data points.",
				},
				"endpoint": map[string]interface{}{
					"type":        "string",
					"description": "Named Prometheus endpoint to query. Defaults to 'default'.",
				},
			},
			"required": []string{"query", "start"},
		},
	}

	return result
}

// getPrometheusSeriesAndLabelTools returns the series, label values, and list endpoints tool definitions.
func getPrometheusSeriesAndLabelTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolPrometheusSeries,
			Description: "Find time series matching label selectors. Useful for discovering what metrics exist for a given job, namespace, or set of labels.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"match": map[string]interface{}{
						"type":        "array",
						"description": "Series selectors to match. Example: ['{job=\"prometheus\"}', '{__name__=~\"http_.*\"}']",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
					"start": map[string]interface{}{
						"type":        "string",
						"description": "Start time as relative duration or RFC3339 timestamp (optional)",
					},
					"end": map[string]interface{}{
						"type":        "string",
						"description": "End time as 'now' or RFC3339 timestamp (optional)",
					},
					"endpoint": map[string]interface{}{
						"type":        "string",
						"description": "Named Prometheus endpoint to query. Defaults to 'default'.",
					},
				},
				"required": []string{"match"},
			},
		},
		{
			Name:        toolPrometheusLabelValues,
			Description: "Get all values for a given label name. Useful for discovering available targets, namespaces, jobs, or other label values. Supports optional series selectors to narrow results.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"label": map[string]interface{}{
						"type":        "string",
						"description": "Label name to get values for. Example: 'namespace', 'job', '__name__'",
					},
					"match": map[string]interface{}{
						"type":        "array",
						"description": "Optional series selectors to filter label values. Example: ['{job=\"prometheus\"}']",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
					"endpoint": map[string]interface{}{
						"type":        "string",
						"description": "Named Prometheus endpoint to query. Defaults to 'default'.",
					},
				},
				"required": []string{"label"},
			},
		},
		{
			Name:        toolPrometheusListEndpoints,
			Description: "List all configured Prometheus-compatible endpoints. Shows named endpoints available for querying metrics.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}

	return result
}

// resolvePrometheusClient resolves the Prometheus client for the given endpoint name.
func (s *Server) resolvePrometheusClient(args map[string]interface{}) (client *PrometheusClient, err error) {
	if len(s.prometheusClients) == 0 {
		err = errors.New("no Prometheus endpoints configured. Set PROMETHEUS_URL or PROMETHEUS_<NAME>_URL environment variables")
		return client, err
	}

	endpointName := prometheusEndpointDefault
	if name, ok := args["endpoint"].(string); ok && name != "" {
		endpointName = strings.ToLower(name)
	}

	var exists bool
	client, exists = s.prometheusClients[endpointName]
	if !exists {
		available := make([]string, 0, len(s.prometheusClients))
		for name := range s.prometheusClients {
			available = append(available, name)
		}
		err = fmt.Errorf("prometheus endpoint %q not configured. Available endpoints: %v", endpointName, available)
		return client, err
	}

	return client, err
}

// executePrometheusQuery executes an instant PromQL query.
func (s *Server) executePrometheusQuery(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var client *PrometheusClient
	client, err = s.resolvePrometheusClient(args)
	if err != nil {
		return result, err
	}

	query, _ := args["query"].(string)
	if query == "" {
		err = errors.New("query parameter is required")
		return result, err
	}

	// Parse optional time parameter.
	var queryTime *time.Time
	timeStr, _ := args["time"].(string)
	if timeStr != "" {
		parsed, parseErr := parseTimeArg(timeStr)
		if parseErr != nil {
			err = fmt.Errorf("parsing time: %w", parseErr)
			return result, err
		}
		queryTime = &parsed
	}

	s.logger.InfoContext(ctx, "executing Prometheus instant query",
		"query", query,
		"endpoint", client.baseURL)

	var queryResult PrometheusQueryResult
	queryResult, err = client.Query(ctx, query, queryTime)
	if err != nil {
		return result, err
	}

	queryResult.Endpoint = client.baseURL

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(queryResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting query result: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executePrometheusQueryRange executes a range PromQL query.
func (s *Server) executePrometheusQueryRange(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var client *PrometheusClient
	client, err = s.resolvePrometheusClient(args)
	if err != nil {
		return result, err
	}

	query, _ := args["query"].(string)
	if query == "" {
		err = errors.New("query parameter is required")
		return result, err
	}

	startStr, _ := args["start"].(string)
	if startStr == "" {
		err = errors.New("start parameter is required")
		return result, err
	}

	var startTime time.Time
	startTime, err = parseTimeArg(startStr)
	if err != nil {
		err = fmt.Errorf("parsing start: %w", err)
		return result, err
	}

	// Parse end time (defaults to now).
	endTimeStr := parseEndTimeArg(args)
	var endTime time.Time
	endTime, err = parseTimeArg(endTimeStr)
	if err != nil {
		err = fmt.Errorf("parsing end: %w", err)
		return result, err
	}

	// Parse or auto-calculate step.
	step := calculateAutoStep(startTime, endTime)
	stepStr, _ := args["step"].(string)
	if stepStr != "" {
		var parsed time.Duration
		parsed, err = parseRelativeDuration(stepStr)
		if err != nil {
			err = fmt.Errorf("parsing step: %w", err)
			return result, err
		}
		step = parsed
	}

	s.logger.InfoContext(ctx, "executing Prometheus range query",
		"query", query,
		"start", startTime.Format(time.RFC3339),
		"end", endTime.Format(time.RFC3339),
		"step", step.String(),
		"endpoint", client.baseURL)

	var queryResult PrometheusQueryResult
	queryResult, err = client.QueryRange(ctx, query, startTime, endTime, step)
	if err != nil {
		return result, err
	}

	queryResult.Endpoint = client.baseURL

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(queryResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting range query result: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executePrometheusSeries executes a series discovery query.
func (s *Server) executePrometheusSeries(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var client *PrometheusClient
	client, err = s.resolvePrometheusClient(args)
	if err != nil {
		return result, err
	}

	var matchers []string
	matchers, err = parseMatchersArg(args)
	if err != nil {
		return result, err
	}

	// Parse optional time range.
	var startTime, endTime *time.Time

	startStr, _ := args["start"].(string)
	if startStr != "" {
		parsed, parseErr := parseTimeArg(startStr)
		if parseErr != nil {
			err = fmt.Errorf("parsing start: %w", parseErr)
			return result, err
		}
		startTime = &parsed
	}

	endStr := parseEndTimeArg(args)
	if endStr != timeNow {
		parsed, parseErr := parseTimeArg(endStr)
		if parseErr != nil {
			err = fmt.Errorf("parsing end: %w", parseErr)
			return result, err
		}
		endTime = &parsed
	}

	s.logger.InfoContext(ctx, "executing Prometheus series query",
		"matchers", matchers,
		"endpoint", client.baseURL)

	var queryResult PrometheusQueryResult
	queryResult, err = client.Series(ctx, matchers, startTime, endTime)
	if err != nil {
		return result, err
	}

	queryResult.Endpoint = client.baseURL

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(queryResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting series result: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executePrometheusLabelValues queries label values.
func (s *Server) executePrometheusLabelValues(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var client *PrometheusClient
	client, err = s.resolvePrometheusClient(args)
	if err != nil {
		return result, err
	}

	labelName, _ := args["label"].(string)
	if labelName == "" {
		err = errors.New("label parameter is required")
		return result, err
	}

	// Parse optional matchers.
	var matchers []string
	matchRaw, matchOk := args["match"].([]interface{})
	if matchOk {
		for _, m := range matchRaw {
			mStr, mOk := m.(string)
			if mOk && mStr != "" {
				matchers = append(matchers, mStr)
			}
		}
	}

	s.logger.InfoContext(ctx, "executing Prometheus label values query",
		"label", labelName,
		"matchers", matchers,
		"endpoint", client.baseURL)

	var queryResult PrometheusQueryResult
	queryResult, err = client.LabelValues(ctx, labelName, matchers)
	if err != nil {
		return result, err
	}

	queryResult.Endpoint = client.baseURL

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(queryResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting label values result: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executePrometheusListEndpoints returns the list of configured Prometheus endpoints.
func (s *Server) executePrometheusListEndpoints(_ context.Context, _ map[string]interface{}) (result string, err error) {
	if len(s.prometheusClients) == 0 {
		err = errors.New("no Prometheus endpoints configured. Set PROMETHEUS_URL or PROMETHEUS_<NAME>_URL environment variables")
		return result, err
	}

	endpoints := make([]PrometheusEndpointInfo, 0, len(s.prometheusClients))
	for name, client := range s.prometheusClients {
		endpoints = append(endpoints, PrometheusEndpointInfo{
			Name:    name,
			BaseURL: client.baseURL,
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

// parseMatchersArg parses the match argument from the args map.
func parseMatchersArg(args map[string]interface{}) (matchers []string, err error) {
	matchRaw, ok := args["match"].([]interface{})
	if !ok || len(matchRaw) == 0 {
		err = errors.New("match parameter is required and must be a non-empty array")
		return matchers, err
	}

	for _, m := range matchRaw {
		mStr, mOk := m.(string)
		if mOk && mStr != "" {
			matchers = append(matchers, mStr)
		}
	}

	if len(matchers) == 0 {
		err = errors.New("match must contain at least one valid series selector")
		return matchers, err
	}

	return matchers, err
}
