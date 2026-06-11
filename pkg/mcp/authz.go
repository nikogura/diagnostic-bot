package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/authz"
	"github.com/nikogura/diagnostic-bot/pkg/mcp/auth"
	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

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
// policy is configured. Every decision is metered; denials are logged.
func (s *Server) authorize(ctx context.Context, tool string) (err error) {
	if s.authorizer == nil {
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

	granted := s.authorizer.GrantedTools(p)
	if len(granted) > 0 {
		msg = fmt.Sprintf("%s. You currently have access to: %s", msg, strings.Join(granted, ", "))
	}

	err = errors.New(msg)
	return err
}
