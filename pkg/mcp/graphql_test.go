package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewGraphQLClientIsolatesTransport guards the per-client Transport
// pattern. See pkg/mcp/tempo.go for the failure mode this prevents.
func TestNewGraphQLClientIsolatesTransport(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewGraphQLClient("default", "http://gql.example.com", nil, nil, logger)
	require.NoError(t, err)
	require.NotNil(t, client.httpClient.Transport, "client must have its own Transport; nil falls back to http.DefaultTransport")
}

// TestNewGraphQLClient tests GraphQL client initialization.
func TestNewGraphQLClient(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	tests := []struct {
		name      string
		url       string
		expectErr bool
		errorMsg  string
	}{
		{
			name:      "valid_url",
			url:       "https://api.example.com/graphql",
			expectErr: false,
		},
		{
			name:      "valid_url_with_trailing_slash",
			url:       "https://api.example.com/graphql/",
			expectErr: false,
		},
		{
			name:      "empty_url",
			url:       "",
			expectErr: true,
			errorMsg:  "URL is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewGraphQLClient("test", tt.url, nil, nil, logger)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, client)
				// Trailing slash should be trimmed.
				assert.False(t, strings.HasSuffix(client.url, "/"), "trailing slash should be trimmed")
			}
		})
	}
}

// TestGraphQLClientQuery tests the Query method.
func TestGraphQLClientQuery(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("successful_query_no_variables", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var reqBody GraphQLRequestBody
			err := json.NewDecoder(r.Body).Decode(&reqBody)
			assert.NoError(t, err)
			assert.Equal(t, "{ viewer { login } }", reqBody.Query)
			assert.Nil(t, reqBody.Variables)

			resp := `{"data":{"viewer":{"login":"testuser"}}}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewGraphQLClient("test", server.URL, nil, nil, logger)
		require.NoError(t, err)

		result, err := client.Query(ctx, "{ viewer { login } }", nil, "")

		require.NoError(t, err)
		assert.Contains(t, result, "testuser")
	})

	t.Run("successful_query_with_variables", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody GraphQLRequestBody
			err := json.NewDecoder(r.Body).Decode(&reqBody)
			assert.NoError(t, err)
			assert.Equal(t, "query($id: ID!){ node(id: $id) { id } }", reqBody.Query)
			assert.Equal(t, "abc123", reqBody.Variables["id"])

			resp := `{"data":{"node":{"id":"abc123"}}}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewGraphQLClient("test", server.URL, nil, nil, logger)
		require.NoError(t, err)

		vars := map[string]interface{}{"id": "abc123"}
		result, err := client.Query(ctx, "query($id: ID!){ node(id: $id) { id } }", vars, "")

		require.NoError(t, err)
		assert.Contains(t, result, "abc123")
	})

	t.Run("graphql_error_response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{"errors":[{"message":"Field 'foo' not found"}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		client, err := NewGraphQLClient("test", server.URL, nil, nil, logger)
		require.NoError(t, err)

		result, err := client.Query(ctx, "{ foo }", nil, "")

		require.NoError(t, err) // HTTP was 200, so no Go error.
		assert.Contains(t, result, "GraphQL errors")
		assert.Contains(t, result, "Field 'foo' not found")
	})

	t.Run("http_error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`Internal Server Error`))
		}))
		t.Cleanup(server.Close)

		client, err := NewGraphQLClient("test", server.URL, nil, nil, logger)
		require.NoError(t, err)

		_, err = client.Query(ctx, "{ viewer { login } }", nil, "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 500")
	})

	t.Run("context_cancellation", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulate slow response; context should cancel before response.
			<-r.Context().Done()
		}))
		t.Cleanup(server.Close)

		client, err := NewGraphQLClient("test", server.URL, nil, nil, logger)
		require.NoError(t, err)

		cancelCtx, cancel := context.WithCancel(ctx)
		cancel() // Cancel immediately.

		_, err = client.Query(cancelCtx, "{ viewer { login } }", nil, "")

		require.Error(t, err)
	})

	t.Run("response_truncation", func(t *testing.T) {
		t.Parallel()

		// Generate a large response.
		largeData := strings.Repeat("x", graphqlMaxResponseBytes+1000)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(largeData))
		}))
		t.Cleanup(server.Close)

		client, err := NewGraphQLClient("test", server.URL, nil, nil, logger)
		require.NoError(t, err)

		result, err := client.Query(ctx, "{ big }", nil, "")

		require.NoError(t, err)
		assert.Contains(t, result, "[Response truncated")
	})

	t.Run("bearer_token_sent", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer mytoken123", r.Header.Get("Authorization"))

			resp := `{"data":{"ok":true}}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		headers := map[string]string{"Authorization": "Bearer mytoken123"}
		client, err := NewGraphQLClient("test", server.URL, headers, nil, logger)
		require.NoError(t, err)

		_, err = client.Query(ctx, "{ ok }", nil, "")

		require.NoError(t, err)
	})

	t.Run("custom_headers_sent", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "my-custom-value", r.Header.Get("X-Custom-Header"))

			resp := `{"data":{"ok":true}}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(server.Close)

		headers := map[string]string{"X-Custom-Header": "my-custom-value"}
		client, err := NewGraphQLClient("test", server.URL, headers, nil, logger)
		require.NoError(t, err)

		_, err = client.Query(ctx, "{ ok }", nil, "")

		require.NoError(t, err)
	})
}

