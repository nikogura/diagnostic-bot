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

package investigations

import (
	"strings"
)

// KnowledgeHeading introduces the operator's context documents in a prompt.
const KnowledgeHeading = "# Operator Knowledge\n\n"

// knowledgePreamble frames the context documents for the model.
const knowledgePreamble = `This deployment belongs to a specific environment, and its operator has
written down how that environment works. The sections below are that knowledge.
Prefer them over inference: where they name an account, a cluster, a namespace
or a convention, they are authoritative and you do not need to rediscover them.

`

// Context is the assembled operator knowledge plus what could not be used.
type Context struct {
	// Text is the rendered knowledge, empty when no documents were loaded.
	Text string

	// Names are the investigations and documents actually rendered, in order.
	Names []InvestigationType

	// Missing names entries that do not exist in the library, which is almost
	// always a typo in the operator's configuration.
	Missing []string

	// Skipped names entries dropped because the budget could not fit them.
	Skipped []string
}

// RenderContext assembles the context documents both front-ends share.
//
// A context document is reference material, not a procedure: an environment
// map, an account topology, a naming convention. It carries no triggers and is
// never matched or "run" — it is simply present, so that whatever the model is
// asked, it already knows which account "staging" means. That is the opposite
// of an investigation, which is a procedure fetched only when it applies.
//
// The Slack front-end and the MCP transport drive one identical toolset; they
// must also carry one identical picture of the environment. An environment map
// that reaches an MCP client but not a Slack investigation would leave the
// Slack path guessing at exactly the facts the map exists to settle — which
// account holds staging, which cluster serves a realm — and guessing wrong is
// the failure the document was written to prevent.
//
// Rendering lives here, in the package that owns the library, so neither
// front-end can drift from the other by re-implementing it.
//
// budget bounds the assembled text; zero or less means unbounded. Documents
// that do not fit are reported rather than truncated: half a topology table
// reads as authoritative and is quietly wrong.
func RenderContext(library *SkillLibrary, names []string, budget int) (result Context) {
	if library == nil || len(names) == 0 {
		return result
	}

	builder := &strings.Builder{}
	builder.WriteString(KnowledgeHeading)
	builder.WriteString(knowledgePreamble)

	remaining := budget - builder.Len()

	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}

		invType := InvestigationType(name)

		skill, err := library.GetSkill(invType)
		if err != nil {
			result.Missing = append(result.Missing, name)
			continue
		}

		section := renderSection(invType, skill)

		if budget > 0 && len(section) > remaining {
			result.Skipped = append(result.Skipped, name)
			continue
		}

		builder.WriteString(section)
		remaining -= len(section)
		result.Names = append(result.Names, invType)
	}

	// Nothing usable was rendered, so emit nothing rather than a bare heading
	// promising knowledge that is not there.
	if len(result.Names) == 0 {
		return result
	}

	result.Text = builder.String()

	return result
}

// renderSection renders one context document.
func renderSection(invType InvestigationType, skill *InvestigationSkill) (section string) {
	builder := &strings.Builder{}

	builder.WriteString("## ")
	builder.WriteString(SkillTitle(invType, skill))
	builder.WriteString("\n\n")

	if skill.Description != "" {
		builder.WriteString(skill.Description)
		builder.WriteString("\n\n")
	}

	builder.WriteString(strings.TrimSpace(skill.InitialPrompt))
	builder.WriteString("\n\n")

	section = builder.String()

	return section
}

// SkillTitle prefers the skill's own name, falling back to its type.
func SkillTitle(invType InvestigationType, skill *InvestigationSkill) (title string) {
	title = strings.TrimSpace(skill.Name)
	if title == "" {
		title = string(invType)
	}

	return title
}
