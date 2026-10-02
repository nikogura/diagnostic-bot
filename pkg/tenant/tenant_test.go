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

package tenant

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIDsAcceptsCommasAndNewlines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "only separators", raw: " ,\n, ", want: nil},
		{name: "single", raw: "monitoring", want: []string{"monitoring"}},
		{name: "commas", raw: "monitoring,logging", want: []string{"monitoring", "logging"}},
		{name: "commas with spaces", raw: "monitoring, logging ,cloudtrail", want: []string{"monitoring", "logging", "cloudtrail"}},
		{name: "block scalar", raw: "monitoring\nlogging\ncloudtrail\n", want: []string{"monitoring", "logging", "cloudtrail"}},
		{name: "windows newlines", raw: "monitoring\r\nlogging", want: []string{"monitoring", "logging"}},
		{name: "mixed", raw: "monitoring,\n  logging\n", want: []string{"monitoring", "logging"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, ParseIDs(tt.raw))
		})
	}
}

func TestNewPolicyRejectsDefaultOutsideAllowlist(t *testing.T) {
	t.Parallel()

	_, err := NewPolicy("loki", "production", []string{"monitoring", "logging"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "production")
	assert.Contains(t, err.Error(), "allowlist")
}

func TestZeroPolicySendsNoHeader(t *testing.T) {
	t.Parallel()

	var policy Policy

	assert.False(t, policy.Configured())
	assert.Empty(t, policy.Allowed())

	resolved, err := policy.Resolve("")
	require.NoError(t, err)
	assert.Empty(t, resolved, "untenanted backends get no header and use their built-in tenant")

	tenants, err := policy.Each("")
	require.NoError(t, err)
	assert.Equal(t, []string{""}, tenants, "discovery still runs once, without a header")
}

func TestResolve(t *testing.T) {
	t.Parallel()

	restricted, err := NewPolicy("loki", "monitoring", []string{"monitoring", "logging", "cloudtrail"})
	require.NoError(t, err)

	noDefault, err := NewPolicy("tempo", "", []string{"monitoring", "logging"})
	require.NoError(t, err)

	defaultOnly, err := NewPolicy("loki", "logging", nil)
	require.NoError(t, err)

	tests := []struct {
		name      string
		policy    Policy
		requested string
		want      string
		wantErr   string
	}{
		{name: "default applied", policy: restricted, requested: "", want: "monitoring"},
		{name: "explicit allowed", policy: restricted, requested: "logging", want: "logging"},
		{name: "multi-tenant read", policy: restricted, requested: "monitoring|cloudtrail", want: "monitoring|cloudtrail"},
		{name: "whitespace normalized", policy: restricted, requested: " monitoring | cloudtrail ", want: "monitoring|cloudtrail"},
		{name: "explicit rejected", policy: restricted, requested: "production", wantErr: `tenant "production" is not in the loki allowlist`},
		{name: "any segment rejected", policy: restricted, requested: "monitoring|production", wantErr: `"production"`},
		{name: "allowlist without default requires tenant", policy: noDefault, requested: "", wantErr: "TEMPO_DEFAULT_ORG_ID"},
		{name: "default without allowlist", policy: defaultOnly, requested: "", want: "logging"},
		{name: "no allowlist passes anything", policy: defaultOnly, requested: "anything", want: "anything"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, resolveErr := tt.policy.Resolve(tt.requested)
			if tt.wantErr != "" {
				require.Error(t, resolveErr)
				assert.Contains(t, resolveErr.Error(), tt.wantErr)

				return
			}

			require.NoError(t, resolveErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEach(t *testing.T) {
	t.Parallel()

	restricted, err := NewPolicy("loki", "monitoring", []string{"monitoring", "logging", "cloudtrail"})
	require.NoError(t, err)

	defaultOnly, err := NewPolicy("loki", "logging", nil)
	require.NoError(t, err)

	tests := []struct {
		name      string
		policy    Policy
		requested string
		want      []string
		wantErr   bool
	}{
		{name: "unnamed inspects every allowed tenant", policy: restricted, requested: "", want: []string{"cloudtrail", "logging", "monitoring"}},
		{name: "named inspects only that tenant", policy: restricted, requested: "logging", want: []string{"logging"}},
		{name: "joined tenants are inspected separately", policy: restricted, requested: "logging|cloudtrail", want: []string{"logging", "cloudtrail"}},
		{name: "named but not allowed", policy: restricted, requested: "production", wantErr: true},
		{name: "default without allowlist", policy: defaultOnly, requested: "", want: []string{"logging"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, eachErr := tt.policy.Each(tt.requested)
			if tt.wantErr {
				require.Error(t, eachErr)

				return
			}

			require.NoError(t, eachErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUnsearched(t *testing.T) {
	t.Parallel()

	policy, err := NewPolicy("loki", "monitoring", []string{"monitoring", "logging", "cloudtrail"})
	require.NoError(t, err)

	assert.Equal(t, []string{"cloudtrail", "logging"}, policy.Unsearched("monitoring"))
	assert.Equal(t, []string{"logging"}, policy.Unsearched("monitoring|cloudtrail"))
	assert.Empty(t, policy.Unsearched("monitoring", "logging", "cloudtrail"))

	var unrestricted Policy
	assert.Empty(t, unrestricted.Unsearched(""), "with no allowlist there is nothing known to be unsearched")
}
