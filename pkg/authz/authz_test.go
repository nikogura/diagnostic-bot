package authz

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// testConfig is the policy used across evaluation tests: a read-only role on
// both front-ends, a platform role, and a grafana-write role restricted to MCP.
func testConfig() (cfg *config) {
	cfg = &config{
		Default: defaultDeny,
		Roles: map[string]role{
			"read-only": {Tools: []string{"k8s_*", "prometheus_*"}},
			"platform":  {Tools: []string{"ec2_describe_*", "cloudwatch_*"}},
			"grafana-write": {
				Tools: []string{"grafana_*"},
				Via:   []string{SourceMCP},
			},
			"everything": {Tools: []string{"*"}},
		},
		Users: []user{
			{Name: "Alice", Emails: []string{"alice@corp.com", "alice@alt.com"}, SlackIDs: []string{"U01ALICE"}, Roles: []string{"read-only", "platform"}},
			{Name: "Carol", Emails: []string{"carol@corp.com"}, SlackIDs: []string{"U02CAROL"}, Roles: []string{"grafana-write"}},
			{Name: "Root", Emails: []string{"root@corp.com"}, Roles: []string{"everything"}},
		},
		Groups: map[string][]string{
			"sre": {"platform"},
		},
	}
	return cfg
}

func TestEvaluate(t *testing.T) {
	t.Parallel()

	cfg := testConfig()

	cases := []struct {
		name    string
		p       Principal
		tool    string
		allowed bool
	}{
		{"mcp email grants k8s glob", Principal{Email: "alice@corp.com", Source: SourceMCP}, "k8s_get_resource", true},
		{"read-only grants prometheus glob", Principal{Email: "alice@corp.com", Source: SourceMCP}, "prometheus_query", true},
		{"additive: alice also has platform", Principal{Email: "alice@corp.com", Source: SourceMCP}, "ec2_describe_vpcs", true},
		{"second email resolves same user", Principal{Email: "alice@alt.com", Source: SourceMCP}, "k8s_get_resource", true},
		{"email match is case-insensitive", Principal{Email: "ALICE@corp.com", Source: SourceMCP}, "k8s_get_resource", true},
		{"slack id resolves same user", Principal{SlackID: "U01ALICE", Source: SourceSlack}, "k8s_get_resource", true},
		{"slack id gets additive roles", Principal{SlackID: "U01ALICE", Source: SourceSlack}, "ec2_describe_vpcs", true},
		{"unknown slack id denied", Principal{SlackID: "U99NOPE", Source: SourceSlack}, "k8s_get_resource", false},
		{"slack id is exact (case-sensitive)", Principal{SlackID: "u01alice", Source: SourceSlack}, "k8s_get_resource", false},
		{"default-deny: alice cannot grafana", Principal{Email: "alice@corp.com", Source: SourceMCP}, "grafana_create_dashboard", false},
		{"unknown user denied", Principal{Email: "nobody@corp.com", Source: SourceMCP}, "k8s_get_resource", false},
		{"empty principal denied", Principal{Source: SourceSlack}, "k8s_get_resource", false},
		{"via mcp: grafana-write allowed over MCP", Principal{Email: "carol@corp.com", Source: SourceMCP}, "grafana_create_dashboard", true},
		{"via mcp: grafana-write blocked over Slack id", Principal{SlackID: "U02CAROL", Source: SourceSlack}, "grafana_create_dashboard", false},
		{"group grants platform role", Principal{Email: "dave@corp.com", Groups: []string{"sre"}, Source: SourceMCP}, "cloudwatch_logs_query", true},
		{"unknown group grants nothing", Principal{Email: "dave@corp.com", Groups: []string{"interns"}, Source: SourceMCP}, "cloudwatch_logs_query", false},
		{"wildcard role grants anything", Principal{Email: "root@corp.com", Source: SourceMCP}, "ecr_scan_results", true},
		{"slack id cannot reach an email-only user's roles", Principal{SlackID: "U03ROOT", Source: SourceSlack}, "ecr_scan_results", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := evaluate(cfg, tc.p, tc.tool)
			if got.Allowed != tc.allowed {
				t.Errorf("evaluate(%+v, %q) allowed=%v (%s), want %v", tc.p, tc.tool, got.Allowed, got.Reason, tc.allowed)
			}
		})
	}
}

// policyIdentifiers are the role and group names in testConfig. None of them
// must ever appear in a user-facing denial reason — that would enumerate the
// policy and tell a user which role/group to acquire.
var policyIdentifiers = []string{"read-only", "platform", "grafana-write", "everything", "sre"} //nolint:gochecknoglobals // test fixture

func assertNoPolicyLeak(t *testing.T, s string) {
	t.Helper()
	for _, id := range policyIdentifiers {
		if strings.Contains(s, id) {
			t.Errorf("text leaks policy identifier %q: %q", id, s)
		}
	}
}

