package mcp

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func readOnlyTestServer(readOnly bool) (server *Server) {
	server = &Server{
		grafanaClient: &GrafanaClient{},
		readOnly:      readOnly,
		logger:        slog.New(slog.DiscardHandler),
	}
	return server
}

func toolNameSet(tools []MCPTool) (names map[string]bool) {
	names = make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Name] = true
	}
	return names
}

func TestReadOnlyEnabled(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{"TRUE", true},
		{" true ", true},
		{"false", false},
		{"0", false},
		{"", false},
		{"nope", false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("READ_ONLY", tt.value)

			got := ReadOnlyEnabled()
			if got != tt.want {
				t.Errorf("ReadOnlyEnabled() with %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestIsWriteTool(t *testing.T) {
	t.Parallel()

	writes := []string{
		toolGrafanaCreateDashboard,
		toolGrafanaUpdateDashboard,
		toolGrafanaPatchDashboard,
		toolGrafanaDeleteDashboard,
		toolGrafanaCreateFolder,
		toolGrafanaRestoreDashboardVersion,
	}
	for _, name := range writes {
		if !isWriteTool(name) {
			t.Errorf("isWriteTool(%q) = false, want true", name)
		}
	}

	reads := []string{
		toolGrafanaListDashboards,
		toolGrafanaGetDashboard,
		toolGrafanaGetDashboardVersion,
		toolLokiQuery,
		toolDatabaseQuery,
		toolWhoisLookup,
	}
	for _, name := range reads {
		if isWriteTool(name) {
			t.Errorf("isWriteTool(%q) = true, want false", name)
		}
	}
}

func TestReadOnlyWithholdsGrafanaWriteTools(t *testing.T) {
	t.Parallel()

	writeNames := []string{
		toolGrafanaCreateDashboard,
		toolGrafanaUpdateDashboard,
		toolGrafanaPatchDashboard,
		toolGrafanaDeleteDashboard,
		toolGrafanaCreateFolder,
		toolGrafanaRestoreDashboardVersion,
	}

	// Writable: write tools present.
	writable := toolNameSet(readOnlyTestServer(false).ToolDefinitions())
	if !writable[toolGrafanaGetDashboard] {
		t.Fatal("expected grafana read tools when writable")
	}
	for _, name := range writeNames {
		if !writable[name] {
			t.Errorf("expected write tool %q to be advertised when writable", name)
		}
	}

	// Read-only: read tools remain, write tools gone.
	readonly := toolNameSet(readOnlyTestServer(true).ToolDefinitions())
	if !readonly[toolGrafanaGetDashboard] {
		t.Error("grafana read tools must remain available in read-only mode")
	}
	for _, name := range writeNames {
		if readonly[name] {
			t.Errorf("write tool %q must NOT be advertised in read-only mode", name)
		}
	}
}

func TestReadOnlyRejectsWriteDispatch(t *testing.T) {
	t.Parallel()

	server := readOnlyTestServer(true)

	_, err := server.DispatchTool(context.Background(), toolGrafanaDeleteDashboard, map[string]any{"uid": "abc"})

	if err == nil {
		t.Fatal("expected read-only mode to reject a write tool dispatch")
	}

	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("expected a read-only error, got: %v", err)
	}
}
