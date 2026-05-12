package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/bot"
	"github.com/nikogura/diagnostic-bot/pkg/k8s"
	"github.com/nikogura/diagnostic-bot/pkg/mcp"
	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

func main() {
	// Initialize structured logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	slog.SetDefault(logger)

	// Load configuration from environment
	cfg := bot.Config{
		SlackBotToken:    getEnv("SLACK_BOT_TOKEN", ""),
		SlackAppToken:    getEnv("SLACK_APP_TOKEN", ""),
		AnthropicAPIKey:  getEnv("ANTHROPIC_API_KEY", ""),
		InvestigationDir: getEnv("INVESTIGATION_DIR", "./investigations"),
		FileRetention:    parseFileRetention(logger),
		GitHubToken:      getEnv("GITHUB_TOKEN", ""),
		ClaudeModel:      getEnv("CLAUDE_MODEL", "claude-sonnet-4-5-20250929"),
	}

	// Validate required configuration
	if cfg.SlackBotToken == "" {
		logger.Warn("SLACK_BOT_TOKEN environment variable not set - Slack integration will not work")
	}

	if cfg.SlackAppToken == "" {
		logger.Warn("SLACK_APP_TOKEN environment variable not set - Slack integration will not work")
	}

	if cfg.AnthropicAPIKey == "" {
		logger.Warn("ANTHROPIC_API_KEY environment variable not set - Claude Code will not work")
	}

	logger.Info("starting Diagnostic Bot",
		slog.String("investigation_dir", cfg.InvestigationDir))

	// Setup context and signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start metrics server unconditionally — needed for liveness probes
	metricsServer := metrics.NewServer(":9090", logger)

	go func() {
		metricsErr := metricsServer.Start(ctx)
		if metricsErr != nil {
			logger.ErrorContext(ctx, "metrics server error", slog.String("error", metricsErr.Error()))
		}
	}()

	// Start MCP HTTP server unconditionally if enabled — independent of Slack
	startMCPHTTPServer(ctx, cfg.GitHubToken, logger)

	// Create bot — Slack is optional, MCP and metrics are not
	diagnosticBot, err := bot.NewBot(cfg, logger)
	if err != nil {
		logger.Warn("failed to create bot, MCP and metrics servers still running",
			slog.String("error", err.Error()))

		// Block on signal — servers are already running
		<-sigChan
		logger.Info("received shutdown signal")
		return
	}

	// Wire bot health checker now that bot exists
	metricsServer.SetHealthChecker(diagnosticBot)

	// Start bot in goroutine
	errChan := make(chan error, 1)

	go func() {
		startErr := diagnosticBot.Start(ctx)
		if startErr != nil {
			errChan <- startErr
		}
	}()

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		logger.Info("received shutdown signal", slog.String("signal", sig.String()))
		cancel()

	case botErr := <-errChan:
		logger.Error("bot encountered fatal error", slog.String("error", botErr.Error()))
		cancel()
		os.Exit(1)
	}

	logger.Info("bot shutdown complete")
}

// getEnv retrieves an environment variable with a default value.
func getEnv(key string, defaultValue string) (result string) {
	value := os.Getenv(key)
	if value == "" {
		result = defaultValue
		return result
	}

	result = value
	return result
}

// parseFileRetention parses the FILE_RETENTION environment variable.
// Returns 0 if not set or invalid (which triggers use of DefaultFileRetention).
func parseFileRetention(logger *slog.Logger) (result time.Duration) {
	retentionStr := os.Getenv("FILE_RETENTION")
	if retentionStr == "" {
		// Not set, use default (0 triggers DefaultFileRetention in NewBot)
		result = 0
		return result
	}

	var err error

	result, err = time.ParseDuration(retentionStr)
	if err != nil {
		logger.Warn("invalid FILE_RETENTION value, using default 24h",
			slog.String("value", retentionStr),
			slog.String("error", err.Error()))
		result = 0
		return result
	}

	if result <= 0 {
		logger.Warn("FILE_RETENTION must be positive, using default 24h",
			slog.Duration("value", result))
		result = 0
		return result
	}

	logger.Info("file retention configured",
		slog.Duration("retention", result))

	return result
}

// startMCPHTTPServer starts the MCP HTTP server if MCP_HTTP_ENABLED is true.
func startMCPHTTPServer(ctx context.Context, githubToken string, logger *slog.Logger) {
	mcpHTTPEnabled := getEnv("MCP_HTTP_ENABLED", "false")
	if mcpHTTPEnabled != "true" {
		return
	}

	mcpHTTPPort := getEnv("MCP_HTTP_PORT", "8090")
	mcpHTTPAddr := ":" + mcpHTTPPort

	lokiEndpoint := getEnv("LOKI_ENDPOINT", "")
	if lokiEndpoint == "" {
		logger.WarnContext(ctx, "LOKI_ENDPOINT not set - MCP Loki tools will be unavailable")
		lokiEndpoint = "http://localhost:3100" // Fallback
	}

	lokiClient := k8s.NewLokiClient(lokiEndpoint, logger)
	legacyServer := mcp.NewServer(lokiClient, githubToken, nil, logger)
	sdkServer := mcp.NewSDKServer(legacyServer)

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/mcp", sdkServer.StreamableHTTPHandler())
		mux.Handle("/sse", sdkServer.SSEHandler())

		logger.InfoContext(ctx, "starting MCP HTTP server",
			slog.String("addr", mcpHTTPAddr),
			slog.String("streamable_http", "/mcp"),
			slog.String("sse", "/sse"))

		httpErr := http.ListenAndServe(mcpHTTPAddr, mux)
		if httpErr != nil {
			logger.ErrorContext(ctx, "MCP HTTP server error", slog.String("error", httpErr.Error()))
		}
	}()
}
