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

// Package tenant resolves which tenant a request to a multi-tenant
// observability backend is sent as.
//
// Loki, Tempo, Mimir and Pyroscope share one tenancy model: the tenant is named
// by the X-Scope-OrgID header, and a read may name several tenants joined with
// "|". Whether a deployment uses it at all depends on the setup — with
// multi-tenancy off the backend ignores the header and files everything under
// its built-in tenant ("fake" in Loki), so the zero Policy sends no header.
package tenant

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Header is the tenant identifier header the backends read.
const Header = "X-Scope-OrgID"

// separator joins several tenants into one multi-tenant read.
const separator = "|"

// Policy is one backend's tenant configuration: the tenant used when a request
// names none, and the tenants a request may name.
//
// The allowlist is not a security boundary against the backend, which trusts
// whatever header it receives. It keeps a calling model from inventing tenant
// names, and tells it which ones exist.
type Policy struct {
	backend       string
	defaultTenant string
	allowed       map[string]struct{}
}

// NewPolicy builds a Policy. backend names the system in error messages and in
// the <BACKEND>_DEFAULT_ORG_ID variable they point at. An empty allowlist means
// no restriction; a non-empty one must contain the default.
func NewPolicy(backend, defaultTenant string, allowed []string) (policy Policy, err error) {
	defaultTenant = strings.TrimSpace(defaultTenant)

	allowSet := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		allowSet[id] = struct{}{}
	}

	if defaultTenant != "" && len(allowSet) > 0 {
		_, ok := allowSet[defaultTenant]
		if !ok {
			err = fmt.Errorf("default %s tenant %q is not in the allowlist %v", backend, defaultTenant, allowed)
			return policy, err
		}
	}

	policy = Policy{backend: backend, defaultTenant: defaultTenant, allowed: allowSet}

	return policy, err
}

// ParseIDs splits a list-valued setting into tenant IDs. Commas and any
// whitespace separate entries, so a YAML block scalar with one tenant per line
// parses the same as a comma-separated string.
func ParseIDs(raw string) (ids []string) {
	ids = strings.FieldsFunc(raw, isIDSeparator)
	if len(ids) == 0 {
		ids = nil
	}

	return ids
}

// isIDSeparator reports whether r separates entries in a tenant list.
func isIDSeparator(r rune) (isSeparator bool) {
	isSeparator = r == ',' || unicode.IsSpace(r)
	return isSeparator
}

// Configured reports whether any tenant configuration was supplied. When it
// was not, no tenant header is sent.
func (p Policy) Configured() (configured bool) {
	configured = p.defaultTenant != "" || len(p.allowed) > 0
	return configured
}

// Default returns the tenant used when a request names none.
func (p Policy) Default() (defaultTenant string) {
	defaultTenant = p.defaultTenant
	return defaultTenant
}

// Allowed returns the allowlist in sorted order, empty when unrestricted.
func (p Policy) Allowed() (allowed []string) {
	allowed = make([]string, 0, len(p.allowed))
	for id := range p.allowed {
		allowed = append(allowed, id)
	}

	sort.Strings(allowed)

	return allowed
}

// Resolve returns the header value for a request that asked for requested,
// applying the default and checking every "|"-joined tenant against the
// allowlist. An empty result with a nil error means no header is sent.
func (p Policy) Resolve(requested string) (resolved string, err error) {
	resolved = strings.TrimSpace(requested)
	if resolved == "" {
		resolved = p.defaultTenant
	}

	if resolved == "" {
		if len(p.allowed) > 0 {
			err = fmt.Errorf("%s tenant allowlist is configured but no tenant was specified — set a default via %s_DEFAULT_ORG_ID or pass tenant explicitly",
				p.backend, strings.ToUpper(p.backend))
		}

		return resolved, err
	}

	segments := strings.Split(resolved, separator)
	for i, segment := range segments {
		segments[i] = strings.TrimSpace(segment)
	}

	resolved = strings.Join(segments, separator)

	if len(p.allowed) == 0 {
		return resolved, err
	}

	for _, segment := range segments {
		_, ok := p.allowed[segment]
		if !ok {
			err = fmt.Errorf("tenant %q is not in the %s allowlist", segment, p.backend)
			return resolved, err
		}
	}

	return resolved, err
}

// Each returns the tenants a discovery request should inspect one at a time,
// so each answer can be attributed to the tenant it came from.
//
// A request that names tenants gets exactly those. A request that names none
// gets every allowed tenant: what a tenant holds cannot be told from its name,
// so finding where something lives means looking in all of them. With no
// allowlist that is the default tenant alone, which is the empty string — no
// header — when tenancy is not configured.
func (p Policy) Each(requested string) (tenants []string, err error) {
	if strings.TrimSpace(requested) == "" && len(p.allowed) > 0 {
		tenants = p.Allowed()
		return tenants, err
	}

	var resolved string

	resolved, err = p.Resolve(requested)
	if err != nil {
		return tenants, err
	}

	tenants = strings.Split(resolved, separator)

	return tenants, err
}

// Unsearched returns the allowed tenants that are not among searched, each of
// which may be a "|"-joined header value. It names what a lookup has not yet
// covered.
func (p Policy) Unsearched(searched ...string) (remaining []string) {
	covered := make(map[string]struct{})

	for _, value := range searched {
		for _, segment := range strings.Split(value, separator) {
			covered[strings.TrimSpace(segment)] = struct{}{}
		}
	}

	remaining = make([]string, 0, len(p.allowed))

	for _, id := range p.Allowed() {
		_, done := covered[id]
		if !done {
			remaining = append(remaining, id)
		}
	}

	return remaining
}
