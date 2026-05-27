package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPrometheusClientIsolatesTransport guards the per-client Transport
// pattern. See pkg/mcp/tempo.go for the failure mode this prevents.
func TestNewPrometheusClientIsolatesTransport(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewPrometheusClient("http://prom.example.com", logger)
	require.NoError(t, err)
	require.NotNil(t, client.httpClient.Transport, "client must have its own Transport; nil falls back to http.DefaultTransport")
}

// TestNewPrometheusClient tests Prometheus client initialization.
func TestNewPrometheusClient(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	tests := []struct {
		name      string
		baseURL   string
		expectErr bool
		errorMsg  string
	}{
		{
			name:      "valid_url",
			baseURL:   "http://prometheus.example.com:9090",
			expectErr: false,
		},
		{
			name:      "valid_url_with_trailing_slash",
			baseURL:   "http://prometheus.example.com:9090/",
			expectErr: false,
		},
		{
			name:      "empty_url",
			baseURL:   "",
			expectErr: true,
			errorMsg:  "base URL is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewPrometheusClient(tt.baseURL, logger)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, client)
				// Trailing slash should be trimmed.
				assert.NotEqual(t, byte('/'), client.baseURL[len(client.baseURL)-1], "trailing slash should be trimmed")
			}
		})
	}
}

// TestPrometheusClientQuery tests instant queries.
func TestPrometheusClientQuery(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("successful_vector_query", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/v1/query", r.URL.Path)
			assert.Equal(t, "up", r.URL.Query().Get("query"))

			resp := `{
				"status": "success",
				"data": {
					"resultType": "vector",
					"result": [
						{
							"metric": {"__name__": "up", "job": "prometheus"},
							"value": [1704067200, "1"]
						}
					]
				}
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		result, err := client.Query(ctx, "up", nil)

		require.NoError(t, err)
		assert.Equal(t, "success", result.Status)
		assert.Equal(t, "vector", result.ResultType)
		assert.Equal(t, "up", result.Query)

		vectorResults, ok := result.Result.([]PrometheusVectorResult)
		require.True(t, ok, "result should be vector type")
		require.Len(t, vectorResults, 1)
		assert.Equal(t, "up", vectorResults[0].Metric["__name__"])
		assert.Equal(t, "prometheus", vectorResults[0].Metric["job"])
	})

	t.Run("query_with_time", func(t *testing.T) {
		t.Parallel()

		queryTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.NotEmpty(t, r.URL.Query().Get("time"))

			resp := `{
				"status": "success",
				"data": {
					"resultType": "vector",
					"result": []
				}
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		result, err := client.Query(ctx, "up", &queryTime)

		require.NoError(t, err)
		assert.Equal(t, "success", result.Status)
	})

	t.Run("api_error_response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{
				"status": "error",
				"errorType": "bad_data",
				"error": "invalid expression"
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		_, err = client.Query(ctx, "invalid{", nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad_data")
		assert.Contains(t, err.Error(), "invalid expression")
	})

	t.Run("http_error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`Internal Server Error`))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		_, err = client.Query(ctx, "up", nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 500")
	})

	t.Run("invalid_json_response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`not json`))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		_, err = client.Query(ctx, "up", nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshaling prometheus response")
	})

	t.Run("query_with_warnings", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{
				"status": "success",
				"warnings": ["query exceeded max time"],
				"data": {
					"resultType": "vector",
					"result": []
				}
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		result, err := client.Query(ctx, "up", nil)

		require.NoError(t, err)
		assert.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "exceeded max time")
	})
}

