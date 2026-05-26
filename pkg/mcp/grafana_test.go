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

func TestNewGrafanaClient(t *testing.T) {
	// Cannot run in parallel due to shared logger

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tests := []struct {
		name      string
		baseURL   string
		apiKey    string
		expectErr bool
		errorMsg  string
	}{
		{
			name:      "valid_config",
			baseURL:   "http://grafana.example.com",
			apiKey:    "test-api-key",
			expectErr: false,
		},
		{
			name:      "missing_base_url",
			baseURL:   "",
			apiKey:    "test-api-key",
			expectErr: true,
			errorMsg:  "base URL is required",
		},
		{
			name:      "missing_api_key",
			baseURL:   "http://grafana.example.com",
			apiKey:    "",
			expectErr: true,
			errorMsg:  "API key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel due to shared resources

			client, err := NewGrafanaClient(tt.baseURL, tt.apiKey, logger)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, client)
				assert.Equal(t, tt.baseURL, client.baseURL)
				assert.Equal(t, tt.apiKey, client.apiKey)
			}
		})
	}
}

func TestGrafanaClientListDashboards(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	tests := []struct {
		name           string
		serverResponse string
		serverStatus   int
		expectErr      bool
		expectedCount  int
	}{
		{
			name: "successful_list",
			serverResponse: `[
				{"id": 1, "uid": "dash1", "title": "Dashboard 1", "type": "dash-db"},
				{"id": 2, "uid": "dash2", "title": "Dashboard 2", "type": "dash-db"}
			]`,
			serverStatus:  http.StatusOK,
			expectErr:     false,
			expectedCount: 2,
		},
		{
			name:           "empty_list",
			serverResponse: `[]`,
			serverStatus:   http.StatusOK,
			expectErr:      false,
			expectedCount:  0,
		},
		{
			name:           "server_error",
			serverResponse: `{"message": "Internal server error"}`,
			serverStatus:   http.StatusInternalServerError,
			expectErr:      true,
		},
		{
			name:           "invalid_json",
			serverResponse: `invalid json`,
			serverStatus:   http.StatusOK,
			expectErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel due to shared resources

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/search", r.URL.Path)
				assert.Equal(t, "type=dash-db", r.URL.RawQuery)
				assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			t.Cleanup(server.Close)

			client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
			require.NoError(t, err)

			dashboards, err := client.ListDashboards(ctx)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, dashboards, tt.expectedCount)
			}
		})
	}
}

func TestGrafanaClientGetDashboard(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	tests := []struct {
		name           string
		uid            string
		serverResponse string
		serverStatus   int
		expectErr      bool
		expectedTitle  string
	}{
		{
			name: "successful_get",
			uid:  "test-uid",
			serverResponse: `{
				"dashboard": {
					"uid": "test-uid",
					"title": "Test Dashboard",
					"version": 1,
					"panels": []
				},
				"meta": {
					"version": 1,
					"slug": "test-dashboard",
					"folderUid": "test-folder"
				}
			}`,
			serverStatus:  http.StatusOK,
			expectErr:     false,
			expectedTitle: "Test Dashboard",
		},
		{
			name:           "dashboard_not_found",
			uid:            "nonexistent",
			serverResponse: `{"message": "Dashboard not found"}`,
			serverStatus:   http.StatusNotFound,
			expectErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel due to shared resources

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/api/dashboards/uid/" + tt.uid
				assert.Equal(t, expectedPath, r.URL.Path)

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			t.Cleanup(server.Close)

			client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
			require.NoError(t, err)

			resp, err := client.GetDashboard(ctx, tt.uid)

			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, resp)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, tt.expectedTitle, resp.Title)
				assert.Equal(t, "test-folder", resp.FolderUID)
			}
		})
	}
}

