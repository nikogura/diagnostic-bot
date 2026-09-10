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
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/investigations"
)

// MaxInstructionsBytes bounds the assembled server instructions.
//
// Instructions are delivered once at initialize and then live in every context
// window the client opens, so they are charged for on every request whether or
// not they are used. Preloading a whole playbook library would quietly tax each
// conversation; the cap keeps that from happening by accident.
const MaxInstructionsBytes = 24000

// maxDigestTriggers is how many trigger terms are listed per investigation in
// the digest. Enough to recognise the vocabulary, not the whole regex set.
const maxDigestTriggers = 8

// InstructionsInput is everything needed to assemble the server instructions.
type InstructionsInput struct {
	// Library is the operator's loaded investigation skills. Nil is fine and
	// yields empty instructions.
	Library *investigations.SkillLibrary

	// ContextDocuments names the documents whose full prompt is inlined, in the
	// operator's chosen order. Everything else appears only in the digest.
	ContextDocuments []string
}

// InstructionsResult is the assembled instructions plus what had to be left
// out, so the caller can say so rather than silently truncating.
type InstructionsResult struct {
	Text string

	// Skipped names preloads that did not fit within MaxInstructionsBytes.
	Skipped []string

	// Missing names preloads that do not exist in the library, which is almost
	// always a typo in the operator's configuration.
	Missing []string
}

// BuildInstructions assembles the text an MCP client receives at connect.
//
// This is the whole point of the feature: a locally started agent should not
// have to rediscover an environment that its operator has already written down.
// Two tiers, because the two kinds of knowledge have different economics.
//
// Preloaded investigations are inlined in full. These are the orientation
// documents — which accounts hold which clusters, what humans call each
// environment, which realm maps to which namespace. They are short, they apply
// to everything, and they are useless if the agent has to know to ask for them.
//
// Everything else is listed as a name, a description, and the terms that signal
// it. That is enough for the agent to recognise when a playbook applies and
// fetch it with get_investigation, without charging every conversation for text
// it will not read.
func BuildInstructions(input InstructionsInput) (result InstructionsResult) {
	if input.Library == nil {
		return result
	}

	// The preloaded half is rendered by the investigations package, which the
	// Slack front-end uses too, so a client and a Slack investigation are given
	// byte-identical operator knowledge.
	knowledge := investigations.RenderContext(input.Library, input.ContextDocuments, MaxInstructionsBytes-catalogReserve)

	result.Missing = knowledge.Missing
	result.Skipped = knowledge.Skipped

	builder := &strings.Builder{}
	builder.WriteString(knowledge.Text)
	builder.WriteString(renderCatalogPointer(input.Library))

	result.Text = builder.String()

	return result
}

// catalogReserve is the room held back from the instruction budget for the
// pointer to the investigation tools. It is small and fixed, because the
// catalog itself is not listed here.
const catalogReserve = 1024

// renderDigest lists the investigations that were not inlined, so the agent
// knows they exist and what vocabulary reaches them.
// renderCatalogPointer tells the client how to discover investigations.
//
// The catalog itself is deliberately NOT listed here. Instructions are returned
// once at initialize as a single static string, identical for every caller, so
// anything named in them is disclosed to everyone who can connect. A procedure's
// name and the vocabulary that reaches it describe what an organisation
// monitors and worries about; listing them here would tell a caller with no
// security permission that a security procedure exists, which is exactly the
// disclosure the permission is meant to prevent.
//
// Discovery therefore happens through list_investigations and
// match_investigation, which run per request with an authenticated principal
// and return only what that caller may see.
func renderCatalogPointer(library *investigations.SkillLibrary) (pointer string) {
	if library == nil || len(library.ListSkills()) == 0 {
		return pointer
	}

	pointer = "## Investigations\n\n" +
		"This deployment has written procedures for diagnosing things in this environment. " +
		"Before investigating anything here, call `" + toolMatchInvestigation + "` with the problem in " +
		"your own words — it routes to the right procedure without you needing its name, exactly as a " +
		"Slack user is routed. Carry the procedure out yourself with this server's tools. " +
		"`" + toolListInvestigations + "` shows what is available to you, and `" + toolGetInvestigation +
		"` fetches one by name.\n\n" +
		"What you can see depends on your permissions, so ask rather than assuming a procedure " +
		"does not exist.\n"

	return pointer
}

// summarize returns a one-line description of a skill.
func summarize(skill *investigations.InvestigationSkill) (summary string) {
	summary = strings.TrimSpace(skill.Description)
	if summary == "" {
		summary = strings.TrimSpace(skill.Name)
	}

	if summary == "" {
		summary = "no description provided"
	}

	summary = strings.ReplaceAll(summary, "\n", " ")

	return summary
}

// digestTriggers renders a bounded, readable sample of the trigger vocabulary.
// Regex metacharacters are stripped: the point is to show the model which human
// words reach this investigation, not to hand it a pattern to evaluate.
func digestTriggers(skill *investigations.InvestigationSkill) (rendered string) {
	seen := map[string]bool{}
	terms := make([]string, 0, maxDigestTriggers)

	for _, pattern := range skill.TriggerPatterns {
		term := humanizePattern(pattern)
		if term == "" || seen[term] {
			continue
		}

		seen[term] = true
		terms = append(terms, term)

		if len(terms) == maxDigestTriggers {
			break
		}
	}

	rendered = strings.Join(terms, ", ")

	return rendered
}

// humanizePattern reduces a trigger regex to the plain words inside it.
func humanizePattern(pattern string) (term string) {
	replacer := strings.NewReplacer(
		".*", " ", "^", "", "$", "", "\\b", "", "\\s", " ",
		"(", "", ")", "", "[", "", "]", "", "?", "", "+", "", "|", " ", "*", "",
	)

	term = strings.TrimSpace(replacer.Replace(pattern))
	term = strings.Join(strings.Fields(term), " ")

	return term
}