// TestPrometheusClientQueryRange tests range queries.
func TestPrometheusClientQueryRange(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("successful_matrix_query", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/v1/query_range", r.URL.Path)
			assert.NotEmpty(t, r.URL.Query().Get("query"))
			assert.NotEmpty(t, r.URL.Query().Get("start"))
			assert.NotEmpty(t, r.URL.Query().Get("end"))
			assert.NotEmpty(t, r.URL.Query().Get("step"))

			resp := `{
				"status": "success",
				"data": {
					"resultType": "matrix",
					"result": [
						{
							"metric": {"__name__": "up", "job": "prometheus"},
							"values": [
								[1704067200, "1"],
								[1704067215, "1"],
								[1704067230, "0"]
							]
						}
					]
				}
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		start := time.Now().Add(-1 * time.Hour)
		end := time.Now()
		step := 15 * time.Second

		result, err := client.QueryRange(ctx, "up", start, end, step)

		require.NoError(t, err)
		assert.Equal(t, "success", result.Status)
		assert.Equal(t, "matrix", result.ResultType)

		matrixResults, ok := result.Result.([]PrometheusMatrixResult)
		require.True(t, ok, "result should be matrix type")
		require.Len(t, matrixResults, 1)
		assert.Equal(t, "up", matrixResults[0].Metric["__name__"])
		assert.Len(t, matrixResults[0].Values, 3)
	})

	t.Run("api_error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{
				"status": "error",
				"errorType": "bad_data",
				"error": "end timestamp must not be before start time"
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		_, err = client.QueryRange(ctx, "up", time.Now(), time.Now().Add(-1*time.Hour), 15*time.Second)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "end timestamp must not be before start time")
	})
}

// TestPrometheusClientSeries tests series discovery.
func TestPrometheusClientSeries(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("successful_series_query", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/v1/series", r.URL.Path)
			assert.Contains(t, r.URL.Query()["match[]"], `{job="prometheus"}`)

			resp := `{
				"status": "success",
				"data": [
					{"__name__": "up", "job": "prometheus", "instance": "localhost:9090"},
					{"__name__": "scrape_duration_seconds", "job": "prometheus", "instance": "localhost:9090"}
				]
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		result, err := client.Series(ctx, []string{`{job="prometheus"}`}, nil, nil)

		require.NoError(t, err)
		assert.Equal(t, "success", result.Status)

		series, ok := result.Result.(PrometheusSeriesResult)
		require.True(t, ok, "result should be series type")
		assert.Len(t, series, 2)
		assert.Equal(t, "up", series[0]["__name__"])
	})

	t.Run("with_time_range", func(t *testing.T) {
		t.Parallel()

		start := time.Now().Add(-1 * time.Hour)
		end := time.Now()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.NotEmpty(t, r.URL.Query().Get("start"))
			assert.NotEmpty(t, r.URL.Query().Get("end"))

			resp := `{"status": "success", "data": []}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		_, err = client.Series(ctx, []string{`{job="test"}`}, &start, &end)

		require.NoError(t, err)
	})

	t.Run("error_response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{
				"status": "error",
				"errorType": "bad_data",
				"error": "invalid match[] selector"
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		_, err = client.Series(ctx, []string{`invalid{`}, nil, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "series error")
	})
}

// TestPrometheusClientLabelValues tests label value discovery.
func TestPrometheusClientLabelValues(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("successful_label_values", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/v1/label/job/values", r.URL.Path)

			resp := `{
				"status": "success",
				"data": ["prometheus", "node-exporter", "alertmanager"]
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		result, err := client.LabelValues(ctx, "job", nil)

		require.NoError(t, err)
		assert.Equal(t, "success", result.Status)

		values, ok := result.Result.(PrometheusLabelValuesResult)
		require.True(t, ok, "result should be label values type")
		assert.Len(t, values, 3)
		assert.Contains(t, []string(values), "prometheus")
	})

	t.Run("with_matchers", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/v1/label/namespace/values", r.URL.Path)
			assert.Contains(t, r.URL.Query()["match[]"], `{job="kube-state-metrics"}`)

			resp := `{"status": "success", "data": ["default", "kube-system"]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		result, err := client.LabelValues(ctx, "namespace", []string{`{job="kube-state-metrics"}`})

		require.NoError(t, err)
		values, ok := result.Result.(PrometheusLabelValuesResult)
		require.True(t, ok)
		assert.Len(t, values, 2)
	})

	t.Run("error_response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{"status": "error", "errorType": "bad_data", "error": "invalid label name"}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewPrometheusClient(server.URL, logger)
		require.NoError(t, err)

		_, err = client.LabelValues(ctx, "invalid label", nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "label values error")
	})
}

// TestCalculateAutoStep tests the auto-step calculation logic.
func TestCalculateAutoStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rangeDur    time.Duration
		expectedMin time.Duration
		expectedMax time.Duration
	}{
		{
			name:        "one_hour_range",
			rangeDur:    1 * time.Hour,
			expectedMin: 14 * time.Second,
			expectedMax: 15 * time.Second, // 3600/250=14.4, clamped to 15s minimum.
		},
		{
			name:        "one_day_range",
			rangeDur:    24 * time.Hour,
			expectedMin: 345 * time.Second, // 86400/250=345.6.
			expectedMax: 346 * time.Second,
		},
		{
			name:        "seven_day_range",
			rangeDur:    7 * 24 * time.Hour,
			expectedMin: 2419 * time.Second, // 604800/250=2419.2.
			expectedMax: 2420 * time.Second,
		},
		{
			name:        "five_minute_range",
			rangeDur:    5 * time.Minute,
			expectedMin: 15 * time.Second, // 300/250=1.2, clamped to 15s.
			expectedMax: 15 * time.Second,
		},
		{
			name:        "zero_range",
			rangeDur:    0,
			expectedMin: 15 * time.Second,
			expectedMax: 15 * time.Second,
		},
		{
			name:        "negative_range",
			rangeDur:    -1 * time.Hour,
			expectedMin: 15 * time.Second,
			expectedMax: 15 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			end := time.Now()
			start := end.Add(-tt.rangeDur)

			step := calculateAutoStep(start, end)

			assert.GreaterOrEqual(t, step, tt.expectedMin, "step should be >= expected min")
			assert.LessOrEqual(t, step, tt.expectedMax, "step should be <= expected max")
		})
	}
}

// TestFormatPrometheusTime tests time formatting for the Prometheus API.
func TestFormatPrometheusTime(t *testing.T) {
	t.Parallel()

	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	result := formatPrometheusTime(ts)

	assert.Contains(t, result, "1704110400")
}

// TestFormatStep tests step duration formatting.
func TestFormatStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{
			name:     "fifteen_seconds",
			duration: 15 * time.Second,
			expected: "15s",
		},
		{
			name:     "one_minute",
			duration: 1 * time.Minute,
			expected: "60s",
		},
		{
			name:     "five_minutes",
			duration: 5 * time.Minute,
			expected: "300s",
		},
		{
			name:     "zero_duration",
			duration: 0,
			expected: "15s",
		},
		{
			name:     "negative_duration",
			duration: -1 * time.Second,
			expected: "15s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := formatStep(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestParseMatchersArg tests the matchers argument parsing.
func TestParseMatchersArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        map[string]interface{}
		expected    []string
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid_single_matcher",
			args: map[string]interface{}{
				"match": []interface{}{`{job="prometheus"}`},
			},
			expected:    []string{`{job="prometheus"}`},
			expectError: false,
		},
		{
			name: "valid_multiple_matchers",
			args: map[string]interface{}{
				"match": []interface{}{`{job="prometheus"}`, `{__name__=~"http_.*"}`},
			},
			expected:    []string{`{job="prometheus"}`, `{__name__=~"http_.*"}`},
			expectError: false,
		},
		{
			name:        "missing_match",
			args:        map[string]interface{}{},
			expectError: true,
			errorMsg:    "match parameter is required",
		},
		{
			name: "empty_match_array",
			args: map[string]interface{}{
				"match": []interface{}{},
			},
			expectError: true,
			errorMsg:    "match parameter is required",
		},
		{
			name: "match_with_empty_strings",
			args: map[string]interface{}{
				"match": []interface{}{"", ""},
			},
			expectError: true,
			errorMsg:    "match must contain at least one valid series selector",
		},
		{
			name: "wrong_type",
			args: map[string]interface{}{
				"match": "not-an-array",
			},
			expectError: true,
			errorMsg:    "match parameter is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := parseMatchersArg(tt.args)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestParsePrometheusResponse tests the common response parser.
func TestParsePrometheusResponse(t *testing.T) {
	t.Parallel()

	t.Run("vector_result", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{
			"status": "success",
			"data": {
				"resultType": "vector",
				"result": [
					{"metric": {"__name__": "up"}, "value": [1704067200, "1"]}
				]
			}
		}`)

		result, err := parsePrometheusResponse(body)

		require.NoError(t, err)
		assert.Equal(t, "success", result.Status)
		assert.Equal(t, "vector", result.ResultType)

		vectorResults, ok := result.Result.([]PrometheusVectorResult)
		require.True(t, ok)
		assert.Len(t, vectorResults, 1)
	})

	t.Run("matrix_result", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{
			"status": "success",
			"data": {
				"resultType": "matrix",
				"result": [
					{
						"metric": {"__name__": "up"},
						"values": [[1704067200, "1"], [1704067215, "1"]]
					}
				]
			}
		}`)

		result, err := parsePrometheusResponse(body)

		require.NoError(t, err)
		assert.Equal(t, "matrix", result.ResultType)

		matrixResults, ok := result.Result.([]PrometheusMatrixResult)
		require.True(t, ok)
		assert.Len(t, matrixResults, 1)
		assert.Len(t, matrixResults[0].Values, 2)
	})

	t.Run("error_response", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{
			"status": "error",
			"errorType": "bad_data",
			"error": "invalid query"
		}`)

		_, err := parsePrometheusResponse(body)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad_data")
		assert.Contains(t, err.Error(), "invalid query")
	})

	t.Run("invalid_json", func(t *testing.T) {
		t.Parallel()

		_, err := parsePrometheusResponse([]byte(`not json`))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshaling prometheus response")
	})

	t.Run("scalar_result", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{
			"status": "success",
			"data": {
				"resultType": "scalar",
				"result": [1704067200, "42"]
			}
		}`)

		result, err := parsePrometheusResponse(body)

		require.NoError(t, err)
		assert.Equal(t, "scalar", result.ResultType)
		// Scalar results are kept as raw JSON.
		assert.NotNil(t, result.Result)
	})
}

// TestGetPrometheusTools tests the tool definitions.
func TestGetPrometheusTools(t *testing.T) {
	t.Parallel()

	tools := getPrometheusTools()

	assert.Len(t, tools, 5)

	// Verify tool names.
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	assert.True(t, toolNames[toolPrometheusQuery])
	assert.True(t, toolNames[toolPrometheusQueryRange])
	assert.True(t, toolNames[toolPrometheusSeries])
	assert.True(t, toolNames[toolPrometheusLabelValues])
	assert.True(t, toolNames[toolPrometheusListEndpoints])

	// Verify each tool has required fields.
	for _, tool := range tools {
		assert.NotEmpty(t, tool.Name)
		assert.NotEmpty(t, tool.Description)
		assert.NotNil(t, tool.InputSchema)
	}
}

// TestServerResolvePrometheusClient tests endpoint resolution.
func TestServerResolvePrometheusClient(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("no_clients_configured", func(t *testing.T) {
		t.Parallel()

		server := &Server{
			prometheusClients: map[string]*PrometheusClient{},
			logger:            logger,
		}

		_, err := server.resolvePrometheusClient(map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no Prometheus endpoints configured")
	})

	t.Run("default_endpoint", func(t *testing.T) {
		t.Parallel()

		defaultClient, _ := NewPrometheusClient("http://localhost:9090", logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{
				"default": defaultClient,
			},
			logger: logger,
		}

		client, err := server.resolvePrometheusClient(map[string]interface{}{})

		require.NoError(t, err)
		assert.Equal(t, "http://localhost:9090", client.baseURL)
	})

	t.Run("named_endpoint", func(t *testing.T) {
		t.Parallel()

		prodClient, _ := NewPrometheusClient("http://prod-prometheus:9090", logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{
				"prod": prodClient,
			},
			logger: logger,
		}

		client, err := server.resolvePrometheusClient(map[string]interface{}{
			"endpoint": "prod",
		})

		require.NoError(t, err)
		assert.Equal(t, "http://prod-prometheus:9090", client.baseURL)
	})

	t.Run("endpoint_not_found", func(t *testing.T) {
		t.Parallel()

		defaultClient, _ := NewPrometheusClient("http://localhost:9090", logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{
				"default": defaultClient,
			},
			logger: logger,
		}

		_, err := server.resolvePrometheusClient(map[string]interface{}{
			"endpoint": "nonexistent",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not configured")
		assert.Contains(t, err.Error(), "Available endpoints")
	})

	t.Run("case_insensitive_lookup", func(t *testing.T) {
		t.Parallel()

		prodClient, _ := NewPrometheusClient("http://prod:9090", logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{
				"prod": prodClient,
			},
			logger: logger,
		}

		client, err := server.resolvePrometheusClient(map[string]interface{}{
			"endpoint": "PROD",
		})

		require.NoError(t, err)
		assert.Equal(t, "http://prod:9090", client.baseURL)
	})
}

// TestServerExecutePrometheusQuery tests the server's instant query execution.
func TestServerExecutePrometheusQuery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("missing_query", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		_, err := server.executePrometheusQuery(ctx, map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "query parameter is required")
	})

	t.Run("invalid_time", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		_, err := server.executePrometheusQuery(ctx, map[string]interface{}{
			"query": "up",
			"time":  "invalid-time",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing time")
	})

	t.Run("no_clients", func(t *testing.T) {
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{},
			logger:            logger,
		}

		_, err := server.executePrometheusQuery(ctx, map[string]interface{}{
			"query": "up",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no Prometheus endpoints configured")
	})

	t.Run("successful_query", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{
				"status": "success",
				"data": {
					"resultType": "vector",
					"result": [{"metric": {"__name__": "up"}, "value": [1704067200, "1"]}]
				}
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		result, err := server.executePrometheusQuery(ctx, map[string]interface{}{
			"query": "up",
		})

		require.NoError(t, err)
		assert.Contains(t, result, "success")
		assert.Contains(t, result, "vector")
	})
}

// TestServerExecutePrometheusQueryRange tests the server's range query execution.
func TestServerExecutePrometheusQueryRange(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("missing_query", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		_, err := server.executePrometheusQueryRange(ctx, map[string]interface{}{
			"start": "1h",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "query parameter is required")
	})

	t.Run("missing_start", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		_, err := server.executePrometheusQueryRange(ctx, map[string]interface{}{
			"query": "up",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start parameter is required")
	})

	t.Run("invalid_step", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		_, err := server.executePrometheusQueryRange(ctx, map[string]interface{}{
			"query": "up",
			"start": "1h",
			"step":  "invalid",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing step")
	})

	t.Run("successful_range_query", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{
				"status": "success",
				"data": {
					"resultType": "matrix",
					"result": [{
						"metric": {"__name__": "up"},
						"values": [[1704067200, "1"], [1704067215, "1"]]
					}]
				}
			}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		result, err := server.executePrometheusQueryRange(ctx, map[string]interface{}{
			"query": "up",
			"start": "1h",
			"step":  "15s",
		})

		require.NoError(t, err)
		assert.Contains(t, result, "matrix")
	})
}

// TestServerExecutePrometheusSeries tests the server's series execution.
func TestServerExecutePrometheusSeries(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("missing_match", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		_, err := server.executePrometheusSeries(ctx, map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "match parameter is required")
	})

	t.Run("successful_series", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{"status": "success", "data": [{"__name__": "up", "job": "test"}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		result, err := server.executePrometheusSeries(ctx, map[string]interface{}{
			"match": []interface{}{`{job="test"}`},
		})

		require.NoError(t, err)
		assert.Contains(t, result, "success")
	})
}

// TestServerExecutePrometheusLabelValues tests the server's label values execution.
func TestServerExecutePrometheusLabelValues(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("missing_label", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		_, err := server.executePrometheusLabelValues(ctx, map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "label parameter is required")
	})

	t.Run("successful_label_values", func(t *testing.T) {
		promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{"status": "success", "data": ["val1", "val2"]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(promServer.Close)

		promClient, _ := NewPrometheusClient(promServer.URL, logger)
		server := &Server{
			prometheusClients: map[string]*PrometheusClient{"default": promClient},
			logger:            logger,
		}

		result, err := server.executePrometheusLabelValues(ctx, map[string]interface{}{
			"label": "job",
		})

		require.NoError(t, err)
		assert.Contains(t, result, "val1")
		assert.Contains(t, result, "val2")
	})
}

// TestServerExecutePrometheusListEndpoints tests listing configured endpoints.
func TestServerExecutePrometheusListEndpoints(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("no_endpoints", func(t *testing.T) {
		t.Parallel()

		server := &Server{
			prometheusClients: map[string]*PrometheusClient{},
			logger:            logger,
		}

		_, err := server.executePrometheusListEndpoints(ctx, map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no Prometheus endpoints configured")
	})

	t.Run("multiple_endpoints", func(t *testing.T) {
		t.Parallel()

		defaultClient, _ := NewPrometheusClient("http://default:9090", logger)
		prodClient, _ := NewPrometheusClient("http://prod:9090", logger)

		server := &Server{
			prometheusClients: map[string]*PrometheusClient{
				"default": defaultClient,
				"prod":    prodClient,
			},
			logger: logger,
		}

		result, err := server.executePrometheusListEndpoints(ctx, map[string]interface{}{})

		require.NoError(t, err)

		// Parse result to verify structure.
		var endpoints []PrometheusEndpointInfo
		err = json.Unmarshal([]byte(result), &endpoints)
		require.NoError(t, err)
		assert.Len(t, endpoints, 2)

		// Build map for easier assertions.
		endpointMap := make(map[string]string)
		for _, ep := range endpoints {
			endpointMap[ep.Name] = ep.BaseURL
		}
		assert.Contains(t, endpointMap, "default")
		assert.Contains(t, endpointMap, "prod")
	})
}

// TestLoadPrometheusClients tests loading clients from environment variables.
func TestLoadPrometheusClients(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().

	t.Run("no_env_vars", func(t *testing.T) {
		t.Setenv("PROMETHEUS_URL", "")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadPrometheusClients(logger)

		assert.Empty(t, clients)
	})

	t.Run("default_url_only", func(t *testing.T) {
		t.Setenv("PROMETHEUS_URL", "http://localhost:9090")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadPrometheusClients(logger)

		assert.Len(t, clients, 1)
		assert.Contains(t, clients, "default")
		assert.Equal(t, "http://localhost:9090", clients["default"].baseURL)
	})

	t.Run("named_endpoints", func(t *testing.T) {
		t.Setenv("PROMETHEUS_URL", "http://default:9090")
		t.Setenv("PROMETHEUS_PROD_URL", "http://prod:9090")
		t.Setenv("PROMETHEUS_DEV_URL", "http://dev:9090")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadPrometheusClients(logger)

		assert.Len(t, clients, 3)
		assert.Contains(t, clients, "default")
		assert.Contains(t, clients, "prod")
		assert.Contains(t, clients, "dev")
		assert.Equal(t, "http://prod:9090", clients["prod"].baseURL)
		assert.Equal(t, "http://dev:9090", clients["dev"].baseURL)
	})

	t.Run("case_normalization", func(t *testing.T) {
		t.Setenv("PROMETHEUS_MYENDPOINT_URL", "http://myendpoint:9090")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadPrometheusClients(logger)

		_, ok := clients["myendpoint"]
		assert.True(t, ok, "endpoint name should be lowercased")
	})
}

// TestScanPrometheusEnvVars tests environment variable scanning.
func TestScanPrometheusEnvVars(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().

	t.Run("skips_default_url", func(t *testing.T) {
		t.Setenv("PROMETHEUS_URL", "http://default:9090")
		t.Setenv("PROMETHEUS_PROD_URL", "http://prod:9090")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := scanPrometheusEnvVars(logger)

		// Should only have prod, not default (handled separately).
		assert.Len(t, clients, 1)
		_, ok := clients["prod"]
		assert.True(t, ok)
		_, ok = clients["default"]
		assert.False(t, ok)
	})
}