func TestGrafanaClientCreateDashboard(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	dashboard := &Dashboard{
		Title: "New Dashboard",
		Tags:  []string{"test"},
		Panels: []Panel{
			{
				ID:    1,
				Type:  "stat",
				Title: "Test Panel",
			},
		},
	}

	tests := []struct {
		name           string
		serverResponse string
		serverStatus   int
		expectErr      bool
		expectedUID    string
	}{
		{
			name: "successful_create",
			serverResponse: `{
				"id": 123,
				"uid": "new-dash-uid",
				"url": "/d/new-dash-uid/new-dashboard",
				"status": "success",
				"version": 1
			}`,
			serverStatus: http.StatusOK,
			expectErr:    false,
			expectedUID:  "new-dash-uid",
		},
		{
			name:           "permission_denied",
			serverResponse: `{"message": "Permission denied"}`,
			serverStatus:   http.StatusForbidden,
			expectErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel due to shared resources

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/dashboards/db", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)

				// Verify request body
				var req DashboardSaveRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				assert.NoError(t, err)
				assert.Equal(t, dashboard.Title, req.Dashboard.Title)
				assert.False(t, req.Overwrite)

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			t.Cleanup(server.Close)

			client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
			require.NoError(t, err)

			uid, err := client.CreateDashboard(ctx, dashboard, "", "Test message")

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedUID, uid)
			}
		})
	}
}

