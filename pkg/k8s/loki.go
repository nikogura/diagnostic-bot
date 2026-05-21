package k8s

import (
	"context"
	"encoding/json"
	"errors"
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
)

// LokiClient handles queries to the Loki log aggregation system.
type LokiClient struct {
	endpoint       string
	httpClient     *http.Client
	logger         *slog.Logger
	defaultTenant  string
	allowedTenants map[string]struct{}
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
		},
		logger:         logger,
		allowedTenants: map[string]struct{}{},
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
	allowSet := make(map[string]struct{}, len(allowedTenants))
	for _, t := range allowedTenants {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		allowSet[t] = struct{}{}
	}

	if defaultTenant != "" && len(allowSet) > 0 {
		_, ok := allowSet[defaultTenant]
		if !ok {
			err = fmt.Errorf("default tenant %q is not in the allowlist %v", defaultTenant, allowedTenants)
			return err
		}
	}

	l.defaultTenant = defaultTenant
	l.allowedTenants = allowSet
	return err
}

// AllowedTenants returns the configured tenant allowlist in sorted order.
// The MCP layer injects this list into the query_loki tool description so
// the calling LLM can discover which tenants are queryable.
func (l *LokiClient) AllowedTenants() (result []string) {
	result = make([]string, 0, len(l.allowedTenants))
	for t := range l.allowedTenants {
		result = append(result, t)
	}
	sort.Strings(result)
	return result
}

// resolveTenant returns the X-Scope-OrgID value to send for a given
// QueryRequest, after applying the default and validating against the
// allowlist. Empty return with nil err means no header should be sent
// (the auth_enabled:false backwards-compatible path).
func (l *LokiClient) resolveTenant(req QueryRequest) (tenant string, err error) {
	tenant = strings.TrimSpace(req.Tenant)
	if tenant == "" {
		tenant = l.defaultTenant
	}

	if tenant == "" {
		if len(l.allowedTenants) > 0 {
			err = errors.New("loki tenant allowlist is configured but no tenant was specified — set a default via LOKI_DEFAULT_ORG_ID or pass tenant explicitly")
			return tenant, err
		}
		return tenant, err
	}

	if len(l.allowedTenants) == 0 {
		return tenant, err
	}

	// Loki accepts pipe-delimited tenants on read paths (query_range, labels,
	// series). Validate every segment against the allowlist.
	for _, segment := range strings.Split(tenant, "|") {
		segment = strings.TrimSpace(segment)
		_, ok := l.allowedTenants[segment]
		if !ok {
			err = fmt.Errorf("tenant %q is not in the loki allowlist", segment)
			return tenant, err
		}
	}
	return tenant, err
}

// QueryRequest represents a query request to Loki.
type QueryRequest struct {
	Query  string
	Start  string // RFC3339 format or relative duration (e.g., "1h", "24h")
	End    string // RFC3339 format or "now"
	Limit  int
	Tenant string // X-Scope-OrgID value. Empty means use the client's default.
}

// QueryResult represents the result from a Loki query.
type QueryResult struct {
	Entries   []LogEntry
	Stats     QueryStats
	RawResult string
}

// LogEntry represents a single log entry from Loki.
type LogEntry struct {
	Timestamp time.Time
	Line      string
	Labels    map[string]string
}

// QueryStats provides statistics about the query execution.
type QueryStats struct {
	TotalEntries int
	BytesQueried int64
	Duration     time.Duration
}

