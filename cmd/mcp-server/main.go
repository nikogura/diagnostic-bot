package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/apiconfig"
	"github.com/nikogura/diagnostic-bot/pkg/k8s"
	"github.com/nikogura/diagnostic-bot/pkg/mcp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Get configuration from environment
	lokiEndpoint := os.Getenv("LOKI_ENDPOINT")
	if lokiEndpoint == "" {
		logger.Error("LOKI_ENDPOINT environment variable not set")
		os.Exit(1)
	}

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		logger.Warn("GITHUB_TOKEN not set - GitHub tools will be unavailable")
	}

	// Initialize clients
	lokiClient := k8s.NewLokiClient(lokiEndpoint, logger)
	configureLokiTenants(lokiClient, logger)

	// Load third-party API configs
	apiToolRegistry := buildAPIToolRegistry(logger)

	// Create legacy server (holds all service clients and tool handlers)
	legacyServer := mcp.NewServer(lokiClient, githubToken, apiToolRegistry, logger)

	// Wrap with SDK server for Streamable HTTP and stdio transports
	sdkServer := mcp.NewSDKServer(legacyServer)

	// Run server (stdio transport)
	ctx := context.Background()

	err := sdkServer.RunStdio(ctx)
	if err != nil {
		logger.Error("MCP server error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func buildAPIToolRegistry(logger *slog.Logger) (registry *apiconfig.APIToolRegistry) {
	apiDir := os.Getenv("API_CONFIG_DIR")
	if apiDir == "" {
		apiDir = "./apis"
	}

	configs, err := apiconfig.LoadConfigs(apiDir, logger)
	if err != nil {
		logger.Warn("Failed to load API configs",
			slog.String("error", err.Error()))
		return registry
	}

	if len(configs) == 0 {
		logger.Info("No third-party API configs loaded")
		return registry
	}

	registry = apiconfig.NewAPIToolRegistry(configs, logger)

	logger.Info("Third-party API tool registry initialized",
		slog.Int("api_count", len(configs)))

	return registry
}

// configureLokiTenants reads LOKI_DEFAULT_ORG_ID and LOKI_ORG_IDS and applies
// them to the supplied client. Both empty preserves the auth_enabled:false
// behavior (no X-Scope-OrgID sent). Misconfiguration is fatal.
func configureLokiTenants(client *k8s.LokiClient, logger *slog.Logger) {
	defaultTenant := os.Getenv("LOKI_DEFAULT_ORG_ID")
	orgIDsCSV := os.Getenv("LOKI_ORG_IDS")

	var allowedTenants []string
	if orgIDsCSV != "" {
		for _, t := range strings.Split(orgIDsCSV, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				allowedTenants = append(allowedTenants, t)
			}
		}
	}

	if defaultTenant == "" && len(allowedTenants) == 0 {
		return
	}

	err := client.ConfigureTenants(defaultTenant, allowedTenants)
	if err != nil {
		logger.Error("invalid Loki tenant configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.Info("Loki multi-tenant mode enabled",
		slog.String("default_tenant", defaultTenant),
		slog.Any("allowed_tenants", allowedTenants))
}