// TestResolveGraphQLClient tests endpoint resolution.
func TestResolveGraphQLClient(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("no_clients_configured", func(t *testing.T) {
		t.Parallel()

		server := &Server{
			graphqlClients: map[string]*GraphQLClient{},
			logger:         logger,
		}

		_, err := server.resolveGraphQLClient(map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no GraphQL endpoints configured")
	})

	t.Run("default_endpoint", func(t *testing.T) {
		t.Parallel()

		defaultClient, _ := NewGraphQLClient("default", "https://api.example.com/graphql", nil, nil, logger)
		server := &Server{
			graphqlClients: map[string]*GraphQLClient{
				"default": defaultClient,
			},
			logger: logger,
		}

		client, err := server.resolveGraphQLClient(map[string]interface{}{})

		require.NoError(t, err)
		assert.Equal(t, "https://api.example.com/graphql", client.url)
	})

	t.Run("named_endpoint", func(t *testing.T) {
		t.Parallel()

		wizClient, _ := NewGraphQLClient("wiz", "https://api.us1.app.wiz.io/graphql", nil, nil, logger)
		server := &Server{
			graphqlClients: map[string]*GraphQLClient{
				"wiz": wizClient,
			},
			logger: logger,
		}

		client, err := server.resolveGraphQLClient(map[string]interface{}{
			"endpoint": "wiz",
		})

		require.NoError(t, err)
		assert.Equal(t, "https://api.us1.app.wiz.io/graphql", client.url)
	})

	t.Run("endpoint_not_found", func(t *testing.T) {
		t.Parallel()

		defaultClient, _ := NewGraphQLClient("default", "https://api.example.com/graphql", nil, nil, logger)
		server := &Server{
			graphqlClients: map[string]*GraphQLClient{
				"default": defaultClient,
			},
			logger: logger,
		}

		_, err := server.resolveGraphQLClient(map[string]interface{}{
			"endpoint": "nonexistent",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not configured")
		assert.Contains(t, err.Error(), "Available endpoints")
	})

	t.Run("case_insensitive_lookup", func(t *testing.T) {
		t.Parallel()

		wizClient, _ := NewGraphQLClient("wiz", "https://api.wiz.io/graphql", nil, nil, logger)
		server := &Server{
			graphqlClients: map[string]*GraphQLClient{
				"wiz": wizClient,
			},
			logger: logger,
		}

		client, err := server.resolveGraphQLClient(map[string]interface{}{
			"endpoint": "WIZ",
		})

		require.NoError(t, err)
		assert.Equal(t, "https://api.wiz.io/graphql", client.url)
	})
}

// TestExecuteGraphQLQuery tests the server's GraphQL query execution.
func TestExecuteGraphQLQuery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("missing_query", func(t *testing.T) {
		gqlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(gqlServer.Close)

		gqlClient, _ := NewGraphQLClient("default", gqlServer.URL, nil, nil, logger)
		server := &Server{
			graphqlClients: map[string]*GraphQLClient{"default": gqlClient},
			logger:         logger,
		}

		_, err := server.executeGraphQLQuery(ctx, map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "query parameter is required")
	})

	t.Run("no_clients", func(t *testing.T) {
		server := &Server{
			graphqlClients: map[string]*GraphQLClient{},
			logger:         logger,
		}

		_, err := server.executeGraphQLQuery(ctx, map[string]interface{}{
			"query": "{ viewer { login } }",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no GraphQL endpoints configured")
	})

	t.Run("successful_query", func(t *testing.T) {
		gqlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := `{"data":{"viewer":{"login":"testuser"}}}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		}))
		t.Cleanup(gqlServer.Close)

		gqlClient, _ := NewGraphQLClient("default", gqlServer.URL, nil, nil, logger)
		server := &Server{
			graphqlClients: map[string]*GraphQLClient{"default": gqlClient},
			logger:         logger,
		}

		result, err := server.executeGraphQLQuery(ctx, map[string]interface{}{
			"query": "{ viewer { login } }",
		})

		require.NoError(t, err)
		assert.Contains(t, result, "testuser")
	})
}

// TestExecuteGraphQLListEndpoints tests listing configured GraphQL endpoints.
func TestExecuteGraphQLListEndpoints(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("no_endpoints", func(t *testing.T) {
		t.Parallel()

		server := &Server{
			graphqlClients: map[string]*GraphQLClient{},
			logger:         logger,
		}

		_, err := server.executeGraphQLListEndpoints(ctx, map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no GraphQL endpoints configured")
	})

	t.Run("multiple_endpoints", func(t *testing.T) {
		t.Parallel()

		defaultClient, _ := NewGraphQLClient("default", "https://api.example.com/graphql", nil, nil, logger)
		wizClient, _ := NewGraphQLClient("wiz", "https://api.wiz.io/graphql", nil, nil, logger)

		server := &Server{
			graphqlClients: map[string]*GraphQLClient{
				"default": defaultClient,
				"wiz":     wizClient,
			},
			logger: logger,
		}

		result, err := server.executeGraphQLListEndpoints(ctx, map[string]interface{}{})

		require.NoError(t, err)

		// Parse result to verify structure.
		var endpoints []GraphQLEndpointInfo
		err = json.Unmarshal([]byte(result), &endpoints)
		require.NoError(t, err)
		assert.Len(t, endpoints, 2)

		// Build map for easier assertions.
		endpointMap := make(map[string]string)
		for _, ep := range endpoints {
			endpointMap[ep.Name] = ep.URL
		}
		assert.Contains(t, endpointMap, "default")
		assert.Contains(t, endpointMap, "wiz")
	})
}

// TestGetGraphQLTools tests the tool definitions.
func TestGetGraphQLTools(t *testing.T) {
	t.Parallel()

	tools := getGraphQLTools()

	assert.Len(t, tools, 2)

	// Verify tool names.
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	assert.True(t, toolNames[toolGraphQLQuery])
	assert.True(t, toolNames[toolGraphQLListEndpoints])

	// Verify each tool has required fields.
	for _, tool := range tools {
		assert.NotEmpty(t, tool.Name)
		assert.NotEmpty(t, tool.Description)
		assert.NotNil(t, tool.InputSchema)
	}
}

// TestLoadGraphQLClients tests loading clients from environment variables.
func TestLoadGraphQLClients(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().

	t.Run("no_env_vars", func(t *testing.T) {
		t.Setenv("GRAPHQL_URL", "")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadGraphQLClients(logger)

		assert.Empty(t, clients)
	})

	t.Run("default_url_only", func(t *testing.T) {
		t.Setenv("GRAPHQL_URL", "https://api.example.com/graphql")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadGraphQLClients(logger)

		assert.Len(t, clients, 1)
		assert.Contains(t, clients, "default")
		assert.Equal(t, "https://api.example.com/graphql", clients["default"].url)
	})

	t.Run("default_url_with_token", func(t *testing.T) {
		t.Setenv("GRAPHQL_URL", "https://api.example.com/graphql")
		t.Setenv("GRAPHQL_TOKEN", "mytoken123")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadGraphQLClients(logger)

		assert.Len(t, clients, 1)
		assert.Contains(t, clients, "default")
		assert.Equal(t, "Bearer mytoken123", clients["default"].headers["Authorization"])
	})

	t.Run("named_endpoints", func(t *testing.T) {
		t.Setenv("GRAPHQL_URL", "https://default.example.com/graphql")
		t.Setenv("GRAPHQL_WIZ_URL", "https://api.wiz.io/graphql")
		t.Setenv("GRAPHQL_WIZ_TOKEN", "wiztoken")
		t.Setenv("GRAPHQL_HASURA_URL", "https://hasura.example.com/v1/graphql")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadGraphQLClients(logger)

		assert.Len(t, clients, 3)
		assert.Contains(t, clients, "default")
		assert.Contains(t, clients, "wiz")
		assert.Contains(t, clients, "hasura")
		assert.Equal(t, "https://api.wiz.io/graphql", clients["wiz"].url)
		assert.Equal(t, "Bearer wiztoken", clients["wiz"].headers["Authorization"])
	})

	t.Run("case_normalization", func(t *testing.T) {
		t.Setenv("GRAPHQL_MYENDPOINT_URL", "https://my.endpoint.com/graphql")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadGraphQLClients(logger)

		_, ok := clients["myendpoint"]
		assert.True(t, ok, "endpoint name should be lowercased")
	})
}

// TestScanGraphQLEnvVars tests environment variable scanning.
func TestScanGraphQLEnvVars(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().

	t.Run("skips_default_url", func(t *testing.T) {
		t.Setenv("GRAPHQL_URL", "https://default.example.com/graphql")
		t.Setenv("GRAPHQL_WIZ_URL", "https://api.wiz.io/graphql")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := scanGraphQLEnvVars(logger)

		// Should only have wiz, not default (handled separately).
		assert.Len(t, clients, 1)
		_, ok := clients["wiz"]
		assert.True(t, ok)
		_, ok = clients["default"]
		assert.False(t, ok)
	})

	t.Run("collects_named_endpoints", func(t *testing.T) {
		t.Setenv("GRAPHQL_ALPHA_URL", "https://alpha.example.com/graphql")
		t.Setenv("GRAPHQL_BETA_URL", "https://beta.example.com/graphql")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := scanGraphQLEnvVars(logger)

		assert.Len(t, clients, 2)
		assert.Contains(t, clients, "alpha")
		assert.Contains(t, clients, "beta")
	})

	t.Run("excludes_auth_url_vars", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_URL", "https://api.wiz.io/graphql")
		t.Setenv("GRAPHQL_WIZ_AUTH_URL", "https://auth.wiz.io/oauth/token")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := scanGraphQLEnvVars(logger)

		// Should have wiz but NOT wiz_auth.
		assert.Len(t, clients, 1)
		_, ok := clients["wiz"]
		assert.True(t, ok)
		_, ok = clients["wiz_auth"]
		assert.False(t, ok, "GRAPHQL_WIZ_AUTH_URL should not create a wiz_auth endpoint")
	})
}

// TestCollectGraphQLHeaders tests header collection from environment variables.
func TestCollectGraphQLHeaders(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().

	t.Run("bearer_token", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_TOKEN", "wiztoken123")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		headers := collectGraphQLHeaders("wiz", logger)

		assert.Equal(t, "Bearer wiztoken123", headers["Authorization"])
	})

	t.Run("custom_headers", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_HEADER_X_CUSTOM_KEY", "custom-value")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		headers := collectGraphQLHeaders("wiz", logger)

		assert.Equal(t, "custom-value", headers["x-custom-key"])
	})

	t.Run("underscore_to_hyphen", func(t *testing.T) {
		t.Setenv("GRAPHQL_API_HEADER_CONTENT_TYPE", "application/json")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		headers := collectGraphQLHeaders("api", logger)

		assert.Equal(t, "application/json", headers["content-type"])
	})

	t.Run("default_endpoint_headers", func(t *testing.T) {
		t.Setenv("GRAPHQL_TOKEN", "defaulttoken")
		t.Setenv("GRAPHQL_HEADER_X_API_KEY", "api-key-123")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		headers := collectGraphQLHeaders("", logger)

		assert.Equal(t, "Bearer defaulttoken", headers["Authorization"])
		assert.Equal(t, "api-key-123", headers["x-api-key"])
	})
}

// TestTruncateResponse tests response truncation.
func TestTruncateResponse(t *testing.T) {
	t.Parallel()

	t.Run("under_limit", func(t *testing.T) {
		t.Parallel()

		body := []byte("short response")
		result := truncateResponse(body)
		assert.Equal(t, "short response", result)
	})

	t.Run("at_limit", func(t *testing.T) {
		t.Parallel()

		body := []byte(strings.Repeat("x", graphqlMaxResponseBytes))
		result := truncateResponse(body)
		assert.Len(t, result, graphqlMaxResponseBytes)
		assert.NotContains(t, result, "truncated")
	})

	t.Run("over_limit", func(t *testing.T) {
		t.Parallel()

		body := []byte(strings.Repeat("x", graphqlMaxResponseBytes+1000))
		result := truncateResponse(body)
		assert.Contains(t, result, "[Response truncated")
	})
}

// TestFormatGraphQLResponse tests response formatting.
func TestFormatGraphQLResponse(t *testing.T) {
	t.Parallel()

	t.Run("data_only", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{"data":{"viewer":{"login":"testuser"}}}`)
		result := formatGraphQLResponse(body)
		assert.Contains(t, result, "testuser")
		assert.NotContains(t, result, "GraphQL errors")
	})

	t.Run("errors_only", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{"errors":[{"message":"Not authorized"}]}`)
		result := formatGraphQLResponse(body)
		assert.Contains(t, result, "GraphQL errors")
		assert.Contains(t, result, "Not authorized")
	})

	t.Run("errors_with_data", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{"data":{"partial":"result"},"errors":[{"message":"Partial failure"}]}`)
		result := formatGraphQLResponse(body)
		assert.Contains(t, result, "GraphQL errors")
		assert.Contains(t, result, "Partial failure")
		assert.Contains(t, result, "Data:")
	})

	t.Run("invalid_json", func(t *testing.T) {
		t.Parallel()

		body := []byte(`not json at all`)
		result := formatGraphQLResponse(body)
		assert.Equal(t, "not json at all", result)
	})
}

// newTestTokenServer creates a mock OAuth2 token server that returns a configurable token response.
func newTestTokenServer(t *testing.T, token string, expiresIn int, statusCode int) (server *httptest.Server) {
	t.Helper()

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)

		resp := fmt.Sprintf(`{"access_token":"%s","token_type":"Bearer","expires_in":%d}`, token, expiresIn)
		_, _ = w.Write([]byte(resp))
	}))

	t.Cleanup(server.Close)

	return server
}

