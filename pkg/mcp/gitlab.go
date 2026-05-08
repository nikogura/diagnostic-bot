package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// Tool name constants for GitLab tools.
const (
	toolGitLabGetFile       = "gitlab_get_file"
	toolGitLabListDirectory = "gitlab_list_directory"
	toolGitLabSearchCode    = "gitlab_search_code"
)

// GitLabClient wraps the GitLab API client.
type GitLabClient struct {
	client *gitlab.Client
	logger *slog.Logger
}

// NewGitLabClient creates a new GitLab client from environment variables.
// Requires GITLAB_TOKEN. GITLAB_URL defaults to https://gitlab.com if not set.
func NewGitLabClient(logger *slog.Logger) (result *GitLabClient, err error) {
	token := os.Getenv("GITLAB_TOKEN")
	if token == "" {
		err = errors.New("GITLAB_TOKEN not set")
		return result, err
	}

	baseURL := os.Getenv("GITLAB_URL")

	var client *gitlab.Client
	if baseURL != "" {
		client, err = gitlab.NewClient(token, gitlab.WithBaseURL(baseURL))
	} else {
		client, err = gitlab.NewClient(token)
	}

	if err != nil {
		err = fmt.Errorf("creating GitLab client: %w", err)
		return result, err
	}

	result = &GitLabClient{
		client: client,
		logger: logger,
	}

	return result, err
}

// getGitLabTools returns GitLab-related tool definitions.
func getGitLabTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolGitLabGetFile,
			Description: "Fetch a file from a GitLab repository. Requires GITLAB_TOKEN to be configured.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"project": map[string]interface{}{
						"type":        "string",
						"description": "Project ID or URL-encoded path (e.g., '12345' or 'my-group/my-project')",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path within repository (e.g., 'db/schema.hcl')",
					},
					"ref": map[string]interface{}{
						"type":        "string",
						"description": "Git ref (branch, tag, or commit SHA). Defaults to 'main'",
					},
				},
				"required": []string{"project", "path"},
			},
		},
		{
			Name:        toolGitLabListDirectory,
			Description: "List files in a GitLab repository directory. Requires GITLAB_TOKEN to be configured.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"project": map[string]interface{}{
						"type":        "string",
						"description": "Project ID or URL-encoded path (e.g., '12345' or 'my-group/my-project')",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory path (e.g., 'db/migrations')",
					},
					"ref": map[string]interface{}{
						"type":        "string",
						"description": "Git ref (branch, tag, or commit SHA). Defaults to 'main'",
					},
				},
				"required": []string{"project", "path"},
			},
		},
		{
			Name:        toolGitLabSearchCode,
			Description: "Search for code across GitLab projects. Requires GITLAB_TOKEN to be configured.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search query string",
					},
					"project": map[string]interface{}{
						"type":        "string",
						"description": "Optional: limit search to a specific project ID or path",
					},
				},
				"required": []string{"query"},
			},
		},
	}

	return result
}

// executeGitLabGetFile fetches a file from a GitLab repository.
func (s *Server) executeGitLabGetFile(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.gitlabClient == nil {
		err = errors.New("GitLab access not configured (GITLAB_TOKEN not set)")
		return result, err
	}

	project, _ := args["project"].(string)
	path, _ := args["path"].(string)
	ref, _ := args["ref"].(string)

	if project == "" {
		err = errors.New("project parameter is required")
		return result, err
	}

	if path == "" {
		err = errors.New("path parameter is required")
		return result, err
	}

	if ref == "" {
		ref = "main"
	}

	opts := &gitlab.GetFileOptions{Ref: gitlab.Ptr(ref)}
	file, _, getErr := s.gitlabClient.client.RepositoryFiles.GetFile(project, path, opts, gitlab.WithContext(ctx))
	if getErr != nil {
		err = fmt.Errorf("fetching file from GitLab: %w", getErr)
		return result, err
	}

	var content []byte
	content, err = base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		err = fmt.Errorf("decoding file content: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "fetched file from GitLab",
		slog.String("project", project),
		slog.String("path", path),
		slog.String("ref", ref),
		slog.Int("size", len(content)))

	result = fmt.Sprintf("File: %s/%s (ref: %s)\nSize: %d bytes\n\n%s", project, path, ref, len(content), string(content))
	return result, err
}

// executeGitLabListDirectory lists files in a GitLab repository directory.
func (s *Server) executeGitLabListDirectory(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.gitlabClient == nil {
		err = errors.New("GitLab access not configured (GITLAB_TOKEN not set)")
		return result, err
	}

	project, _ := args["project"].(string)
	path, _ := args["path"].(string)
	ref, _ := args["ref"].(string)

	if project == "" {
		err = errors.New("project parameter is required")
		return result, err
	}

	if ref == "" {
		ref = "main"
	}

	opts := &gitlab.ListTreeOptions{
		Path: gitlab.Ptr(path),
		Ref:  gitlab.Ptr(ref),
	}
	tree, _, listErr := s.gitlabClient.client.Repositories.ListTree(project, opts, gitlab.WithContext(ctx))
	if listErr != nil {
		err = fmt.Errorf("listing directory: %w", listErr)
		return result, err
	}

	var files []string
	for _, node := range tree {
		files = append(files, fmt.Sprintf("  %-8s %s", node.Type, node.Name))
	}

	s.logger.InfoContext(ctx, "listed directory from GitLab",
		slog.String("project", project),
		slog.String("path", path),
		slog.String("ref", ref),
		slog.Int("file_count", len(files)))

	result = fmt.Sprintf("Directory: %s/%s (ref: %s)\nEntries: %d\n\n%s",
		project, path, ref, len(files), strings.Join(files, "\n"))
	return result, err
}

// executeGitLabSearchCode searches for code across GitLab projects.
func (s *Server) executeGitLabSearchCode(ctx context.Context, args map[string]interface{}) (result string, err error) {
	if s.gitlabClient == nil {
		err = errors.New("GitLab access not configured (GITLAB_TOKEN not set)")
		return result, err
	}

	query, _ := args["query"].(string)
	if query == "" {
		err = errors.New("query parameter is required")
		return result, err
	}

	project, _ := args["project"].(string)

	var blobs []*gitlab.Blob

	if project != "" {
		// Search within a specific project
		blobs, _, err = s.gitlabClient.client.Search.BlobsByProject(project, query, &gitlab.SearchOptions{
			ListOptions: gitlab.ListOptions{PerPage: 10},
		}, gitlab.WithContext(ctx))
	} else {
		// Global search
		blobs, _, err = s.gitlabClient.client.Search.Blobs(query, &gitlab.SearchOptions{
			ListOptions: gitlab.ListOptions{PerPage: 10},
		}, gitlab.WithContext(ctx))
	}

	if err != nil {
		err = fmt.Errorf("searching code: %w", err)
		return result, err
	}

	var results []string
	for _, blob := range blobs {
		results = append(results, fmt.Sprintf("  %s:%s\n    Project: %d",
			blob.Filename, blob.Path, blob.ProjectID))
	}

	s.logger.InfoContext(ctx, "searched GitLab code",
		slog.String("query", query),
		slog.Int("returned", len(results)))

	result = fmt.Sprintf("Found %d results for: %s\n\n%s",
		len(results), query, strings.Join(results, "\n\n"))
	return result, err
}
