package k8s

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDummyLokiServer stands up an httptest server that returns a minimal
// valid /loki/api/v1/query_range response and records the request headers
// it observed. Tests inspect the captured header to verify what the client
// actually sent.
func newDummyLokiServer(t *testing.T, captured *http.Header) (server *httptest.Server) {
	t.Helper()
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[],"stats":{}}}`))
	}))
	return server
}

func newTestLokiClient(t *testing.T, endpoint string) (client *LokiClient) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client = NewLokiClient(endpoint, logger)
	return client
}

func sampleQueryRequest() (req QueryRequest) {
	req = QueryRequest{
		Query: `{job="test"}`,
		Start: "1h",
		End:   "now",
		Limit: 10,
	}
	return req
}

// TestLokiQueryNoTenantSendsNoHeader verifies the backwards-compatible path:
// a client with no tenant configuration sends no X-Scope-OrgID header, so
// auth_enabled:false deployments keep working unchanged.
func TestLokiQueryNoTenantSendsNoHeader(t *testing.T) {
	var captured http.Header
	server := newDummyLokiServer(t, &captured)
	t.Cleanup(server.Close)

	client := newTestLokiClient(t, server.URL)

	_, err := client.Query(context.Background(), sampleQueryRequest())
	require.NoError(t, err)
	assert.Empty(t, captured.Get(LokiTenantHeader), "no tenant configured must send no X-Scope-OrgID")
}

// TestLokiQueryDefaultTenantSetsHeader verifies a configured default tenant
// gets applied when the caller doesn't specify one.
func TestLokiQueryDefaultTenantSetsHeader(t *testing.T) {
	var captured http.Header
	server := newDummyLokiServer(t, &captured)
	t.Cleanup(server.Close)

	client := newTestLokiClient(t, server.URL)
	err := client.ConfigureTenants("monitoring", nil)
	require.NoError(t, err)

	_, err = client.Query(context.Background(), sampleQueryRequest())
	require.NoError(t, err)
	assert.Equal(t, "monitoring", captured.Get(LokiTenantHeader))
}

// TestLokiQueryCallerTenantOverridesDefault verifies a request-level tenant
// takes precedence over the configured default.
func TestLokiQueryCallerTenantOverridesDefault(t *testing.T) {
	var captured http.Header
	server := newDummyLokiServer(t, &captured)
	t.Cleanup(server.Close)

	client := newTestLokiClient(t, server.URL)
	err := client.ConfigureTenants("monitoring", []string{"monitoring", "cloudtrail"})
	require.NoError(t, err)

	req := sampleQueryRequest()
	req.Tenant = "cloudtrail"

	_, err = client.Query(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "cloudtrail", captured.Get(LokiTenantHeader))
}

// TestLokiQueryPipeDelimitedMultiTenant verifies Loki's pipe-delimited
// multi-tenant read syntax is forwarded intact, and each segment is
// validated against the allowlist.
func TestLokiQueryPipeDelimitedMultiTenant(t *testing.T) {
	var captured http.Header
	server := newDummyLokiServer(t, &captured)
	t.Cleanup(server.Close)

	client := newTestLokiClient(t, server.URL)
	err := client.ConfigureTenants("monitoring", []string{"monitoring", "cloudtrail", "self-monitoring"})
	require.NoError(t, err)

	req := sampleQueryRequest()
	req.Tenant = "monitoring|cloudtrail"

	_, err = client.Query(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "monitoring|cloudtrail", captured.Get(LokiTenantHeader))
}

// TestLokiQueryRejectsTenantNotInAllowlist verifies the allowlist actually
// blocks a caller-supplied tenant that isn't on the list. The HTTP request
// must not be issued at all.
func TestLokiQueryRejectsTenantNotInAllowlist(t *testing.T) {
	var captured http.Header
	hitCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		captured = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	client := newTestLokiClient(t, server.URL)
	err := client.ConfigureTenants("monitoring", []string{"monitoring", "cloudtrail"})
	require.NoError(t, err)

	req := sampleQueryRequest()
	req.Tenant = "production"

	_, err = client.Query(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "production")
	assert.Contains(t, err.Error(), "allowlist")
	assert.Equal(t, 0, hitCount, "no HTTP request must be issued when tenant fails allowlist check")
	assert.Empty(t, captured.Get(LokiTenantHeader))
}

// TestLokiQueryRejectsPipeDelimitedWhenAnyElementNotInAllowlist verifies
// every segment of a pipe-delimited tenant string is checked.
func TestLokiQueryRejectsPipeDelimitedWhenAnyElementNotInAllowlist(t *testing.T) {
	var captured http.Header
	server := newDummyLokiServer(t, &captured)
	t.Cleanup(server.Close)

	client := newTestLokiClient(t, server.URL)
	err := client.ConfigureTenants("monitoring", []string{"monitoring", "cloudtrail"})
	require.NoError(t, err)

	req := sampleQueryRequest()
	req.Tenant = "monitoring|production"

	_, err = client.Query(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "production")
}

// TestLokiQueryRequiresTenantWhenAllowlistSetAndNoDefault verifies that an
// allowlist configured without a default forces every caller to specify a
// tenant explicitly — silent fall-through to no header would be a bug.
func TestLokiQueryRequiresTenantWhenAllowlistSetAndNoDefault(t *testing.T) {
	var captured http.Header
	server := newDummyLokiServer(t, &captured)
	t.Cleanup(server.Close)

	client := newTestLokiClient(t, server.URL)
	err := client.ConfigureTenants("", []string{"monitoring", "cloudtrail"})
	require.NoError(t, err)

	_, err = client.Query(context.Background(), sampleQueryRequest())
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "tenant")
}

// TestConfigureTenantsRejectsDefaultNotInAllowlist verifies the cross-field
// validation: a default that isn't on the allowlist would defeat the
// allowlist on every query, so reject at configure time.
func TestConfigureTenantsRejectsDefaultNotInAllowlist(t *testing.T) {
	client := newTestLokiClient(t, "http://example:3100")

	err := client.ConfigureTenants("production", []string{"monitoring", "cloudtrail"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "production")
	assert.Contains(t, err.Error(), "allowlist")
}

// TestConfigureTenantsAcceptsEmptyAllowlistWithDefault verifies the
// single-tenant auth_enabled:true case: a default with no allowlist is
// valid, and all queries use the default.
func TestConfigureTenantsAcceptsEmptyAllowlistWithDefault(t *testing.T) {
	client := newTestLokiClient(t, "http://example:3100")
	err := client.ConfigureTenants("monitoring", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{}, client.AllowedTenants())
}

// TestAllowedTenantsReturnsConfiguredList verifies the accessor used by the
// MCP layer to inject the allowlist into the loki_query tool description.
func TestAllowedTenantsReturnsConfiguredList(t *testing.T) {
	client := newTestLokiClient(t, "http://example:3100")
	err := client.ConfigureTenants("monitoring", []string{"monitoring", "cloudtrail", "self-monitoring"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"monitoring", "cloudtrail", "self-monitoring"}, client.AllowedTenants())
}

// TestNewLokiClientIsolatesTransport guards the per-client Transport
// pattern. nil falls back to http.DefaultTransport (package-global) and
// parallel tests can yank idle connections from unrelated requests —
// see pkg/mcp/tempo.go's NewTempoClient comment for the original failure.
func TestNewLokiClientIsolatesTransport(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client := NewLokiClient("http://loki.example.com", logger)
	require.NotNil(t, client.httpClient.Transport, "client must have its own Transport; nil falls back to http.DefaultTransport")
}

// recordedRequest is what a fake Loki saw: the path, query and tenant header.
type recordedRequest struct {
	path   string
	query  map[string]string
	tenant string
}

// newRecordingLokiServer stands up a fake Loki that answers every request with
// body and records what it was asked.
func newRecordingLokiServer(t *testing.T, body string, seen *[]recordedRequest) (server *httptest.Server) {
	t.Helper()

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := map[string]string{}
		for key := range r.URL.Query() {
			query[key] = r.URL.Query().Get(key)
		}

		*seen = append(*seen, recordedRequest{path: r.URL.Path, query: query, tenant: r.Header.Get(LokiTenantHeader)})

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server
}

// TestLokiQueryReturnsMetricSeries is the regression for metric queries being
// read as log streams: a matrix result parsed that way has no entries, so a
// query that found data was reported as having found no log entries.
func TestLokiQueryReturnsMetricSeries(t *testing.T) {
	t.Parallel()

	var seen []recordedRequest

	body := `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"service_name":"my-service","namespace":"apps"},"values":[[1759420560,"42"],[1759420860,"7"]]},` +
		`{"metric":{"service_name":"other-service","namespace":"apps"},"values":[[1759420560,"3"]]}]}}`
	server := newRecordingLokiServer(t, body, &seen)

	result, err := newTestLokiClient(t, server.URL).Query(context.Background(), QueryRequest{
		Query: `sum by (service_name, namespace) (count_over_time({namespace="apps"}[5m]))`,
		Start: "1h",
	})
	require.NoError(t, err)

	assert.False(t, result.Empty(), "a metric query with series is not an empty result")
	require.Len(t, result.Series, 2)
	assert.Equal(t, "my-service", result.Series[0].Labels["service_name"])
	require.Len(t, result.Series[0].Samples, 2)
	assert.Equal(t, "42", result.Series[0].Samples[0].Value)
	assert.Equal(t, int64(1759420560), result.Series[0].Samples[0].Timestamp.Unix())

	text := result.FormatResultAsText()
	assert.Contains(t, text, "Found 2 series")
	assert.Contains(t, text, `{namespace="apps", service_name="my-service"}`, "each series is shown under its labels")
	assert.Contains(t, text, "2025-10-02T15:56:00Z  42")
	assert.NotContains(t, text, "No log entries found")
}

func TestLokiQueryStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		step     string
		wantStep string
		wantErr  string
	}{
		{name: "default yields a bounded number of samples", step: "", wantStep: "180"},
		{name: "explicit step is passed through", step: "5m", wantStep: "300"},
		{name: "unparseable step is rejected", step: "soon", wantErr: "step"},
		{name: "non-positive step is rejected", step: "-5m", wantErr: "positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var seen []recordedRequest

			server := newRecordingLokiServer(t, `{"status":"success","data":{"resultType":"streams","result":[]}}`, &seen)

			_, err := newTestLokiClient(t, server.URL).Query(context.Background(), QueryRequest{
				Query: `{job="test"}`,
				Start: "1h",
				Step:  tt.step,
			})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, seen, "an invalid step must not reach Loki")

				return
			}

			require.NoError(t, err)
			require.Len(t, seen, 1)
			assert.Equal(t, tt.wantStep, seen[0].query["step"])
		})
	}
}

func TestLokiLabelNames(t *testing.T) {
	t.Parallel()

	var seen []recordedRequest

	server := newRecordingLokiServer(t, `{"status":"success","data":["service_name","namespace","app"]}`, &seen)

	client := newTestLokiClient(t, server.URL)
	require.NoError(t, client.ConfigureTenants("monitoring", []string{"monitoring", "logging"}))

	names, err := client.LabelNames(context.Background(), LabelRequest{Tenant: "logging", Query: `{namespace="apps"}`})
	require.NoError(t, err)

	assert.Equal(t, []string{"app", "namespace", "service_name"}, names, "names come back sorted")
	require.Len(t, seen, 1)
	assert.Equal(t, "/loki/api/v1/labels", seen[0].path)
	assert.Equal(t, "logging", seen[0].tenant, "the requested tenant is sent as X-Scope-OrgID")
	assert.Equal(t, `{namespace="apps"}`, seen[0].query["query"])
	assert.NotEmpty(t, seen[0].query["start"], "a default window is applied when no start is given")
}

func TestLokiLabelNamesHandlesNoLabels(t *testing.T) {
	t.Parallel()

	var seen []recordedRequest

	server := newRecordingLokiServer(t, `{"status":"success"}`, &seen)

	names, err := newTestLokiClient(t, server.URL).LabelNames(context.Background(), LabelRequest{})
	require.NoError(t, err)
	assert.Empty(t, names)
	require.Len(t, seen, 1)
	assert.Empty(t, seen[0].tenant, "no tenant configured must send no X-Scope-OrgID")
}

func TestLokiLabelValues(t *testing.T) {
	t.Parallel()

	var seen []recordedRequest

	server := newRecordingLokiServer(t, `{"status":"success","data":["my-service","another-service"]}`, &seen)

	client := newTestLokiClient(t, server.URL)
	require.NoError(t, client.ConfigureTenants("monitoring", []string{"monitoring", "logging"}))

	values, err := client.LabelValues(context.Background(), "service_name", LabelRequest{Start: "2h"})
	require.NoError(t, err)

	assert.Equal(t, []string{"another-service", "my-service"}, values)
	require.Len(t, seen, 1)
	assert.Equal(t, "/loki/api/v1/label/service_name/values", seen[0].path)
	assert.Equal(t, "monitoring", seen[0].tenant, "the default tenant applies when none is requested")
}

func TestLokiLabelLookupRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		label   string
		tenant  string
		wantErr string
	}{
		{name: "path traversal in label", label: "../../config", wantErr: "invalid label name"},
		{name: "empty label", label: "", wantErr: "invalid label name"},
		{name: "label starting with a digit", label: "9lives", wantErr: "invalid label name"},
		{name: "tenant outside the allowlist", label: "service_name", tenant: "production", wantErr: "allowlist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var seen []recordedRequest

			server := newRecordingLokiServer(t, `{"status":"success","data":[]}`, &seen)

			client := newTestLokiClient(t, server.URL)
			require.NoError(t, client.ConfigureTenants("monitoring", []string{"monitoring", "logging"}))

			_, err := client.LabelValues(context.Background(), tt.label, LabelRequest{Tenant: tt.tenant})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, seen, "rejected input must not reach Loki")
		})
	}
}

func TestLokiLabelLookupSurfacesBackendFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no org id", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	_, err := newTestLokiClient(t, server.URL).LabelNames(context.Background(), LabelRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "no org id")
}

func TestLokiDiscoveryAndUnsearchedTenants(t *testing.T) {
	t.Parallel()

	client := newTestLokiClient(t, "http://example:3100")
	require.NoError(t, client.ConfigureTenants("monitoring", []string{"monitoring", "logging", "cloudtrail"}))

	tenants, err := client.DiscoveryTenants("")
	require.NoError(t, err)
	assert.Equal(t, []string{"cloudtrail", "logging", "monitoring"}, tenants, "unnamed discovery inspects every allowed tenant")

	tenants, err = client.DiscoveryTenants("logging")
	require.NoError(t, err)
	assert.Equal(t, []string{"logging"}, tenants)

	assert.Equal(t, []string{"cloudtrail", "logging"}, client.UnsearchedTenants(""), "a default-tenant query leaves the others unsearched")
	assert.Equal(t, []string{"cloudtrail"}, client.UnsearchedTenants("monitoring|logging"))

	single := newTestLokiClient(t, "http://example:3100")

	tenants, err = single.DiscoveryTenants("")
	require.NoError(t, err)
	assert.Equal(t, []string{""}, tenants, "without tenancy, discovery runs once with no header")
	assert.Empty(t, single.UnsearchedTenants(""))
}

func TestFormatLabels(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "{}", FormatLabels(nil))
	assert.Equal(t, `{app="x", namespace="apps"}`, FormatLabels(map[string]string{"namespace": "apps", "app": "x"}))
}