// TestEnsureAuth tests the OAuth2 token refresh gating logic.
func TestEnsureAuth(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("nil_config_is_noop", func(t *testing.T) {
		t.Parallel()

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, nil, logger)
		require.NoError(t, err)

		err = client.ensureAuth(context.Background())

		require.NoError(t, err)
		// No Authorization header should be set.
		_, hasAuth := client.headers["Authorization"]
		assert.False(t, hasAuth)
	})

	t.Run("valid_cached_token_skips_fetch", func(t *testing.T) {
		t.Parallel()

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, &GraphQLOAuth2Config{
			TokenURL:     "https://should-not-be-called.example.com",
			ClientID:     "id",
			ClientSecret: "secret",
		}, logger)
		require.NoError(t, err)

		// Pre-populate cache with a valid token.
		client.tokenCache = &graphqlTokenCache{
			token:  "cached-token",
			expiry: time.Now().Add(10 * time.Minute),
		}
		client.headers["Authorization"] = "Bearer cached-token"

		err = client.ensureAuth(context.Background())

		require.NoError(t, err)
		assert.Equal(t, "Bearer cached-token", client.headers["Authorization"])
	})

	t.Run("expired_token_triggers_fetch", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTestTokenServer(t, "new-token", 3600, http.StatusOK)

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, &GraphQLOAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "id",
			ClientSecret: "secret",
		}, logger)
		require.NoError(t, err)

		// Set expired cache.
		client.tokenCache = &graphqlTokenCache{
			token:  "old-token",
			expiry: time.Now().Add(-1 * time.Minute),
		}

		err = client.ensureAuth(context.Background())

		require.NoError(t, err)
		assert.Equal(t, "Bearer new-token", client.headers["Authorization"])
	})
}

