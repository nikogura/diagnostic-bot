package mcp

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"github.com/google/go-github/v57/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSDKServer(t *testing.T) (sdkServer *SDKServer) {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	// Create a minimal legacy server with no backends — only utility tools will register
	legacy := &Server{
		logger:      logger,
		companyName: "TestCorp",
	}

	sdkServer = NewSDKServer(legacy)

	return sdkServer
}

func TestSDKServerStreamableHTTP(t *testing.T) {
	t.Parallel()

	sdkServer := newTestSDKServer(t)

	// Start test HTTP server with Streamable HTTP handler
	ts := httptest.NewServer(sdkServer.StreamableHTTPHandler())
	defer ts.Close()

	ctx := context.Background()

	// Create SDK client
	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "test-client",
		Version: "0.0.1",
	}, nil)

	// Connect via Streamable HTTP
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint: ts.URL,
	}, nil)
	require.NoError(t, err)
	defer session.Close()

	// List tools
	toolsResult, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, toolsResult.Tools, "expected at least utility tools to be registered")

	// Verify whois_lookup tool exists
	foundWhois := false
	for _, tool := range toolsResult.Tools {
		if tool.Name == "whois_lookup" {
			foundWhois = true
			break
		}
	}
	require.True(t, foundWhois, "whois_lookup tool should be registered")
}

// sdkAdvertisedToolNames connects a real MCP client to the SDK server over the
// Streamable HTTP transport and returns the set of advertised tool names — the
// exact set an external MCP client (Claude Code) would discover and can call.
func sdkAdvertisedToolNames(t *testing.T, sdkServer *SDKServer) (names map[string]bool) {
	t.Helper()

	ts := httptest.NewServer(sdkServer.StreamableHTTPHandler())
	t.Cleanup(ts.Close)

	ctx := context.Background()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "test-client",
		Version: "0.0.1",
	}, nil)

	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	toolsResult, err := session.ListTools(ctx, nil)
	require.NoError(t, err)

	names = make(map[string]bool, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		names[tool.Name] = true
	}

	return names
}

// configuredLegacyServer builds a legacy Server with a broad, deterministic set
// of backends enabled — via struct fields (github, gitlab, tempo, graphql) and
// env (cloudwatch, ecr, aws) — so both the SDK transport and getToolDefinitions
// exercise many tool families at once. The clients are minimal non-nil values;
// nothing calls them, only the presence gates matter.
func configuredLegacyServer(t *testing.T) (legacy *Server) {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	legacy = &Server{
		logger:                         logger,
		companyName:                    "TestCorp",
		githubClient:                   github.NewClient(nil),
		gitlabClient:                   &GitLabClient{},
		tempoClients:                   map[string]*TempoClient{"default": {}},
		graphqlClients:                 map[string]*GraphQLClient{"default": {}},
		cloudWatchClientFactory:        defaultCloudWatchClientFactory,
		cloudWatchMetricsClientFactory: defaultCloudWatchMetricsClientFactory,
	}

	return legacy
}

// TestSDKServerAdvertisesCloudWatchTools is the regression test for the v2.3.0
// bug where the CloudWatch Metrics/Alarms tools were wired into the legacy
// dispatch path but never registered on the SDK MCP transport — leaving them
// undiscoverable and uncallable by every MCP client. When CloudWatch is
// configured, all eight CloudWatch tools MUST be advertised over MCP.
func TestSDKServerAdvertisesCloudWatchTools(t *testing.T) {
	// Not parallel: sets env to configure the CloudWatch gate (shared by both paths).
	t.Setenv(envCloudWatchAccounts, `{"test":"arn:aws:iam::111111111111:role/cw-reader"}`)
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	legacy := &Server{
		logger:                         logger,
		companyName:                    "TestCorp",
		cloudWatchClientFactory:        defaultCloudWatchClientFactory,
		cloudWatchMetricsClientFactory: defaultCloudWatchMetricsClientFactory,
	}

	advertised := sdkAdvertisedToolNames(t, NewSDKServer(legacy))

	for _, name := range []string{
		toolCloudWatchLogsQuery,
		toolCloudWatchLogsListGroups,
		toolCloudWatchLogsGetEvents,
		toolCloudWatchMetricsList,
		toolCloudWatchMetricsGetStatistics,
		toolCloudWatchMetricsQuery,
		toolCloudWatchAlarmsList,
		toolCloudWatchAlarmsHistory,
	} {
		assert.True(t, advertised[name], "SDK transport must advertise %q", name)
	}
}

// TestSDKServerToolSurfaceParity is the structural guardrail: the set of tools
// the SDK/MCP transport advertises MUST equal the set the legacy dispatch path
// (getToolDefinitions — which drives Slack and list_my_tools) exposes, for one
// configuration. The two are maintained as separate lists; this test fails the
// moment they drift — the exact class of bug that hid the CloudWatch and AWS
// tool gaps.
func TestSDKServerToolSurfaceParity(t *testing.T) {
	// Not parallel: sets env-based gates (cloudwatch, ecr, aws) deterministically.
	t.Setenv(envCloudWatchAccounts, `{"test":"arn:aws:iam::111111111111:role/cw-reader"}`)
	t.Setenv(envCloudWatchAssumeRole, "")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_DEFAULT_REGION", "")

	legacy := configuredLegacyServer(t)

	advertised := sortedNames(sdkAdvertisedToolNames(t, NewSDKServer(legacy)))
	defined := sortedNames(namesFromTools(legacy.getToolDefinitions()))

	assert.Equal(t, defined, advertised,
		"SDK transport must advertise exactly the tools the legacy path dispatches")

	// Guard against a vacuous pass: the config must actually exercise the
	// families that were previously divergent.
	for _, name := range []string{
		toolCloudWatchMetricsList,
		toolCloudWatchAlarmsList,
		toolSTSGetCallerIdentity,
		toolGitLabGetFile,
		toolTempoGetTrace,
	} {
		assert.Contains(t, advertised, name, "config should exercise %q", name)
	}
}

// namesFromTools collects the names of a tool slice into a set.
func namesFromTools(tools []MCPTool) (names map[string]bool) {
	names = make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}

	return names
}

// sortedNames returns the set's keys as a sorted slice for stable comparison.
func sortedNames(names map[string]bool) (sorted []string) {
	for name := range names {
		sorted = append(sorted, name)
	}

	sort.Strings(sorted)

	return sorted
}

func TestSDKServerToolCall(t *testing.T) {
	t.Parallel()

	sdkServer := newTestSDKServer(t)

	ts := httptest.NewServer(sdkServer.StreamableHTTPHandler())
	defer ts.Close()

	ctx := context.Background()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "test-client",
		Version: "0.0.1",
	}, nil)

	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint: ts.URL,
	}, nil)
	require.NoError(t, err)
	defer session.Close()

	// Call generate_pdf to exercise the dispatch path end-to-end.
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "generate_pdf",
		Arguments: map[string]any{
			"markdown_content": "# Test",
			"filename":         "test",
		},
	})

	// The tool must be dispatched (no "unknown tool" error).
	if err != nil {
		require.NotContains(t, err.Error(), "unknown tool", "tool should be registered and dispatched")
	} else {
		require.NotNil(t, result)
	}
}
