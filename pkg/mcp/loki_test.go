// Copyright © 2026 Nik Ogura <nik.ogura@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikogura/diagnostic-bot/pkg/authz"
	"github.com/nikogura/diagnostic-bot/pkg/k8s"
)

const (
	lokiEmptyStreams = `{"status":"success","data":{"resultType":"streams","result":[]}}`
	lokiNoLabels     = `{"status":"success","data":[]}`
)

// fakeLoki is a Loki whose answer depends on the tenant asking, recording
// which tenants and paths it was asked for.
type fakeLoki struct {
	mu sync.Mutex

	// byTenant maps an X-Scope-OrgID value ("" for none) to the body returned.
	byTenant map[string]string

	// failing lists tenants answered with a 500.
	failing map[string]bool

	tenants []string
	paths   []string
}

func (f *fakeLoki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get(k8s.LokiTenantHeader)

	f.mu.Lock()
	f.tenants = append(f.tenants, tenantID)
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()

	if f.failing[tenantID] {
		http.Error(w, "tenant unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(f.byTenant[tenantID]))
}

// seen returns the tenants the fake was asked for, in order.
func (f *fakeLoki) seen() (tenants []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	tenants = append(tenants, f.tenants...)

	return tenants
}

// newLokiToolServer builds a Server over a fake Loki. allowedTenants empty
// leaves tenancy unconfigured.
func newLokiToolServer(t *testing.T, fake *fakeLoki, defaultTenant string, allowedTenants []string) (server *Server) {
	t.Helper()

	backend := httptest.NewServer(fake)
	t.Cleanup(backend.Close)

	logger := slog.New(slog.DiscardHandler)
	client := k8s.NewLokiClient(backend.URL, logger)

	if defaultTenant != "" || len(allowedTenants) > 0 {
		require.NoError(t, client.ConfigureTenants(defaultTenant, allowedTenants))
	}

	server = &Server{lokiClient: client, logger: logger}

	return server
}

// TestLokiLabelValuesInspectsEveryAllowedTenant is the multi-tenant case the
// label tools exist for: with no tenant named, each allowed tenant is asked
// separately and reported under its own heading, so the caller learns which
// tenant holds a service instead of guessing from tenant names.
func TestLokiLabelValuesInspectsEveryAllowedTenant(t *testing.T) {
	t.Parallel()

	fake := &fakeLoki{byTenant: map[string]string{
		"monitoring": `{"status":"success","data":["my-service","ingress-nginx"]}`,
		"logging":    `{"status":"success","data":["relay"]}`,
		"cloudtrail": lokiNoLabels,
	}}
	server := newLokiToolServer(t, fake, "monitoring", []string{"monitoring", "logging", "cloudtrail"})

	result, err := server.executeLokiLabelValues(context.Background(), map[string]interface{}{"label": "service_name"})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"cloudtrail", "logging", "monitoring"}, fake.seen(),
		"every allowed tenant is asked, not only the default")
	assert.Contains(t, result, `values of label "service_name"`)
	assert.Contains(t, result, "Tenant monitoring (2): ingress-nginx, my-service")
	assert.Contains(t, result, "Tenant logging (1): relay")
	assert.Contains(t, result, "Tenant cloudtrail: none")

	for _, path := range fake.paths {
		assert.Equal(t, "/loki/api/v1/label/service_name/values", path)
	}
}

func TestLokiLabelNamesHonoursRequestedTenants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tenant    string
		wantAsked []string
		wantErr   string
	}{
		{name: "one tenant", tenant: "logging", wantAsked: []string{"logging"}},
		{name: "joined tenants are asked separately", tenant: "logging|cloudtrail", wantAsked: []string{"logging", "cloudtrail"}},
		{name: "tenant outside the allowlist", tenant: "production", wantErr: "allowlist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeLoki{byTenant: map[string]string{
				"monitoring": `{"status":"success","data":["service_name"]}`,
				"logging":    `{"status":"success","data":["app","namespace"]}`,
				"cloudtrail": `{"status":"success","data":["aws_region"]}`,
			}}
			server := newLokiToolServer(t, fake, "monitoring", []string{"monitoring", "logging", "cloudtrail"})

			result, err := server.executeLokiLabelNames(context.Background(), map[string]interface{}{"tenant": tt.tenant})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, fake.seen(), "a rejected tenant must not reach Loki")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantAsked, fake.seen())
			assert.Contains(t, result, "Tenant logging (2): app, namespace")
			assert.NotContains(t, result, "Tenant monitoring", "a tenant that was not requested is not inspected")
		})
	}
}

