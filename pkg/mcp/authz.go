package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/authz"
	"github.com/nikogura/diagnostic-bot/pkg/mcp/auth"
	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

// metaToolListMyTools is always permitted: a caller must be able to ask what
// they can do even under a default-deny policy. It reveals only the caller's own
// access, so it is never gated.
const metaToolListMyTools = "list_my_tools"

// Environment variables that configure tool authorization. With neither set,
// authorization is disabled and every front-end gets the full tool surface
// (backward compatible). MCP_AUTHZ_FILE is re-read on change (hot reload).
const (
	envAuthzFile   = "MCP_AUTHZ_FILE"
	envAuthzInline = "MCP_AUTHZ"
)

// loadAuthorizer builds the tool-authorization policy from the environment. A
// configured-but-unparseable policy fails closed (deny all) so a broken file
// never silently disables authorization; an unconfigured policy returns nil,
// leaving authorization off.
func loadAuthorizer(logger *slog.Logger) (authorizer *authz.Policy) {
	path := os.Getenv(envAuthzFile)
	inline := os.Getenv(envAuthzInline)

	if path == "" && inline == "" {
		logger.Info("tool authorization disabled - no MCP_AUTHZ_FILE or MCP_AUTHZ set; all tools available to all callers")
		return authorizer
	}

	policy, err := authz.LoadPolicy(path, inline, logger)
	if err != nil {
		logger.Error("authz policy failed to load - failing closed (deny all) until corrected",
			slog.String("error", err.Error()))
		authorizer = authz.DenyAll(logger)
		return authorizer
	}

	logger.Info("tool authorization enabled",
		slog.String("file", path),
		slog.Bool("inline", inline != ""))
	authorizer = policy
	return authorizer
}

// principalFromContext resolves the identity behind a tool call. The Slack path
// sets an explicit principal (source=slack); the MCP HTTP path carries a
// verified auth result (source=mcp); anything else is a local in-process call.
func (s *Server) principalFromContext(ctx context.Context) (p authz.Principal) {
	got, ok := authz.FromContext(ctx)
	if ok {
		p = got
		return p
	}

	r := auth.ResultFromContext(ctx)
	if r != nil {
		p = authz.Principal{Email: r.Email, Groups: r.Groups, Source: authz.SourceMCP}
		return p
	}

	p = authz.Principal{Source: authz.SourceLocal}
	return p
}

// authorize enforces the tool-access policy for the principal behind ctx,
// returning a non-nil error when the tool is denied. It is a no-op when no
// policy is configured, and always permits the list_my_tools meta-tool. Every
// decision is metered; denials are logged.
func (s *Server) authorize(ctx context.Context, tool string) (err error) {
	if s.authorizer == nil || tool == metaToolListMyTools {
		return err
	}

	p := s.principalFromContext(ctx)
	decision := s.authorizer.Evaluate(p, tool)

	metrics.RecordAuthzDecision(ctx, decision.Allowed, p.Source)

	if decision.Allowed {
		s.logger.DebugContext(ctx, "authz allow",
			slog.String("tool", tool),
			slog.String("email", p.Email),
			slog.String("slack_id", p.SlackID),
			slog.String("source", p.Source))
		return err
	}

	s.logger.WarnContext(ctx, "authz deny",
		slog.String("tool", tool),
		slog.String("email", p.Email),
		slog.String("slack_id", p.SlackID),
		slog.String("source", p.Source),
		slog.String("reason", decision.Reason))

	// Build a user-facing message that explains why and, honestly, what they CAN
	// do — without enumerating the policy (no role or group names, no "ask to be
	// added to X"). On the MCP path the client shows it directly; on the Slack
	// path it returns as a tool error the model relays to the user.
	msg := fmt.Sprintf("permission denied: you are not allowed to run %q — %s", tool, decision.Reason)

	capability := s.capabilities(ctx)
	if len(capability.Here) > 0 {
		msg = fmt.Sprintf("%s. You currently have access to: %s", msg, strings.Join(capability.Here, ", "))
	}

	err = errors.New(msg)
	return err
}

// allows reports whether the principal behind ctx may dispatch tool, with no
// logging or metrics — for capability listing and catalog filtering, which must
// not emit deny audit events. It shares Evaluate with authorize, so it agrees
// with dispatch. The list_my_tools meta-tool is always allowed.
func (s *Server) allows(ctx context.Context, tool string) (ok bool) {
	if s.authorizer == nil || tool == metaToolListMyTools {
		ok = true
		return ok
	}

	ok = s.authorizer.Evaluate(s.principalFromContext(ctx), tool).Allowed
	return ok
}

// toolNames returns the names of every currently-advertised tool.
func (s *Server) toolNames() (names []string) {
	tools := s.ToolDefinitions()
	names = make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}

// capabilities reports what the principal behind ctx can do: the tools that
// dispatch here, and those their roles grant only on another front-end. It is
// the shared source of truth for list_my_tools, the denial message, and catalog
// filtering. With no policy configured, every advertised tool is available.
func (s *Server) capabilities(ctx context.Context) (capability authz.Capability) {
	names := s.toolNames()

	if s.authorizer == nil {
		capability.Here = names
		capability.Elsewhere = map[string][]string{}
		sort.Strings(capability.Here)
		return capability
	}

	capability = s.authorizer.Capabilities(s.principalFromContext(ctx), names)

	// list_my_tools is always available; ensure it shows in the usable set.
	if slices.Contains(names, metaToolListMyTools) && !slices.Contains(capability.Here, metaToolListMyTools) {
		capability.Here = append(capability.Here, metaToolListMyTools)
		sort.Strings(capability.Here)
	}

	return capability
}

// AllowedTools returns the subset of tools the principal behind ctx may actually
// dispatch — filtered by the same check dispatch uses, so the advertised set
// matches enforcement. With no policy configured it returns tools unchanged.
func (s *Server) AllowedTools(ctx context.Context, tools []MCPTool) (allowed []MCPTool) {
	if s.authorizer == nil {
		allowed = tools
		return allowed
	}

	allowed = make([]MCPTool, 0, len(tools))
	for _, t := range tools {
		if s.allows(ctx, t.Name) {
			allowed = append(allowed, t)
		}
	}
	return allowed
}
