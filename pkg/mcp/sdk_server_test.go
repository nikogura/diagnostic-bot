package mcp

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
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
