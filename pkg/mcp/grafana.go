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
	"time"
)

// Constants for Grafana formats.
const (
	FormatTable      = "table"
	FormatTimeSeries = "time_series"
)

// GrafanaClient handles interactions with Grafana API.
type GrafanaClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

// Dashboard represents a Grafana dashboard.
type Dashboard struct {
	ID            int                    `json:"id,omitempty"`
	UID           string                 `json:"uid,omitempty"`
	Title         string                 `json:"title"`
	Tags          []string               `json:"tags,omitempty"`
	Timezone      string                 `json:"timezone,omitempty"`
	SchemaVersion int                    `json:"schemaVersion,omitempty"`
	Version       int                    `json:"version,omitempty"`
	Panels        []Panel                `json:"panels,omitempty"`
	Templating    map[string]interface{} `json:"templating,omitempty"`
	Time          map[string]interface{} `json:"time,omitempty"`
	Editable      bool                   `json:"editable"`
	Style         string                 `json:"style,omitempty"`
}

// Panel represents a panel in a Grafana dashboard.
type Panel struct {
	ID              int                      `json:"id"`
	GridPos         GridPos                  `json:"gridPos"`
	Type            string                   `json:"type"`
	Title           string                   `json:"title"`
	Targets         []Target                 `json:"targets"`
	FieldConfig     map[string]interface{}   `json:"fieldConfig,omitempty"`
	Options         map[string]interface{}   `json:"options,omitempty"`
	Transparent     bool                     `json:"transparent,omitempty"`
	Datasource      map[string]interface{}   `json:"datasource,omitempty"`
	Description     string                   `json:"description,omitempty"`
	Transformations []map[string]interface{} `json:"transformations,omitempty"`
}

// GridPos represents panel position in the dashboard grid.
type GridPos struct {
	H int `json:"h"` // Height
	W int `json:"w"` // Width
	X int `json:"x"` // X position
	Y int `json:"y"` // Y position
}

// Target represents a query target in a panel.
type Target struct {
	RefID         string                 `json:"refId"`
	Datasource    map[string]interface{} `json:"datasource,omitempty"`
	RawSQL        string                 `json:"rawSql,omitempty"` // For SQL datasources
	Expr          string                 `json:"expr,omitempty"`   // For Prometheus
	Query         string                 `json:"query,omitempty"`  // Generic query field
	Format        string                 `json:"format,omitempty"`
	Hide          bool                   `json:"hide,omitempty"`
	Alias         string                 `json:"alias,omitempty"`
	IntervalMs    int                    `json:"intervalMs,omitempty"`
	MaxDataPoints int                    `json:"maxDataPoints,omitempty"`

	// Infinity datasource fields
	InfinityType string              `json:"type,omitempty"`
	Source       string              `json:"source,omitempty"`
	URL          string              `json:"url,omitempty"`
	URLOptions   *InfinityURLOptions `json:"url_options,omitempty"`
	RootSelector string              `json:"root_selector,omitempty"`
	Columns      []InfinityColumn    `json:"columns,omitempty"`
	Parser       string              `json:"parser,omitempty"`

	// CloudWatch datasource fields
	Namespace        string            `json:"namespace,omitempty"`
	MetricName       string            `json:"metricName,omitempty"`
	Region           string            `json:"region,omitempty"`
	Statistic        string            `json:"statistic,omitempty"`
	Dimensions       map[string]string `json:"dimensions,omitempty"`
	MetricQueryType  *int              `json:"metricQueryType,omitempty"`
	MetricEditorMode *int              `json:"metricEditorMode,omitempty"`
}

// InfinityURLOptions configures HTTP request options for Infinity datasource.
type InfinityURLOptions struct {
	Method          string                 `json:"method,omitempty"`
	Body            string                 `json:"data,omitempty"`
	BodyType        string                 `json:"body_type,omitempty"`
	BodyContentType string                 `json:"body_content_type,omitempty"`
	Headers         []InfinityKeyValuePair `json:"headers,omitempty"`
	Params          []InfinityKeyValuePair `json:"params,omitempty"`
}

// InfinityKeyValuePair represents a key-value pair for Infinity headers or params.
type InfinityKeyValuePair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// InfinityColumn defines a column mapping for Infinity datasource responses.
type InfinityColumn struct {
	Selector string `json:"selector"`
	Text     string `json:"text"`
	Type     string `json:"type"` // "string", "number", "timestamp", "timestamp_epoch"
}

