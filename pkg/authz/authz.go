// Package authz implements role-based authorization over the bot's tool
// surface. Identity (email + groups + the front-end the request arrived on) is
// resolved at each front-end and carried in the request context; the dispatch
// layer reads it back and asks a Policy whether a given tool may run.
//
// The model is deliberately simple and additive: a principal holds the union of
// the roles bound to its emails and groups, each role grants a set of tool-name
// globs (optionally scoped to specific front-ends via `via`), and a tool is
// allowed if any held, in-scope role grants it. Anything not granted falls to
// the configured default (deny or allow). The bot's own credentials are
// unchanged — this filters which tools a requester may invoke, nothing more.
package authz

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Front-end sources a request can arrive on. Roles may be scoped to a subset of
// these via their `via` list; an empty `via` means the role applies everywhere.
const (
	SourceSlack = "slack"
	SourceMCP   = "mcp"
	SourceLocal = "local"
)

// Policy default modes.
const (
	defaultDeny  = "deny"
	defaultAllow = "allow"
)

// Principal is the resolved identity behind a tool call. The MCP path carries a
// verified Email (and Groups) from the OIDC/Google token; the Slack path carries
// the immutable SlackID (never the user-editable profile email) so a user cannot
// evade authorization by changing their email.
type Principal struct {
	Email   string
	SlackID string
	Groups  []string
	Source  string
}

// Decision is the outcome of an authorization check. Reason is for audit logs.
type Decision struct {
	Allowed bool
	Reason  string
}

// Capability is what a principal can do over a candidate tool set on its current
// front-end. Here are the tools that actually dispatch; Elsewhere maps a tool
// the principal's roles grant but that is scoped to another front-end to the
// front-end(s) it is available on ("available via mcp, not here"). It reflects
// only the requester's own access — never roles, groups, or what they'd need.
type Capability struct {
	Here      []string
	Elsewhere map[string][]string
}

// config is the parsed policy document (the body under the top-level `authz:`).
//
// Groups maps a group name to roles. Groups come from the verified OIDC/Google
// token, so they only ever apply on the MCP path — Slack principals carry no
// groups and are matched solely by their slack_ids.
type config struct {
	Default string              `yaml:"default"`
	Roles   map[string]role     `yaml:"roles"`
	Users   []user              `yaml:"users"`
	Groups  map[string][]string `yaml:"groups"`
}

// role grants a set of tool-name globs, optionally restricted to certain
// front-ends. An empty Via applies on every front-end.
type role struct {
	Tools []string `yaml:"tools"`
	Via   []string `yaml:"via,omitempty"`
}

// user binds a person's identifiers to one or more roles. Name is for human
// readability of the config only — it is not used in matching. Emails match the
// MCP path (case-insensitive); SlackIDs match the Slack path (the immutable
// Slack user ID, exact match). A user may list both so the same person is
// recognized on both front-ends.
type user struct {
	Name     string   `yaml:"name"`
	Emails   []string `yaml:"emails"`
	SlackIDs []string `yaml:"slack_ids"`
	Roles    []string `yaml:"roles"`
}

// document is the top-level file shape: an `authz:` key wrapping the config.
type document struct {
	Authz config `yaml:"authz"`
}

type principalKey struct{}

// NewContext returns a context carrying the principal. Front-ends set this at
// the request boundary; the dispatch layer reads it back via FromContext.
func NewContext(ctx context.Context, p Principal) (newCtx context.Context) {
	newCtx = context.WithValue(ctx, principalKey{}, p)
	return newCtx
}

// FromContext returns the principal carried by ctx, if one was set.
func FromContext(ctx context.Context) (p Principal, ok bool) {
	p, ok = ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// validate normalizes and checks a parsed config. An empty default becomes
// deny (fail-closed); anything other than deny/allow is an error.
func validate(cfg *config) (err error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Default)) {
	case "", defaultDeny:
		cfg.Default = defaultDeny
	case defaultAllow:
		cfg.Default = defaultAllow
	default:
		err = fmt.Errorf("authz: invalid default %q (want %q or %q)", cfg.Default, defaultDeny, defaultAllow)
		return err
	}
	return err
}

// assess is the single decision core every path shares: for one tool it reports
// whether the principal may dispatch it on its own front-end, and — when a held
// role grants it but only on a different front-end — which front-end(s) that is.
// Dispatch (evaluate), the denial reason, and capability listing all build on
// this, so the reported and enforced sets cannot drift. Pure: no I/O.
func assess(cfg *config, p Principal, tool string) (allowed bool, elsewhere []string) {
	for _, name := range matchedRoles(cfg, p) {
		r, ok := cfg.Roles[name]
		if !ok || !roleGrantsTool(r, tool) {
			continue
		}
		if sourceAllowed(r.Via, p.Source) {
			allowed = true
			return allowed, elsewhere
		}
		// A held role grants the tool, but not on this front-end — remember
		// where it would, so callers can point the user at the right place.
		elsewhere = append(elsewhere, r.Via...)
	}

	if strings.EqualFold(cfg.Default, defaultAllow) {
		allowed = true
		return allowed, elsewhere
	}

	elsewhere = dedupe(elsewhere)
	return allowed, elsewhere
}

