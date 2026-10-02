package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/metrics"
	"github.com/nikogura/diagnostic-bot/pkg/tenant"
)

const (
	// DefaultLokiEndpoint is the default Loki gateway endpoint (in-cluster service).
	DefaultLokiEndpoint = "http://loki-gateway.logging.svc.cluster.local"

	// MaxLokiResults is the maximum number of results to return from Loki.
	MaxLokiResults = 1000

	// LokiTenantHeader is the multi-tenant identifier header Loki expects
	// when auth_enabled: true. The value is Go's canonicalized form of
	// "X-Scope-OrgID" — Loki treats it case-insensitively per HTTP spec.
	LokiTenantHeader = "X-Scope-Orgid"

	// DefaultLokiLabelWindow is how far back label discovery looks when the
	// caller gives no start.
	DefaultLokiLabelWindow = "1h"

	lokiBackend       = "loki"
	lokiStatusSuccess = "success"
	lokiStatusError   = "error"
	lokiResultMatrix  = "matrix"

	// lokiDefaultSteps is how many samples per series a metric query returns
	// when the caller gives no step. Loki's own default is far finer, which
	// buries the answer to "which streams exist" under hundreds of samples each.
	lokiDefaultSteps = 20
)

// LokiClient handles queries to the Loki log aggregation system.
type LokiClient struct {
	endpoint   string
	httpClient *http.Client
	logger     *slog.Logger
	tenants    tenant.Policy
}

// NewLokiClient creates a new Loki client. Multi-tenant Loki deployments
// (auth_enabled: true) additionally require calling ConfigureTenants before
// issuing queries.
func NewLokiClient(endpoint string, logger *slog.Logger) (result *LokiClient) {
	if endpoint == "" {
		endpoint = DefaultLokiEndpoint
	}

	result = &LokiClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// Per-client Transport isolates the connection pool from
			// http.DefaultTransport (see pkg/mcp/tempo.go for context).
			Transport: &http.Transport{},
		},
		logger: logger,
	}

	return result
}

// ConfigureTenants enables multi-tenant Loki support by attaching tenant
// configuration after construction. defaultTenant is used as the
// X-Scope-OrgID value when a query doesn't specify one (empty means the
// caller must specify per-query). allowedTenants restricts which tenant
// identifiers may be sent — empty means no restriction.
//
// The allowlist is not a security boundary against Loki — Loki trusts
// whatever X-Scope-OrgID it receives. It exists to keep the calling LLM
// from hallucinating tenant names. When the allowlist is non-empty, the
// default tenant must be on it.
func (l *LokiClient) ConfigureTenants(defaultTenant string, allowedTenants []string) (err error) {
	var policy tenant.Policy

	policy, err = tenant.NewPolicy(lokiBackend, defaultTenant, allowedTenants)
	if err != nil {
		return err
	}

	l.tenants = policy

	return err
}

// AllowedTenants returns the configured tenant allowlist in sorted order.
// The MCP layer injects this list into the Loki tool descriptions so the
// calling LLM can discover which tenants are queryable.
func (l *LokiClient) AllowedTenants() (result []string) {
	result = l.tenants.Allowed()
	return result
}

// DiscoveryTenants returns the tenants a label lookup should inspect one at a
// time: the ones requested, or every allowed tenant when none is named.
func (l *LokiClient) DiscoveryTenants(requested string) (tenants []string, err error) {
	tenants, err = l.tenants.Each(requested)
	return tenants, err
}

// UnsearchedTenants returns the allowed tenants a lookup for requested did not
// cover, so an empty result can say where else to look.
func (l *LokiClient) UnsearchedTenants(requested string) (remaining []string) {
	searched, err := l.tenants.Resolve(requested)
	if err != nil {
		return remaining
	}

	remaining = l.tenants.Unsearched(searched)

	return remaining
}

// QueryRequest represents a query request to Loki.
type QueryRequest struct {
	Query  string
	Start  string // RFC3339 format or relative duration (e.g., "1h", "24h")
	End    string // RFC3339 format or "now"
	Limit  int
	Step   string // Metric-query resolution as a duration (e.g., "5m"). Empty picks one from the range.
	Tenant string // X-Scope-OrgID value. Empty means use the client's default.
}

// LabelRequest scopes a label-discovery request to Loki.
type LabelRequest struct {
	Start  string // RFC3339 format or relative duration. Empty means DefaultLokiLabelWindow.
	End    string // RFC3339 format or "now"
	Query  string // Optional stream selector limiting which streams are considered.
	Tenant string // X-Scope-OrgID value. Empty means use the client's default.
}