// TestLokiLabelNamesWithoutTenancy covers a Loki with multi-tenancy off, where
// everything is filed under Loki's built-in tenant: one request, no header, and
// no tenant heading to mislead.
func TestLokiLabelNamesWithoutTenancy(t *testing.T) {
	t.Parallel()

	fake := &fakeLoki{byTenant: map[string]string{"": `{"status":"success","data":["namespace","service_name"]}`}}
	server := newLokiToolServer(t, fake, "", nil)

	result, err := server.executeLokiLabelNames(context.Background(), map[string]interface{}{})
	require.NoError(t, err)

	assert.Equal(t, []string{""}, fake.seen(), "exactly one request, with no X-Scope-OrgID")
	assert.Contains(t, result, "All streams (2): namespace, service_name")
	assert.NotContains(t, result, "Tenant ")
}

func TestLokiLabelLookupReportsTenantFailures(t *testing.T) {
	t.Parallel()

	answers := map[string]string{
		"monitoring": `{"status":"success","data":["my-service"]}`,
		"logging":    `{"status":"success","data":["relay"]}`,
	}

	t.Run("one tenant failing is reported beside the others", func(t *testing.T) {
		t.Parallel()

		fake := &fakeLoki{byTenant: answers, failing: map[string]bool{"logging": true}}
		server := newLokiToolServer(t, fake, "monitoring", []string{"monitoring", "logging"})

		result, err := server.executeLokiLabelValues(context.Background(), map[string]interface{}{"label": "service_name"})
		require.NoError(t, err)
		assert.Contains(t, result, "Tenant monitoring (1): my-service")
		assert.Contains(t, result, "Tenant logging: error:")
		assert.Contains(t, result, "500")
	})

	t.Run("every tenant failing fails the call", func(t *testing.T) {
		t.Parallel()

		fake := &fakeLoki{byTenant: answers, failing: map[string]bool{"logging": true, "monitoring": true}}
		server := newLokiToolServer(t, fake, "monitoring", []string{"monitoring", "logging"})

		_, err := server.executeLokiLabelValues(context.Background(), map[string]interface{}{"label": "service_name"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loki label lookup failed")
	})
}

func TestLokiLabelValuesRequiresLabel(t *testing.T) {
	t.Parallel()

	fake := &fakeLoki{}
	server := newLokiToolServer(t, fake, "", nil)

	_, err := server.executeLokiLabelValues(context.Background(), map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "label parameter is required")
	assert.Empty(t, fake.seen())
}

func TestFormatLokiLabelSectionBoundsLongLists(t *testing.T) {
	t.Parallel()

	items := make([]string, 0, maxLokiLabelItems+25)
	for i := range maxLokiLabelItems + 25 {
		items = append(items, fmt.Sprintf("value-%04d", i))
	}

	section := formatLokiLabelSection("monitoring", items, nil)

	assert.Contains(t, section, fmt.Sprintf("Tenant monitoring (%d):", maxLokiLabelItems+25), "the true count is reported")
	assert.Contains(t, section, fmt.Sprintf("showing %d of %d", maxLokiLabelItems, maxLokiLabelItems+25))
	assert.Contains(t, section, "value-0499")
	assert.NotContains(t, section, "value-0500", "values past the bound are withheld")
}

// TestLokiToolsWithoutLokiFailCleanly keeps an unconfigured Loki from being a
// nil dereference: every Loki tool is routed and answers with a clear error.
func TestLokiToolsWithoutLokiFailCleanly(t *testing.T) {
	t.Parallel()

	server := &Server{logger: slog.New(slog.DiscardHandler)}

	for _, name := range []string{toolLokiQuery, toolLokiLabelNames, toolLokiLabelValues} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := server.dispatchToolCall(context.Background(), name, map[string]interface{}{
				"query": `{job="test"}`, "start": "1h", "label": "service_name",
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "LOKI_ENDPOINT")
			assert.NotContains(t, err.Error(), "unknown tool", "%s must be routed by the dispatcher", name)
		})
	}
}