// DashboardGetResponse wraps a dashboard with metadata from the Grafana API response.
type DashboardGetResponse struct {
	Dashboard *Dashboard
	FolderUID string
}

// DashboardSaveRequest represents the request to save a dashboard.
type DashboardSaveRequest struct {
	Dashboard Dashboard `json:"dashboard"`
	Message   string    `json:"message,omitempty"`
	Overwrite bool      `json:"overwrite"`
	FolderID  int       `json:"folderId,omitempty"`
	FolderUID string    `json:"folderUid,omitempty"`
}

// DashboardSearchResponse represents a dashboard search result.
type DashboardSearchResponse struct {
	ID          int      `json:"id"`
	UID         string   `json:"uid"`
	Title       string   `json:"title"`
	URI         string   `json:"uri"`
	URL         string   `json:"url"`
	Type        string   `json:"type"`
	Tags        []string `json:"tags"`
	IsStarred   bool     `json:"isStarred"`
	FolderID    int      `json:"folderId"`
	FolderUID   string   `json:"folderUid"`
	FolderTitle string   `json:"folderTitle"`
}

// NewGrafanaClient creates a new Grafana API client.
func NewGrafanaClient(baseURL, apiKey string, logger *slog.Logger) (client *GrafanaClient, err error) {
	if baseURL == "" {
		err = errors.New("grafana base URL is required")
		return client, err
	}
	if apiKey == "" {
		err = errors.New("grafana API key is required")
		return client, err
	}

	client = &GrafanaClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}

	return client, err
}

// makeRequest performs an HTTP request to Grafana API.
func (c *GrafanaClient) makeRequest(ctx context.Context, method, endpoint string, body interface{}) (responseBody []byte, err error) {
	url := c.baseURL + endpoint

	var reqBody io.Reader
	if body != nil {
		var jsonBytes []byte
		jsonBytes, err = json.Marshal(body)
		if err != nil {
			err = fmt.Errorf("marshaling request body: %w", err)
			return responseBody, err
		}
		reqBody = bytes.NewReader(jsonBytes)
	}

	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		err = fmt.Errorf("creating request: %w", err)
		return responseBody, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	c.logger.DebugContext(ctx, "making Grafana API request",
		"method", method,
		"endpoint", endpoint,
	)

	var resp *http.Response
	resp, err = c.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("executing request: %w", err)
		return responseBody, err
	}
	defer resp.Body.Close()

	responseBody, err = io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("reading response body: %w", err)
		return responseBody, err
	}

	if resp.StatusCode >= 400 {
		err = fmt.Errorf("grafana API error (status %d): %s", resp.StatusCode, string(responseBody))
		return responseBody, err
	}

	return responseBody, err
}

// ListDashboards lists all dashboards the user has access to.
func (c *GrafanaClient) ListDashboards(ctx context.Context) (dashboards []DashboardSearchResponse, err error) {
	var responseBody []byte
	responseBody, err = c.makeRequest(ctx, http.MethodGet, "/api/search?type=dash-db", nil)
	if err != nil {
		err = fmt.Errorf("listing dashboards: %w", err)
		return dashboards, err
	}

	err = json.Unmarshal(responseBody, &dashboards)
	if err != nil {
		err = fmt.Errorf("unmarshaling dashboard list: %w", err)
		return dashboards, err
	}

	c.logger.InfoContext(ctx, "listed dashboards", "count", len(dashboards))
	return dashboards, err
}

// GetDashboard retrieves a dashboard by UID, including folder metadata.
func (c *GrafanaClient) GetDashboard(ctx context.Context, uid string) (result *DashboardGetResponse, err error) {
	endpoint := fmt.Sprintf("/api/dashboards/uid/%s", uid)

	var responseBody []byte
	responseBody, err = c.makeRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		err = fmt.Errorf("getting dashboard %s: %w", uid, err)
		return result, err
	}

	var response struct {
		Dashboard Dashboard `json:"dashboard"`
		Meta      struct {
			Version   int    `json:"version"`
			Slug      string `json:"slug"`
			FolderUID string `json:"folderUid"`
		} `json:"meta"`
	}

	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		err = fmt.Errorf("unmarshaling dashboard: %w", err)
		return result, err
	}

	result = &DashboardGetResponse{
		Dashboard: &response.Dashboard,
		FolderUID: response.Meta.FolderUID,
	}

	c.logger.InfoContext(ctx, "retrieved dashboard", "uid", uid, "title", result.Dashboard.Title, "folderUid", result.FolderUID)
	return result, err
}