// TestFetchOAuth2Token tests the token exchange with a mock OAuth2 server.
func TestFetchOAuth2Token(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("successful_exchange", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTestTokenServer(t, "test-access-token", 3600, http.StatusOK)

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, &GraphQLOAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "my-client-id",
			ClientSecret: "my-client-secret",
		}, logger)
		require.NoError(t, err)

		client.tokenMu.Lock()
		err = client.fetchOAuth2Token(context.Background())
		client.tokenMu.Unlock()

		require.NoError(t, err)
		assert.Equal(t, "Bearer test-access-token", client.headers["Authorization"])
		assert.NotNil(t, client.tokenCache)
		assert.Equal(t, "test-access-token", client.tokenCache.token)
		assert.True(t, client.tokenCache.expiry.After(time.Now()))
	})

	t.Run("http_error", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTestTokenServer(t, "", 0, http.StatusUnauthorized)

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, &GraphQLOAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "bad-id",
			ClientSecret: "bad-secret",
		}, logger)
		require.NoError(t, err)

		client.tokenMu.Lock()
		err = client.fetchOAuth2Token(context.Background())
		client.tokenMu.Unlock()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 401")
	})

	t.Run("missing_access_token", func(t *testing.T) {
		t.Parallel()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":3600}`))
		}))
		t.Cleanup(tokenServer.Close)

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, &GraphQLOAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "id",
			ClientSecret: "secret",
		}, logger)
		require.NoError(t, err)

		client.tokenMu.Lock()
		err = client.fetchOAuth2Token(context.Background())
		client.tokenMu.Unlock()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing access_token")
	})

	t.Run("audience_included_in_form", func(t *testing.T) {
		t.Parallel()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parseErr := r.ParseForm()
			assert.NoError(t, parseErr)
			assert.Equal(t, "wiz-api", r.FormValue("audience"))
			assert.Equal(t, "client_credentials", r.FormValue("grant_type"))
			assert.Equal(t, "my-id", r.FormValue("client_id"))
			assert.Equal(t, "my-secret", r.FormValue("client_secret"))

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
		}))
		t.Cleanup(tokenServer.Close)

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, &GraphQLOAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "my-id",
			ClientSecret: "my-secret",
			Audience:     "wiz-api",
		}, logger)
		require.NoError(t, err)

		client.tokenMu.Lock()
		err = client.fetchOAuth2Token(context.Background())
		client.tokenMu.Unlock()

		require.NoError(t, err)
	})
}