// TestLokiQueryReturnsMetricSeries verifies a metric query reaches the caller
// as series rather than as an empty log result.
func TestLokiQueryReturnsMetricSeries(t *testing.T) {
	t.Parallel()

	fake := &fakeLoki{byTenant: map[string]string{
		"": `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"service_name":"my-service"},"values":[[1759420560,"42"]]}]}}`,
	}}
	server := newLokiToolServer(t, fake, "", nil)

	result, err := server.executeLokiQuery(context.Background(), map[string]interface{}{
		"query": `sum by (service_name) (count_over_time({service_name=~".+"}[5m]))`,
		"start": "1h",
	})
	require.NoError(t, err)

	assert.Contains(t, result, "Found 1 series")
	assert.Contains(t, result, `{service_name="my-service"}`)
	assert.NotContains(t, result, "No log entries found")
	assert.NotContains(t, result, "Nothing matched", "a query that found data carries no empty-result note")
}

// TestEmptyLokiQueryNamesWhatWasNotSearched is the regression for an empty
// Loki answer being taken as proof the logs do not exist. The answer covered
// one tenant; it must say so, and say how to find the right labels.
func TestEmptyLokiQueryNamesWhatWasNotSearched(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, "")
	t.Setenv(envCloudWatchAssumeRole, "")

	fake := &fakeLoki{byTenant: map[string]string{"monitoring": lokiEmptyStreams, "monitoring|logging": lokiEmptyStreams}}
	server := newLokiToolServer(t, fake, "monitoring", []string{"monitoring", "logging", "cloudtrail"})

	args := map[string]interface{}{"query": `{service_name="relay"}`, "start": "1h"}

	result, err := server.executeLokiQuery(context.Background(), args)
	require.NoError(t, err)

	assert.Contains(t, result, "No log entries found.")
	assert.Contains(t, result, "does not show these logs do not exist")
	assert.Contains(t, result, "did not search: cloudtrail, logging", "the default-tenant query left two tenants unsearched")
	assert.Contains(t, result, "loki_label_names, loki_label_values")
	assert.NotContains(t, result, "CloudWatch", "an unconfigured backend is not suggested")
	assert.NotContains(t, result, "Kubernetes")

	// Naming a tenant narrows what is left.
	args["tenant"] = "monitoring|logging"

	result, err = server.executeLokiQuery(context.Background(), args)
	require.NoError(t, err)
	assert.Contains(t, result, "did not search: cloudtrail (")
}

// TestEmptyLogSearchNamesOtherConfiguredBackends verifies an empty Loki answer
// points at the other log backends this deployment has — and only those.
func TestEmptyLogSearchNamesOtherConfiguredBackends(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, "")
	t.Setenv(envCloudWatchAssumeRole, "arn:aws:iam::123456789012:role/logs-reader")

	fake := &fakeLoki{byTenant: map[string]string{"": lokiEmptyStreams}}
	server := newLokiToolServer(t, fake, "", nil)
	server.k8sClusters = map[string]*k8s.Agent{"default": nil}

	result, err := server.executeLokiQuery(context.Background(),
		map[string]interface{}{"query": `{service_name="relay"}`, "start": "1h"})
	require.NoError(t, err)

	assert.Contains(t, result, "CloudWatch Logs (cloudwatch_logs_list_groups, cloudwatch_logs_query)")
	assert.Contains(t, result, "Kubernetes pod logs (k8s_pod_logs)")
	assert.NotContains(t, result, "did not search:", "with no tenant allowlist there are no other tenants to name")

	// The same holds in the other direction: an empty CloudWatch answer
	// points back at Loki.
	hint := server.emptyLogSearchHint(context.Background(), logFamilyCloudWatch, nil)
	assert.Contains(t, hint, "Loki (loki_label_names, loki_label_values, loki_query)")
	assert.NotContains(t, hint, "CloudWatch Logs (", "the backend just searched is not offered as another source")
}