// CreateDashboard creates a new dashboard.
func (c *GrafanaClient) CreateDashboard(ctx context.Context, dashboard *Dashboard, folderUID string, message string) (uid string, err error) {
	// Ensure new dashboard doesn't have ID
	dashboard.ID = 0
	dashboard.Version = 0

	request := DashboardSaveRequest{
		Dashboard: *dashboard,
		Message:   message,
		Overwrite: false,
		FolderUID: folderUID,
	}

	var responseBody []byte
	responseBody, err = c.makeRequest(ctx, http.MethodPost, "/api/dashboards/db", request)
	if err != nil {
		err = fmt.Errorf("creating dashboard: %w", err)
		return uid, err
	}

	var response struct {
		ID      int    `json:"id"`
		UID     string `json:"uid"`
		URL     string `json:"url"`
		Status  string `json:"status"`
		Version int    `json:"version"`
		Slug    string `json:"slug"`
	}

	err = json.Unmarshal(responseBody, &response)
	if err != nil {
		err = fmt.Errorf("unmarshaling create response: %w", err)
		return uid, err
	}

	uid = response.UID
	c.logger.InfoContext(ctx, "created dashboard",
		"uid", uid,
		"title", dashboard.Title,
		"url", response.URL,
	)
	return uid, err
}

// UpdateDashboard updates an existing dashboard, preserving its folder placement.
func (c *GrafanaClient) UpdateDashboard(ctx context.Context, dashboard *Dashboard, folderUID string, message string) (err error) {
	request := DashboardSaveRequest{
		Dashboard: *dashboard,
		Message:   message,
		Overwrite: true,
		FolderUID: folderUID,
	}

	_, err = c.makeRequest(ctx, http.MethodPost, "/api/dashboards/db", request)
	if err != nil {
		err = fmt.Errorf("updating dashboard: %w", err)
		return err
	}

	c.logger.InfoContext(ctx, "updated dashboard",
		"uid", dashboard.UID,
		"title", dashboard.Title,
		"folderUid", folderUID,
	)
	return err
}

// CreateFolderRequest is the input for CreateFolder. Title is required.
// UID and ParentUID are optional — Grafana auto-generates UID when omitted
// and treats an absent ParentUID as a top-level (General) folder.
type CreateFolderRequest struct {
	Title     string `json:"title"`
	UID       string `json:"uid,omitempty"`
	ParentUID string `json:"parentUid,omitempty"`
}