func TestGrafanaClientUpdateDashboard(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	// Raw bytes mirror what executeGrafanaUpdateDashboard now sends — the
	// caller's dashboard payload marshaled directly, with no typed
	// round-trip in between.
	dashboardBytes := []byte(`{"uid":"test-uid","title":"Updated Dashboard","panels":[{"id":1,"type":"stat","title":"Panel 1"}]}`)

	tests := []struct {
		name           string
		folderUID      string
		serverStatus   int
		serverResponse string
		expectErr      bool
	}{
		{
			name:           "successful_update_preserves_folder",
			folderUID:      "wiz-folder-uid",
			serverStatus:   http.StatusOK,
			serverResponse: `{"id": 1, "uid": "test-uid", "status": "success", "version": 2}`,
			expectErr:      false,
		},
		{
			name:           "successful_update_empty_folder",
			folderUID:      "",
			serverStatus:   http.StatusOK,
			serverResponse: `{"id": 1, "uid": "test-uid", "status": "success", "version": 2}`,
			expectErr:      false,
		},
		{
			name:           "server_error",
			folderUID:      "wiz-folder-uid",
			serverStatus:   http.StatusInternalServerError,
			serverResponse: `{"message": "Internal error"}`,
			expectErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel due to shared resources

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/dashboards/db", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)

				// Verify request body: dashboard bytes reach Grafana
				// verbatim, overwrite is set, folderUid round-trips.
				var req dashboardSaveRequestRaw
				err := json.NewDecoder(r.Body).Decode(&req)
				assert.NoError(t, err)
				assert.JSONEq(t, string(dashboardBytes), string(req.Dashboard))
				assert.True(t, req.Overwrite)
				assert.Equal(t, tt.folderUID, req.FolderUID)

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			t.Cleanup(server.Close)

			client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
			require.NoError(t, err)

			err = client.UpdateDashboard(ctx, dashboardBytes, tt.folderUID, "Test update")

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGrafanaClientDeleteDashboard(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	tests := []struct {
		name         string
		uid          string
		serverStatus int
		expectErr    bool
	}{
		{
			name:         "successful_delete",
			uid:          "test-uid",
			serverStatus: http.StatusOK,
			expectErr:    false,
		},
		{
			name:         "dashboard_not_found",
			uid:          "nonexistent",
			serverStatus: http.StatusNotFound,
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run in parallel due to shared resources

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/api/dashboards/uid/" + tt.uid
				assert.Equal(t, expectedPath, r.URL.Path)
				assert.Equal(t, http.MethodDelete, r.Method)

				w.WriteHeader(tt.serverStatus)
				if tt.serverStatus != http.StatusOK {
					_, _ = w.Write([]byte(`{"message": "Error"}`))
				}
			}))
			t.Cleanup(server.Close)

			client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
			require.NoError(t, err)

			err = client.DeleteDashboard(ctx, tt.uid)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGrafanaClientCreateDashboardFromSQL(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	queries := []SQLPanelConfig{
		{
			Title:       "User Count",
			SQL:         "SELECT COUNT(*) FROM users",
			PanelType:   "stat",
			Description: "Total number of users",
		},
		{
			Title:     "Daily Signups",
			SQL:       "SELECT date_trunc('day', created_at) as time, COUNT(*) FROM users GROUP BY 1 ORDER BY 1",
			PanelType: "timeseries",
		},
		{
			Title:     "Top Users",
			SQL:       "SELECT name, email FROM users ORDER BY created_at DESC LIMIT 10",
			PanelType: "table",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/dashboards/db", r.URL.Path)

		var req DashboardSaveRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)

		// Verify dashboard structure
		assert.Equal(t, "Test Dashboard", req.Dashboard.Title)
		assert.Len(t, req.Dashboard.Panels, 3)
		assert.Contains(t, req.Dashboard.Tags, "auto-generated")
		assert.Contains(t, req.Dashboard.Tags, "sql")

		// Check first panel (stat)
		statPanel := req.Dashboard.Panels[0]
		assert.Equal(t, "stat", statPanel.Type)
		assert.Equal(t, "User Count", statPanel.Title)
		assert.Equal(t, 1, statPanel.ID)
		assert.Equal(t, "SELECT COUNT(*) FROM users", statPanel.Targets[0].RawSQL)

		// Check grid positioning
		assert.Equal(t, 0, statPanel.GridPos.X)
		assert.Equal(t, 0, statPanel.GridPos.Y)
		assert.Equal(t, 12, statPanel.GridPos.W)
		assert.Equal(t, 8, statPanel.GridPos.H)

		// Second panel should be next to first
		timeseriesPanel := req.Dashboard.Panels[1]
		assert.Equal(t, 12, timeseriesPanel.GridPos.X)
		assert.Equal(t, 0, timeseriesPanel.GridPos.Y)

		// Third panel should be on new row
		tablePanel := req.Dashboard.Panels[2]
		assert.Equal(t, 0, tablePanel.GridPos.X)
		assert.Equal(t, 8, tablePanel.GridPos.Y)

		response := `{
			"id": 456,
			"uid": "sql-dashboard-uid",
			"url": "/d/sql-dashboard-uid/test-dashboard",
			"status": "success"
		}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
	require.NoError(t, err)

	uid, err := client.CreateDashboardFromSQL(ctx, "Test Dashboard", queries, "postgres-uid")
	require.NoError(t, err)
	assert.Equal(t, "sql-dashboard-uid", uid)
}

func TestGrafanaClientCreateDashboardFromQueries(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	queries := []PanelQueryConfig{
		{
			Title:          "Database Connections",
			Query:          "SELECT COUNT(*) FROM pg_stat_activity",
			PanelType:      "stat",
			DatasourceType: "postgres",
			DatasourceUID:  "postgres-uid",
		},
		{
			Title:          "CPU Usage",
			Query:          "rate(node_cpu_seconds_total[5m])",
			PanelType:      "timeseries",
			DatasourceType: "prometheus",
			DatasourceUID:  "prometheus-uid",
			Legend:         "{{instance}}",
		},
		{
			Title:          "EC2 CPU Utilization",
			Query:          "",
			PanelType:      "timeseries",
			DatasourceType: "cloudwatch",
			DatasourceUID:  "cloudwatch-uid",
			Region:         "us-east-1",
			Namespace:      "AWS/EC2",
			MetricName:     "CPUUtilization",
			Statistics:     []string{"Average", "Maximum"},
			Dimensions: map[string]string{
				"InstanceId": "i-1234567890abcdef0",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/dashboards/db", r.URL.Path)

		var req DashboardSaveRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)

		// Verify dashboard structure
		assert.Equal(t, "Multi-Datasource Dashboard", req.Dashboard.Title)
		assert.Len(t, req.Dashboard.Panels, 3)
		assert.Contains(t, req.Dashboard.Tags, "auto-generated")

		// Check PostgreSQL panel
		postgresPanel := req.Dashboard.Panels[0]
		assert.Equal(t, "stat", postgresPanel.Type)
		assert.Equal(t, "Database Connections", postgresPanel.Title)
		assert.Equal(t, "SELECT COUNT(*) FROM pg_stat_activity", postgresPanel.Targets[0].RawSQL)
		assert.Equal(t, "postgres-uid", postgresPanel.Datasource["uid"])

		// Check Prometheus panel
		promPanel := req.Dashboard.Panels[1]
		assert.Equal(t, "timeseries", promPanel.Type)
		assert.Equal(t, "CPU Usage", promPanel.Title)
		assert.Equal(t, "rate(node_cpu_seconds_total[5m])", promPanel.Targets[0].Expr)
		assert.Equal(t, "prometheus-uid", promPanel.Datasource["uid"])

		// Check CloudWatch panel
		cwPanel := req.Dashboard.Panels[2]
		assert.Equal(t, "timeseries", cwPanel.Type)
		assert.Equal(t, "EC2 CPU Utilization", cwPanel.Title)
		assert.Equal(t, "cloudwatch-uid", cwPanel.Datasource["uid"])
		if assert.NotEmpty(t, cwPanel.Targets, "cloudwatch panel must have a target") {
			cwTarget := cwPanel.Targets[0]
			assert.Equal(t, "AWS/EC2", cwTarget.Namespace)
			assert.Equal(t, "CPUUtilization", cwTarget.MetricName)
			assert.Equal(t, "us-east-1", cwTarget.Region)
			assert.Equal(t, "Average", cwTarget.Statistic)
			assert.Equal(t, "i-1234567890abcdef0", cwTarget.Dimensions["InstanceId"])
			if assert.NotNil(t, cwTarget.MetricQueryType) {
				assert.Equal(t, 0, *cwTarget.MetricQueryType)
			}
			if assert.NotNil(t, cwTarget.MetricEditorMode) {
				assert.Equal(t, 0, *cwTarget.MetricEditorMode)
			}
		}

		response := `{
			"id": 789,
			"uid": "multi-datasource-uid",
			"url": "/d/multi-datasource-uid/multi-datasource-dashboard",
			"status": "success"
		}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
	require.NoError(t, err)

	uid, err := client.CreateDashboardFromQueries(ctx, "Multi-Datasource Dashboard", queries, "", "test: multi-datasource")
	require.NoError(t, err)
	assert.Equal(t, "multi-datasource-uid", uid)
}

func TestBuildPanelTargetInfinityDefaults(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewGrafanaClient("http://grafana.example.com", "test-key", logger)
	require.NoError(t, err)

	queryConfig := PanelQueryConfig{
		DatasourceType: "yesoreyeram-infinity-datasource",
		DatasourceUID:  "infinity-uid",
	}

	target := client.buildPanelTarget(queryConfig)

	// Verify defaults
	assert.Equal(t, "json", target.InfinityType, "default InfinityType should be json")
	assert.Equal(t, "url", target.Source, "default Source should be url")
	assert.Equal(t, "backend", target.Parser, "default Parser should be backend")
	assert.Equal(t, FormatTable, target.Format, "default Format should be table")
	require.NotNil(t, target.URLOptions, "URLOptions should not be nil")
	assert.Equal(t, "GET", target.URLOptions.Method, "default method should be GET")

	// Verify datasource
	assert.Equal(t, "yesoreyeram-infinity-datasource", target.Datasource["type"])
	assert.Equal(t, "infinity-uid", target.Datasource["uid"])
}

func TestBuildPanelTargetInfinityGraphQL(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewGrafanaClient("http://grafana.example.com", "test-key", logger)
	require.NoError(t, err)

	graphqlBody := `{"query": "{ vulnerabilities { id severity } }"}`
	queryConfig := PanelQueryConfig{
		DatasourceType:       "yesoreyeram-infinity-datasource",
		DatasourceUID:        "wiz-infinity",
		InfinityQueryType:    "graphql",
		InfinityBody:         graphqlBody,
		InfinityRootSelector: "data.vulnerabilities",
		InfinityColumns: []InfinityColumn{
			{Selector: "id", Text: "ID", Type: "string"},
			{Selector: "severity", Text: "Severity", Type: "string"},
		},
	}

	target := client.buildPanelTarget(queryConfig)

	// GraphQL defaults: method=POST, body_content_type=application/json
	assert.Equal(t, "graphql", target.InfinityType)
	require.NotNil(t, target.URLOptions)
	assert.Equal(t, "POST", target.URLOptions.Method, "GraphQL should default to POST")
	assert.Equal(t, "application/json", target.URLOptions.BodyContentType, "GraphQL should default to application/json")
	assert.Equal(t, graphqlBody, target.URLOptions.Body)
	assert.Equal(t, "raw", target.URLOptions.BodyType)

	// Verify root selector and columns
	assert.Equal(t, "data.vulnerabilities", target.RootSelector)
	require.Len(t, target.Columns, 2)
	assert.Equal(t, "id", target.Columns[0].Selector)
	assert.Equal(t, "ID", target.Columns[0].Text)
	assert.Equal(t, "string", target.Columns[0].Type)
}

func TestBuildPanelTargetInfinityJSONWithBody(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewGrafanaClient("http://grafana.example.com", "test-key", logger)
	require.NoError(t, err)

	// This is the pattern used by Wiz vulnerability dashboards: json type with a POST body
	// containing a GraphQL query string. The body_content_type must be set to application/json
	// or the upstream API returns 400 Bad Request.
	body := `{"query": "query { vulnerabilityFindings(filterBy: { severity: [CRITICAL], projectId: [\"proj-id\"] }, first: 0) { totalCount } }"}`
	queryConfig := PanelQueryConfig{
		DatasourceType:       "yesoreyeram-infinity-datasource",
		DatasourceUID:        "wiz-infinity",
		InfinityQueryType:    "json",
		InfinityMethod:       "POST",
		InfinityBody:         body,
		InfinityURL:          "https://api.us20.app.wiz.io/graphql",
		InfinityRootSelector: "data.vulnerabilityFindings",
		InfinityColumns: []InfinityColumn{
			{Selector: "totalCount", Text: "Count", Type: "number"},
		},
	}

	target := client.buildPanelTarget(queryConfig)

	// JSON type with body must set body_content_type to application/json
	assert.Equal(t, "json", target.InfinityType)
	require.NotNil(t, target.URLOptions)
	assert.Equal(t, "POST", target.URLOptions.Method, "explicit POST should be respected")
	assert.Equal(t, "application/json", target.URLOptions.BodyContentType, "JSON type with body should default to application/json")
	assert.Equal(t, body, target.URLOptions.Body)
	assert.Equal(t, "raw", target.URLOptions.BodyType)
	assert.Equal(t, "https://api.us20.app.wiz.io/graphql", target.URL)
	assert.Equal(t, "data.vulnerabilityFindings", target.RootSelector)
	require.Len(t, target.Columns, 1)
	assert.Equal(t, "totalCount", target.Columns[0].Selector)
}

func TestBuildPanelTargetInfinityBodyContentTypeNotOverridden(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewGrafanaClient("http://grafana.example.com", "test-key", logger)
	require.NoError(t, err)

	// When an explicit InfinityMethod of POST is used with a body and a custom content type
	// could be set in the future, the default should still apply correctly.
	// This test verifies that csv type with a body also gets application/json default.
	body := `some,csv,data`
	queryConfig := PanelQueryConfig{
		DatasourceType:    "yesoreyeram-infinity-datasource",
		DatasourceUID:     "csv-ds",
		InfinityQueryType: "csv",
		InfinityMethod:    "POST",
		InfinityBody:      body,
	}

	target := client.buildPanelTarget(queryConfig)

	require.NotNil(t, target.URLOptions)
	assert.Equal(t, "POST", target.URLOptions.Method)
	assert.Equal(t, body, target.URLOptions.Body)
	assert.Equal(t, "raw", target.URLOptions.BodyType)
	assert.Equal(t, "application/json", target.URLOptions.BodyContentType, "any type with body should default to application/json")
}

func TestBuildPanelTargetInfinity(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewGrafanaClient("http://grafana.example.com", "test-key", logger)
	require.NoError(t, err)

	queryConfig := PanelQueryConfig{
		DatasourceType:       "yesoreyeram-infinity-datasource",
		DatasourceUID:        "rest-api",
		InfinityQueryType:    "json",
		InfinityParser:       "simple",
		InfinitySource:       "url",
		InfinityURL:          "https://api.example.com/data",
		InfinityMethod:       "GET",
		InfinityRootSelector: "results",
		InfinityColumns: []InfinityColumn{
			{Selector: "name", Text: "Name", Type: "string"},
			{Selector: "count", Text: "Count", Type: "number"},
		},
		Format: "time_series",
	}

	target := client.buildPanelTarget(queryConfig)

	assert.Equal(t, "json", target.InfinityType)
	assert.Equal(t, "url", target.Source)
	assert.Equal(t, "simple", target.Parser)
	assert.Equal(t, "time_series", target.Format, "should respect explicit format override")
	assert.Equal(t, "https://api.example.com/data", target.URL)
	assert.Equal(t, "results", target.RootSelector)
	require.NotNil(t, target.URLOptions)
	assert.Equal(t, "GET", target.URLOptions.Method)
	require.Len(t, target.Columns, 2)
	assert.Equal(t, "name", target.Columns[0].Selector)
	assert.Equal(t, "number", target.Columns[1].Type)
}

func TestCreateDashboardFromQueriesInfinity(t *testing.T) {
	// Cannot run in parallel due to shared resources

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	queries := []PanelQueryConfig{
		{
			Title:                "Wiz Vulnerabilities",
			PanelType:            "table",
			DatasourceType:       "yesoreyeram-infinity-datasource",
			DatasourceUID:        "wiz-infinity",
			InfinityQueryType:    "graphql",
			InfinityBody:         `{"query": "{ vulnerabilities { id severity } }"}`,
			InfinityRootSelector: "data.vulnerabilities",
			InfinityColumns: []InfinityColumn{
				{Selector: "id", Text: "ID", Type: "string"},
				{Selector: "severity", Text: "Severity", Type: "string"},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/dashboards/db", r.URL.Path)

		var req DashboardSaveRequest
		decodeErr := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, decodeErr)

		// Verify dashboard structure
		assert.Equal(t, "Infinity Dashboard", req.Dashboard.Title)
		assert.Len(t, req.Dashboard.Panels, 1)

		// Verify Infinity panel
		panel := req.Dashboard.Panels[0]
		assert.Equal(t, "table", panel.Type)
		assert.Equal(t, "Wiz Vulnerabilities", panel.Title)
		assert.Equal(t, "yesoreyeram-infinity-datasource", panel.Datasource["type"])
		assert.Equal(t, "wiz-infinity", panel.Datasource["uid"])

		// Verify target
		assert.Len(t, panel.Targets, 1)
		target := panel.Targets[0]
		assert.Equal(t, "graphql", target.InfinityType)
		assert.Equal(t, "url", target.Source)
		assert.Equal(t, "backend", target.Parser)
		assert.Equal(t, "data.vulnerabilities", target.RootSelector)
		assert.NotNil(t, target.URLOptions)
		assert.Equal(t, "POST", target.URLOptions.Method)
		assert.Equal(t, "application/json", target.URLOptions.BodyContentType)
		assert.Len(t, target.Columns, 2)

		response := `{
			"id": 999,
			"uid": "infinity-dash-uid",
			"url": "/d/infinity-dash-uid/infinity-dashboard",
			"status": "success"
		}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)

	client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
	require.NoError(t, err)

	uid, err := client.CreateDashboardFromQueries(ctx, "Infinity Dashboard", queries, "", "test: infinity dashboard")
	require.NoError(t, err)
	assert.Equal(t, "infinity-dash-uid", uid)
}

// TestBuildCloudWatchTargetSerialization verifies the marshaled JSON for a
// CloudWatch panel target includes the query fields Grafana needs to render
// the panel. Regression test for the bug where namespace, metricName,
// dimensions, statistic, and region were silently dropped from the target.
func TestBuildCloudWatchTargetSerialization(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := NewGrafanaClient("http://grafana.example.com", "test-api-key", logger)
	require.NoError(t, err)

	queryConfig := PanelQueryConfig{
		Title:          "ECS CPU",
		PanelType:      "timeseries",
		DatasourceType: "cloudwatch",
		DatasourceUID:  "cloudwatch-prod",
		Namespace:      "AWS/ECS",
		MetricName:     "CPUUtilization",
		Dimensions: map[string]string{
			"ClusterName": "my-cluster",
			"ServiceName": "my-service",
		},
		Statistics: []string{"Average"},
		Region:     "us-east-1",
	}

	target := client.buildPanelTarget(queryConfig)

	jsonBytes, err := json.Marshal(target)
	require.NoError(t, err)

	var raw map[string]interface{}
	err = json.Unmarshal(jsonBytes, &raw)
	require.NoError(t, err)

	assert.Equal(t, "AWS/ECS", raw["namespace"], "namespace must be serialized into the target")
	assert.Equal(t, "CPUUtilization", raw["metricName"], "metricName must be serialized into the target")
	assert.Equal(t, "us-east-1", raw["region"], "region must be serialized into the target")
	assert.Equal(t, "Average", raw["statistic"], "statistic (singular) must be serialized from Statistics[0]")

	dims, ok := raw["dimensions"].(map[string]interface{})
	require.True(t, ok, "dimensions must be serialized as a JSON object")
	assert.Equal(t, "my-cluster", dims["ClusterName"])
	assert.Equal(t, "my-service", dims["ServiceName"])

	assert.EqualValues(t, 0, raw["metricQueryType"], "metricQueryType must be present (0 = Metric Search)")
	assert.EqualValues(t, 0, raw["metricEditorMode"], "metricEditorMode must be present (0 = Builder)")

	ds, ok := raw["datasource"].(map[string]interface{})
	require.True(t, ok, "datasource block must be serialized")
	assert.Equal(t, "cloudwatch", ds["type"])
	assert.Equal(t, "cloudwatch-prod", ds["uid"])
}

// TestGrafanaClientCreateFolder verifies CreateFolder posts to /api/folders
// with the given title/uid/parentUid and returns the UID from the response.
func TestGrafanaClientCreateFolder(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	tests := []struct {
		name          string
		input         CreateFolderRequest
		serverStatus  int
		serverBody    string
		expectErr     bool
		expectedUID   string
		expectedTitle string
	}{
		{
			name: "create_top_level_folder",
			input: CreateFolderRequest{
				Title: "Operations",
			},
			serverStatus:  http.StatusOK,
			serverBody:    `{"id": 12, "uid": "ops-uid", "title": "Operations", "url": "/dashboards/f/ops-uid/operations", "version": 1}`,
			expectedUID:   "ops-uid",
			expectedTitle: "Operations",
		},
		{
			name: "create_nested_folder_with_uid",
			input: CreateFolderRequest{
				Title:     "Production",
				UID:       "prod-uid",
				ParentUID: "ops-uid",
			},
			serverStatus:  http.StatusOK,
			serverBody:    `{"id": 13, "uid": "prod-uid", "title": "Production", "url": "/dashboards/f/prod-uid/production", "version": 1, "parentUid": "ops-uid"}`,
			expectedUID:   "prod-uid",
			expectedTitle: "Production",
		},
		{
			name: "missing_title_rejected_client_side",
			input: CreateFolderRequest{
				Title: "",
			},
			expectErr: true,
		},
		{
			name: "server_error_propagates",
			input: CreateFolderRequest{
				Title: "Operations",
			},
			serverStatus: http.StatusInternalServerError,
			serverBody:   `{"message": "boom"}`,
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/folders", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

				var body map[string]interface{}
				decodeErr := json.NewDecoder(r.Body).Decode(&body)
				assert.NoError(t, decodeErr)
				assert.Equal(t, tt.input.Title, body["title"])
				if tt.input.UID != "" {
					assert.Equal(t, tt.input.UID, body["uid"])
				} else {
					_, hasUID := body["uid"]
					assert.False(t, hasUID, "uid should be omitted when not set")
				}
				if tt.input.ParentUID != "" {
					assert.Equal(t, tt.input.ParentUID, body["parentUid"])
				} else {
					_, hasParent := body["parentUid"]
					assert.False(t, hasParent, "parentUid should be omitted when not set")
				}

				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverBody))
			}))
			t.Cleanup(server.Close)

			client, err := NewGrafanaClient(server.URL, "test-api-key", logger)
			require.NoError(t, err)

			folder, err := client.CreateFolder(ctx, tt.input)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedUID, folder.UID)
			assert.Equal(t, tt.expectedTitle, folder.Title)
		})
	}
}