// TestCollectGraphQLOAuth2Config tests OAuth2 env var scanning.
func TestCollectGraphQLOAuth2Config(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().

	t.Run("all_vars_set", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_AUTH_URL", "https://auth.wiz.io/oauth/token")
		t.Setenv("GRAPHQL_WIZ_CLIENT_ID", "wiz-id")
		t.Setenv("GRAPHQL_WIZ_CLIENT_SECRET", "wiz-secret")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		config := collectGraphQLOAuth2Config("wiz", logger)

		require.NotNil(t, config)
		assert.Equal(t, "https://auth.wiz.io/oauth/token", config.TokenURL)
		assert.Equal(t, "wiz-id", config.ClientID)
		assert.Equal(t, "wiz-secret", config.ClientSecret)
		assert.Empty(t, config.Audience)
	})

	t.Run("missing_auth_url", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_AUTH_URL", "")
		t.Setenv("GRAPHQL_WIZ_CLIENT_ID", "wiz-id")
		t.Setenv("GRAPHQL_WIZ_CLIENT_SECRET", "wiz-secret")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		config := collectGraphQLOAuth2Config("wiz", logger)

		assert.Nil(t, config)
	})

	t.Run("missing_client_id", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_AUTH_URL", "https://auth.wiz.io/oauth/token")
		t.Setenv("GRAPHQL_WIZ_CLIENT_ID", "")
		t.Setenv("GRAPHQL_WIZ_CLIENT_SECRET", "wiz-secret")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		config := collectGraphQLOAuth2Config("wiz", logger)

		assert.Nil(t, config)
	})

	t.Run("with_audience", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_AUTH_URL", "https://auth.wiz.io/oauth/token")
		t.Setenv("GRAPHQL_WIZ_CLIENT_ID", "wiz-id")
		t.Setenv("GRAPHQL_WIZ_CLIENT_SECRET", "wiz-secret")
		t.Setenv("GRAPHQL_WIZ_AUDIENCE", "wiz-api")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		config := collectGraphQLOAuth2Config("wiz", logger)

		require.NotNil(t, config)
		assert.Equal(t, "wiz-api", config.Audience)
	})

	t.Run("default_endpoint_empty_name", func(t *testing.T) {
		t.Setenv("GRAPHQL_AUTH_URL", "https://auth.default.io/oauth/token")
		t.Setenv("GRAPHQL_CLIENT_ID", "default-id")
		t.Setenv("GRAPHQL_CLIENT_SECRET", "default-secret")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		config := collectGraphQLOAuth2Config("", logger)

		require.NotNil(t, config)
		assert.Equal(t, "https://auth.default.io/oauth/token", config.TokenURL)
		assert.Equal(t, "default-id", config.ClientID)
		assert.Equal(t, "default-secret", config.ClientSecret)
	})
}

