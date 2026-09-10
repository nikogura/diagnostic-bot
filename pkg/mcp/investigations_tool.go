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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/investigations"
)

// Investigation tool names.
const (
	// toolGetInvestigation retrieves one investigation playbook in full.
	toolGetInvestigation = "get_investigation"

	// toolListInvestigations enumerates what this deployment has.
	toolListInvestigations = "list_investigations"

	// toolMatchInvestigation routes a question to the right playbook, which is
	// what a Slack user gets for free by simply describing their problem.
	toolMatchInvestigation = "match_investigation"
)

// getInvestigationTools returns the investigation tool definitions. The tool
// exists only when the operator has actually loaded skills, so a deployment
// without them advertises nothing it cannot answer.
func getInvestigationTools(library *investigations.SkillLibrary) (result []MCPTool) {
	if library == nil || len(library.ListSkills()) == 0 {
		return result
	}

	result = append(result, MCPTool{
		Name: toolMatchInvestigation,
		Description: "Find the operator's written procedure for a problem, described in your own words, " +
			"and return it ready to follow. This is the same routing a Slack user gets by simply " +
			"describing their problem: you do not need to know the procedure's name. " +
			"Call this FIRST when investigating anything about this environment — the procedure encodes " +
			"which queries to run, how to interpret the results, and what the answer should contain here. " +
			"Then carry it out yourself using this server's tools.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"question": map[string]interface{}{
					"type":        "string",
					"description": "The problem in plain language, e.g. \"waf blocks in prod\" or \"why did the atlas migration stall\".",
				},
			},
			"required": []string{"question"},
		},
	})

	result = append(result, MCPTool{
		Name: toolListInvestigations,
		Description: "List the investigation procedures and reference documents this deployment has, " +
			"with the terms that signal each. Use match_investigation to route a specific problem.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})

	result = append(result, MCPTool{
		Name: toolGetInvestigation,
		Description: "Retrieve the operator's full written procedure for an investigation, by name. " +
			"The available names and the terms that signal each are listed in this server's instructions. " +
			"Call this before improvising a diagnostic approach: the procedure encodes which queries to run, " +
			"how to interpret the results, and what the answer should contain for this specific environment.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Investigation name, as listed in the server instructions.",
				},
			},
			"required": []string{"name"},
		},
	})

	return result
}

// executeGetInvestigation returns one investigation, ready to carry out.
func (s *Server) executeGetInvestigation(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.skillLibrary == nil {
		err = errors.New("no investigations are loaded on this server")
		return result, err
	}

	name, _ := args["name"].(string)

	name = strings.TrimSpace(name)
	if name == "" {
		err = errors.New("name is required")
		return result, err
	}

	invType := investigations.InvestigationType(name)

	skill, found := s.lookupVisible(ctx, invType)
	if !found {
		err = fmt.Errorf("unknown investigation %q; available: %s", name, s.visibleNames(ctx))
		return result, err
	}

	result = s.renderInvestigation(ctx, invType, skill)

	return result, err
}

// executeMatchInvestigation routes a plain-language question to a procedure.
//
// This is the parity piece. A Slack user describes a problem and is routed to
// the right playbook without knowing its name; an MCP client had no equivalent
// and would have to already know what to ask for. The same matcher serves both,
// so the two front-ends route identically.
func (s *Server) executeMatchInvestigation(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.skillLibrary == nil {
		err = errors.New("no investigations are loaded on this server")
		return result, err
	}

	question, _ := args["question"].(string)

	question = strings.TrimSpace(question)
	if question == "" {
		err = errors.New("question is required")
		return result, err
	}

	match := investigations.NewMatcher(s.skillLibrary).Match(question)

	// Routing must not reach past the caller's permissions. Matching first and
	// filtering after would otherwise disclose a restricted procedure to
	// anyone who guessed a phrase that reaches it.
	skill, found := s.lookupVisible(ctx, match.InvestigationType)
	if !match.Matched || !found {
		err = fmt.Errorf("no investigation matches %q; available: %s", question, s.visibleNames(ctx))
		return result, err
	}

	builder := &strings.Builder{}

	fmt.Fprintf(builder, "Matched investigation: **%s**\n\n", match.InvestigationType)

	others := s.visibleOnly(ctx, match.MultipleMatches)
	if len(others) > 1 {
		fmt.Fprintf(builder, "Other candidates: %s\n\n", joinTypes(others))
	}

	builder.WriteString(s.renderInvestigation(ctx, match.InvestigationType, skill))

	result = builder.String()

	return result, err
}

