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

package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLokiTenantsFromEnvAcceptsCommasAndNewlines is the regression for
// LOKI_ORG_IDS being split on commas only: a YAML block scalar with one tenant
// per line arrived as a single tenant named "monitoring\nlogging\ncloudtrail".
func TestLokiTenantsFromEnvAcceptsCommasAndNewlines(t *testing.T) {
	tests := []struct {
		name          string
		defaultTenant string
		orgIDs        string
		wantDefault   string
		wantAllowed   []string
	}{
		{name: "unset", wantAllowed: nil},
		{name: "comma separated", defaultTenant: "monitoring", orgIDs: "monitoring,logging", wantDefault: "monitoring", wantAllowed: []string{"monitoring", "logging"}},
		{name: "block scalar", defaultTenant: "monitoring", orgIDs: "monitoring\nlogging\ncloudtrail\n", wantDefault: "monitoring", wantAllowed: []string{"monitoring", "logging", "cloudtrail"}},
		{name: "mixed with stray indentation", defaultTenant: " logging ", orgIDs: "monitoring,\n  logging\n", wantDefault: "logging", wantAllowed: []string{"monitoring", "logging"}},
		{name: "default only", defaultTenant: "logging", wantDefault: "logging", wantAllowed: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LOKI_DEFAULT_ORG_ID", tt.defaultTenant)
			t.Setenv("LOKI_ORG_IDS", tt.orgIDs)

			defaultTenant, allowed := lokiTenantsFromEnv()

			assert.Equal(t, tt.wantDefault, defaultTenant)
			assert.Equal(t, tt.wantAllowed, allowed)
		})
	}
}

// TestLokiClientFromEnv verifies the Loki client exists only when LOKI_ENDPOINT
// is set, so an unconfigured Loki advertises no tools and is not counted as a
// log source.
func TestLokiClientFromEnv(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	t.Setenv("LOKI_ENDPOINT", "")
	t.Setenv("LOKI_DEFAULT_ORG_ID", "")
	t.Setenv("LOKI_ORG_IDS", "")

	assert.Nil(t, lokiClientFromEnv(context.Background(), logger), "no endpoint, no client")

	t.Setenv("LOKI_ENDPOINT", "http://loki-gateway.monitoring.svc:80")
	t.Setenv("LOKI_DEFAULT_ORG_ID", "monitoring")
	t.Setenv("LOKI_ORG_IDS", "monitoring\nlogging")

	client := lokiClientFromEnv(context.Background(), logger)
	require.NotNil(t, client)
	assert.Equal(t, []string{"logging", "monitoring"}, client.AllowedTenants())
}

// TestBuildToolServerWithholdsLokiWhenUnconfigured verifies the tool surface
// offers no Loki tools without LOKI_ENDPOINT, and all of them with it.
func TestBuildToolServerWithholdsLokiWhenUnconfigured(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	lokiTools := []string{"loki_query", "loki_label_names", "loki_label_values"}

	t.Setenv("LOKI_DEFAULT_ORG_ID", "")
	t.Setenv("LOKI_ORG_IDS", "")
	t.Setenv("API_CONFIG_DIR", t.TempDir())
	t.Setenv("LOKI_ENDPOINT", "")

	names := make([]string, 0)
	for _, tool := range buildToolServer(context.Background(), "", logger).ToolDefinitions() {
		names = append(names, tool.Name)
	}

	for _, name := range lokiTools {
		assert.NotContains(t, names, name)
	}

	t.Setenv("LOKI_ENDPOINT", "http://loki-gateway.monitoring.svc:80")

	names = names[:0]
	for _, tool := range buildToolServer(context.Background(), "", logger).ToolDefinitions() {
		names = append(names, tool.Name)
	}

	for _, name := range lokiTools {
		assert.Contains(t, names, name)
	}
}