// QueryResult represents the result from a Loki query. A log query fills
// Entries; a metric query fills Series.
type QueryResult struct {
	Entries   []LogEntry
	Series    []MetricSeries
	Stats     QueryStats
	RawResult string
}

// LogEntry represents a single log entry from Loki.
type LogEntry struct {
	Timestamp time.Time
	Line      string
	Labels    map[string]string
}

// MetricSeries is one series returned by a metric query, with the labels that
// identify it.
type MetricSeries struct {
	Labels  map[string]string
	Samples []MetricSample
}

// MetricSample is one point of a MetricSeries.
type MetricSample struct {
	Timestamp time.Time
	Value     string
}

// QueryStats provides statistics about the query execution.
type QueryStats struct {
	TotalEntries int
	BytesQueried int64
	Duration     time.Duration
}

// Query executes a LogQL query against Loki.
func (l *LokiClient) Query(ctx context.Context, req QueryRequest) (result QueryResult, err error) {
	var startTime time.Time
	var endTime time.Time

	start := time.Now()

	startTime, endTime, err = parseTimeRange(req.Start, req.End)
	if err != nil {
		return result, err
	}

	var step time.Duration

	step, err = resolveStep(req.Step, startTime, endTime)
	if err != nil {
		return result, err
	}

	// Set default and max limit
	if req.Limit == 0 {
		req.Limit = 100
	}

	if req.Limit > MaxLokiResults {
		req.Limit = MaxLokiResults
	}

	// Resolve and validate the Loki tenant (X-Scope-OrgID) before issuing
	// the request. Multi-tenant deployments (auth_enabled: true) refuse
	// queries without a tenant header — the allowlist check runs first so
	// invalid tenants never hit the wire.
	var tenantID string

	tenantID, err = l.tenants.Resolve(req.Tenant)
	if err != nil {
		return result, err
	}

	l.logger.InfoContext(ctx, "executing Loki query",
		slog.String("query", req.Query),
		slog.String("tenant", tenantID),
		slog.Time("start", startTime),
		slog.Time("end", endTime),
		slog.Int("limit", req.Limit))

	// step only shapes metric queries; Loki ignores it for log queries.
	params := url.Values{}
	params.Set("query", req.Query)
	params.Set("start", strconv.FormatInt(startTime.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(endTime.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(req.Limit))
	params.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))

	var body []byte

	body, err = l.get(ctx, "/loki/api/v1/query_range", params, tenantID)
	if err != nil {
		return result, err
	}

	var lokiResp lokiQueryRangeResponse

	err = json.Unmarshal(body, &lokiResp)
	if err != nil {
		err = fmt.Errorf("parsing Loki response: %w", err)
		return result, err
	}

	if lokiResp.Status != lokiStatusSuccess {
		err = fmt.Errorf("loki query unsuccessful: %s", lokiResp.Status)
		return result, err
	}

	// A metric query (count_over_time, rate, sum by …) answers with series
	// rather than log lines. Reading it as log lines yields nothing, which
	// reports data that exists as absent.
	if lokiResp.Data.ResultType == lokiResultMatrix {
		result.Series = parseSeries(lokiResp.Data.Result)
	} else {
		result.Entries = parseLogEntries(lokiResp.Data.Result)
	}

	duration := time.Since(start)

	l.logger.InfoContext(ctx, "Loki query completed",
		slog.Int("entries", len(result.Entries)),
		slog.Int("series", len(result.Series)),
		slog.Duration("duration", duration))

	result.Stats = QueryStats{TotalEntries: len(result.Entries), Duration: duration}
	result.RawResult = string(body)

	return result, err
}

// LabelNames returns the label names present on one tenant's streams within
// the requested window, sorted.
func (l *LokiClient) LabelNames(ctx context.Context, req LabelRequest) (names []string, err error) {
	names, err = l.labels(ctx, "/loki/api/v1/labels", req)
	return names, err
}

// LabelValues returns the values one tenant's streams carry for label within
// the requested window, sorted.
func (l *LokiClient) LabelValues(ctx context.Context, label string, req LabelRequest) (values []string, err error) {
	if !validLabelName(label) {
		err = fmt.Errorf("invalid label name %q: expected letters, digits and underscores, not starting with a digit", label)
		return values, err
	}

	values, err = l.labels(ctx, "/loki/api/v1/label/"+label+"/values", req)

	return values, err
}