// executeListInvestigations enumerates what this deployment has.
func (s *Server) executeListInvestigations(ctx context.Context, _ map[string]interface{}) (result string, err error) {
	if s.skillLibrary == nil {
		err = errors.New("no investigations are loaded on this server")
		return result, err
	}

	builder := &strings.Builder{}

	for _, invType := range s.visibleInvestigations(ctx) {
		skill, found := s.lookupVisible(ctx, invType)
		if !found {
			continue
		}

		kind := "investigation"
		if skill.Document {
			kind = "reference document"
		}

		fmt.Fprintf(builder, "- **%s** (%s) — %s\n", invType, kind, summarize(skill))

		triggers := digestTriggers(skill)
		if triggers != "" {
			fmt.Fprintf(builder, "  Recognised by: %s\n", triggers)
		}
	}

	result = builder.String()

	return result, err
}

// renderInvestigation assembles a procedure the caller can carry out.
//
// The prompt is built by the investigations package, the same code the Slack
// front-end uses, so an MCP client is handed identical instructions: the
// operator's procedure, the expected report structure, and the rule that tool
// output is untrusted data rather than instructions. A reference document has
// no procedure to run and is returned as-is.
//
// The operator's context documents are deliberately not repeated here — an MCP
// client already received them in the server instructions at connect, and
// including them again would pay for the same text on every call.
func (s *Server) renderInvestigation(ctx context.Context, invType investigations.InvestigationType, skill *investigations.InvestigationSkill) (rendered string) {
	if skill.Document {
		builder := &strings.Builder{}
		builder.WriteString("# ")
		builder.WriteString(investigations.SkillTitle(invType, skill))
		builder.WriteString("\n\n")
		builder.WriteString(strings.TrimSpace(skill.InitialPrompt))
		builder.WriteString("\n")

		rendered = builder.String()

		return rendered
	}

	rendered = investigations.BuildInvestigationPrompt(investigations.PromptOptions{
		Skill:    skill,
		Tools:    s.investigationToolsNote(ctx),
		Delivery: mcpDelivery,
	})

	return rendered
}

// mcpDelivery tells the caller how to return its findings. Unlike the Slack
// front-end there is no PDF upload and no channel to post to: the caller is the
// agent that will present the result.
const mcpDelivery = `# Delivery

Carry out the procedure above yourself, using this server's tools, and present
your findings to the person who asked. Do not call generate_pdf unless they ask
for a file.

`

// investigationToolsNote points the caller at the tools it may actually use.
// The catalog is already authorization-filtered per principal, so a caller is
// never directed at a tool it cannot dispatch.
func (s *Server) investigationToolsNote(ctx context.Context) (note string) {
	allowed := s.AllowedTools(ctx, s.getToolDefinitions())
	if len(allowed) == 0 {
		return note
	}

	names := make([]string, 0, len(allowed))
	for _, tool := range allowed {
		names = append(names, tool.Name)
	}

	sort.Strings(names)

	note = "# Available Tools\n\nCarry out the procedure with these tools, which you are authorized to use here:\n\n" +
		strings.Join(names, ", ") + "\n\n"

	return note
}

// joinTypes renders investigation types for a message.
func joinTypes(types []investigations.InvestigationType) (joined string) {
	listed := make([]string, 0, len(types))
	for _, invType := range types {
		listed = append(listed, string(invType))
	}

	joined = strings.Join(listed, ", ")

	return joined
}

// visibleOnly filters a candidate list to what the caller may see.
func (s *Server) visibleOnly(ctx context.Context, candidates []investigations.InvestigationType) (visible []investigations.InvestigationType) {
	for _, name := range candidates {
		if !s.allowsInvestigation(ctx, name) {
			continue
		}

		visible = append(visible, name)
	}

	return visible
}
