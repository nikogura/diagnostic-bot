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

// White-box tests that the two transports expose the same toolset.
package mcp

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikogura/diagnostic-bot/pkg/investigations"
)

// transportServer builds a legacy server plus the SDK server over it.
func transportServer(t *testing.T, library *investigations.SkillLibrary) (sdk *SDKServer, legacy *Server) {
	t.Helper()

	legacy = &Server{skillLibrary: library, logger: slog.New(slog.DiscardHandler)}
	sdk = NewSDKServer(legacy)

	return sdk, legacy
}

// TestTransportsExposeTheSameTools is the guard for a whole class of silent
// failure, not just the one that prompted it.
//
// Two separate things decide whether a tool works over Streamable-HTTP. The
// legacy catalog decides what is advertised and what authorization reasons
// about, including what list_my_tools reports as usable. A separate
// registration on the SDK server decides what the /mcp endpoint can actually
// dispatch. Nothing structural ties them together, so a family added to one and
// forgotten in the other fails silently: the tool is reported as available and
// then cannot be called.
//
// Asserting the two sets are equal catches that for every family at once,
// which testing the handlers directly does not.
func TestTransportsExposeTheSameTools(t *testing.T) {
	t.Parallel()

	library, err := investigations.NewSkillLibrary("testdata/skills")
	require.NoError(t, err)

	sdk, legacy := transportServer(t, library)

	advertised := make([]string, 0)
	for _, tool := range legacy.getToolDefinitions() {
		advertised = append(advertised, tool.Name)
	}

	dispatchable := sdk.RegisteredTools()

	assert.ElementsMatch(t, advertised, dispatchable,
		"a tool advertised by the legacy catalog but not registered on the SDK server "+
			"is reported as usable and then cannot be called")
}

// TestInvestigationToolsReachTheHTTPTransport is the specific regression: the
// investigation catalog was wired into the legacy server only, so external MCP
// clients could not see or call it while list_my_tools said they could.
func TestInvestigationToolsReachTheHTTPTransport(t *testing.T) {
	t.Parallel()

	library, err := investigations.NewSkillLibrary("testdata/skills")
	require.NoError(t, err)

	sdk, _ := transportServer(t, library)

	registered := sdk.RegisteredTools()

	for _, name := range []string{toolGetInvestigation, toolMatchInvestigation, toolListInvestigations} {
		assert.Contains(t, registered, name,
			"%s must be dispatchable over the /mcp endpoint, not only in-process", name)
	}
}

// TestNoInvestigationToolsWithoutALibrary keeps a deployment with no skills
// from registering tools it cannot answer, on either transport.
func TestNoInvestigationToolsWithoutALibrary(t *testing.T) {
	t.Parallel()

	sdk, legacy := transportServer(t, nil)

	registered := sdk.RegisteredTools()

	for _, name := range []string{toolGetInvestigation, toolMatchInvestigation, toolListInvestigations} {
		assert.NotContains(t, registered, name)
	}

	advertised := make([]string, 0)
	for _, tool := range legacy.getToolDefinitions() {
		advertised = append(advertised, tool.Name)
	}

	assert.ElementsMatch(t, advertised, registered,
		"the two transports must agree when there are no investigations either")
}
