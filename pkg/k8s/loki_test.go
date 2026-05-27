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
// MCP layer to inject the allowlist into the query_loki tool description.
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