// TestLoadGraphQLClientsWithOAuth2 tests that OAuth2 env vars create a client with oauth2Config.
func TestLoadGraphQLClientsWithOAuth2(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().

	t.Run("oauth2_config_set", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_URL", "https://api.wiz.io/graphql")
		t.Setenv("GRAPHQL_WIZ_AUTH_URL", "https://auth.wiz.io/oauth/token")
		t.Setenv("GRAPHQL_WIZ_CLIENT_ID", "wiz-id")
		t.Setenv("GRAPHQL_WIZ_CLIENT_SECRET", "wiz-secret")
		t.Setenv("GRAPHQL_WIZ_AUDIENCE", "wiz-api")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadGraphQLClients(logger)

		require.Contains(t, clients, "wiz")
		assert.NotNil(t, clients["wiz"].oauth2Config)
		assert.Equal(t, "https://auth.wiz.io/oauth/token", clients["wiz"].oauth2Config.TokenURL)
		assert.Equal(t, "wiz-id", clients["wiz"].oauth2Config.ClientID)
		assert.Equal(t, "wiz-secret", clients["wiz"].oauth2Config.ClientSecret)
		assert.Equal(t, "wiz-api", clients["wiz"].oauth2Config.Audience)
	})

	t.Run("no_oauth2_config_when_vars_missing", func(t *testing.T) {
		t.Setenv("GRAPHQL_WIZ_URL", "https://api.wiz.io/graphql")
		// Explicitly clear OAuth2 vars.
		t.Setenv("GRAPHQL_WIZ_AUTH_URL", "")
		t.Setenv("GRAPHQL_WIZ_CLIENT_ID", "")
		t.Setenv("GRAPHQL_WIZ_CLIENT_SECRET", "")

		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

		clients := LoadGraphQLClients(logger)

		require.Contains(t, clients, "wiz")
		assert.Nil(t, clients["wiz"].oauth2Config)
	})
}

