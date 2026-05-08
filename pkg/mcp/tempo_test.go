package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTempoServer(t *testing.T, handler http.HandlerFunc) (ts *httptest.Server, client *TempoClient) {
	t.Helper()

	ts = httptest.NewServer(handler)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	client = NewTempoClient("default", ts.URL, logger)

	return ts, client
}

func newTestServerWithTempo(t *testing.T, handler http.HandlerFunc) (testServer *httptest.Server, mcpServer *Server) {
	t.Helper()

	var tempoClient *TempoClient
	testServer, tempoClient = newTestTempoServer(t, handler)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mcpServer = &Server{
		tempoClients: map[string]*TempoClient{
			"default": tempoClient,
		},
		logger:      logger,
		companyName: "TestCorp",
	}

	return testServer, mcpServer
}

func TestGetTempoTools(t *testing.T) {
	t.Parallel()

	tools := getTempoTools()
	require.Len(t, tools, 3)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	assert.True(t, names[toolTempoGetTrace])
	assert.True(t, names[toolTempoSearchTraces])
	assert.True(t, names[toolTempoListEndpoints])
}

func TestExecuteTempoGetTrace(t *testing.T) {
	t.Parallel()

	traceResponse := map[string]interface{}{
		"batches": []map[string]interface{}{
			{
				"resource": map[string]interface{}{
					"attributes": []map[string]interface{}{
						{"key": "service.name", "value": map[string]interface{}{"stringValue": "api-service"}},
					},
				},
				"scopeSpans": []map[string]interface{}{
					{
						"spans": []map[string]interface{}{
							{
								"traceId":           "abc123",
								"spanId":            "span1",
								"operationName":     "GET /api/v1/users",
								"startTimeUnixNano": "1700000000000000000",
								"endTimeUnixNano":   "1700000000100000000",
							},
						},
					},
				},
			},
		},
	}

	ts, server := newTestServerWithTempo(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/api/traces/abc123")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(traceResponse)
	})
	defer ts.Close()

	ctx := context.Background()
	result, err := server.executeTempoGetTrace(ctx, map[string]interface{}{
		"trace_id": "abc123",
	})

	require.NoError(t, err)
	assert.Contains(t, result, "api-service")
	assert.Contains(t, result, "abc123")
}

func TestExecuteTempoGetTraceMissingID(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithTempo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()

	ctx := context.Background()
	_, err := server.executeTempoGetTrace(ctx, map[string]interface{}{})
	assert.ErrorContains(t, err, "trace_id parameter is required")
}

func TestExecuteTempoGetTraceNotConfigured(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{logger: logger}

	ctx := context.Background()
	_, err := server.executeTempoGetTrace(ctx, map[string]interface{}{
		"trace_id": "abc123",
	})
	assert.ErrorContains(t, err, "no Tempo endpoints configured")
}

func TestExecuteTempoGetTraceServerError(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithTempo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer ts.Close()

	ctx := context.Background()
	_, err := server.executeTempoGetTrace(ctx, map[string]interface{}{
		"trace_id": "abc123",
	})
	assert.ErrorContains(t, err, "status 500")
}

func TestExecuteTempoSearchTraces(t *testing.T) {
	t.Parallel()

	searchResponse := map[string]interface{}{
		"traces": []map[string]interface{}{
			{
				"traceID":           "abc123",
				"rootServiceName":   "api-service",
				"rootTraceName":     "GET /api/v1/users",
				"startTimeUnixNano": "1700000000000000000",
				"durationMs":        42,
			},
			{
				"traceID":           "def456",
				"rootServiceName":   "api-service",
				"rootTraceName":     "POST /api/v1/orders",
				"startTimeUnixNano": "1700000001000000000",
				"durationMs":        128,
			},
		},
	}

	ts, server := newTestServerWithTempo(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/api/search")
		assert.Contains(t, r.URL.RawQuery, "tags=service.name=api-service")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(searchResponse)
	})
	defer ts.Close()

	ctx := context.Background()
	result, err := server.executeTempoSearchTraces(ctx, map[string]interface{}{
		"tags":  "service.name=api-service",
		"limit": float64(10),
	})

	require.NoError(t, err)
	assert.Contains(t, result, "abc123")
	assert.Contains(t, result, "def456")
}

