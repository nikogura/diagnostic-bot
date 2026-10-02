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
	"fmt"
	"strings"
)

// The log backends a deployment may have. Which of them hold a given
// workload's logs depends entirely on the setup, so an empty answer from one
// says nothing about the others.
const (
	logFamilyLoki       = "loki"
	logFamilyCloudWatch = "cloudwatch"
	logFamilyKubernetes = "kubernetes"
)

// logFamily is one place logs can live, with the tools that search it.
type logFamily struct {
	name  string
	label string
	tools []string
}

// logFamilies lists the log backends in the order they are suggested.
func logFamilies() (families []logFamily) {
	families = []logFamily{
		{
			name:  logFamilyLoki,
			label: "Loki",
			tools: []string{toolLokiLabelNames, toolLokiLabelValues, toolLokiQuery},
		},
		{
			name:  logFamilyCloudWatch,
			label: "CloudWatch Logs",
			tools: []string{toolCloudWatchLogsListGroups, toolCloudWatchLogsQuery},
		},
		{
			name:  logFamilyKubernetes,
			label: "Kubernetes pod logs",
			tools: []string{toolK8sPodLogs},
		},
	}

	return families
}

// logFamilyConfigured reports whether the named log backend is configured on
// this deployment, by the same gates that decide whether its tools exist.
func (s *Server) logFamilyConfigured(name string) (configured bool) {
	switch name {
	case logFamilyLoki:
		configured = s.lokiClient != nil
	case logFamilyCloudWatch:
		configured = isCloudWatchConfigured()
	case logFamilyKubernetes:
		configured = len(s.k8sClusters) > 0
	}

	return configured
}

// permittedTools returns the subset of tools the caller behind ctx may
// dispatch, in the order given.
func (s *Server) permittedTools(ctx context.Context, tools ...string) (permitted []string) {
	for _, tool := range tools {
		if s.allows(ctx, tool) {
			permitted = append(permitted, tool)
		}
	}

	return permitted
}

// otherLogSources names the log backends, other than the one just searched,
// that are configured here and that this caller may use.
//
// It is computed per call, from the caller's own permissions, so it never
// describes a tool the caller cannot dispatch.
func (s *Server) otherLogSources(ctx context.Context, searched string) (sources []string) {
	for _, family := range logFamilies() {
		if family.name == searched || !s.logFamilyConfigured(family.name) {
			continue
		}

		tools := s.permittedTools(ctx, family.tools...)
		if len(tools) == 0 {
			continue
		}

		sources = append(sources, fmt.Sprintf("%s (%s)", family.label, strings.Join(tools, ", ")))
	}

	return sources
}

// emptyLogSearchHint is the sentence returned with a log lookup that matched
// nothing: what the lookup did not cover, so the miss is not taken for proof
// that the logs do not exist.
//
// leads are the untried options within the backend just searched; the other
// configured log backends are added to them. With nothing left to suggest it
// returns the empty string.
func (s *Server) emptyLogSearchHint(ctx context.Context, searched string, leads []string) (hint string) {
	suggestions := make([]string, 0, len(leads))
	suggestions = append(suggestions, leads...)

	others := s.otherLogSources(ctx, searched)
	if len(others) > 0 {
		suggestions = append(suggestions, "the other log sources on this deployment: "+strings.Join(others, "; "))
	}

	if len(suggestions) == 0 {
		return hint
	}

	hint = "Nothing matched here, which does not show these logs do not exist. " +
		"Before concluding they are unavailable, check: " + strings.Join(suggestions, "; ") + "."

	return hint
}

// emptyLogSearchNote is emptyLogSearchHint set off as a trailing paragraph,
// for appending to a text result.
func (s *Server) emptyLogSearchNote(ctx context.Context, searched string, leads []string) (note string) {
	hint := s.emptyLogSearchHint(ctx, searched, leads)
	if hint != "" {
		note = "\n\n" + hint
	}

	return note
}
