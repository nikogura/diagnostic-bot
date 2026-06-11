package authz

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func discardLogger() (logger *slog.Logger) {
	logger = slog.New(slog.DiscardHandler)
	return logger
}

const sampleYAML = `
authz:
  default: deny
  roles:
    read-only:
      tools: ["k8s_*", "prometheus_*"]
    grafana-write:
      tools: ["grafana_*"]
      via: ["mcp"]
  users:
    - name: "Alice"
      emails: ["alice@corp.com"]
      slack_ids: ["U01ALICE"]
      roles: ["read-only"]
  groups:
    sre: ["read-only"]
`

func TestLoadPolicyDisabledWhenUnset(t *testing.T) {
	t.Parallel()

	policy, err := LoadPolicy("", "", discardLogger())
	if err != nil {
		t.Fatalf("LoadPolicy(empty): %v", err)
	}
	if policy != nil {
		t.Error("LoadPolicy with no path and no inline must return nil (disabled)")
	}
}

func TestLoadPolicyInline(t *testing.T) {
	t.Parallel()

	policy, err := LoadPolicy("", sampleYAML, discardLogger())
	if err != nil {
		t.Fatalf("LoadPolicy(inline): %v", err)
	}
	if policy == nil {
		t.Fatal("LoadPolicy(inline) returned nil")
	}

	// MCP path: matched by email.
	allow := policy.Evaluate(Principal{Email: "alice@corp.com", Source: SourceMCP}, "k8s_get_resource")
	if !allow.Allowed {
		t.Errorf("alice (email) should be allowed k8s_get_resource, got deny (%s)", allow.Reason)
	}

	// Slack path: matched by immutable Slack ID, not email.
	allowSlack := policy.Evaluate(Principal{SlackID: "U01ALICE", Source: SourceSlack}, "k8s_get_resource")
	if !allowSlack.Allowed {
		t.Errorf("alice (slack id) should be allowed k8s_get_resource, got deny (%s)", allowSlack.Reason)
	}

	deny := policy.Evaluate(Principal{Email: "alice@corp.com", Source: SourceMCP}, "ecr_scan_results")
	if deny.Allowed {
		t.Error("alice should be denied ecr_scan_results under default-deny")
	}
}

func TestLoadPolicyInvalidYAML(t *testing.T) {
	t.Parallel()

	_, err := LoadPolicy("", "authz: [this is not a map", discardLogger())
	if err == nil {
		t.Error("LoadPolicy with invalid YAML must return an error")
	}
}

func TestLoadPolicyInvalidDefault(t *testing.T) {
	t.Parallel()

	_, err := LoadPolicy("", "authz:\n  default: maybe\n", discardLogger())
	if err == nil {
		t.Error("LoadPolicy with an invalid default must return an error")
	}
}

func TestLoadPolicyEmptyDefaultIsDeny(t *testing.T) {
	t.Parallel()

	// No default specified → fail closed (deny).
	policy, err := LoadPolicy("", "authz:\n  roles:\n    r: {tools: [\"k8s_*\"]}\n", discardLogger())
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	got := policy.Evaluate(Principal{Email: "x@corp.com", Source: SourceMCP}, "k8s_get_resource")
	if got.Allowed {
		t.Error("an unmapped user must be denied when default is unset (fail-closed)")
	}
}

func TestDenyAll(t *testing.T) {
	t.Parallel()

	policy := DenyAll(discardLogger())
	got := policy.Evaluate(Principal{Email: "root@corp.com", Source: SourceMCP}, "k8s_get_resource")
	if got.Allowed {
		t.Error("DenyAll must deny every tool for everyone")
	}
}

func TestLoadPolicyFromFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "authz.yaml")
	writeErr := os.WriteFile(path, []byte(sampleYAML), 0o600)
	if writeErr != nil {
		t.Fatalf("write temp policy: %v", writeErr)
	}

	policy, err := LoadPolicy(path, "", discardLogger())
	if err != nil {
		t.Fatalf("LoadPolicy(file): %v", err)
	}

	got := policy.Evaluate(Principal{Email: "alice@corp.com", Source: SourceMCP}, "prometheus_query")
	if !got.Allowed {
		t.Errorf("alice should be allowed prometheus_query from file policy, got deny (%s)", got.Reason)
	}
}

func TestLoadPolicyMissingFile(t *testing.T) {
	t.Parallel()

	_, err := LoadPolicy(filepath.Join(t.TempDir(), "nope.yaml"), "", discardLogger())
	if err == nil {
		t.Error("LoadPolicy with a missing file must return an error (caller fails closed)")
	}
}

func TestShippedExamplePolicyLoads(t *testing.T) {
	t.Parallel()

	// The example we ship to users must always parse and behave. It is part of
	// the distribution, so it is tested like code.
	policy, err := LoadPolicy(filepath.Join("..", "..", "examples", "authz.yaml"), "", discardLogger())
	if err != nil {
		t.Fatalf("examples/authz.yaml must load cleanly: %v", err)
	}

	// alice has the security role (via:mcp): ecr over MCP (email) allowed, but the
	// same role over Slack (by her Slack ID) is denied by via-scoping.
	overMCP := policy.Evaluate(Principal{Email: "alice@example.com", Source: SourceMCP}, "ecr_scan_results")
	if !overMCP.Allowed {
		t.Errorf("example policy: alice should get ecr_scan_results over MCP, got deny (%s)", overMCP.Reason)
	}

	overSlack := policy.Evaluate(Principal{SlackID: "U01ALICE000", Source: SourceSlack}, "ecr_scan_results")
	if overSlack.Allowed {
		t.Error("example policy: security role is via:mcp; ecr_scan_results over Slack must be denied")
	}

	// bob is read-only → k8s allowed over Slack (matched by his Slack ID).
	bobK8s := policy.Evaluate(Principal{SlackID: "U02BOB00000", Source: SourceSlack}, "k8s_get_resource")
	if !bobK8s.Allowed {
		t.Errorf("example policy: bob should get k8s_get_resource, got deny (%s)", bobK8s.Reason)
	}
}

func TestPolicyHotReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "authz.yaml")

	// v1: alice gets read-only.
	writeErr := os.WriteFile(path, []byte(sampleYAML), 0o600)
	if writeErr != nil {
		t.Fatalf("write v1: %v", writeErr)
	}

	policy, err := LoadPolicy(path, "", discardLogger())
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	before := policy.Evaluate(Principal{Email: "alice@corp.com", Source: SourceMCP}, "ecr_scan_results")
	if before.Allowed {
		t.Fatal("precondition: alice must not have ecr access in v1")
	}

	// v2: alice gets a role granting ecr. Bump mtime forward so the reload fires
	// even on coarse filesystem timestamps.
	v2 := `
authz:
  default: deny
  roles:
    security:
      tools: ["ecr_*"]
  users:
    - emails: ["alice@corp.com"]
      roles: ["security"]
`
	writeErr = os.WriteFile(path, []byte(v2), 0o600)
	if writeErr != nil {
		t.Fatalf("write v2: %v", writeErr)
	}
	future := time.Now().Add(2 * time.Second)
	chErr := os.Chtimes(path, future, future)
	if chErr != nil {
		t.Fatalf("chtimes: %v", chErr)
	}

	after := policy.Evaluate(Principal{Email: "alice@corp.com", Source: SourceMCP}, "ecr_scan_results")
	if !after.Allowed {
		t.Errorf("after hot reload, alice should have ecr access, got deny (%s)", after.Reason)
	}
}
