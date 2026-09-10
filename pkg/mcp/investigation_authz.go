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
	"sort"
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/investigations"
)

// InvestigationPermissionPrefix namespaces investigation permissions in the
// authorization policy, so a role grants them the same way it grants tools:
//
//	tools:
//	  - "investigation:*"            # every procedure
//	  - "investigation:security-*"   # only the security ones
const InvestigationPermissionPrefix = "investigation:"

// InvestigationPermission is the policy name gating one investigation.
func InvestigationPermission(name investigations.InvestigationType) (permission string) {
	permission = InvestigationPermissionPrefix + string(name)
	return permission
}

// allowsInvestigation reports whether the caller may see and run one
// investigation. With no policy loaded everything is permitted, matching how
// tool authorization already behaves.
func (s *Server) allowsInvestigation(ctx context.Context, name investigations.InvestigationType) (ok bool) {
	if s.authorizer == nil {
		ok = true
		return ok
	}

	ok = s.authorizer.Evaluate(s.principalFromContext(ctx), InvestigationPermission(name)).Allowed

	return ok
}

// visibleInvestigations returns the investigations this caller may see, sorted.
//
// Filtering visibility, not merely execution, is the point. A procedure's name,
// its description and the vocabulary that reaches it all describe what an
// organisation monitors and worries about — a "security-incident-triage"
// playbook discloses that such triage exists and roughly how it works. A caller
// without the permission should not learn that the procedure exists at all,
// which means it must be absent from listings and indistinguishable from a
// misspelling when asked for by name.
func (s *Server) visibleInvestigations(ctx context.Context) (visible []investigations.InvestigationType) {
	if s.skillLibrary == nil {
		return visible
	}

	for _, name := range s.skillLibrary.ListSkills() {
		if !s.allowsInvestigation(ctx, name) {
			continue
		}

		visible = append(visible, name)
	}

	sortTypes(visible)

	return visible
}

// lookupVisible resolves an investigation the caller is permitted to see.
//
// A denied investigation and a nonexistent one are reported identically and on
// the same code path. Distinguishing them — "permission denied" versus "no such
// investigation" — would confirm the existence of everything it refused, which
// is the disclosure the permission exists to prevent.
func (s *Server) lookupVisible(ctx context.Context, name investigations.InvestigationType) (skill *investigations.InvestigationSkill, found bool) {
	if s.skillLibrary == nil {
		return skill, found
	}

	if !s.allowsInvestigation(ctx, name) {
		return skill, found
	}

	resolved, err := s.skillLibrary.GetSkill(name)
	if err != nil {
		return skill, found
	}

	skill = resolved
	found = true

	return skill, found
}

// visibleNames lists the caller's permitted investigations for an error
// message. It never mentions one they cannot see.
func (s *Server) visibleNames(ctx context.Context) (names string) {
	visible := s.visibleInvestigations(ctx)

	listed := make([]string, 0, len(visible))
	for _, name := range visible {
		listed = append(listed, string(name))
	}

	if len(listed) == 0 {
		names = "none available to you"
		return names
	}

	sort.Strings(listed)

	names = strings.Join(listed, ", ")

	return names
}

// sortTypes orders investigation types for stable output.
func sortTypes(types []investigations.InvestigationType) {
	sort.Slice(types, func(i int, j int) (less bool) {
		less = types[i] < types[j]
		return less
	})
}
