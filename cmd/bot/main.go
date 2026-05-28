package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/nikogura/diagnostic-bot/pkg/bot"
	"github.com/nikogura/diagnostic-bot/pkg/k8s"
	"github.com/nikogura/diagnostic-bot/pkg/mcp"
	"github.com/nikogura/diagnostic-bot/pkg/mcp/auth"
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

// configureLokiTenants reads LOKI_DEFAULT_ORG_ID and LOKI_ORG_IDS and applies
// them to the supplied client. Both empty preserves the auth_enabled:false
// behavior (no X-Scope-OrgID sent). Misconfiguration is fatal — silently
// running with a half-applied tenant config would be worse than failing
// at startup.
func configureLokiTenants(ctx context.Context, client *k8s.LokiClient, logger *slog.Logger) {
	defaultTenant := getEnv("LOKI_DEFAULT_ORG_ID", "")
	orgIDsCSV := getEnv("LOKI_ORG_IDS", "")

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
		logger.ErrorContext(ctx, "invalid Loki tenant configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.InfoContext(ctx, "Loki multi-tenant mode enabled",
		slog.String("default_tenant", defaultTenant),
		slog.Any("allowed_tenants", allowedTenants))
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
	configureLokiTenants(ctx, lokiClient, logger)
	legacyServer := mcp.NewServer(lokiClient, githubToken, nil, logger)
	sdkServer := mcp.NewSDKServer(legacyServer)

	mcpHandler := mcp.WithAuditSourceMiddleware(sdkServer.StreamableHTTPHandler())
	sseHandler := mcp.WithAuditSourceMiddleware(sdkServer.SSEHandler())

	// Auth dispatch: exactly one of MCP_OIDC_ISSUER or GOOGLE_OAUTH_CLIENT_ID
	// may be set. Either path wraps /mcp + /sse with auth.WithAuth (401 +
	// WWW-Authenticate triggers Claude Code's browser-pop OAuth flow);
	// neither set leaves the handlers unwrapped. Slack-bot stdio path is
	// untouched — it never hits this listener.
	provider, providerErr := selectAuthProvider(getEnv("MCP_OIDC_ISSUER", ""), getEnv("GOOGLE_OAUTH_CLIENT_ID", ""))
	if providerErr != nil {
		logger.ErrorContext(ctx, providerErr.Error())
		os.Exit(1)
	}
	switch provider {
	case authProviderOIDC:
		var oidcErr error
		mcpHandler, sseHandler, oidcErr = buildOIDCHandlers(ctx, mcpHandler, sseHandler, logger)
		if oidcErr != nil {
			logger.ErrorContext(ctx, oidcErr.Error())
			os.Exit(1)
		}
	case authProviderGoogle:
		mcpHandler, sseHandler = maybeWrapGoogleAuth(ctx, mcpHandler, sseHandler, logger)
	case authProviderNone:
		logger.InfoContext(ctx, "no auth provider configured — MCP HTTP/SSE will run without auth")
	}

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/mcp", mcpHandler)
		mux.Handle("/sse", sseHandler)

		// Protected-resource metadata is served unauthenticated — clients
		// have to read it BEFORE they have a token.
		registerOAuthMetadataRoute(mux, logger)

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

// maybeWrapGoogleAuth turns on Google OAuth gating when GOOGLE_OAUTH_CLIENT_ID
// is set. Without it, the handlers are returned unwrapped — current
// no-auth, VPC-gated behavior is preserved by default.
func maybeWrapGoogleAuth(ctx context.Context, mcpHandler, sseHandler http.Handler, logger *slog.Logger) (mcp, sse http.Handler) {
	mcp, sse = mcpHandler, sseHandler

	clientID := getEnv("GOOGLE_OAUTH_CLIENT_ID", "")
	if clientID == "" {
		logger.InfoContext(ctx, "GOOGLE_OAUTH_CLIENT_ID not set — MCP HTTP/SSE will run without auth")
		return mcp, sse
	}

	publicURL := strings.TrimRight(getEnv("MCP_PUBLIC_URL", ""), "/")
	if publicURL == "" {
		logger.ErrorContext(ctx, "GOOGLE_OAUTH_CLIENT_ID set but MCP_PUBLIC_URL is empty — cannot build resource_metadata URL; exiting")
		os.Exit(1)
	}

	cfg := auth.GoogleConfig{
		ClientID:                 clientID,
		AllowedHostedDomains:     splitCSV(getEnv("GOOGLE_ALLOWED_HOSTED_DOMAINS", "")),
		AllowedEmails:            splitCSV(getEnv("GOOGLE_ALLOWED_EMAILS", "")),
		AllowedHostedDomainsFile: getEnv("GOOGLE_ALLOWED_HOSTED_DOMAINS_FILE", ""),
		AllowedEmailsFile:        getEnv("GOOGLE_ALLOWED_EMAILS_FILE", ""),
	}
	googleAuth, err := auth.NewGoogleAuth(cfg, logger)
	if err != nil {
		logger.ErrorContext(ctx, "Google OAuth configuration invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}

	resourceMetaURL := publicURL + "/.well-known/oauth-protected-resource"
	mcp = auth.WithAuth(googleAuth, resourceMetaURL, logger)(mcpHandler)
	sse = auth.WithAuth(googleAuth, resourceMetaURL, logger)(sseHandler)

	logger.InfoContext(ctx, "Google OAuth enabled for MCP HTTP/SSE",
		slog.String("client_id", clientID),
		slog.Any("allowed_hosted_domains", cfg.AllowedHostedDomains),
		slog.Int("allowed_emails_count", len(cfg.AllowedEmails)),
		slog.Bool("hosted_domains_from_file", cfg.AllowedHostedDomainsFile != ""),
		slog.Bool("emails_from_file", cfg.AllowedEmailsFile != ""),
		slog.String("hosted_domains_file", cfg.AllowedHostedDomainsFile),
		slog.String("emails_file", cfg.AllowedEmailsFile),
		slog.String("resource_metadata_url", resourceMetaURL),
	)
	return mcp, sse
}

// authProvider enumerates the auth backends the MCP HTTP server can wrap
// /mcp and /sse with. Exactly one may be active at a time; both-set is a
// startup error.
type authProvider int

const (
	authProviderNone authProvider = iota
	authProviderOIDC
	authProviderGoogle
)

// selectAuthProvider resolves which auth backend, if any, the operator
// configured. Pure function — testable without subprocess-spawning to
// observe os.Exit. The caller turns the both-set error into os.Exit.
func selectAuthProvider(oidcIssuer, googleClientID string) (provider authProvider, err error) {
	oidcOn := oidcIssuer != ""
	googleOn := googleClientID != ""
	if oidcOn && googleOn {
		err = errors.New("both MCP_OIDC_ISSUER and GOOGLE_OAUTH_CLIENT_ID are set — pick exactly one")
		return provider, err
	}
	if oidcOn {
		provider = authProviderOIDC
		return provider, err
	}
	if googleOn {
		provider = authProviderGoogle
		return provider, err
	}
	provider = authProviderNone
	return provider, err
}

// oauthMetadataConfig returns the authorization server URL and supported
// scopes to advertise in the protected-resource metadata document,
// based on which auth provider is active. Pure function.
func oauthMetadataConfig(provider authProvider, oidcIssuer, googleClientID string) (authServerURL string, scopes []string, ok bool) {
	switch provider {
	case authProviderOIDC:
		authServerURL = oidcIssuer
		scopes = []string{"openid", "email", "profile", "groups"}
		ok = true
		return authServerURL, scopes, ok
	case authProviderGoogle:
		_ = googleClientID // signature symmetry; the AS URL is constant for Google
		authServerURL = "https://accounts.google.com"
		scopes = []string{"openid", "email", "profile"}
		ok = true
		return authServerURL, scopes, ok
	case authProviderNone:
		return authServerURL, scopes, ok
	}
	return authServerURL, scopes, ok
}

// buildOIDCHandlers is the OIDC counterpart to maybeWrapGoogleAuth. Pure
// in the sense that errors are returned rather than os.Exit'd, so the
// caller (or tests) can decide what to do.
func buildOIDCHandlers(ctx context.Context, mcpHandler, sseHandler http.Handler, logger *slog.Logger) (mcpOut, sseOut http.Handler, err error) {
	mcpOut, sseOut = mcpHandler, sseHandler

	issuer := strings.TrimRight(getEnv("MCP_OIDC_ISSUER", ""), "/")
	if issuer == "" {
		return mcpOut, sseOut, err
	}

	audience := getEnv("MCP_OIDC_AUDIENCE", "")
	if audience == "" {
		err = errors.New("MCP_OIDC_ISSUER set but MCP_OIDC_AUDIENCE is empty — refusing to run without audience binding")
		return mcpOut, sseOut, err
	}

	publicURL := strings.TrimRight(getEnv("MCP_PUBLIC_URL", ""), "/")
	if publicURL == "" {
		err = errors.New("MCP_OIDC_ISSUER set but MCP_PUBLIC_URL is empty — cannot build resource_metadata URL")
		return mcpOut, sseOut, err
	}

	jwksCacheSeconds := 300
	if s := getEnv("MCP_OIDC_JWKS_CACHE_SECONDS", ""); s != "" {
		parsed, parseErr := strconv.Atoi(s)
		if parseErr == nil {
			jwksCacheSeconds = parsed
		}
	}

	cfg := &auth.OIDCConfig{
		IssuerURL:                issuer,
		Audience:                 audience,
		AllowedGroups:            splitCSV(getEnv("MCP_OIDC_ALLOWED_GROUPS", "")),
		AllowedHostedDomains:     splitCSV(getEnv("MCP_OIDC_ALLOWED_HOSTED_DOMAINS", "")),
		AllowedEmails:            splitCSV(getEnv("MCP_OIDC_ALLOWED_EMAILS", "")),
		AllowedGroupsFile:        getEnv("MCP_OIDC_ALLOWED_GROUPS_FILE", ""),
		AllowedHostedDomainsFile: getEnv("MCP_OIDC_ALLOWED_HOSTED_DOMAINS_FILE", ""),
		AllowedEmailsFile:        getEnv("MCP_OIDC_ALLOWED_EMAILS_FILE", ""),
		JWKSCacheTime:            jwksCacheSeconds,
	}
	oidcAuth := auth.NewOIDCAuth(cfg, logger)

	resourceMetaURL := publicURL + "/.well-known/oauth-protected-resource"
	mcpOut = auth.WithAuth(oidcAuth, resourceMetaURL, logger)(mcpHandler)
	sseOut = auth.WithAuth(oidcAuth, resourceMetaURL, logger)(sseHandler)

	logger.InfoContext(ctx, "OIDC auth enabled for MCP HTTP/SSE",
		slog.String("issuer", issuer),
		slog.String("audience", audience),
		slog.Any("allowed_groups", cfg.AllowedGroups),
		slog.Any("allowed_hosted_domains", cfg.AllowedHostedDomains),
		slog.Int("allowed_emails_count", len(cfg.AllowedEmails)),
		slog.Bool("groups_from_file", cfg.AllowedGroupsFile != ""),
		slog.Bool("hosted_domains_from_file", cfg.AllowedHostedDomainsFile != ""),
		slog.Bool("emails_from_file", cfg.AllowedEmailsFile != ""),
		slog.String("groups_file", cfg.AllowedGroupsFile),
		slog.String("hosted_domains_file", cfg.AllowedHostedDomainsFile),
		slog.String("emails_file", cfg.AllowedEmailsFile),
		slog.Int("jwks_cache_seconds", cfg.JWKSCacheTime),
		slog.String("resource_metadata_url", resourceMetaURL),
	)
	return mcpOut, sseOut, err
}

// registerOAuthMetadataRoute serves /.well-known/oauth-protected-resource
// when an OAuth/OIDC provider is configured. With neither, the route is
// omitted entirely — pointing clients at nothing is worse than 404.
func registerOAuthMetadataRoute(mux *http.ServeMux, logger *slog.Logger) {
	oidcIssuer := strings.TrimRight(getEnv("MCP_OIDC_ISSUER", ""), "/")
	googleClientID := getEnv("GOOGLE_OAUTH_CLIENT_ID", "")

	provider, err := selectAuthProvider(oidcIssuer, googleClientID)
	if err != nil || provider == authProviderNone {
		return
	}

	publicURL := strings.TrimRight(getEnv("MCP_PUBLIC_URL", ""), "/")
	if publicURL == "" {
		return
	}

	authServerURL, scopes, ok := oauthMetadataConfig(provider, oidcIssuer, googleClientID)
	if !ok {
		return
	}

	mux.Handle("/.well-known/oauth-protected-resource", auth.ProtectedResourceMetadataHandler(
		publicURL+"/mcp",
		authServerURL,
		scopes,
	))
	logger.Info("registered /.well-known/oauth-protected-resource for MCP OAuth discovery",
		slog.String("authorization_server", authServerURL),
	)
}

// splitCSV parses an env-var-style list, accepting commas, whitespace,
// or any combination as separators. Empty entries are dropped.
//
// The original implementation only split on commas, which meant a long
// allowlist like MCP_OIDC_ALLOWED_EMAILS had to live on a single long
// line in the Deployment YAML — fine for two or three entries, awful at
// a dozen. Accepting whitespace too lets operators write a multi-line
// YAML block scalar with one entry per line:
//
//	env:
//	  - name: MCP_OIDC_ALLOWED_EMAILS
//	    value: |-
//	      alice@katn-solutions.io
//	      bob@katn-solutions.io
//	      carol@katn-solutions.io
//
// Both forms produce identical output. Mixed forms (commas AND newlines)
// also work, which matters because YAML block scalars sometimes leave
// stray indentation.
func splitCSV(v string) (out []string) {
	out = strings.FieldsFunc(v, isCSVSeparator)
	if len(out) == 0 {
		out = nil
	}
	return out
}

// isCSVSeparator reports whether r should be treated as a boundary
// between splitCSV entries: literal comma or any Unicode whitespace.
// Extracted to a top-level function so the namedreturns linter doesn't
// flag the predicate's unnamed bool return (per the project's
// "Nested closures with returns" anti-pattern guidance).
func isCSVSeparator(r rune) (isSep bool) {
	isSep = r == ',' || unicode.IsSpace(r)
	return isSep
}