// evaluate decides whether tool is permitted for p under cfg. On a denial,
// Reason is a user-facing explanation of *why* (wrong interface, missing role,
// or unresolved identity) so the bot can tell the requester.
func evaluate(cfg *config, p Principal, tool string) (decision Decision) {
	allowed, elsewhere := assess(cfg, p, tool)
	if allowed {
		decision = Decision{Allowed: true, Reason: "granted by a matching role"}
		return decision
	}

	decision = Decision{Allowed: false, Reason: denyReason(p, elsewhere)}
	return decision
}

// capabilities partitions a candidate tool set for a principal: Here lists the
// tools that dispatch on the principal's front-end; Elsewhere maps a tool the
// principal's roles grant but that is scoped to another front-end to where it
// is available. It shares assess with dispatch, so Here matches dispatch exactly.
func capabilities(cfg *config, p Principal, candidates []string) (capability Capability) {
	capability.Elsewhere = make(map[string][]string)

	for _, tool := range candidates {
		allowed, elsewhere := assess(cfg, p, tool)
		if allowed {
			capability.Here = append(capability.Here, tool)
			continue
		}
		if len(elsewhere) > 0 {
			capability.Elsewhere[tool] = elsewhere
		}
	}

	sort.Strings(capability.Here)
	return capability
}

// roleGrantsTool reports whether any of the role's tool globs match tool.
func roleGrantsTool(r role, tool string) (grants bool) {
	for _, pattern := range r.Tools {
		if matchTool(pattern, tool) {
			grants = true
			return grants
		}
	}
	return grants
}

// denyReason builds a user-facing explanation for a denial. If a role the
// requester holds would grant the tool on a different front-end, it names that
// front-end (the actionable case); if their identity could not be resolved it
// says so; otherwise they simply lack a granting role.
func denyReason(p Principal, allowedSources []string) (reason string) {
	if len(allowedSources) > 0 {
		reason = fmt.Sprintf("this tool is only available over the %s interface, not %s",
			strings.Join(dedupe(allowedSources), "/"), p.Source)
		return reason
	}

	identified := (p.Source == SourceSlack && p.SlackID != "") || (p.Source != SourceSlack && p.Email != "")
	if !identified {
		reason = "your identity could not be resolved, so no tools are permitted"
		return reason
	}

	reason = "your role does not grant access to this tool"
	return reason
}

// dedupe returns the input with duplicates removed, preserving order.
func dedupe(in []string) (out []string) {
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// matchedRoles returns the de-duplicated set of role names a principal holds:
// every role bound to one of its emails, plus every role bound to one of its
// groups. Roles are additive — a principal gets the union of their grants.
func matchedRoles(cfg *config, p Principal) (names []string) {
	seen := make(map[string]bool)

	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	for _, u := range cfg.Users {
		if principalMatchesUser(p, u) {
			for _, name := range u.Roles {
				add(name)
			}
		}
	}

	for _, g := range p.Groups {
		for _, name := range cfg.Groups[g] {
			add(name)
		}
	}

	return names
}

// principalMatchesUser reports whether a principal matches a user entry, using
// the hardened identifier for its front-end: the Slack path matches ONLY by the
// immutable Slack user ID (never the user-editable email, so a user can't
// escalate by changing their email); every other path matches by the verified
// email. Slack user IDs are case-sensitive (exact match); emails are not.
func principalMatchesUser(p Principal, u user) (matched bool) {
	if p.Source == SourceSlack {
		matched = p.SlackID != "" && slices.Contains(u.SlackIDs, p.SlackID)
		return matched
	}

	matched = p.Email != "" && emailMatches(u.Emails, p.Email)
	return matched
}

// emailMatches reports whether email is in list, case-insensitively (email
// addresses are not case-sensitive).
func emailMatches(list []string, email string) (matched bool) {
	for _, e := range list {
		if strings.EqualFold(e, email) {
			matched = true
			return matched
		}
	}
	return matched
}

// sourceAllowed reports whether a role with the given via list applies on the
// request's source. An empty via means the role applies on every front-end.
func sourceAllowed(via []string, source string) (allowed bool) {
	if len(via) == 0 {
		allowed = true
		return allowed
	}
	for _, v := range via {
		if strings.EqualFold(v, source) {
			allowed = true
			return allowed
		}
	}
	return allowed
}

// matchTool reports whether a tool name matches a glob pattern. Supported
// forms: "*" (any tool), "prefix_*" (prefix match), or an exact tool name.
func matchTool(pattern, tool string) (matched bool) {
	if pattern == "*" {
		matched = true
		return matched
	}
	prefix, isPrefixGlob := strings.CutSuffix(pattern, "*")
	if isPrefixGlob {
		matched = strings.HasPrefix(tool, prefix)
		return matched
	}
	matched = pattern == tool
	return matched
}