// labels issues one label-discovery request and returns its sorted answer.
func (l *LokiClient) labels(ctx context.Context, path string, req LabelRequest) (found []string, err error) {
	var startTime time.Time
	var endTime time.Time

	startStr := req.Start
	if startStr == "" {
		startStr = DefaultLokiLabelWindow
	}

	startTime, endTime, err = parseTimeRange(startStr, req.End)
	if err != nil {
		return found, err
	}

	var tenantID string

	tenantID, err = l.tenants.Resolve(req.Tenant)
	if err != nil {
		return found, err
	}

	params := url.Values{}
	params.Set("start", strconv.FormatInt(startTime.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(endTime.UnixNano(), 10))

	if req.Query != "" {
		params.Set("query", req.Query)
	}

	l.logger.InfoContext(ctx, "executing Loki label lookup",
		slog.String("path", path),
		slog.String("tenant", tenantID),
		slog.String("query", req.Query))

	var body []byte

	body, err = l.get(ctx, path, params, tenantID)
	if err != nil {
		return found, err
	}

	var labelResp lokiLabelResponse

	err = json.Unmarshal(body, &labelResp)
	if err != nil {
		err = fmt.Errorf("parsing Loki response: %w", err)
		return found, err
	}

	if labelResp.Status != lokiStatusSuccess {
		err = fmt.Errorf("loki label lookup unsuccessful: %s", labelResp.Status)
		return found, err
	}

	found = labelResp.Data
	sort.Strings(found)

	return found, err
}

// get issues a GET against a Loki API path as tenantID and returns the body.
// An empty tenantID sends no X-Scope-OrgID header.
func (l *LokiClient) get(ctx context.Context, path string, params url.Values, tenantID string) (body []byte, err error) {
	var httpReq *http.Request
	var resp *http.Response

	fullURL := fmt.Sprintf("%s%s?%s", l.endpoint, path, params.Encode())

	httpReq, err = http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		err = fmt.Errorf("creating HTTP request: %w", err)
		return body, err
	}

	if tenantID != "" {
		httpReq.Header.Set(LokiTenantHeader, tenantID)
	}

	resp, err = l.httpClient.Do(httpReq)
	if err != nil {
		metrics.RecordLokiQuery(ctx, lokiStatusError)
		err = fmt.Errorf("executing HTTP request: %w", err)

		return body, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var statusBody []byte

		statusBody, _ = io.ReadAll(resp.Body)
		metrics.RecordLokiQuery(ctx, lokiStatusError)
		err = fmt.Errorf("loki query failed with status %d: %s", resp.StatusCode, string(statusBody))

		return body, err
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		metrics.RecordLokiQuery(ctx, lokiStatusError)
		err = fmt.Errorf("reading response body: %w", err)

		return body, err
	}

	metrics.RecordLokiQuery(ctx, lokiStatusSuccess)

	return body, err
}

// parseLogEntries extracts the log lines from a streams result.
func parseLogEntries(results []lokiResult) (entries []LogEntry) {
	for _, stream := range results {
		for _, value := range stream.Values {
			if len(value) < 2 {
				continue
			}

			// Timestamp is a string of nanoseconds.
			timestampStr, ok := value[0].(string)
			if !ok {
				continue
			}

			timestamp, parseErr := strconv.ParseInt(timestampStr, 10, 64)
			if parseErr != nil {
				continue
			}

			line, ok := value[1].(string)
			if !ok {
				continue
			}

			entries = append(entries, LogEntry{
				Timestamp: time.Unix(0, timestamp),
				Line:      line,
				Labels:    stream.Stream,
			})
		}
	}

	return entries
}

// parseSeries extracts the series from a matrix result.
func parseSeries(results []lokiResult) (series []MetricSeries) {
	for _, metric := range results {
		samples := make([]MetricSample, 0, len(metric.Values))

		for _, value := range metric.Values {
			if len(value) < 2 {
				continue
			}

			// Timestamp is a number of seconds, possibly fractional.
			seconds, ok := value[0].(float64)
			if !ok {
				continue
			}

			sample, ok := value[1].(string)
			if !ok {
				continue
			}

			samples = append(samples, MetricSample{
				Timestamp: time.Unix(0, int64(seconds*float64(time.Second))),
				Value:     sample,
			})
		}

		series = append(series, MetricSeries{Labels: metric.Metric, Samples: samples})
	}

	return series
}

// Empty reports whether the query matched nothing.
func (q *QueryResult) Empty() (empty bool) {
	empty = len(q.Entries) == 0 && len(q.Series) == 0
	return empty
}

