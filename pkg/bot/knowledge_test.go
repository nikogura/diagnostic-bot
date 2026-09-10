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

// White-box tests for operator knowledge in the investigation prompt.
package bot

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikogura/diagnostic-bot/pkg/investigations"
)

// knowledgeSkill is a minimal investigation for prompt assembly.
func knowledgeSkill() (skill *investigations.InvestigationSkill) {
	skill = &investigations.InvestigationSkill{
		Name:          "WAF Block Diagnosis",
		InitialPrompt: "Query Loki for ModSecurity 403s in staging and categorise them.",
	}

	return skill
}

// TestInvestigationPromptCarriesOperatorKnowledge is the point of sharing the
// preload between front-ends. A playbook that says "query staging" is ambiguous
// on its own; the environment knowledge is what settles which account and
// cluster that means. An MCP client receives this in the server instructions,
// and a Slack investigation must receive the same facts or the two front-ends
// answer the same question differently.
func TestInvestigationPromptCarriesOperatorKnowledge(t *testing.T) {
	t.Parallel()

	runner := &InvestigationRunner{
		logger:    slog.New(slog.DiscardHandler),
		toolUsage: ToolConfig{},
	}

	runner.SetKnowledge("# Operator Knowledge\n\nstaging lives in account 222222222222, cluster stage-use1.\n\n")

	prompt := runner.buildSystemPrompt(knowledgeSkill(), false, nil)

	assert.Contains(t, prompt, "222222222222",
		"the investigation must be told which account staging actually is")
	assert.Contains(t, prompt, "stage-use1")

	// Knowledge precedes the task, so the task's references resolve against it.
	assert.Less(t, strings.Index(prompt, "Operator Knowledge"), strings.Index(prompt, "Investigation Task"))
}

// TestInvestigationPromptWithoutKnowledge keeps a deployment that preloads
// nothing from gaining an empty heading promising knowledge it does not have.
func TestInvestigationPromptWithoutKnowledge(t *testing.T) {
	t.Parallel()

	runner := &InvestigationRunner{
		logger:    slog.New(slog.DiscardHandler),
		toolUsage: ToolConfig{},
	}

	prompt := runner.buildSystemPrompt(knowledgeSkill(), false, nil)

	assert.NotContains(t, prompt, "Operator Knowledge")
	assert.Contains(t, prompt, "Investigation Task")
}

// TestBothFrontEndsRenderIdenticalKnowledge guards the property that motivated
// putting the renderer in the investigations package: if the two surfaces
// assembled this text separately they would drift, and an operator would have
// to discover which one was stale by being given a wrong answer.
func TestBothFrontEndsRenderIdenticalKnowledge(t *testing.T) {
	t.Parallel()

	library, err := investigations.NewSkillLibrary("../mcp/testdata/skills")
	require.NoError(t, err)

	slackSide := investigations.RenderContext(library, []string{"account-topology"}, 0)
	require.NotEmpty(t, slackSide.Text)

	// The MCP transport prepends exactly this block to its digest.
	assert.Contains(t, slackSide.Text, "333333333333")
	assert.Contains(t, slackSide.Text, "Account Topology")
}