func TestExecuteTempoSearchTracesMissingTags(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithTempo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()

	ctx := context.Background()
	_, err := server.executeTempoSearchTraces(ctx, map[string]interface{}{})
	assert.ErrorContains(t, err, "tags parameter is required")
}

func TestExecuteTempoListEndpoints(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	server := &Server{
		tempoClients: map[string]*TempoClient{
			"default": NewTempoClient("default", "http://tempo:3200", logger),
			"prod":    NewTempoClient("prod", "http://tempo-prod:3200", logger),
		},
		logger: logger,
	}

	ctx := context.Background()
	result, err := server.executeTempoListEndpoints(ctx, map[string]interface{}{})

	require.NoError(t, err)
	assert.Contains(t, result, "default")
	assert.Contains(t, result, "prod")
	assert.Contains(t, result, "2")
}

func TestExecuteTempoListEndpointsNotConfigured(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{logger: logger}

	ctx := context.Background()
	_, err := server.executeTempoListEndpoints(ctx, map[string]interface{}{})
	assert.ErrorContains(t, err, "no Tempo endpoints configured")
}

func TestResolveTempoClientDefault(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{
		tempoClients: map[string]*TempoClient{
			"default": NewTempoClient("default", "http://tempo:3200", logger),
		},
		logger: logger,
	}

	client, err := server.resolveTempoClient(map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "default", client.name)
}

func TestResolveTempoClientNamed(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{
		tempoClients: map[string]*TempoClient{
			"default": NewTempoClient("default", "http://tempo:3200", logger),
			"prod":    NewTempoClient("prod", "http://tempo-prod:3200", logger),
		},
		logger: logger,
	}

	client, err := server.resolveTempoClient(map[string]interface{}{
		"endpoint": "prod",
	})
	require.NoError(t, err)
	assert.Equal(t, "prod", client.name)
}

func TestResolveTempoClientNotFound(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{
		tempoClients: map[string]*TempoClient{
			"default": NewTempoClient("default", "http://tempo:3200", logger),
		},
		logger: logger,
	}

	_, err := server.resolveTempoClient(map[string]interface{}{
		"endpoint": "nonexistent",
	})
	require.ErrorContains(t, err, "not configured")
	require.ErrorContains(t, err, "Available")
}

func TestFormatTempoResponse(t *testing.T) {
	t.Parallel()

	input := `{"traceID":"abc123","spans":[{"name":"test"}]}`
	result := formatTempoResponse([]byte(input))
	assert.Contains(t, result, "abc123")
	assert.Contains(t, result, "test")
}

func TestFormatTempoResponseInvalidJSON(t *testing.T) {
	t.Parallel()

	result := formatTempoResponse([]byte("not json"))
	assert.Equal(t, "not json", result)
}

func TestTruncateTempoResponse(t *testing.T) {
	t.Parallel()

	// Short response — no truncation
	short := []byte("hello")
	assert.Equal(t, "hello", truncateTempoResponse(short))

	// Long response — truncated
	long := make([]byte, tempoMaxResponseBytes+100)
	for i := range long {
		long[i] = 'x'
	}

	truncated := truncateTempoResponse(long)
	assert.Contains(t, truncated, "[Response truncated")
	assert.LessOrEqual(t, len(truncated), tempoMaxResponseBytes+100)
}

func TestNewTempoClient(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := NewTempoClient("test", "http://tempo:3200/", logger)

	assert.Equal(t, "test", client.name)
	assert.Equal(t, "http://tempo:3200", client.baseURL) // trailing slash stripped
	assert.NotNil(t, client.httpClient)
}
