package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

func newTestGitLabServer(t *testing.T, handler http.HandlerFunc) (server *httptest.Server, client *GitLabClient) {
	t.Helper()

	server = httptest.NewServer(handler)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	glClient, err := gitlab.NewClient("test-token", gitlab.WithBaseURL(server.URL+"/api/v4"))
	require.NoError(t, err)

	client = &GitLabClient{
		client: glClient,
		logger: logger,
	}

	return server, client
}

func newTestServerWithGitLab(t *testing.T, handler http.HandlerFunc) (testServer *httptest.Server, mcpServer *Server) {
	t.Helper()

	var glClient *GitLabClient
	testServer, glClient = newTestGitLabServer(t, handler)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mcpServer = &Server{
		gitlabClient: glClient,
		logger:       logger,
		companyName:  "TestCorp",
	}

	return testServer, mcpServer
}

func TestGetGitLabTools(t *testing.T) {
	t.Parallel()

	tools := getGitLabTools()
	require.Len(t, tools, 3)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	assert.True(t, names[toolGitLabGetFile])
	assert.True(t, names[toolGitLabListDirectory])
	assert.True(t, names[toolGitLabSearchCode])
}

func TestExecuteGitLabGetFile(t *testing.T) {
	t.Parallel()

	fileContent := "package main\n\nfunc main() {}\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(fileContent))

	ts, server := newTestServerWithGitLab(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"file_name":      "main.go",
			"file_path":      "cmd/main.go",
			"size":           len(fileContent),
			"encoding":       "base64",
			"content":        encoded,
			"ref":            "main",
			"blob_id":        "abc123",
			"commit_id":      "def456",
			"last_commit_id": "def456",
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer ts.Close()

	ctx := context.Background()
	result, err := server.executeGitLabGetFile(ctx, map[string]interface{}{
		"project": "my-group/my-project",
		"path":    "cmd/main.go",
		"ref":     "main",
	})

	require.NoError(t, err)
	assert.Contains(t, result, "package main")
	assert.Contains(t, result, "cmd/main.go")
}

func TestExecuteGitLabGetFileDefaultRef(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithGitLab(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify the ref parameter defaults to main
		assert.Contains(t, r.URL.Query().Get("ref"), "main")
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"file_name": "README.md",
			"file_path": "README.md",
			"size":      5,
			"encoding":  "base64",
			"content":   base64.StdEncoding.EncodeToString([]byte("hello")),
			"ref":       "main",
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer ts.Close()

	ctx := context.Background()
	result, err := server.executeGitLabGetFile(ctx, map[string]interface{}{
		"project": "my-group/my-project",
		"path":    "README.md",
		// No ref — should default to "main"
	})

	require.NoError(t, err)
	assert.Contains(t, result, "hello")
}

func TestExecuteGitLabGetFileMissingParams(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{
		gitlabClient: &GitLabClient{logger: logger},
		logger:       logger,
	}

	ctx := context.Background()

	_, err := server.executeGitLabGetFile(ctx, map[string]interface{}{
		"path": "foo.go",
	})
	require.ErrorContains(t, err, "project parameter is required")

	_, err = server.executeGitLabGetFile(ctx, map[string]interface{}{
		"project": "my-group/my-project",
	})
	require.ErrorContains(t, err, "path parameter is required")
}

func TestExecuteGitLabGetFileNotConfigured(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{logger: logger}

	ctx := context.Background()
	_, err := server.executeGitLabGetFile(ctx, map[string]interface{}{
		"project": "x",
		"path":    "y",
	})
	assert.ErrorContains(t, err, "GitLab access not configured")
}

func TestExecuteGitLabListDirectory(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithGitLab(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := []map[string]interface{}{
			{"id": "1", "name": "main.go", "type": "blob", "path": "cmd/main.go", "mode": "100644"},
			{"id": "2", "name": "utils", "type": "tree", "path": "cmd/utils", "mode": "040000"},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer ts.Close()

	ctx := context.Background()
	result, err := server.executeGitLabListDirectory(ctx, map[string]interface{}{
		"project": "my-group/my-project",
		"path":    "cmd",
	})

	require.NoError(t, err)
	assert.Contains(t, result, "main.go")
	assert.Contains(t, result, "utils")
	assert.Contains(t, result, "Entries: 2")
}

func TestExecuteGitLabListDirectoryNotConfigured(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{logger: logger}

	ctx := context.Background()
	_, err := server.executeGitLabListDirectory(ctx, map[string]interface{}{
		"project": "x",
		"path":    "y",
	})
	assert.ErrorContains(t, err, "GitLab access not configured")
}

func TestExecuteGitLabSearchCode(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithGitLab(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := []map[string]interface{}{
			{
				"basename":   "main",
				"data":       "func main() {}",
				"path":       "cmd/main.go",
				"filename":   "main.go",
				"id":         nil,
				"ref":        "main",
				"startline":  1,
				"project_id": 42,
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer ts.Close()

	ctx := context.Background()
	result, err := server.executeGitLabSearchCode(ctx, map[string]interface{}{
		"query": "func main",
	})

	require.NoError(t, err)
	assert.Contains(t, result, "main.go")
	assert.Contains(t, result, "Found 1 results")
}

func TestExecuteGitLabSearchCodeMissingQuery(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithGitLab(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()

	ctx := context.Background()
	_, err := server.executeGitLabSearchCode(ctx, map[string]interface{}{})
	assert.ErrorContains(t, err, "query parameter is required")
}

func TestExecuteGitLabSearchCodeNotConfigured(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{logger: logger}

	ctx := context.Background()
	_, err := server.executeGitLabSearchCode(ctx, map[string]interface{}{
		"query": "test",
	})
	assert.ErrorContains(t, err, "GitLab access not configured")
}

func TestExecuteGitLabGetFileServerError(t *testing.T) {
	t.Parallel()

	ts, server := newTestServerWithGitLab(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer ts.Close()

	ctx := context.Background()
	_, err := server.executeGitLabGetFile(ctx, map[string]interface{}{
		"project": "my-group/my-project",
		"path":    "nonexistent.go",
	})
	assert.Error(t, err)
}