// Folder represents a Grafana folder (directory) as returned by /api/folders.
type Folder struct {
	ID        int    `json:"id"`
	UID       string `json:"uid"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Version   int    `json:"version"`
	ParentUID string `json:"parentUid,omitempty"`
}

// CreateFolder creates a new folder (directory) via POST /api/folders. A
// folder is Grafana's equivalent of a directory — dashboards reference it
// by FolderUID. Pass ParentUID to nest under another folder.
func (c *GrafanaClient) CreateFolder(ctx context.Context, req CreateFolderRequest) (folder Folder, err error) {
	if req.Title == "" {
		err = errors.New("folder title is required")
		return folder, err
	}

	var responseBody []byte
	responseBody, err = c.makeRequest(ctx, http.MethodPost, "/api/folders", req)
	if err != nil {
		err = fmt.Errorf("creating folder: %w", err)
		return folder, err
	}

	err = json.Unmarshal(responseBody, &folder)
	if err != nil {
		err = fmt.Errorf("unmarshaling folder response: %w", err)
		return folder, err
	}

	c.logger.InfoContext(ctx, "created folder",
		"uid", folder.UID,
		"title", folder.Title,
		"parentUid", folder.ParentUID,
	)
	return folder, err
}

// DeleteDashboard deletes a dashboard by UID.
func (c *GrafanaClient) DeleteDashboard(ctx context.Context, uid string) (err error) {
	endpoint := fmt.Sprintf("/api/dashboards/uid/%s", uid)
	_, err = c.makeRequest(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		err = fmt.Errorf("deleting dashboard %s: %w", uid, err)
		return err
	}

	c.logger.InfoContext(ctx, "deleted dashboard", "uid", uid)
	return err
}

// buildPanelTarget builds a Target structure based on query configuration.
func (c *GrafanaClient) buildPanelTarget(queryConfig PanelQueryConfig) (target Target) {
	target = Target{
		RefID: "A",
		Datasource: map[string]interface{}{
			"type": queryConfig.DatasourceType,
			"uid":  queryConfig.DatasourceUID,
		},
	}

	switch queryConfig.DatasourceType {
	case "postgres", "mysql":
		target.RawSQL = queryConfig.Query
		target.Format = FormatTable
		if queryConfig.Format != "" {
			target.Format = queryConfig.Format
		}

	case "prometheus":
		target.Expr = queryConfig.Query
		target.Format = FormatTimeSeries
		if queryConfig.Legend != "" {
			target.Alias = queryConfig.Legend
		}
		if queryConfig.Interval != "" {
			target.IntervalMs = 1000 // Default to 1s
		}

	case "cloudwatch":
		c.buildCloudWatchTarget(&target, queryConfig)

	case grafanaInfinityDatasource:
		c.buildInfinityTarget(&target, queryConfig)

	default:
		target.Query = queryConfig.Query
	}

	return target
}

// buildCloudWatchTarget builds CloudWatch-specific target configuration.
// Grafana's CloudWatch datasource expects query fields at the top level of
// the target (namespace, metricName, dimensions, statistic, region) along
// with the routing fields metricQueryType and metricEditorMode (both 0 =
// Metric Search / Builder mode).
func (c *GrafanaClient) buildCloudWatchTarget(target *Target, queryConfig PanelQueryConfig) {
	target.Query = queryConfig.Query
	target.Format = FormatTimeSeries

	target.Namespace = queryConfig.Namespace
	target.MetricName = queryConfig.MetricName
	target.Region = queryConfig.Region
	target.Dimensions = queryConfig.Dimensions

	// Grafana's CloudWatch target carries a single `statistic` per target.
	// Multiple statistics require multiple targets; collapse to the first
	// supplied value (callers needing N statistics should add N panels).
	if len(queryConfig.Statistics) > 0 {
		target.Statistic = queryConfig.Statistics[0]
	}

	// 0 = Metric Search, 0 = Builder mode. Must be present (not omitted) for
	// Grafana to route the query correctly.
	zero := 0
	target.MetricQueryType = &zero
	target.MetricEditorMode = &zero
}

// buildInfinityTarget builds Infinity datasource-specific target configuration.
func (c *GrafanaClient) buildInfinityTarget(target *Target, queryConfig PanelQueryConfig) {
	// Set Infinity type (json, graphql, csv, xml) — default to json
	target.InfinityType = queryConfig.InfinityQueryType
	if target.InfinityType == "" {
		target.InfinityType = "json"
	}

	// Set source (url, inline) — default to url
	target.Source = queryConfig.InfinitySource
	if target.Source == "" {
		target.Source = "url"
	}

	// Set parser (simple, backend, uql, groq) — default to backend
	target.Parser = queryConfig.InfinityParser
	if target.Parser == "" {
		target.Parser = "backend"
	}

	// Set format — default to table
	target.Format = FormatTable
	if queryConfig.Format != "" {
		target.Format = queryConfig.Format
	}

	// Set URL override (empty means use datasource default)
	target.URL = queryConfig.InfinityURL

	// Set root selector for JSONPath
	target.RootSelector = queryConfig.InfinityRootSelector

	// Set column definitions
	target.Columns = queryConfig.InfinityColumns

	// Determine HTTP method — default to GET, but POST for GraphQL
	method := queryConfig.InfinityMethod
	if method == "" {
		method = "GET"
		if target.InfinityType == "graphql" {
			method = "POST"
		}
	}

	// Build URL options
	urlOptions := &InfinityURLOptions{
		Method: method,
	}

	// Set body and content type
	if queryConfig.InfinityBody != "" {
		urlOptions.Body = queryConfig.InfinityBody
		urlOptions.BodyType = "raw"

		// Default content type to application/json when a body is present
		if urlOptions.BodyContentType == "" {
			urlOptions.BodyContentType = "application/json"
		}
	}

	target.URLOptions = urlOptions
}

// CreateDashboardFromQueries creates a dashboard with panels based on any type of queries.
// Supports SQL (PostgreSQL/MySQL), Prometheus (PromQL), CloudWatch, and Infinity queries.
func (c *GrafanaClient) CreateDashboardFromQueries(ctx context.Context, title string, queries []PanelQueryConfig, folderUID string) (uid string, err error) {
	dashboard := &Dashboard{
		Title:    title,
		Tags:     []string{"auto-generated", "mcp"},
		Editable: true,
		Panels:   []Panel{},
		Time: map[string]interface{}{
			"from": "now-6h",
			"to":   "now",
		},
	}

	// Calculate grid positions for panels (2 panels per row, each 12 units wide)
	panelsPerRow := 2
	panelWidth := 24 / panelsPerRow
	panelHeight := 8

	for i, queryConfig := range queries {
		row := i / panelsPerRow
		col := i % panelsPerRow

		// Build the target based on datasource type
		target := c.buildPanelTarget(queryConfig)

		panel := Panel{
			ID:    i + 1,
			Type:  queryConfig.PanelType,
			Title: queryConfig.Title,
			GridPos: GridPos{
				H: panelHeight,
				W: panelWidth,
				X: col * panelWidth,
				Y: row * panelHeight,
			},
			Targets: []Target{target},
			Datasource: map[string]interface{}{
				"type": queryConfig.DatasourceType,
				"uid":  queryConfig.DatasourceUID,
			},
			Description: queryConfig.Description,
		}

		// Set panel options based on type
		c.setPanelOptions(&panel, queryConfig.PanelType)

		dashboard.Panels = append(dashboard.Panels, panel)
	}

	uid, err = c.CreateDashboard(ctx, dashboard, folderUID, fmt.Sprintf("Auto-generated dashboard: %s", title))
	return uid, err
}

// setPanelOptions sets default options for a panel based on its type.
func (c *GrafanaClient) setPanelOptions(panel *Panel, panelType string) {
	switch panelType {
	case "stat":
		panel.Options = map[string]interface{}{
			"reduceOptions": map[string]interface{}{
				"values": false,
				"calcs":  []string{"lastNotNull"},
			},
			"orientation": "auto",
			"textMode":    "auto",
			"colorMode":   "value",
		}
	case "gauge":
		panel.Options = map[string]interface{}{
			"reduceOptions": map[string]interface{}{
				"values": false,
				"calcs":  []string{"lastNotNull"},
			},
			"orientation":          "auto",
			"showThresholdLabels":  false,
			"showThresholdMarkers": true,
		}
		panel.FieldConfig = map[string]interface{}{
			"defaults": map[string]interface{}{
				"min":  0,
				"max":  100,
				"unit": "percent",
				"thresholds": map[string]interface{}{
					"mode": "absolute",
					"steps": []map[string]interface{}{
						{"color": "green", "value": nil},
						{"color": "yellow", "value": 60},
						{"color": "red", "value": 80},
					},
				},
			},
		}
	case "timeseries":
		panel.Options = map[string]interface{}{
			"legend": map[string]interface{}{
				"displayMode": "list",
				"placement":   "bottom",
				"showLegend":  true,
			},
			"tooltip": map[string]interface{}{
				"mode": "single",
				"sort": "none",
			},
		}
		panel.FieldConfig = map[string]interface{}{
			"defaults": map[string]interface{}{
				"unit": "short",
				"custom": map[string]interface{}{
					"drawStyle":         "line",
					"lineInterpolation": "linear",
					"lineWidth":         1,
					"fillOpacity":       10,
				},
			},
		}
	case "heatmap":
		panel.Options = map[string]interface{}{
			"calculate": false,
			"cellGap":   2,
			"color": map[string]interface{}{
				"scheme": "Spectral",
				"mode":   "spectrum",
			},
		}
	case "table":
		panel.Options = map[string]interface{}{
			"showHeader": true,
			"sortBy":     []interface{}{},
		}
	case "piechart":
		panel.Options = map[string]interface{}{
			"reduceOptions": map[string]interface{}{
				"values": false,
				"calcs":  []string{"lastNotNull"},
			},
			"pieType":       "pie",
			"displayLabels": []string{"name", "percent"},
		}
	case "bargauge":
		panel.Options = map[string]interface{}{
			"reduceOptions": map[string]interface{}{
				"values": false,
				"calcs":  []string{"lastNotNull"},
			},
			"orientation":  "horizontal",
			"displayMode":  "gradient",
			"showUnfilled": true,
		}
	}
}

// CreateDashboardFromSQL creates a dashboard with panels based on SQL queries.
// This is a helper method that constructs panels from SQL queries for easier CEO use.
// Deprecated: Use CreateDashboardFromQueries for multi-datasource support.
func (c *GrafanaClient) CreateDashboardFromSQL(ctx context.Context, title string, queries []SQLPanelConfig, datasourceUID string) (uid string, err error) {
	dashboard := &Dashboard{
		Title:    title,
		Tags:     []string{"auto-generated", "sql"},
		Editable: true,
		Panels:   []Panel{},
		Time: map[string]interface{}{
			"from": "now-6h",
			"to":   "now",
		},
	}

	// Calculate grid positions for panels (2 panels per row, each 12 units wide)
	panelsPerRow := 2
	panelWidth := 24 / panelsPerRow
	panelHeight := 8

	for i, queryConfig := range queries {
		row := i / panelsPerRow
		col := i % panelsPerRow

		panel := Panel{
			ID:    i + 1,
			Type:  queryConfig.PanelType,
			Title: queryConfig.Title,
			GridPos: GridPos{
				H: panelHeight,
				W: panelWidth,
				X: col * panelWidth,
				Y: row * panelHeight,
			},
			Targets: []Target{
				{
					RefID:  "A",
					RawSQL: queryConfig.SQL,
					Format: "table",
					Datasource: map[string]interface{}{
						"type": "postgres",
						"uid":  datasourceUID,
					},
				},
			},
			Datasource: map[string]interface{}{
				"type": "postgres",
				"uid":  datasourceUID,
			},
			Description: queryConfig.Description,
		}

		// Set default options based on panel type
		c.setPanelOptions(&panel, queryConfig.PanelType)

		dashboard.Panels = append(dashboard.Panels, panel)
	}

	uid, err = c.CreateDashboard(ctx, dashboard, "", fmt.Sprintf("Auto-generated dashboard: %s", title))
	return uid, err
}

// PanelQueryConfig represents configuration for creating a panel from any query.
type PanelQueryConfig struct {
	Title          string            `json:"title"`
	Query          string            `json:"query"`     // SQL, PromQL, or CloudWatch query
	PanelType      string            `json:"panelType"` // stat, timeseries, table, piechart, bargauge, gauge, heatmap
	Description    string            `json:"description,omitempty"`
	DatasourceType string            `json:"datasourceType"`       // postgres, prometheus, cloudwatch, yesoreyeram-infinity-datasource
	DatasourceUID  string            `json:"datasourceUID"`        // Specific datasource UID
	Legend         string            `json:"legend,omitempty"`     // Legend format for Prometheus
	Format         string            `json:"format,omitempty"`     // Query result format
	Interval       string            `json:"interval,omitempty"`   // Scrape interval for metrics
	Step           string            `json:"step,omitempty"`       // Query resolution step
	Region         string            `json:"region,omitempty"`     // CloudWatch region
	Namespace      string            `json:"namespace,omitempty"`  // CloudWatch namespace
	MetricName     string            `json:"metricName,omitempty"` // CloudWatch metric name
	Statistics     []string          `json:"statistics,omitempty"` // CloudWatch statistics
	Dimensions     map[string]string `json:"dimensions,omitempty"` // CloudWatch dimensions

	// Infinity datasource fields
	InfinityQueryType    string           `json:"infinityQueryType,omitempty"`    // json, graphql, csv, xml
	InfinityParser       string           `json:"infinityParser,omitempty"`       // simple, backend, uql, groq
	InfinitySource       string           `json:"infinitySource,omitempty"`       // url, inline
	InfinityURL          string           `json:"infinityUrl,omitempty"`          // Override URL (empty = datasource default)
	InfinityMethod       string           `json:"infinityMethod,omitempty"`       // GET, POST
	InfinityBody         string           `json:"infinityBody,omitempty"`         // Request body (GraphQL query, JSON)
	InfinityRootSelector string           `json:"infinityRootSelector,omitempty"` // JSONPath root selector
	InfinityColumns      []InfinityColumn `json:"infinityColumns,omitempty"`      // Column definitions
}

// SQLPanelConfig represents configuration for creating a panel from SQL (deprecated).
type SQLPanelConfig struct {
	Title       string `json:"title"`
	SQL         string `json:"sql"`
	PanelType   string `json:"panelType"` // stat, timeseries, table, piechart, bargauge
	Description string `json:"description,omitempty"`
}
