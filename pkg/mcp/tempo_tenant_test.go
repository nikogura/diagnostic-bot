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
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikogura/diagnostic-bot/pkg/tenant"
)

// tempoTenantRecorder is a fake Tempo that records the tenant header of each
// request and answers with a fixed status and body.
type tempoTenantRecorder struct {
	mu      sync.Mutex
	status  int
	body    string
	tenants []string
}

func (r *tempoTenantRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.tenants = append(r.tenants, req.Header.Get(tenant.Header))
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte(r.body))
}

// seen returns the tenant header of each request received, in order.
func (r *tempoTenantRecorder) seen() (tenants []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tenants = append(tenants, r.tenants...)

	return tenants
}

// newTenantedTempoServer builds a Server over a fake Tempo with the given
// tenant configuration. An empty configuration leaves tenancy off.
func newTenantedTempoServer(t *testing.T, recorder *tempoTenantRecorder, defaultTenant string, allowed []string) (server *Server) {
	t.Helper()

	backend, mcpServer := newTestServerWithTempo(t, recorder.ServeHTTP)
	t.Cleanup(backend.Close)

	policy, err := tenant.NewPolicy(tempoBackend, defaultTenant, allowed)
	require.NoError(t, err)

	mcpServer.tempoTenants = policy
	server = mcpServer

	return server
}

func TestTempoToolsExposeTenantArgWhenAllowlistSet(t *testing.T) {
	t.Parallel()

	tenantTools := map[string]bool{toolTempoGetTrace: true, toolTempoSearchTraces: true, toolTempoListEndpoints: false}

	for _, tool := range getTempoTools([]string{"monitoring", "platform"}) {
		props, ok := tool.InputSchema["properties"].(map[string]interface{})
		require.True(t, ok, "%s schema must have properties", tool.Name)

		if !tenantTools[tool.Name] {
			assert.NotContains(t, props, "tenant", tool.Name)
			continue
		}

		assert.Contains(t, props, "tenant", "%s must accept a tenant", tool.Name)
		assert.Contains(t, tool.Description, "Allowed tenants: monitoring, platform.", tool.Name)

		required, _ := tool.InputSchema["required"].([]string)
		assert.NotContains(t, required, "tenant", "%s tenant is optional", tool.Name)
	}

	for _, tool := range getTempoTools(nil) {
		props, ok := tool.InputSchema["properties"].(map[string]interface{})
		require.True(t, ok, "%s schema must have properties", tool.Name)
		assert.NotContains(t, props, "tenant", "%s must not offer a tenant arg without an allowlist", tool.Name)
		assert.NotContains(t, tool.Description, "Allowed tenants", tool.Name)
	}
}

func TestTempoRequestsCarryTheResolvedTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		defaultTenant string
		allowed       []string
		requested     string
		wantHeader    string
		wantErr       string
	}{
		{name: "tenancy off sends no header", wantHeader: ""},
		{name: "default tenant applied", defaultTenant: "monitoring", allowed: []string{"monitoring", "platform"}, wantHeader: "monitoring"},
		{name: "requested tenant sent", defaultTenant: "monitoring", allowed: []string{"monitoring", "platform"}, requested: "platform", wantHeader: "platform"},
		{name: "multi-tenant read", defaultTenant: "monitoring", allowed: []string{"monitoring", "platform"}, requested: "monitoring|platform", wantHeader: "monitoring|platform"},
		{name: "default without allowlist", defaultTenant: "monitoring", wantHeader: "monitoring"},
		{name: "tenant outside the allowlist", defaultTenant: "monitoring", allowed: []string{"monitoring", "platform"}, requested: "production", wantErr: "tempo allowlist"},
		{name: "allowlist without default requires a tenant", allowed: []string{"monitoring", "platform"}, wantErr: "TEMPO_DEFAULT_ORG_ID"},
	}

	calls := map[string]map[string]interface{}{
		toolTempoGetTrace:     {"trace_id": "abc123"},
		toolTempoSearchTraces: {"tags": "service.name=api"},
	}

	for _, tt := range tests {
		for toolName, baseArgs := range calls {
			t.Run(tt.name+"/"+toolName, func(t *testing.T) {
				t.Parallel()

				recorder := &tempoTenantRecorder{status: http.StatusOK, body: `{"traces":[{"traceID":"abc123"}]}`}
				server := newTenantedTempoServer(t, recorder, tt.defaultTenant, tt.allowed)

				args := map[string]interface{}{"tenant": tt.requested}
				for key, value := range baseArgs {
					args[key] = value
				}

				_, err := server.dispatchToolCall(context.Background(), toolName, args)
				if tt.wantErr != "" {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tt.wantErr)
					assert.Empty(t, recorder.seen(), "a rejected tenant must not reach Tempo")

					return
				}

				require.NoError(t, err)
				assert.Equal(t, []string{tt.wantHeader}, recorder.seen())
			})
		}
	}
}

