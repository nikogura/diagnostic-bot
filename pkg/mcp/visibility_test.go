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

// White-box tests for investigation visibility.
package mcp

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikogura/diagnostic-bot/pkg/authz"
	"github.com/nikogura/diagnostic-bot/pkg/investigations"
)

// visibilityPolicy grants the security procedure to one person only.
const visibilityPolicy = `
authz:
  default: deny
  roles:
    responder:
      tools:
        - "investigation:*"
    engineer:
      tools:
        - "investigation:waf-block"
        - "investigation:account-topology"
        - "investigation:environment-map"
  users:
    - name: Responder
      emails: ["responder@example.com"]
      roles: ["responder"]
    - name: Engineer
      emails: ["engineer@example.com"]
      roles: ["engineer"]
`

// gatedServer builds a server whose investigations are permission-gated.
func gatedServer(t *testing.T) (server *Server) {
	t.Helper()

	library, err := investigations.NewSkillLibrary("testdata/skills")
	require.NoError(t, err)

	policy, err := authz.LoadPolicy("", visibilityPolicy, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	server = &Server{skillLibrary: library, authorizer: policy, logger: slog.New(slog.DiscardHandler)}

	return server
}

// asUser returns a context carrying an authenticated MCP principal.
func asUser(email string) (ctx context.Context) {
	ctx = authz.NewContext(context.Background(), authz.Principal{
		Email:  email,
		Source: authz.SourceMCP,
	})

	return ctx
}

// TestRestrictedInvestigationIsInvisible is the requirement in full: a caller
// without the permission must not learn that the procedure exists.
//
// Not merely be refused — not know. The name and description of a
// "security-incident" procedure disclose that such triage exists here and
// roughly what it covers, which is information in its own right.
func TestRestrictedInvestigationIsInvisible(t *testing.T) {
	t.Parallel()

	server := gatedServer(t)
	ctx := asUser("engineer@example.com")

	listing, err := server.executeListInvestigations(ctx, nil)
	require.NoError(t, err)

	assert.NotContains(t, listing, "security-incident")
	assert.NotContains(t, listing, "Security Incident Triage")
	assert.NotContains(t, listing, "evidence timeline")
	assert.Contains(t, listing, "waf-block", "permitted procedures are still listed")
}

// TestDeniedAndNonexistentAreIndistinguishable closes the side channel. If a
// refusal said "permission denied" while a typo said "no such investigation",
// the error message itself would confirm the existence of everything it
// refused.
func TestDeniedAndNonexistentAreIndistinguishable(t *testing.T) {
	t.Parallel()

	server := gatedServer(t)
	ctx := asUser("engineer@example.com")

	_, denied := server.executeGetInvestigation(ctx, map[string]interface{}{"name": "security-incident"})
	require.Error(t, denied)

	_, missing := server.executeGetInvestigation(ctx, map[string]interface{}{"name": "no-such-thing"})
	require.Error(t, missing)

	// The two messages may differ only where they echo the caller's own input.
	// Everything else — the wording and the list of what IS available — must be
	// identical, so the response cannot be used to probe for existence.
	normalise := func(err error, echoed string) (shape string) {
		shape = strings.Replace(err.Error(), echoed, "<name>", 1)
		return shape
	}

	assert.Equal(t,
		normalise(missing, "no-such-thing"),
		normalise(denied, "security-incident"),
		"a refusal must be shaped exactly like a miss, or it confirms what it refused")

	assert.NotContains(t, denied.Error(), "permission")
	assert.NotContains(t, denied.Error(), "denied")
	assert.NotContains(t, denied.Error(), "forbidden")
}

// TestMatchingCannotReachPastPermissions proves routing does not leak either.
// Matching first and filtering after would disclose a restricted procedure to
// anyone who guessed a phrase that reaches it.
func TestMatchingCannotReachPastPermissions(t *testing.T) {
	t.Parallel()

	server := gatedServer(t)

	_, err := server.executeMatchInvestigation(asUser("engineer@example.com"),
		map[string]interface{}{"question": "we have a breach, possible compromise"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "security-incident")

	result, err := server.executeMatchInvestigation(asUser("responder@example.com"),
		map[string]interface{}{"question": "we have a breach, possible compromise"})
	require.NoError(t, err)
	assert.Contains(t, result, "security-incident", "the responder is routed normally")
	assert.Contains(t, result, "evidence timeline")
}

// TestErrorListingNamesOnlyWhatTheCallerMaySee keeps the "available:" hint from
// becoming the enumeration the filter just prevented.
func TestErrorListingNamesOnlyWhatTheCallerMaySee(t *testing.T) {
	t.Parallel()

	server := gatedServer(t)

	_, err := server.executeGetInvestigation(asUser("engineer@example.com"),
		map[string]interface{}{"name": "no-such-thing"})
	require.Error(t, err)

	assert.NotContains(t, err.Error(), "security-incident")
	assert.Contains(t, err.Error(), "waf-block")
}

// TestNoPolicyMeansNoRestriction keeps a deployment that has not configured
// authorization working exactly as before.
func TestNoPolicyMeansNoRestriction(t *testing.T) {
	t.Parallel()

	listing, err := parityServer(t).executeListInvestigations(context.Background(), nil)
	require.NoError(t, err)

	assert.Contains(t, listing, "security-incident")
}