// TestGraphQLClientQueryWithOAuth2 tests end-to-end: mock token server + mock graphql server.
func TestGraphQLClientQueryWithOAuth2(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("first_query_triggers_token_fetch", func(t *testing.T) {
		t.Parallel()

		tokenFetchCount := 0

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			tokenFetchCount++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"oauth-token-123","token_type":"Bearer","expires_in":3600}`))
		}))
		t.Cleanup(tokenServer.Close)

		gqlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer oauth-token-123", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
		}))
		t.Cleanup(gqlServer.Close)

		client, err := NewGraphQLClient("test", gqlServer.URL, nil, &GraphQLOAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "id",
			ClientSecret: "secret",
		}, logger)
		require.NoError(t, err)

		// First query — should trigger token fetch.
		result, err := client.Query(ctx, "{ ok }", nil, "")
		require.NoError(t, err)
		assert.Contains(t, result, "ok")
		assert.Equal(t, 1, tokenFetchCount)

		// Second query — should reuse cached token.
		result, err = client.Query(ctx, "{ ok }", nil, "")
		require.NoError(t, err)
		assert.Contains(t, result, "ok")
		assert.Equal(t, 1, tokenFetchCount, "second query should not fetch a new token")
	})

	t.Run("token_fetch_failure_propagates", func(t *testing.T) {
		t.Parallel()

		tokenServer := newTestTokenServer(t, "", 0, http.StatusForbidden)

		client, err := NewGraphQLClient("test", "https://example.com/graphql", nil, &GraphQLOAuth2Config{
			TokenURL:     tokenServer.URL,
			ClientID:     "id",
			ClientSecret: "secret",
		}, logger)
		require.NoError(t, err)

		_, err = client.Query(ctx, "{ ok }", nil, "")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 403")
	})
}