// TestTempoTraceMissNamesOtherTenants covers a trace looked up in the wrong
// tenant: Tempo answers 404, which must not read as "this trace does not exist".
func TestTempoTraceMissNamesOtherTenants(t *testing.T) {
	t.Parallel()

	recorder := &tempoTenantRecorder{status: http.StatusNotFound, body: `trace not found`}
	server := newTenantedTempoServer(t, recorder, "monitoring", []string{"monitoring", "platform", "edge"})

	_, err := server.executeTempoGetTrace(context.Background(), map[string]interface{}{"trace_id": "abc123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), `searched tenant "monitoring"`)
	assert.Contains(t, err.Error(), "other allowed tenants not searched: edge, platform")

	// With tenancy off there is nothing else to name.
	untenanted := newTenantedTempoServer(t, &tempoTenantRecorder{status: http.StatusNotFound}, "", nil)

	_, err = untenanted.executeTempoGetTrace(context.Background(), map[string]interface{}{"trace_id": "abc123"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "tenant")
}

func TestTempoEmptySearchNamesOtherTenants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		allowed  []string
		wantNote bool
	}{
		{name: "empty search with other tenants", body: `{"traces":[]}`, allowed: []string{"monitoring", "platform"}, wantNote: true},
		{name: "matching search carries no note", body: `{"traces":[{"traceID":"abc123"}]}`, allowed: []string{"monitoring", "platform"}},
		{name: "empty search with nothing else to search", body: `{"traces":[]}`, allowed: []string{"monitoring"}},
		{name: "unparseable body is not treated as empty", body: `not json`, allowed: []string{"monitoring", "platform"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			recorder := &tempoTenantRecorder{status: http.StatusOK, body: tt.body}
			server := newTenantedTempoServer(t, recorder, "monitoring", tt.allowed)

			result, err := server.executeTempoSearchTraces(context.Background(), map[string]interface{}{"tags": "service.name=api"})
			require.NoError(t, err)

			if tt.wantNote {
				assert.Contains(t, result, `No traces matched in tenant "monitoring"`)
				assert.Contains(t, result, "not searched: platform")

				return
			}

			assert.NotContains(t, result, "not searched")
		})
	}
}

func TestTempoListEndpointsShowsTenants(t *testing.T) {
	t.Parallel()

	recorder := &tempoTenantRecorder{status: http.StatusOK}

	tenanted := newTenantedTempoServer(t, recorder, "monitoring", []string{"monitoring", "platform"})

	result, err := tenanted.executeTempoListEndpoints(context.Background(), nil)
	require.NoError(t, err)
	assert.Contains(t, result, "Tenants (X-Scope-OrgID): default monitoring; allowed monitoring, platform")

	untenanted := newTenantedTempoServer(t, recorder, "", nil)

	result, err = untenanted.executeTempoListEndpoints(context.Background(), nil)
	require.NoError(t, err)
	assert.NotContains(t, result, "Tenants")
}

func TestLoadTempoTenants(t *testing.T) {
	t.Setenv(envTempoDefaultTenant, "monitoring")
	t.Setenv(envTempoTenants, "monitoring\nplatform,\n  edge\n")

	policy, err := loadTempoTenants()
	require.NoError(t, err)
	assert.Equal(t, "monitoring", policy.Default())
	assert.Equal(t, []string{"edge", "monitoring", "platform"}, policy.Allowed(), "the allowlist accepts commas and newlines")

	t.Setenv(envTempoDefaultTenant, "production")

	_, err = loadTempoTenants()
	require.Error(t, err, "a default outside the allowlist is a configuration error")

	t.Setenv(envTempoDefaultTenant, "")
	t.Setenv(envTempoTenants, "")

	policy, err = loadTempoTenants()
	require.NoError(t, err)
	assert.False(t, policy.Configured(), "with neither variable set, tenancy is off")
}

// TestInvalidTempoTenantConfigWithholdsTempoTools verifies a tenant
// configuration that cannot be applied fails closed: the Tempo tools are
// withheld rather than run under a tenant the operator did not intend.
func TestInvalidTempoTenantConfigWithholdsTempoTools(t *testing.T) {
	t.Setenv("TEMPO_URL", "http://tempo.example:3200")
	t.Setenv(envTempoDefaultTenant, "production")
	t.Setenv(envTempoTenants, "monitoring,platform")

	server := NewServer(nil, "", nil, slog.New(slog.DiscardHandler))

	for _, tool := range server.getToolDefinitions() {
		assert.NotContains(t, []string{toolTempoGetTrace, toolTempoSearchTraces, toolTempoListEndpoints}, tool.Name)
	}

	t.Setenv(envTempoDefaultTenant, "monitoring")

	server = NewServer(nil, "", nil, slog.New(slog.DiscardHandler))

	names := make([]string, 0)
	for _, tool := range server.getToolDefinitions() {
		names = append(names, tool.Name)
	}

	assert.Contains(t, names, toolTempoSearchTraces, "a valid tenant configuration keeps the Tempo tools")
}
