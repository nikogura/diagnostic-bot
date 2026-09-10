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

package mcp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikogura/diagnostic-bot/pkg/investigations"
	"github.com/nikogura/diagnostic-bot/pkg/mcp"
)

// testLibrary loads the fixture playbooks.
func testLibrary(t *testing.T) (library *investigations.SkillLibrary) {
	t.Helper()

	var err error

	library, err = investigations.NewSkillLibrary("testdata/skills")
	require.NoError(t, err)
	require.Len(t, library.ListSkills(), 4)

	return library
}

// TestPreloadedKnowledgeArrivesWithoutBeingAsked is the whole point of the
// feature. An agent starting against this server must already know how the
// environment is arranged — which accounts hold which clusters, what the humans
// here call each one — without invoking anything. That knowledge is delivered
// in the initialize response, so it is present before the first question.
func TestPreloadedKnowledgeArrivesWithoutBeingAsked(t *testing.T) {
	t.Parallel()

	result := mcp.BuildInstructions(mcp.InstructionsInput{
		Library:          testLibrary(t),
		ContextDocuments: []string{"environment-map"},
	})

	require.Empty(t, result.Missing)
	require.Empty(t, result.Skipped)

	// The topology itself is inlined, not merely referenced.
	assert.Contains(t, result.Text, "111111111111")
	assert.Contains(t, result.Text, "prod-use1")
	assert.Contains(t, result.Text, "The billing and reporting services run on ECS")
	assert.Contains(t, result.Text, "Environment Map")
}

// TestInstructionsDoNotEnumerateInvestigations is a disclosure control, not a
// formatting choice.
//
// Instructions are returned once at initialize as a single static string,
// identical for every caller, so anything named in them reaches everyone who
// can connect. A procedure's name and the vocabulary that reaches it describe
// what an organisation monitors and worries about: listing them here would tell
// a caller with no security permission that a security procedure exists, which
// is precisely what the permission is meant to prevent.
func TestInstructionsDoNotEnumerateInvestigations(t *testing.T) {
	t.Parallel()

	result := mcp.BuildInstructions(mcp.InstructionsInput{
		Library:          testLibrary(t),
		ContextDocuments: []string{"environment-map"},
	})

	assert.NotContains(t, result.Text, "waf-block",
		"an investigation name must not be broadcast to every caller")
	assert.NotContains(t, result.Text, "WAF Block Diagnosis")
	assert.NotContains(t, result.Text, "modsec",
		"trigger vocabulary discloses what a procedure is for")
	assert.NotContains(t, result.Text, "categorize each block as false positive",
		"and certainly not its body")

	// Discovery is redirected to the per-request, authorization-filtered tools.
	assert.Contains(t, result.Text, "match_investigation")
	assert.Contains(t, result.Text, "list_investigations")
}

// TestMisnamedPreloadIsReported keeps a configuration typo from silently
// withholding knowledge the operator believed they had published.
func TestMisnamedPreloadIsReported(t *testing.T) {
	t.Parallel()

	result := mcp.BuildInstructions(mcp.InstructionsInput{
		Library:          testLibrary(t),
		ContextDocuments: []string{"environment-map", "enviroment-map"},
	})

	assert.Equal(t, []string{"enviroment-map"}, result.Missing)
	assert.Contains(t, result.Text, "111111111111", "the correctly named preload still loads")
}

// TestNoLibraryYieldsNoInstructions keeps a deployment without playbooks from
// advertising knowledge it does not have.
func TestNoLibraryYieldsNoInstructions(t *testing.T) {
	t.Parallel()

	result := mcp.BuildInstructions(mcp.InstructionsInput{})
	assert.Empty(t, result.Text)
}

// TestInstructionsRespectTheBudget proves an over-eager preload is reported
// rather than silently truncated. A half-delivered environment map is worse
// than none: it reads as authoritative and is quietly wrong.
func TestInstructionsRespectTheBudget(t *testing.T) {
	t.Parallel()

	library, err := investigations.NewSkillLibrary("testdata/skills")
	require.NoError(t, err)

	// Ask for the same large playbook far more times than can fit.
	preload := make([]string, 0, 400)
	for range 400 {
		preload = append(preload, "environment-map")
	}

	result := mcp.BuildInstructions(mcp.InstructionsInput{Library: library, ContextDocuments: preload})

	assert.LessOrEqual(t, len(result.Text), mcp.MaxInstructionsBytes)
	assert.NotEmpty(t, result.Skipped, "what did not fit must be reported, not dropped in silence")
}

// TestMarkdownDocumentsLoadDirectly covers the path an operator actually has:
// this knowledge already exists as a document. Requiring it to be re-wrapped in
// a YAML string block is friction that earns nothing, so a plain .md file in
// the skills directory is loaded as reference material.
func TestMarkdownDocumentsLoadDirectly(t *testing.T) {
	t.Parallel()

	result := mcp.BuildInstructions(mcp.InstructionsInput{
		Library:          testLibrary(t),
		ContextDocuments: []string{"account-topology"},
	})

	require.Empty(t, result.Missing)
	assert.Contains(t, result.Text, "Account Topology", "the first heading becomes the title")
	assert.Contains(t, result.Text, "333333333333", "the document body is inlined verbatim")
	assert.Contains(t, result.Text, "Do not guess an account number")
}

// TestDocumentsNeverTriggerInvestigations is what makes documents safe to drop
// in: they carry no trigger patterns, so no Slack message can start one.
func TestDocumentsNeverTriggerInvestigations(t *testing.T) {
	t.Parallel()

	document, err := testLibrary(t).GetSkill("account-topology")
	require.NoError(t, err)

	assert.True(t, document.Document)
	assert.Empty(t, document.TriggerPatterns)

	for _, message := range []string{"billing", "account topology", "which account is billing in"} {
		assert.False(t, document.Matches(message),
			"a reference document must never start an investigation (%q)", message)
	}
}

// TestUnloadedDocumentsAreNotDisclosedEither keeps the same rule applying to
// reference material: a document nobody chose to load is not named to everyone.
func TestUnloadedDocumentsAreNotDisclosedEither(t *testing.T) {
	t.Parallel()

	result := mcp.BuildInstructions(mcp.InstructionsInput{Library: testLibrary(t)})

	assert.NotContains(t, result.Text, "account-topology")
	assert.NotContains(t, result.Text, "waf-block")
}
