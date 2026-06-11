package mcp

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nikogura/diagnostic-bot/pkg/authz"
	"github.com/nikogura/diagnostic-bot/pkg/mcp/auth"
)

const testAuthzPolicy = `
authz:
  default: deny
  roles:
    read-only:
      tools: ["k8s_*", "whois_lookup"]
    security:
      tools: ["ecr_*"]
      via: ["mcp"]
  users:
    - name: "Alice"
      emails: ["alice@corp.com"]
      slack_ids: ["U01ALICE"]
      roles: ["read-only"]
    - name: "Carol"
      emails: ["carol@corp.com"]
      slack_ids: ["U02CAROL"]
      roles: ["security"]
`

func testAuthzServer(t *testing.T) (s *Server) {
	t.Helper()

	policy, err := authz.LoadPolicy("", testAuthzPolicy, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	s = &Server{logger: slog.New(slog.DiscardHandler), authorizer: policy}
	return s
}

func TestAuthorizeDisabledIsNoOp(t *testing.T) {
	t.Parallel()

	s := &Server{logger: slog.New(slog.DiscardHandler)} // authorizer nil → disabled

	err := s.authorize(context.Background(), "ecr_scan_results")
	if err != nil {
		t.Errorf("authorize with no policy must be a no-op, got: %v", err)
	}
}

func TestAuthorizeSlackPrincipal(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// Slack identity is the immutable Slack user ID, never the email.
	allowed := authz.NewContext(context.Background(), authz.Principal{SlackID: "U01ALICE", Source: authz.SourceSlack})
	err := s.authorize(allowed, "k8s_get_resource")
	if err != nil {
		t.Errorf("alice (slack id) should be allowed k8s_get_resource over Slack: %v", err)
	}

	denied := authz.NewContext(context.Background(), authz.Principal{SlackID: "U01ALICE", Source: authz.SourceSlack})
	err = s.authorize(denied, "ecr_scan_results")
	if err == nil || !strings.Contains(err.Error(), "not allowed to run") {
		t.Errorf("alice should be denied ecr_scan_results, got: %v", err)
	}
}

func TestAuthorizeSlackEmailDoesNotMatch(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// A Slack principal carrying only an email (the pre-hardening shape) must NOT
	// match a user entry: Slack binds by ID, so a spoofed email grants nothing.
	ctx := authz.NewContext(context.Background(), authz.Principal{Email: "alice@corp.com", Source: authz.SourceSlack})
	err := s.authorize(ctx, "k8s_get_resource")
	if err == nil {
		t.Error("a Slack principal must not be authorized by email; binding is by Slack ID")
	}
}

func TestAuthorizeViaSourceScoping(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// carol's security role grants ecr_* but only via:mcp.
	mcpCtx := authz.NewContext(context.Background(), authz.Principal{Email: "carol@corp.com", Source: authz.SourceMCP})
	err := s.authorize(mcpCtx, "ecr_scan_results")
	if err != nil {
		t.Errorf("carol should be allowed ecr_scan_results over MCP: %v", err)
	}

	slackCtx := authz.NewContext(context.Background(), authz.Principal{SlackID: "U02CAROL", Source: authz.SourceSlack})
	err = s.authorize(slackCtx, "ecr_scan_results")
	if err == nil {
		t.Error("carol's security role is via:mcp; ecr_scan_results over Slack must be denied")
	}
}

func TestAuthorizeMCPPrincipalFromAuthResult(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// Simulate the MCP HTTP path: a verified auth result in ctx, no explicit
	// authz principal. principalFromContext derives source=mcp from it.
	ctx := injectAuthResult(context.Background(), &auth.Result{Authenticated: true, Email: "carol@corp.com"})

	err := s.authorize(ctx, "ecr_scan_results")
	if err != nil {
		t.Errorf("carol (via MCP auth result) should be allowed ecr_scan_results: %v", err)
	}

	err = s.authorize(ctx, "k8s_get_resource")
	if err == nil {
		t.Error("carol has only the security role; k8s_get_resource must be denied")
	}
}

func TestAuthorizeUnidentifiedDenied(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// No principal in context at all → local/anonymous → default-deny.
	err := s.authorize(context.Background(), "k8s_get_resource")
	if err == nil {
		t.Error("an unidentified caller must be denied under default-deny")
	}
}

func TestDispatchToolEnforcesAuthorization(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// alice lacks ecr; DispatchTool must deny BEFORE attempting dispatch (which
	// would otherwise need a real ECR client), proving enforcement is wired in.
	ctx := authz.NewContext(context.Background(), authz.Principal{SlackID: "U01ALICE", Source: authz.SourceSlack})

	_, err := s.DispatchTool(ctx, "ecr_scan_results", map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "not allowed to run") {
		t.Errorf("DispatchTool should deny ecr_scan_results for alice, got: %v", err)
	}
}

func TestDenialMessageIsHelpfulAndLeakFree(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// alice (read-only) is denied ecr over Slack. The message must explain that
	// she's not allowed, honestly list what she CAN do, and name no role/group.
	ctx := authz.NewContext(context.Background(), authz.Principal{SlackID: "U01ALICE", Source: authz.SourceSlack})

	_, err := s.DispatchTool(ctx, "ecr_scan_results", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected a denial")
	}
	msg := err.Error()

	if !strings.Contains(msg, "not allowed to run") {
		t.Errorf("message should tell the user they're not allowed: %q", msg)
	}
	// The denial lists concrete tools from the actual catalog the caller can run.
	if !strings.Contains(msg, "You currently have access to") || !strings.Contains(msg, "whois_lookup") {
		t.Errorf("message should honestly state what they CAN do: %q", msg)
	}
	for _, leak := range []string{"read-only", "security", "grafana-write"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message leaks policy identifier %q: %q", leak, msg)
		}
	}
}
