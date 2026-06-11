package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/nikogura/diagnostic-bot/pkg/authz"
)

// TestCapabilitiesMatchDispatch is the acceptance test: for EVERY advertised
// tool, what the capability listing reports as usable must equal what dispatch
// actually permits — proven by checking each listed tool (must dispatch) and
// each omitted tool (must deny). This is the no-drift guarantee.
func TestCapabilitiesMatchDispatch(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)
	ctx := authz.NewContext(context.Background(), authz.Principal{SlackID: "U01ALICE", Source: authz.SourceSlack})

	here := make(map[string]bool)
	for _, name := range s.capabilities(ctx).Here {
		here[name] = true
	}

	for _, tool := range s.ToolDefinitions() {
		err := s.authorize(ctx, tool.Name)
		dispatchOK := err == nil
		if here[tool.Name] != dispatchOK {
			t.Errorf("tool %q: reported-usable=%v, dispatch-allowed=%v — they must match", tool.Name, here[tool.Name], dispatchOK)
		}
	}
}

// TestListMyToolsReportsUsableSet checks the deterministic "what can I do?"
// answer: it lists tools the caller can run, omits ones they can't, and names no
// role or group.
func TestListMyToolsReportsUsableSet(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)
	ctx := authz.NewContext(context.Background(), authz.Principal{SlackID: "U01ALICE", Source: authz.SourceSlack})

	out, err := s.executeListMyTools(ctx, nil)
	if err != nil {
		t.Fatalf("executeListMyTools: %v", err)
	}

	if !strings.Contains(out, "whois_lookup") || !strings.Contains(out, "list_my_tools") {
		t.Errorf("list_my_tools should report whois_lookup and itself: %q", out)
	}
	if strings.Contains(out, "generate_pdf") {
		t.Errorf("list_my_tools must not report generate_pdf (alice has no role for it): %q", out)
	}
	for _, leak := range []string{"read-only", "security"} {
		if strings.Contains(out, leak) {
			t.Errorf("list_my_tools leaks policy identifier %q: %q", leak, out)
		}
	}
}

// TestListMyToolsAlwaysAllowed verifies the meta-tool is never denied, so even a
// default-denied caller can ask what they can do.
func TestListMyToolsAlwaysAllowed(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)

	// A wholly unidentified caller (would be default-denied for any real tool).
	err := s.authorize(context.Background(), metaToolListMyTools)
	if err != nil {
		t.Errorf("list_my_tools must always be permitted, got: %v", err)
	}
}

// TestAllowedToolsFiltersCatalog verifies the catalog handed to the model is
// trimmed to the caller's usable set (the same set list_my_tools reports).
func TestAllowedToolsFiltersCatalog(t *testing.T) {
	t.Parallel()

	s := testAuthzServer(t)
	ctx := authz.NewContext(context.Background(), authz.Principal{SlackID: "U01ALICE", Source: authz.SourceSlack})

	allowed := s.AllowedTools(ctx, s.ToolDefinitions())

	names := make(map[string]bool)
	for _, tool := range allowed {
		names[tool.Name] = true
	}

	if !names["whois_lookup"] || !names["list_my_tools"] {
		t.Errorf("AllowedTools should keep whois_lookup and list_my_tools: %v", names)
	}
	if names["generate_pdf"] {
		t.Errorf("AllowedTools should drop generate_pdf for alice: %v", names)
	}
}