// TestEmptyLogSearchRespectsAuthorization keeps the note from describing tools
// the caller cannot dispatch: it is built from the caller's own permissions.
func TestEmptyLogSearchRespectsAuthorization(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, "")
	t.Setenv(envCloudWatchAssumeRole, "arn:aws:iam::123456789012:role/logs-reader")

	const policy = `
authz:
  default: deny
  roles:
    loki-only:
      tools: ["loki_query"]
    all-logs:
      tools: ["loki_*", "cloudwatch_*", "k8s_*"]
  users:
    - name: "Alice"
      emails: ["alice@corp.com"]
      roles: ["loki-only"]
    - name: "Bob"
      emails: ["bob@corp.com"]
      roles: ["all-logs"]
`

	authorizer, err := authz.LoadPolicy("", policy, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	fake := &fakeLoki{byTenant: map[string]string{"": lokiEmptyStreams}}
	server := newLokiToolServer(t, fake, "", nil)
	server.authorizer = authorizer
	server.k8sClusters = map[string]*k8s.Agent{"default": nil}

	args := map[string]interface{}{"query": `{service_name="relay"}`, "start": "1h"}

	alice := authz.NewContext(context.Background(), authz.Principal{Email: "alice@corp.com", Source: authz.SourceMCP})

	result, err := server.executeLokiQuery(alice, args)
	require.NoError(t, err)
	assert.Equal(t, "No log entries found.", result,
		"a caller with nothing else to try is told nothing about tools they cannot use")

	bob := authz.NewContext(context.Background(), authz.Principal{Email: "bob@corp.com", Source: authz.SourceMCP})

	result, err = server.executeLokiQuery(bob, args)
	require.NoError(t, err)
	assert.Contains(t, result, "loki_label_names, loki_label_values")
	assert.Contains(t, result, "CloudWatch Logs (cloudwatch_logs_list_groups, cloudwatch_logs_query)")
	assert.Contains(t, result, "Kubernetes pod logs (k8s_pod_logs)")
}

// TestEmptyLokiLabelLookupSuggestsWiderWindow covers a label lookup that finds
// nothing: every requested tenant was inspected, so the leads are a wider
// window and the other backends, not "other tenants".
func TestEmptyLokiLabelLookupSuggestsWiderWindow(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, "")
	t.Setenv(envCloudWatchAssumeRole, "")

	fake := &fakeLoki{byTenant: map[string]string{"monitoring": lokiNoLabels, "logging": lokiNoLabels}}
	server := newLokiToolServer(t, fake, "monitoring", []string{"monitoring", "logging"})

	result, err := server.executeLokiLabelValues(context.Background(), map[string]interface{}{"label": "service_name"})
	require.NoError(t, err)

	assert.Contains(t, result, "Tenant logging: none")
	assert.Contains(t, result, "Tenant monitoring: none")
	assert.Contains(t, result, "a wider window (start)")
	assert.NotContains(t, result, "did not search")
}

// TestLokiToolDescriptionsNameNoOtherTools guards the rule that a description
// never names a tool the caller may not have. Descriptions are static, so they
// may only point at tools in general; the per-caller note names them.
func TestLokiToolDescriptionsNameNoOtherTools(t *testing.T) {
	t.Parallel()

	for _, tool := range getLokiTools([]string{"monitoring", "logging"}) {
		for _, other := range []string{toolLokiQuery, toolLokiLabelNames, toolLokiLabelValues, "cloudwatch_", "k8s_"} {
			if other == tool.Name {
				continue
			}

			assert.NotContains(t, tool.Description, other,
				"%s description names %s, which the caller may not be permitted to use", tool.Name, other)
		}
	}
}

// TestLokiFamilyReachesBothTransports extends the transport-parity guard to a
// server that has Loki and Tempo configured, which the shared fixture does not.
func TestLokiFamilyReachesBothTransports(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	legacy := &Server{
		lokiClient:   k8s.NewLokiClient("http://loki.example:3100", logger),
		tempoClients: map[string]*TempoClient{tempoEndpointDefault: NewTempoClient(tempoEndpointDefault, "http://tempo.example:3200", logger)},
		logger:       logger,
	}
	sdk := NewSDKServer(legacy)

	advertised := make([]string, 0)
	for _, tool := range legacy.getToolDefinitions() {
		advertised = append(advertised, tool.Name)
	}

	registered := sdk.RegisteredTools()

	assert.ElementsMatch(t, advertised, registered)

	for _, name := range []string{toolLokiQuery, toolLokiLabelNames, toolLokiLabelValues} {
		assert.Contains(t, registered, name, "%s must be dispatchable over the /mcp endpoint", name)
		assert.False(t, isWriteTool(name), "%s is a read", name)
	}
}