func TestDenyReasonsAreActionableAndLeakFree(t *testing.T) {
	t.Parallel()

	cfg := testConfig()

	// via-scoped denial: carol's grafana-write is via:mcp, so over Slack she is
	// denied with an interface hint — what she CAN do — and no role/group name.
	viaDeny := evaluate(cfg, Principal{SlackID: "U02CAROL", Source: SourceSlack}, "grafana_create_dashboard")
	if viaDeny.Allowed {
		t.Fatal("precondition: grafana over Slack must be denied for carol")
	}
	if !strings.Contains(viaDeny.Reason, SourceMCP) {
		t.Errorf("via denial should point at the mcp interface, got: %q", viaDeny.Reason)
	}
	assertNoPolicyLeak(t, viaDeny.Reason)

	// missing-role denial: alice lacks ecr; the reason is generic, naming nothing.
	missing := evaluate(cfg, Principal{SlackID: "U01ALICE", Source: SourceSlack}, "ecr_scan_results")
	if missing.Allowed {
		t.Fatal("precondition: alice must not have ecr")
	}
	assertNoPolicyLeak(t, missing.Reason)

	// unidentified denial.
	anon := evaluate(cfg, Principal{Source: SourceSlack}, "k8s_get_resource")
	if !strings.Contains(anon.Reason, "identity") {
		t.Errorf("unidentified denial should mention identity, got: %q", anon.Reason)
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	candidates := []string{
		"k8s_get_resource", "prometheus_query", "ec2_describe_vpcs",
		"cloudwatch_logs_query", "grafana_create_dashboard", "ecr_scan_results",
	}

	// alice (read-only + platform) over MCP: concrete tools she can dispatch.
	alice := capabilities(cfg, Principal{Email: "alice@corp.com", Source: SourceMCP}, candidates)
	for _, want := range []string{"k8s_get_resource", "prometheus_query", "ec2_describe_vpcs", "cloudwatch_logs_query"} {
		if !slices.Contains(alice.Here, want) {
			t.Errorf("alice should be able to run %q: %v", want, alice.Here)
		}
	}
	for _, no := range []string{"grafana_create_dashboard", "ecr_scan_results"} {
		if slices.Contains(alice.Here, no) {
			t.Errorf("alice should NOT be able to run %q: %v", no, alice.Here)
		}
	}

	// carol's grafana-write is via:mcp. Over Slack the tool isn't "here" but is
	// reported as available elsewhere (mcp) — honest, and names no role/group.
	carolSlack := capabilities(cfg, Principal{SlackID: "U02CAROL", Source: SourceSlack}, candidates)
	if slices.Contains(carolSlack.Here, "grafana_create_dashboard") {
		t.Errorf("carol over Slack should not have grafana_create_dashboard here: %v", carolSlack.Here)
	}
	if got := carolSlack.Elsewhere["grafana_create_dashboard"]; !slices.Contains(got, SourceMCP) {
		t.Errorf("grafana_create_dashboard should be flagged available via mcp, got %v", got)
	}
}

func TestEvaluateDefaultAllow(t *testing.T) {
	t.Parallel()

	cfg := &config{Default: defaultAllow, Roles: map[string]role{}}

	// With default allow, a tool no role grants is still permitted.
	got := evaluate(cfg, Principal{Email: "anyone@corp.com", Source: SourceMCP}, "ecr_scan_results")
	if !got.Allowed {
		t.Errorf("default-allow should permit ungranted tool, got deny (%s)", got.Reason)
	}
}

func TestMatchTool(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern string
		tool    string
		want    bool
	}{
		{"*", "anything_at_all", true},
		{"grafana_*", "grafana_get_dashboard", true},
		{"grafana_*", "grafana_delete_dashboard", true},
		{"grafana_*", "k8s_get_resource", false},
		{"grafana_get_*", "grafana_get_dashboard", true},
		{"grafana_get_*", "grafana_create_dashboard", false},
		{"ecr_scan_results", "ecr_scan_results", true},
		{"ecr_scan_results", "ecr_scan_results_extra", false},
		{"k8s_get_resource", "k8s_get_resources", false},
	}

	for _, tc := range cases {
		got := matchTool(tc.pattern, tc.tool)
		if got != tc.want {
			t.Errorf("matchTool(%q, %q) = %v, want %v", tc.pattern, tc.tool, got, tc.want)
		}
	}
}

func TestSourceAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		via    []string
		source string
		want   bool
	}{
		{nil, SourceSlack, true},
		{[]string{}, SourceMCP, true},
		{[]string{SourceMCP}, SourceMCP, true},
		{[]string{SourceMCP}, SourceSlack, false},
		{[]string{SourceSlack, SourceMCP}, SourceSlack, true},
	}

	for _, tc := range cases {
		got := sourceAllowed(tc.via, tc.source)
		if got != tc.want {
			t.Errorf("sourceAllowed(%v, %q) = %v, want %v", tc.via, tc.source, got, tc.want)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()

	want := Principal{Email: "alice@corp.com", Groups: []string{"sre"}, Source: SourceSlack}
	ctx := NewContext(context.Background(), want)

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext returned ok=false after NewContext")
	}
	if got.Email != want.Email || got.Source != want.Source || len(got.Groups) != 1 {
		t.Errorf("round-trip principal = %+v, want %+v", got, want)
	}
}

func TestFromContextAbsent(t *testing.T) {
	t.Parallel()

	_, ok := FromContext(context.Background())
	if ok {
		t.Error("FromContext on a bare context should return ok=false")
	}
}