// Query executes a LogQL query against Loki.
//
//nolint:gocognit,funlen // Loki query execution with parsing and error handling is inherently complex
func (l *LokiClient) Query(ctx context.Context, req QueryRequest) (result QueryResult, err error) {
	var startTime time.Time
	var endTime time.Time
	var httpReq *http.Request
	var resp *http.Response
	var body []byte

	start := time.Now()

	// Parse start and end times
	startTime, err = parseTimeOrDuration(req.Start)
	if err != nil {
		err = fmt.Errorf("parsing start time: %w", err)
		return result, err
	}

	endTime = time.Now()
	if req.End != "" && req.End != "now" {
		endTime, err = parseTimeOrDuration(req.End)
		if err != nil {
			err = fmt.Errorf("parsing end time: %w", err)
			return result, err
		}
	}

	// Set default and max limit
	if req.Limit == 0 {
		req.Limit = 100
	}

	if req.Limit > MaxLokiResults {
		req.Limit = MaxLokiResults
	}

	l.logger.InfoContext(ctx, "executing Loki query",
		slog.String("query", req.Query),
		slog.Time("start", startTime),
		slog.Time("end", endTime),
		slog.Int("limit", req.Limit))

	// Build query URL
	queryURL := fmt.Sprintf("%s/loki/api/v1/query_range", l.endpoint)

	params := url.Values{}
	params.Set("query", req.Query)
	params.Set("start", strconv.FormatInt(startTime.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(endTime.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(req.Limit))

	fullURL := fmt.Sprintf("%s?%s", queryURL, params.Encode())

	// Resolve and validate the Loki tenant (X-Scope-OrgID) before issuing
	// the request. Multi-tenant deployments (auth_enabled: true) refuse
	// queries without a tenant header — the allowlist check runs first so
	// invalid tenants never hit the wire.
	var tenant string
	tenant, err = l.resolveTenant(req)
	if err != nil {
		return result, err
	}

	// Execute HTTP request
	httpReq, err = http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		err = fmt.Errorf("creating HTTP request: %w", err)
		return result, err
	}

	if tenant != "" {
		httpReq.Header.Set(LokiTenantHeader, tenant)
	}

	resp, err = l.httpClient.Do(httpReq)
	if err != nil {
		err = fmt.Errorf("executing HTTP request: %w", err)
		return result, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var statusBody []byte

		statusBody, _ = io.ReadAll(resp.Body)
		metrics.LokiQueriesTotal.WithLabelValues("error").Inc()
		err = fmt.Errorf("loki query failed with status %d: %s", resp.StatusCode, string(statusBody))

		return result, err
	}

	// Parse response
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("reading response body: %w", err)
		return result, err
	}

	var lokiResp lokiQueryRangeResponse

	err = json.Unmarshal(body, &lokiResp)
	if err != nil {
		err = fmt.Errorf("parsing Loki response: %w", err)
		return result, err
	}

	if lokiResp.Status != "success" {
		err = fmt.Errorf("loki query unsuccessful: %s", lokiResp.Status)
		return result, err
	}

	// Extract log entries
	var entries []LogEntry

	for _, stream := range lokiResp.Data.Result {
		labels := stream.Stream

		for _, value := range stream.Values {
			if len(value) < 2 {
				continue
			}

			// Parse timestamp (nanoseconds)
			timestampStr, ok := value[0].(string)
			if !ok {
				continue
			}

			var timestamp int64
			_, scanErr := fmt.Sscanf(timestampStr, "%d", &timestamp)

			if scanErr != nil {
				continue
			}

			// Parse log line
			line, ok := value[1].(string)
			if !ok {
				continue
			}

			entries = append(entries, LogEntry{
				Timestamp: time.Unix(0, timestamp),
				Line:      line,
				Labels:    labels,
			})
		}
	}

	duration := time.Since(start)

	// Record successful query
	metrics.LokiQueriesTotal.WithLabelValues("success").Inc()

	l.logger.InfoContext(ctx, "Loki query completed",
		slog.Int("entries", len(entries)),
		slog.Duration("duration", duration))

	result = QueryResult{
		Entries: entries,
		Stats: QueryStats{
			TotalEntries: len(entries),
			Duration:     duration,
		},
		RawResult: string(body),
	}

	return result, err
}

// FormatResultAsText formats the query result as human-readable text.
func (q *QueryResult) FormatResultAsText() (result string) {
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

// lokiQueryRangeResponse represents the JSON response from Loki query_range endpoint.
type lokiQueryRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]interface{}   `json:"values"`
		} `json:"result"`
	} `json:"data"`
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