// FormatResultAsText formats the query result as human-readable text.
func (q *QueryResult) FormatResultAsText() (result string) {
	if len(q.Series) > 0 {
		result = q.formatSeries()
		return result
	}

	if len(q.Entries) == 0 {
		result = "No log entries found."
		return result
	}

	var builder strings.Builder

	fmt.Fprintf(&builder, "Found %d log entries:\n\n", len(q.Entries))

	for i, entry := range q.Entries {
		fmt.Fprintf(&builder, "[%d] %s\n", i+1, entry.Timestamp.Format(time.RFC3339))
		fmt.Fprintf(&builder, "%s\n\n", entry.Line)
	}

	fmt.Fprintf(&builder, "Query completed in %s\n", q.Stats.Duration)

	result = builder.String()
	return result
}

// formatSeries renders a metric query's series, each under the labels that
// identify it.
func (q *QueryResult) formatSeries() (result string) {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Found %d series:\n\n", len(q.Series))

	for _, series := range q.Series {
		fmt.Fprintf(&builder, "%s\n", FormatLabels(series.Labels))

		for _, sample := range series.Samples {
			fmt.Fprintf(&builder, "  %s  %s\n", sample.Timestamp.UTC().Format(time.RFC3339), sample.Value)
		}

		builder.WriteString("\n")
	}

	fmt.Fprintf(&builder, "Query completed in %s\n", q.Stats.Duration)

	result = builder.String()

	return result
}

// FormatLabels renders a label set as a LogQL selector, keys sorted.
func FormatLabels(labels map[string]string) (rendered string) {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%q", key, labels[key]))
	}

	rendered = "{" + strings.Join(pairs, ", ") + "}"

	return rendered
}

// lokiQueryRangeResponse represents the JSON response from Loki query_range endpoint.
type lokiQueryRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string       `json:"resultType"`
		Result     []lokiResult `json:"result"`
	} `json:"data"`
}

// lokiResult is one element of a query_range result: a log stream (Stream,
// with [nanosecond-string, line] values) or a metric series (Metric, with
// [seconds, value-string] values).
type lokiResult struct {
	Stream map[string]string `json:"stream"`
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

// lokiLabelResponse represents the JSON response from Loki's label endpoints.
type lokiLabelResponse struct {
	Status string   `json:"status"`
	Data   []string `json:"data"`
}

// validLabelName reports whether name is a legal Loki label name. The name is
// placed in a URL path, so anything else is refused before a request is built.
func validLabelName(name string) (valid bool) {
	if name == "" {
		return valid
	}

	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		isDigit := r >= '0' && r <= '9'

		if !isLetter && (!isDigit || i == 0) {
			return valid
		}
	}

	valid = true

	return valid
}

// parseTimeRange parses a query window. An empty or "now" end is the present.
func parseTimeRange(startStr, endStr string) (startTime time.Time, endTime time.Time, err error) {
	startTime, err = parseTimeOrDuration(startStr)
	if err != nil {
		err = fmt.Errorf("parsing start time: %w", err)
		return startTime, endTime, err
	}

	endTime = time.Now()

	if endStr != "" && endStr != "now" {
		endTime, err = parseTimeOrDuration(endStr)
		if err != nil {
			err = fmt.Errorf("parsing end time: %w", err)
			return startTime, endTime, err
		}
	}

	return startTime, endTime, err
}

// resolveStep returns the metric-query resolution: the caller's, or one that
// yields lokiDefaultSteps samples across the window.
func resolveStep(stepStr string, startTime, endTime time.Time) (step time.Duration, err error) {
	if stepStr != "" {
		step, err = time.ParseDuration(stepStr)
		if err != nil {
			err = fmt.Errorf("parsing step (expected a duration such as 30s, 5m, 1h): %w", err)
			return step, err
		}

		if step <= 0 {
			err = fmt.Errorf("step must be positive, got %s", stepStr)
			return step, err
		}

		return step, err
	}

	step = (endTime.Sub(startTime) / lokiDefaultSteps).Truncate(time.Second)
	if step < time.Second {
		step = time.Second
	}

	return step, err
}

// parseTimeOrDuration parses a time string that can be either RFC3339 format
// or a relative duration (e.g., "1h", "24h").
func parseTimeOrDuration(timeStr string) (result time.Time, err error) {
	var parsed time.Time
	var duration time.Duration

	// Try parsing as RFC3339 first
	parsed, err = time.Parse(time.RFC3339, timeStr)
	if err == nil {
		result = parsed
		err = nil
		return result, err
	}

	// Try parsing as duration
	duration, err = time.ParseDuration(timeStr)
	if err != nil {
		err = fmt.Errorf("invalid time format (expected RFC3339 or duration): %w", err)
		return result, err
	}

	result = time.Now().Add(-duration)
	err = nil

	return result, err
}
