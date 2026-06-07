# Diagnostic Bot

A diagnostic automation platform that puts a constrained, safe investigation agent in front of Kubernetes, Loki, and your observability stack. It exists so anyone on the team can self-serve the same structured investigations — without needing to drive a general-purpose agent themselves, and without a path to break anything. One tool surface (an MCP server) is reachable two ways: a Slack bot that runs the agent for you, and the MCP server itself for power users who bring their own MCP client (Claude Code, Claude Desktop, OpenAI, Devin, and other MCP-compatible clients). Investigations are driven by YAML templates and deliver professional PDF reports.

## Features

- **Two doors, one brain**: a Slack bot (the bot runs the agent for you) and an MCP server (you run your own agent) share a single, gated tool surface — see [Architecture](#architecture)
- **In-process agent**: the Slack path drives the model in-process over the same MCP tool handlers — no subprocess, no `--dangerously-skip-permissions`, no environment hand-off, no shell/filesystem escape
- **Safe-by-construction toolset**: reads everywhere; the only write capability is Grafana dashboard management, and a `READ_ONLY` switch disables even that — see [Security](#security)
- **Prompt-injection defenses**: untrusted tool output is defanged inbound (forged role/turn markers) and secret-scrubbed outbound, on every result
- **MCP Server**: Custom tools for Loki, Prometheus, Grafana, CloudWatch, Tempo, databases, GitHub/GitLab, ECR, whois, and PDF generation
- **Third-Party API Integration**: Configuration-driven system for adding read-only API integrations via YAML — no Go code required
- **Investigation Skills**: YAML-based templates define structured investigation workflows
- **PDF Report Generation**: Automated professional reports with company branding via Pandoc + LaTeX
- **Slack Socket Mode**: Supports app mentions, threads, and conversational follow-ups
- **Full OpenTelemetry observability**: golden-signal metrics in Prometheus format, OTLP tracing, trace-correlated JSON logs, a checked-in Grafana dashboard, and Pod/ServiceMonitors — see [Observability](#observability)

## Architecture

The product is one tool surface (`pkg/mcp`) reachable through two front-ends, governed by one capability model:

```
                       ┌────────────────────────────────┐
  Slack user  ───────► │ Slack bot                      │
  ("@bot why is X..")  │   in-process agent loop        │ ──┐
                       │   (pkg/bot + pkg/claude)       │   │
                       └────────────────────────────────┘   │   ┌────────────────────┐
                                                             ├──►│  pkg/mcp           │
                       ┌────────────────────────────────┐   │   │  one gated toolset │
  Power user  ───────► │ MCP server (HTTP/SSE or stdio) │ ──┘   │  reads + Grafana   │
  (own MCP client)     │   authn: OIDC / Google / mTLS  │       └────────────────────┘
                       └────────────────────────────────┘
```

- **Slack = the self-service door.** The user doesn't bring an agent; the bot *is* the agent. It runs the model in-process (`pkg/claude`) against the shared `pkg/mcp` tool handlers, wrapped in the investigation prompts. Because only those handlers are wired in, there is no shell, no filesystem, and no arbitrary-tool escape — the set of possible actions *is* the curated toolset.
- **MCP server = the power-user door.** People who already drive their own MCP client point it at the authenticated MCP HTTP/SSE endpoint (or run the `mcp-server` binary over stdio). Same handlers, same capability limits.
- **One brain.** A single `*mcp.Server` instance backs both doors, so the available tools — and the read-only switch — are identical everywhere.

## Code Quality

- **Linter Compliance**: 100% clean - all golangci-lint and namedreturns checks pass
- **Test Coverage**: Unit tests with race detection, coverage reporting to Codecov
- **CI/CD**: Automated linting, testing, Docker builds, and security scanning via GitHub Actions
- **Multi-arch Support**: Docker images for amd64 and arm64
- **Security Scanning**: Trivy vulnerability scanning with SARIF upload

## Project Structure

```
diagnostic-bot/
├── cmd/
│   ├── bot/main.go                  # Main Slack bot entry point
│   └── mcp-server/main.go           # MCP server for Claude Code tools
├── pkg/
│   ├── bot/                         # Slack bot logic
│   │   ├── bot.go                   # Core bot initialization
│   │   ├── handlers.go              # Event handlers
│   │   ├── agent.go                 # In-process agent loop (model + tool dispatch + filters)
│   │   ├── tools.go                 # Dynamic tool availability config
│   │   └── state.go                 # Conversation state management
│   ├── claude/                      # Direct Claude API integration (legacy)
│   │   ├── client.go                # API client with tool use support
│   │   ├── tools.go                 # Tool definitions for Claude
│   │   └── prompts.go               # System prompt construction
│   ├── investigations/              # Investigation skill system
│   │   ├── template.go              # Skill data structures and loading
│   │   └── matcher.go               # Message matching logic
│   ├── k8s/                         # Kubernetes and Loki clients
│   │   ├── agent.go                 # K8s resource access
│   │   ├── loki.go                  # Loki log query client
│   │   ├── sanitizer.go             # Outbound secret/PII scrubbing
│   │   └── inbound_sanitizer.go     # Inbound forged-control-sequence defanging
│   ├── apiconfig/                   # Third-party API integration framework
│   │   ├── config.go                # YAML config schema and loader
│   │   ├── client.go                # Generic HTTP client (auth, retry, rate limiting)
│   │   ├── validate.go              # Path parameter validation (traversal, injection)
│   │   ├── redact.go                # PII field redaction from JSON responses
│   │   └── tools.go                 # MCP tool generation and dispatch
│   ├── mcp/                         # MCP server implementation
│   │   ├── server.go                # Tool handlers and legacy protocol
│   │   ├── sdk_server.go            # MCP SDK integration (Streamable HTTP + SSE + stdio)
│   │   ├── types.go                 # MCP protocol types
│   │   ├── cloudwatch.go            # CloudWatch Logs tools
│   │   ├── prometheus.go            # Prometheus/PromQL tools
│   │   ├── grafana.go               # Grafana dashboard tools
│   │   ├── database.go              # Database query tools
│   │   ├── ecr.go                   # ECR vulnerability scanning
│   │   └── auth/                    # MCP server authentication
│   └── metrics/                     # Prometheus metrics
│       ├── metrics.go               # OTel instrument definitions + helpers
│       └── server.go                # Admin HTTP server (/metrics + health)
│   └── observability/               # OTel setup: metrics exporter, tracing, log correlation
├── apis/                            # Third-party API configs (YAML)
│   └── bitgo.yaml                   # Example: BitGo read-only wallet API
├── investigations/                  # YAML investigation skills
│   ├── modsecurity-block.yaml
│   ├── atlas-migration.yaml
│   ├── general-diagnostic.yaml
│   └── ecr-vulnerability-scan.yaml
├── latex-templates/                 # PDF report templates
├── docs/
│   ├── ECR_INTEGRATION.md           # ECR vulnerability scanning guide
│   └── PROJECT_SPEC.md              # Original project specification
├── .github/workflows/               # CI/CD pipelines
│   ├── ci.yaml                      # Lint, test, build, Docker push
│   └── release.yaml                 # GoReleaser for versioned releases
├── Dockerfile                       # Multi-stage Alpine build
├── Makefile                         # Build and lint targets
├── .golangci.yml                    # Linter configuration (strict enforcement)
├── .mcp.json                        # MCP server configuration
└── go.mod                           # Go module dependencies
```

## Investigation Skills

The bot includes example investigation skills in the `investigations/` directory. These are generic examples with substitution variables for adaptation to your environment.

### Included Examples

#### ModSecurity WAF Block Diagnosis

Investigates Web Application Firewall blocks with the following capabilities:

- Queries Loki for ModSecurity audit logs
- Analyzes OWASP CRS rule triggers
- Categorizes blocks (false positive vs legitimate)
- Provides exact remediation with rule IDs and config snippets
- Performs IP geolocation via whois

**Trigger patterns**: `modsec`, `modsecurity`, `waf blocked`, `403.*waf`

#### Atlas Migration Troubleshooting

Diagnoses why Atlas migrations are not being applied in GitOps environments:

- Checks AtlasMigration CRD status
- Verifies GitRepository version pinning
- Inspects ConfigMap contents
- Analyzes Flux Kustomization reconciliation
- Identifies root cause (tag pinning, ConfigMap not regenerated, etc.)

**Trigger patterns**: `atlas.*migration`, `migration.*not.*applied`, `migration.*failure`

#### General Diagnostic Investigation

A systematic approach for general production issues:

- Defines investigation phases (scope, context gathering, hypothesis formation, targeted investigation, root cause analysis, remediation)
- Provides examples for querying Prometheus, Thanos, Loki, and Alertmanager
- Covers common patterns (WAF blocks, database migrations, Flux failures, pod crashes)
- Emphasizes security-first and GitOps principles

**Trigger patterns**: `investigate`, `diagnostic`, `troubleshoot`, `issue`, `problem`, `error`

## Creating New Investigations

### Investigation YAML Structure

Investigations are defined in YAML files with the following structure:

```yaml
name: "Investigation Name"
description: "Brief description of what this investigation does"
trigger_patterns:
  - "pattern1"
  - "pattern2.*regex"
  - "specific phrase"

initial_prompt: |
  Multi-line prompt that Claude will use as context for this investigation.

  This should include:
  - Role definition ("You are a diagnostic agent for...")
  - Core principles (security, read-only, systematic approach)
  - Investigation methodology (phases, steps)
  - Available tools and when to use them
  - Output format expectations
  - Critical patterns to recognize
  - Communication style guidelines

kubernetes_resources:
  - type: "pod"
    namespace: "namespace-name"
    selector: "app=component"
    container: "container-name"
    since: "1h"
    grep: "ERROR"
    tail_lines: 500

require_approval: false
```

### Field Descriptions

- **name**: Human-readable name shown in Slack
- **description**: Brief description (prefix with "EXAMPLE:" if this is a template for customization)
- **trigger_patterns**: Regex patterns matched against user messages. More specific patterns (longer solid pattern length) take precedence.
- **initial_prompt**: The system prompt Claude receives. This is the core of your investigation workflow.
- **kubernetes_resources**: (Optional) List of K8s resources to pre-fetch. Useful for common queries.
- **require_approval**: (Optional) If true, bot asks user for confirmation before starting investigation.

### Substitution Variables

Use `{{VARIABLE_NAME}}` syntax for environment-specific values that need to be customized for production deployment:

```yaml
initial_prompt: |
  Query Prometheus at {{PROMETHEUS_URL}} for metrics.
  Check Loki at {{LOKI_URL}} for logs.
  The application runs in namespace {{APP_NAMESPACE}}.
```

**Common substitution variables:**
- `{{PROMETHEUS_URL}}` - Prometheus endpoint
- `{{THANOS_URL}}` - Thanos query endpoint (federated metrics)
- `{{LOKI_URL}}` - Loki log aggregation endpoint
- `{{ALERTMANAGER_URL}}` - Alertmanager endpoint
- `{{REALM}}` - Environment name (prod, staging, dev)
- `{{NAMESPACE}}` - Kubernetes namespace
- `{{DOMAIN}}` - Application domain
- `{{GH_ORG}}` - GitHub organization
- `{{GH_REPO}}` - GitHub repository name

### Trigger Pattern Specificity

The bot uses a specificity algorithm to select the best matching investigation:

1. Patterns are tested in order against the user's message
2. If multiple patterns match, the most specific wins
3. Specificity = length of longest solid pattern (non-regex characters)

**Examples:**
- `"atlas.*migration"` → specificity = 5 (longest solid: "atlas")
- `"migration.*not.*applied"` → specificity = 9 (longest solid: "migration")
- `"database migration failure"` → specificity = 25 (entire phrase is solid)

**Best practices:**
- Use specific phrases for narrow investigations
- Use regex patterns for flexibility within a domain
- Longer solid patterns win ties

### Available Claude Tools (via MCP Server)

Your investigation prompt should reference these MCP tool names that Claude can autonomously use. Tool availability is dynamic — only tools with configured backing services are registered and shown in the prompt.

> **CRITICAL: Investigation skills MUST reference MCP tool names (e.g., `cloudwatch_logs_query`), NOT external CLI commands (e.g., `aws logs start-query`).** Claude Code runs in `--print` mode with MCP tools via stdio — it does NOT have shell access to external CLIs. If a skill references CLI commands instead of MCP tool names, Claude will either fail or hallucinate results.

**Logging (Loki)** — requires `LOKI_ENDPOINT`. For multi-tenant deployments (`auth_enabled: true`) additionally set `LOKI_DEFAULT_ORG_ID` and optionally `LOKI_ORG_IDS`:
- `query_loki` — Query Loki for cluster logs using LogQL syntax
  ```
  Parameters: query, start, end (optional), limit (optional), tenant (optional, multi-tenant only)
  ```
  The `tenant` parameter accepts a single tenant (`monitoring`) or Loki's pipe-delimited multi-tenant read syntax (`monitoring|cloudtrail`). When `LOKI_ORG_IDS` is set, every tenant in the value must appear in the allowlist or the request is rejected before any HTTP call. When `tenant` is omitted, `LOKI_DEFAULT_ORG_ID` is used.

**CloudWatch Logs** — requires `CLOUDWATCH_ACCOUNTS` or `CLOUDWATCH_ASSUME_ROLE`:
- `cloudwatch_logs_query` — Execute CloudWatch Logs Insights queries across log groups
  ```
  Parameters: query, log_groups, start_time, end_time (optional), region (optional), limit (optional), accounts (optional)
  ```
- `cloudwatch_logs_list_groups` — List available CloudWatch log groups
  ```
  Parameters: prefix (optional), region (optional), limit (optional), accounts (optional)
  ```
- `cloudwatch_logs_get_events` — Get log events from a specific log stream
  ```
  Parameters: log_group, log_stream, start_time (optional), end_time (optional), limit (optional), accounts (optional)
  ```

When `CLOUDWATCH_ACCOUNTS` is configured with multiple accounts, the `accounts` parameter filters which accounts to query. If omitted, all configured accounts are queried and results are labeled per account.

**Prometheus/Metrics** — requires `PROMETHEUS_URL` or `PROMETHEUS_<NAME>_URL`:
- `prometheus_query` — Execute an instant PromQL query
- `prometheus_query_range` — Execute a range PromQL query for trend analysis
- `prometheus_series` — Find time series matching label selectors
- `prometheus_label_values` — Get all values for a given label name
- `prometheus_list_endpoints` — List configured Prometheus endpoints

**Grafana** — requires `GRAFANA_URL` + `GRAFANA_API_KEY`. Write operations stamp every change with an audit user (resolved at MCP server startup) and an LLM-supplied intention. See [Grafana audit attribution](#grafana-audit-attribution):
- `grafana_list_dashboards` — List all Grafana dashboards
- `grafana_get_dashboard` — Get a specific dashboard by UID
- `grafana_create_dashboard` — Create a new dashboard. Two modes: pass `panels` (typed builder, convenient for SQL / Prometheus / CloudWatch / Infinity datasources) or `dashboard` (raw JSON object, required for Elasticsearch/OpenSearch and any datasource whose target fields the typed builder doesn't model — e.g. `metrics`, `bucketAggs`, `timeField`). Supply exactly one. Accepts an optional `message` (the intention) that is composed with the audit user into Grafana's version history.
- `grafana_update_dashboard` — Update an existing dashboard. Accepts `message`; same composition as create. Replaces the full model.
- `grafana_patch_dashboard` — Patch a dashboard server-side without round-tripping the full model. Caller supplies `uid` plus exactly one of `merge` (RFC 7386 JSON merge-patch object) or `patches` (RFC 6902 JSON Patch op list). The bot fetches the dashboard losslessly, applies the patch in-memory, and POSTs the result. Use this for small edits on large dashboards (52KB+) to cut token cost and eliminate the transcription-risk surface of re-typing the full payload. Accepts `message`.
- `grafana_get_dashboard_version` — Dashboard version history. With `uid` only, lists all versions. With `uid` and `version`, fetches that specific version's full payload. Optional `limit`/`start` for list pagination. Response is returned verbatim (lossless), same contract as `grafana_get_dashboard`.
- `grafana_restore_dashboard_version` — Restore a dashboard to a previous `version`. Grafana stamps the new version with "Restored from version N" itself; the audit-attribution convention applied to other writes is intentionally not applied here (restore is by definition a revert to a known prior state, and the version history is the audit). The bot records the restore in slog with `audit_user` and `audit_source_ip` for forensic purposes.
- `grafana_delete_dashboard` — Delete a dashboard. Accepts `message`; recorded in slog only (Grafana DELETE has no version history).
- `grafana_create_folder` — Create a folder (directory). Accepts `message`; recorded in slog only.

**Database** — requires `DATABASE_URL` or `DATABASE_<NAME>_URL`:
- `database_query` — Execute read-only SQL queries (SELECT, SHOW, DESCRIBE, EXPLAIN)
- `database_list` — List available databases

**GitHub** — requires `GITHUB_TOKEN`:
- `github_get_file` — Fetch a file from a GitHub repository
- `github_list_directory` — List files in a repository directory
- `github_search_code` — Search code across repositories

**ECR (Container Security)** — requires `AWS_REGION` or `AWS_DEFAULT_REGION`:
- `ecr_scan_results` — Query ECR for container image vulnerability scan results

**Third-Party APIs** — dynamically loaded from `apis/` directory YAML configs:
- Tools are named `{api_name}_{endpoint_name}` (e.g., `bitgo_list_wallets`)
- Only available when the API's auth token env var is set
- See [Third-Party API Integration](#third-party-api-integration) for details

**Utilities** — always available:
- `whois_lookup` — IP geolocation, ISP, ASN lookup
- `generate_pdf` — Generate a PDF report from Markdown content (auto-uploaded to Slack)

**Note:** The legacy direct K8s tool calls (`get_k8s_pod_logs`, `get_k8s_resource`, etc.) are deprecated in favor of the MCP server architecture. Investigation templates should use the MCP tools above.

### Investigation Prompt Guidelines

> **WARNING: Always use MCP tool names in your prompts.** Claude Code runs in `--print` mode without shell access. It can ONLY interact with external services through MCP tools. If your prompt says "run `aws logs start-query`" or "use `kubectl get pods`", Claude will either fail silently or hallucinate output. Instead, say "use `cloudwatch_logs_query`" or "use `query_loki`". See the tool list above.

Your `initial_prompt` should:

1. **Define the role clearly**: "You are a diagnostic agent for X platform investigating Y issues..."

2. **Set boundaries**: Emphasize read-only access, security considerations, GitOps principles

3. **Provide methodology**: Step-by-step investigation phases or decision trees

4. **Reference MCP tools by name**: Tell Claude which MCP tools to use and when. Use the exact tool names from the "Available Claude Tools" section above (e.g., `cloudwatch_logs_query`, `query_loki`, `prometheus_query`). Never reference external CLIs like `aws`, `kubectl`, `psql`, etc.

5. **Define output format**: Specify structure (Summary, Timeline, Root Cause, Remediation, Prevention)

6. **Include critical patterns**: Known issues, false positive indicators, common root causes

7. **Set communication style**: Technical depth, conciseness, use of severity labels

8. **Document substitution variables**: List all `{{VARIABLES}}` at the end with descriptions

### Testing Locally

1. **Create your investigation YAML** in `investigations/my-investigation.yaml`

2. **Substitute variables** for local testing:
   ```bash
   # Use sed or your editor to replace {{VARIABLES}} with actual values
   sed -e 's|{{LOKI_URL}}|http://localhost:3100|g' \
       -e 's|{{NAMESPACE}}|default|g' \
       investigations/my-investigation.yaml > /tmp/test-investigation.yaml
   ```

3. **Run validation tests**:
   ```bash
   # Tests validate YAML structure and pattern matching
   go test ./pkg/investigations/... -v
   ```

4. **Test with bot locally**:
   ```bash
   export INVESTIGATION_DIR=/tmp
   export SLACK_BOT_TOKEN=xoxb-your-token
   export SLACK_APP_TOKEN=xapp-your-token
   export ANTHROPIC_API_KEY=sk-your-key
   export LOKI_ENDPOINT=http://localhost:3100

   go run cmd/bot/main.go
   ```

5. **Send test message in Slack** matching your trigger patterns

### Deploying to Production

For production deployment using Vault-backed secrets:

1. **Substitute production values** in your investigation YAML (replace all `{{VARIABLES}}`)

2. **Store in Vault** (example for HashiCorp Vault):
   ```bash
   vault kv put infra/diagnostic-bot-inv-myinvestigation \
     my-investigation.yaml=@investigations/my-investigation.yaml
   ```

3. **Create VaultStaticSecret manifest**:
   ```yaml
   apiVersion: secrets.hashicorp.com/v1beta1
   kind: VaultStaticSecret
   metadata:
     name: diagnostic-bot-inv-myinvestigation
     namespace: diagnostic-bot
   spec:
     type: kv-v2
     mount: infra
     path: diagnostic-bot-inv-myinvestigation
     destination:
       name: diagnostic-bot-inv-myinvestigation
       create: true
     refreshAfter: 30s
     vaultAuthRef: diagnostic-bot
     rolloutRestartTargets:
       - kind: Deployment
         name: diagnostic-bot
   ```

4. **Mount secret in Deployment**:
   ```yaml
   volumeMounts:
     - name: inv-myinvestigation
       mountPath: /app/investigations/my-investigation.yaml
       subPath: my-investigation.yaml
       readOnly: true
   volumes:
     - name: inv-myinvestigation
       secret:
         secretName: diagnostic-bot-inv-myinvestigation
   ```

5. **Apply with GitOps** - commit manifests and let Flux reconcile

### Example: Creating a Custom Investigation

Let's create an investigation for Nginx ingress controller issues:

```yaml
name: "Nginx Ingress Troubleshooting"
description: "Diagnoses Nginx ingress controller issues (502/504, SSL, routing)"
trigger_patterns:
  - "nginx.*ingress"
  - "502.*bad.*gateway"
  - "504.*gateway.*timeout"
  - "ingress.*not.*working"

initial_prompt: |
  You are diagnosing Nginx ingress controller issues in a Kubernetes environment.

  ## Investigation Steps

  1. **Check ingress controller pods**:
     - Use list_k8s_pods with namespace={{INGRESS_NAMESPACE}}, selector="app.kubernetes.io/name=ingress-nginx"
     - Look for restarts, OOMKilled, CrashLoopBackOff

  2. **Query recent logs**:
     - Use query_loki with: {namespace="{{INGRESS_NAMESPACE}}"} |= "error" or "warn"
     - Time range: last 1 hour
     - Look for upstream errors, SSL handshake failures, timeout messages

  3. **Check ingress resource**:
     - Use get_k8s_resource for the specific Ingress
     - Verify: backend service name, port, TLS config, annotations

  4. **Check backend pods**:
     - Use list_k8s_pods for backend service namespace
     - Verify pods are Running and Ready

  5. **Analyze error patterns**:
     - 502: Backend not responding (pod down, wrong service/port)
     - 504: Backend timeout (slow response, deadlock)
     - SSL errors: Certificate issues, TLS version mismatch

  ## Output Format

  ```
  ## Issue Summary
  [One-line description]

  ## Symptoms
  [What the user is experiencing]

  ## Root Cause
  [Specific cause identified]

  ## Remediation
  [Exact steps to fix with commands/configs]
  ```

  ## Substitution Variables
  - {{INGRESS_NAMESPACE}}: Namespace where ingress controller runs (e.g., "ingress-nginx")

kubernetes_resources:
  - type: "pod"
    namespace: "{{INGRESS_NAMESPACE}}"
    selector: "app.kubernetes.io/name=ingress-nginx"
    container: "controller"
    since: "1h"
    grep: "error"
    tail_lines: 200

require_approval: false
```

After creating this, substitute `{{INGRESS_NAMESPACE}}` with your actual namespace before deploying to production.

## Claude Tool Use via MCP Server

The bot uses Claude Code CLI with a custom MCP (Model Context Protocol) server that provides tools for autonomous investigation. Tool availability is dynamic — only tools backed by configured services are registered.

### Tool Categories

| Category | Env Var Required | Tools |
|----------|-----------------|-------|
| Loki (Logging) | `LOKI_ENDPOINT` (multi-tenant: `LOKI_DEFAULT_ORG_ID`, optional `LOKI_ORG_IDS`) | `query_loki` |
| CloudWatch Logs | `CLOUDWATCH_ACCOUNTS` or `CLOUDWATCH_ASSUME_ROLE` | `cloudwatch_logs_query`, `cloudwatch_logs_list_groups`, `cloudwatch_logs_get_events` |
| Prometheus | `PROMETHEUS_URL` or `PROMETHEUS_<NAME>_URL` | `prometheus_query`, `prometheus_query_range`, `prometheus_series`, `prometheus_label_values`, `prometheus_list_endpoints` |
| Grafana | `GRAFANA_URL` + `GRAFANA_API_KEY` (optional: `MCP_AUDIT_USER` for write attribution) | `grafana_list_dashboards`, `grafana_get_dashboard`, `grafana_create_dashboard`, `grafana_update_dashboard`, `grafana_patch_dashboard`, `grafana_delete_dashboard`, `grafana_create_folder`, `grafana_get_dashboard_version`, `grafana_restore_dashboard_version` |
| Database | `DATABASE_URL` or `DATABASE_<NAME>_URL` | `database_query`, `database_list` |
| GitHub | `GITHUB_TOKEN` | `github_get_file`, `github_list_directory`, `github_search_code` |
| ECR | `AWS_REGION` or `AWS_DEFAULT_REGION` | `ecr_scan_results` |
| Third-Party APIs | Per-API token env var (e.g., `BITGO_ACCESS_TOKEN`) | Dynamically generated from YAML configs in `apis/` directory |
| Utilities | *(always available)* | `whois_lookup`, `generate_pdf` |

See `pkg/bot/tools.go` for the env var detection logic and `pkg/mcp/server.go` for conditional tool registration.

### Grafana audit attribution

Every Grafana write performed through the MCP server is stamped with two pieces of information so changes are traceable after the fact:

- **Audit user (who).** Resolved once at MCP server startup. Priority:
  1. `MCP_AUDIT_USER` environment variable, if set — explicit override for containers, CI, or any deployment where the OS user isn't the right identity.
  2. `user.Current()` — the local OS user. For Claude Code running the MCP server over stdio, this is the developer running Claude Code.
  3. `mcp-server` — last-resort fallback, with a warning logged.
- **Intention (why).** Optional `message` parameter accepted by each write tool. The LLM supplies a short description of what is being changed and why. When omitted, a tool-specific default (`created via mcp`, `updated via mcp`, `deleted via mcp`) is used so the audit user still lands on the change.

The two are composed as `"<audit_user>: <intention>"` and written to:
- **Grafana's version history** for `grafana_create_dashboard`, `grafana_update_dashboard`, and `grafana_patch_dashboard` — visible from the dashboard's Versions tab.
- **Slog (INFO)** on every write tool, with structured fields `tool`, `uid`, `title`, `audit_user`, `audit_source_ip`, and `message`. Deletes and folder creates have no version-history surface in Grafana, so slog is the only audit trail — the bot's stdout collector ships these into Loki.

**`audit_source_ip` — corroborating evidence for HTTP/SSE callers.** The HTTP and SSE handlers are wrapped in `mcp.WithAuditSourceMiddleware`, which extracts the originating client IP (X-Forwarded-For first hop, then X-Real-IP, then `RemoteAddr` with port stripped) and threads it through the request context. Every Grafana-write slog line carries the resolved IP. Notes:
- The Slack-bot stdio path has no network peer, so the field is the literal string `stdio`.
- Port-forwarded callers (`kubectl port-forward` into the MCP server pod) surface as `127.0.0.1`. Port-forward access requires cluster RBAC, so the value `127.0.0.1` in this field is itself a signal that the action came from a cluster admin and you should consult kube audit logs for the human identity behind the port-forward session.
- Direct HTTP callers expose their real IP (subject to trust of the ingress's XFF, which is fine for the VPC-gated deployment).
- IP is **not** identity. Same NAT egress = same IP for multiple humans; this is forensic corroboration, not authentication.

Per-request override: callers that have a more authoritative identity than the process owner (e.g. the Slack bot, which has the Slack user ID for the human who started the investigation) can override the audit user per-request with `mcp.WithAuditUser(ctx, "alice")`. The MCP tool handler reads from context first, falls back to the startup-resolved default.

### Architecture

The MCP server supports three transports, all serving identical tools:

1. **Stdio** (for Claude Code subprocess): Claude Code runs in `--print` mode, which does not load MCP servers registered via `claude mcp add`. Instead, the bot passes `--mcp-config` with the `/app/mcp-server` binary using stdio transport. This spawns a dedicated MCP server process per investigation.

2. **Streamable HTTP** (for external clients — recommended): When `MCP_HTTP_ENABLED=true`, the bot serves Streamable HTTP at `/mcp` on the configured port (default 8090). This is the current MCP standard (spec 2025-03-26) and is required by OpenAI, Devin, and Claude Desktop.

3. **SSE** (legacy, for backward compatibility): Also served at `/sse` on the same port. Existing clients using SSE continue to work.

### Connecting via `.mcp.json`

To connect Claude Code (or other MCP clients) to a running instance, add a `.mcp.json` file to your project root:

**Streamable HTTP (recommended):**
```json
{
  "mcpServers": {
    "diagnostic": {
      "type": "http",
      "url": "https://your-host:8090/mcp"
    }
  }
}
```

**SSE (legacy):**
```json
{
  "mcpServers": {
    "diagnostic": {
      "type": "sse",
      "url": "https://your-host:8090/sse"
    }
  }
}
```

**Both transports simultaneously:**
```json
{
  "mcpServers": {
    "diagnostic-http": {
      "type": "http",
      "url": "https://your-host:8090/mcp"
    },
    "diagnostic-sse": {
      "type": "sse",
      "url": "https://your-host:8090/sse"
    }
  }
}
```

**Note:** For Claude Code, the transport type is `"http"` (not `"streamable-http"`). Both transports expose identical tools — use whichever your client supports.

## Configuration

The bot is configured via environment variables:

**Required:**
- `SLACK_BOT_TOKEN` - Bot OAuth token (xoxb-...)
- `SLACK_APP_TOKEN` - App-level token for Socket Mode (xapp-...)
- `ANTHROPIC_API_KEY` - Claude API key (sk-ant-...)

**Optional:**
- `KUBECONFIG` - Path to kubeconfig (default: uses in-cluster config)
- `INVESTIGATION_DIR` - Path to investigation skills (default: `./investigations`)
- `CLAUDE_MD_PATH` - Path to engineering standards (default: `./docs/CLAUDE.md`)
- `COMPANY_NAME` - Company name for PDF report branding (default: `Company`)
- `FILE_RETENTION` - File cleanup interval (default: `24h`)
- `MCP_HTTP_ENABLED` - Enable HTTP/SSE MCP server (default: `false`, set to `true` for production)
- `MCP_HTTP_PORT` - Port for HTTP MCP server (default: `8090`)
- `READ_ONLY` - Global read-only switch. When truthy (`1`/`true`/`yes`/`on`), every write tool (Grafana dashboard create/update/patch/delete/restore and folder creation) is withheld from the toolset and rejected at dispatch, on both the Slack and MCP doors. Default: `false` (Grafana dashboard writes allowed).
- `METRICS_PORT` - Port for the admin/metrics + health server (default: `9090`). Always a separate port from the client-facing MCP HTTP listener; `/metrics` is never served on the MCP port.
- `OTEL_EXPORTER_OTLP_ENDPOINT` - OTLP endpoint for distributed tracing (e.g. `http://tempo-distributor.monitoring.svc:4318`). Tracing is **no-op when unset** — the service behaves identically without a collector. `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` takes precedence if both are set.

List-valued variables accept commas **and** newlines, so a YAML block scalar stays readable — one entry per line:

```yaml
env:
  - name: LOKI_ORG_IDS
    value: |-
      monitoring
      cloudtrail
      self-monitoring
```

**MCP Server Authentication** (multiple methods supported, configure one or more):
- `MCP_AUTH_TOKEN` - Static bearer token for simple authentication (default: empty = no auth)
- `MCP_JWT_SECRET` - JWT signing secret for JWT bearer token authentication
- `MCP_JWT_ALGORITHM` - JWT algorithm (default: `HS256`, also supports `RS256`)
- `MCP_API_KEYS` - API key authentication in format `key1:user1,key2:user2`
- `MCP_OIDC_ISSUER` - Enables generic OIDC/JWKS auth on the MCP HTTP/SSE endpoints (e.g. `https://dex.yourdomain.com`). When set, every request to `/mcp` and `/sse` must carry a signed RS256 JWT in `Authorization: Bearer …`; the bot validates the signature against the issuer's `/keys` JWKS endpoint, the `iss` claim against this URL, the `aud` claim against `MCP_OIDC_AUDIENCE`, and `exp`/`nbf`. Mutually exclusive with `GOOGLE_OAUTH_CLIENT_ID` — both-set is a startup error. **Recommended setup: [Dex + Google upstream](#dex--google-upstream-recommended).**
- `MCP_OIDC_AUDIENCE` - Required when `MCP_OIDC_ISSUER` is set. The IdP client ID — binds tokens to this specific client. Refusing to run without it closes the audience-binding gap.
- `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` - Allowlist of email domains (e.g. `katn-solutions.io`). The bot derives the domain from the `@`-suffix of the JWT's `email` claim — works whether or not the IdP passes through a separate `hd` claim. Empty = no domain restriction. Entries are separated by commas or any whitespace (newlines, tabs, spaces) — see the YAML `|-` block example in [Step 3](#step-3-bot-env-vars) for the readable multi-line form.
- `MCP_OIDC_ALLOWED_EMAILS` - Allowlist of exact email addresses. Useful for "these specific humans only," typically alongside the broader hosted-domain filter. Empty = no per-email restriction. Same separator rules as `MCP_OIDC_ALLOWED_HOSTED_DOMAINS`.
- `MCP_OIDC_ALLOWED_GROUPS` - Groups the user must be a member of (matched against the `groups` claim in the JWT). Empty means any authenticated user is allowed. Requires the IdP to emit the `groups` claim — for Google upstream, this requires Workspace Admin SDK access via domain-wide delegation; skip this knob if you don't have DWD configured. Same separator rules as `MCP_OIDC_ALLOWED_HOSTED_DOMAINS`.
- `MCP_OIDC_ALLOWED_EMAILS_FILE` - Path to a file whose contents replace `MCP_OIDC_ALLOWED_EMAILS`. Read at request time, mtime-cached so the per-request cost is one `stat`. Edits to the file (typically a ConfigMap mount) propagate without a pod restart. File-missing or file-unreadable fails closed on this axis (deny all + ERROR log per request) rather than silently widening. Empty file = no restriction, matching the env-var semantic. When set, the static `MCP_OIDC_ALLOWED_EMAILS` env var is ignored. See [Hot-reload from a ConfigMap](#hot-reload-allowlists-from-a-configmap) for the deployment shape.
- `MCP_OIDC_ALLOWED_HOSTED_DOMAINS_FILE` - Same pattern as `MCP_OIDC_ALLOWED_EMAILS_FILE` for the hosted-domain axis.
- `MCP_OIDC_ALLOWED_GROUPS_FILE` - Same pattern as `MCP_OIDC_ALLOWED_EMAILS_FILE` for the group axis. Groups churn far less than email lists; this knob exists for symmetry — most deployments don't need it.
- `MCP_OIDC_JWKS_CACHE_SECONDS` - How long to cache the JWKS document. Default `300`.
- `MCP_OIDC_SKIP_ISSUER_VERIFY` - Skip issuer verification (default: `false`, use only for testing).
- `MCP_MTLS_CA_CERT_PATH` - Path to CA certificate for mutual TLS authentication
- `MCP_MTLS_VERIFY_CLIENT` - Verify client certificates against CA (default: `true`)
- `GOOGLE_OAUTH_CLIENT_ID` - Enables Google OAuth on the MCP HTTP/SSE endpoints. When set, every request to `/mcp` and `/sse` must carry a Google access token in `Authorization: Bearer …`; missing/invalid tokens get `401` with `WWW-Authenticate` pointing at `/.well-known/oauth-protected-resource`. Claude Code reads that, opens a browser to Google, and caches the token thereafter. The Slack-bot stdio path is unaffected — it never hits HTTP. When unset, the MCP HTTP/SSE endpoints stay unauthenticated (current VPC-gated behavior). See [Google OAuth setup](#google-oauth-setup).
- `GOOGLE_ALLOWED_HOSTED_DOMAINS` - Allowlist of Google Workspace domains whose users may authenticate (e.g. `katn-solutions.io`). Empty means no domain restriction. Entries are separated by commas or any whitespace (newlines, tabs, spaces).
- `GOOGLE_ALLOWED_EMAILS` - Optional explicit per-user email allowlist applied on top of the hosted-domain filter. Empty means no per-email restriction. Same separator rules as `GOOGLE_ALLOWED_HOSTED_DOMAINS`.
- `GOOGLE_ALLOWED_EMAILS_FILE` - Path to a file whose contents replace `GOOGLE_ALLOWED_EMAILS`. Hot-reloadable: edits propagate without a pod restart. Same semantics as `MCP_OIDC_ALLOWED_EMAILS_FILE` (stat-on-every-call + mtime cache + fail-closed on unreadable + file wins over the static env var). The Google path's userinfo cache holds the identity only; the allowlist check runs per request, so revocation effective on the next request. See [Direct Google OAuth — Bot env vars](#bot-env-vars-1) for the deployment shape.
- `GOOGLE_ALLOWED_HOSTED_DOMAINS_FILE` - Same pattern as `GOOGLE_ALLOWED_EMAILS_FILE` for the hosted-domain axis.
- `MCP_PUBLIC_URL` - The externally-reachable base URL of the MCP HTTP server (e.g. `https://diagnostic-bot.example.com`). Required when `GOOGLE_OAUTH_CLIENT_ID` is set — used to construct the `resource_metadata` URL Claude Code follows.

**Tool Backing Services** (each enables a set of MCP tools — see [Tool Categories](#tool-categories)):
- `LOKI_ENDPOINT` - Loki gateway endpoint (enables `query_loki`)
- `LOKI_DEFAULT_ORG_ID` - Loki tenant (X-Scope-OrgID) sent when a query doesn't specify one. Required for Loki deployments configured with `auth_enabled: true`. Leave unset for single-tenant `auth_enabled: false` deployments — the wire format is unchanged and Loki serves the synthetic `fake` tenant.
- `LOKI_ORG_IDS` - Comma-separated allowlist of tenants the bot may send (e.g. `monitoring,cloudtrail,self-monitoring`). Not a security boundary — Loki trusts whatever `X-Scope-OrgID` header it receives; this list keeps the LLM from inventing tenant names. When set, `LOKI_DEFAULT_ORG_ID` must be one of the listed values or the bot exits at startup. The allowlist is also appended to the `query_loki` tool description so the LLM can discover valid values.
- `CLOUDWATCH_ACCOUNTS` - JSON map of friendly name to full IAM role ARN for multi-account CloudWatch access (enables CloudWatch tools). Example: `{"dev":"arn:aws:iam::111:role/dev-reader","prod":"arn:aws:iam::222:role/prod-reader"}`
- `CLOUDWATCH_ASSUME_ROLE` - IAM role ARN to assume for single-account CloudWatch queries (legacy, enables CloudWatch tools). Use `CLOUDWATCH_ACCOUNTS` for multi-account support.
- `CLOUDWATCH_EXTERNAL_ID` - External ID for cross-account role assumption (optional, used with both `CLOUDWATCH_ACCOUNTS` and `CLOUDWATCH_ASSUME_ROLE`)
- `PROMETHEUS_URL` - Prometheus endpoint (enables Prometheus tools). Multiple endpoints: use `PROMETHEUS_<NAME>_URL` pattern
- `GRAFANA_URL` - Grafana instance URL (requires `GRAFANA_API_KEY`)
- `GRAFANA_API_KEY` - Grafana API key (required with `GRAFANA_URL`, enables Grafana tools)
- `MCP_AUDIT_USER` - Explicit audit identity stamped on every Grafana write (create/update/delete dashboard, create folder). Optional. When unset, the MCP server falls back to the OS user (`user.Current()`) — which is the developer running Claude Code locally. See [Grafana audit attribution](#grafana-audit-attribution).
- `DATABASE_URL` - Database connection string (enables Database tools). Multiple databases: use `DATABASE_<NAME>_URL` pattern
- `GITHUB_TOKEN` - Personal access token for GitHub tools
- `AWS_REGION` or `AWS_DEFAULT_REGION` - AWS region (enables ECR tools)
- `AWS_*` - Standard AWS credentials for ECR vulnerability scanning
- `API_CONFIG_DIR` - Directory containing third-party API YAML configs (default: `./apis`)

## Third-Party API Integration

The bot supports adding read-only API integrations via YAML configuration files — no Go code required. Drop a YAML file in the `apis/` directory, set the auth token env var, and the MCP server automatically registers the endpoints as tools that Claude can call.

### How It Works

1. On startup, the MCP server loads all `.yaml` files from `API_CONFIG_DIR` (default `./apis/`)
2. Each config defines an API with endpoints, parameters, auth, and rate limiting
3. Endpoints become MCP tools named `{api_name}_{endpoint_name}` (e.g., `bitgo_list_wallets`)
4. If the auth token env var is not set, that API's tools are silently skipped
5. Claude can call these tools during investigations just like built-in tools

### API Config YAML Structure

```yaml
name: myapi                              # API name (used as tool name prefix)
description: "My API description"
base_url: https://api.example.com
auth:
  type: bearer                           # "bearer", "header", or "none"
  token_env: MY_API_TOKEN                # env var containing the auth token
headers:                                 # optional custom headers
  User-Agent: "diagnostic-bot/1.0"
rate_limit:
  max_concurrent: 5                      # semaphore size (default: 5)
  retry_on_429: true                     # honor Retry-After header (default: true)
  max_retries: 3                         # max 429 retries (default: 3)
defaults:
  limit: 25                              # default pagination limit
  max_limit: 100                         # server-enforced max limit
endpoints:
  - name: list_items                     # becomes tool "myapi_list_items"
    description: "List all items"
    method: GET
    path: /api/v1/items
    params:
      - name: status
        type: string
        description: "Filter by status"
        required: false
    redact_fields: [email, phone]        # PII fields to redact from responses

  - name: get_item
    description: "Get item by ID"
    method: GET
    path: /api/v1/items/{item_id}        # path parameters use {placeholder} syntax
    params:
      - name: item_id
        type: string
        description: "Item ID"
        required: true
        in: path                         # "path" or "query" (default: "query")
        validate: "[a-f0-9]{24,}"        # regex validation pattern
    redact_fields: [email, phone, ssn]
```

### Security Features

- **Read-only**: Only GET requests are supported
- **Path traversal protection**: All path parameters are validated against `../` and `\..` patterns
- **Query injection protection**: Path parameters are blocked from containing `?`, `&`, `#`
- **Regex validation**: Per-parameter regex patterns reject invalid input before any HTTP request
- **PII redaction**: Configurable field-level redaction walks nested JSON and replaces sensitive values with `[redacted]`
- **Rate limiting**: Semaphore-based concurrency control prevents Claude's fan-out from overwhelming APIs
- **429 retry**: Honors `Retry-After` headers with exponential backoff (capped at 30s)
- **Response size cap**: Responses limited to 5MB
- **Graceful degradation**: Missing auth token means tools are hidden, not erroring

### Adding a New API

1. Create a YAML file in `apis/` (e.g., `apis/fireblocks.yaml`)
2. Set the auth token env var in your deployment (e.g., `FIREBLOCKS_API_TOKEN`)
3. Deploy — tools appear automatically in Claude's tool list

No PRs to the core repo needed. Investigation skills can reference the new tools immediately.

### Included Example: BitGo

The `apis/bitgo.yaml` config provides read-only access to BitGo custodial wallet APIs:

| Tool | Description |
|------|-------------|
| `bitgo_list_enterprises` | List accessible enterprises |
| `bitgo_list_wallets` | List wallets with filters |
| `bitgo_get_wallet` | Get wallet details |
| `bitgo_get_wallet_balance` | Get balances for a coin |
| `bitgo_list_wallet_addresses` | List receive addresses |
| `bitgo_list_wallet_transfers` | List transfers with filters |
| `bitgo_get_transfer` | Get transfer details |
| `bitgo_list_enterprise_transfers` | Enterprise-wide transfers |
| `bitgo_list_pending_approvals` | List pending multi-sig approvals |
| `bitgo_get_pending_approval` | Get pending approval details |

Requires `BITGO_ACCESS_TOKEN` env var. All endpoints redact email, phone, and IP address fields.

## Building

```bash
# Run linters
make lint

# Run tests
make test

# Build binary
make build

# Build Docker image
make docker-build
```

## Usage

### Slack Commands

- `@bot help` - List available investigation types
- `@bot <problem description>` - Start an investigation
- Reply in thread to continue conversation

### Example Interactions

**ModSecurity Investigation:**
```
User: @bot I'm getting 403 errors from the WAF
Bot: 🔍 Starting ModSecurity WAF Block Diagnosis investigation...
Bot: [Queries Loki for recent 403s, analyzes rules, categorizes blocks]
Bot: Found 3 blocked requests in the last hour. Rule 930120 (OS File Access Attempt)
     triggered on cookie data. This appears to be a FALSE POSITIVE - the session
     cookie contains Base64 data that matches the pattern.

     Recommended fix:
     ```nginx
     SecRuleUpdateTargetById 930120 "!REQUEST_COOKIES:/__Secure-session-token/"
     ```
User: Can you check if this is happening in staging too?
Bot: [Queries Loki for staging realm, analyzes results]
```

**Atlas Migration Investigation:**
```
User: @bot migration 20251029093301 not showing up in dev-01
Bot: 🔍 Starting Atlas Migration Troubleshooting investigation...
Bot: [Checks GitRepository, ConfigMap, AtlasMigration CRD]
Bot: Root cause identified:

     dev-01 environment is pinned to tag 0.0.667 in control-kubernetes
     Migration 20251029093301 was added in tag 0.0.673

     Resolution: Update config to reference tag 0.0.673
```

## Authentication

The MCP HTTP/SSE endpoints can be gated behind OAuth so only authenticated humans can connect. Claude Code does the standard browser-pop OAuth flow on first connection, caches the token, and presents it on every subsequent request. Every Grafana write then carries the verified email as the audit identity (visible in Grafana's version history).

The Slack-bot path is unaffected by any of this — it uses stdio, never HTTP.

There are two ways to enable it, with very different operational properties:

- **[Dex + Google upstream](#dex--google-upstream-recommended)** — recommended. Dex acts as the authorization server Claude Code talks to, Google is Dex's upstream identity provider. End users never see Google's client secret. Add/remove users by editing Workspace membership.
- **[Direct Google](#direct-google-oauth)** — simpler to set up but requires distributing Google's "Desktop app" client ID *and* client secret to every user via their `.mcp.json`. The secret can't be kept confidential in distributed binaries (Google's docs acknowledge this; PKCE bears the real security weight), but it still feels gross. Use this only if you can't or don't want to run Dex.

### Authentication vs authorization

Two distinct concerns, two distinct owners:

| Concern | Owner | Mechanism |
|---|---|---|
| **Authn** — is this person who they say they are? | Google | Standard sign-in: password, MFA, Workspace conditional-access policies. Result: a signed ID token with `email`, `email_verified`, `sub`, etc. |
| **Authz** — given a verified identity, can they use *this* server? | This server | `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` (domain filter), `MCP_OIDC_ALLOWED_EMAILS` (per-user allowlist), `MCP_OIDC_ALLOWED_GROUPS` (group filter — needs IdP support for groups claim). Empty allowlist on any axis = no restriction on that axis. |

A user denied at the authn layer never sees the bot — Google rejects sign-in. A user denied at the authz layer sees a 403 from the bot after authenticating. Both events log distinctly.

## Dex + Google upstream (recommended)

### Why this layer exists at all

A reasonable first question: "If users authenticate via Google anyway, why does Dex sit in the middle?" Three concrete answers:

1. **Google has no public-client support.** Every Google OAuth client — even "Desktop app" — is issued a client secret. The secret can't be kept confidential in a distributed CLI, and `claude mcp add` requires `--client-secret` on the direct-Google path. Dex's `staticClients[].public: true` *is* a true public client: PKCE only, no secret on either side of the Claude Code ↔ Dex relationship.

2. **Google's OAuth surface never exposes group membership.** This is the load-bearing reason and it's not obvious from the Google Cloud Console. *No matter what scopes you request, what consent-screen settings you choose, or what OAuth client type you use*, the ID tokens Google issues do not contain a `groups` claim. Google Cloud Console has no toggle to enable it. Group information lives in the Workspace **Admin SDK Directory API**, which is a separate surface that requires a service account with **domain-wide delegation** to query.

   If you need groups in your authz, two things become true: (a) someone has to call the Admin SDK with DWD, and (b) that someone is best off being Dex rather than each downstream application. Dex's Google connector (when configured with `serviceAccountFilePath` + `adminEmail` + `groups:`) makes the Admin SDK call on every login and *populates* the `groups` claim in its JWT from the response — not synthesizing the data, just making the call once on every application's behalf. The win is consolidating one DWD setup at Dex instead of N service-account keys spread across N applications.

   **If you don't need groups at all** — and most internal services don't, because hosted-domain plus email allowlisting is usually sufficient — you skip this whole subsystem. No DWD anywhere. No service account anywhere. Dex's Google connector emits `email` and `email_verified` for free with the `openid email` scopes, and the bot's `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` / `MCP_OIDC_ALLOWED_EMAILS` knobs gate authz from that. **This is the path the diagnostic-bot deployment uses.**

3. **Provider abstraction.** The bot validates JWTs from `MCP_OIDC_ISSUER`, whatever that resolves to. Google today, Okta or Auth0 or self-hosted Keycloak tomorrow, without touching bot code or env vars. The IdP becomes a Dex config detail.

If you don't need groups, never plan to switch identity providers, and are willing to live with the client-secret-in-every-user's-config friction, the [Direct Google OAuth](#direct-google-oauth) path is fine. For most setups it isn't worth the savings.

### Architecture

```
                                             public client       confidential client
                                           (PKCE, no secret)    (Google client secret
Claude Code  ─────►  bot /mcp                                       stays on Dex)
                       │
                       │ 401 + WWW-Authenticate
                       ▼
              .well-known/oauth-protected-resource  ──►  authorization_servers: [Dex]
                       │
                       ▼
                     Dex authorize  ──────────────────►  Google authorize
                                                          (browser sign-in,
                                                           MFA, consent)
                       │   ◄──────────────────────────  ID token (email, hd)
                       │
                       ▼
                  Dex-issued JWT  ◄─────  Claude Code's loopback callback
                       │
                       │ (PKCE code → ID token exchange)
                       ▼
              bot /mcp  +  Authorization: Bearer <Dex JWT>
                       │
              JWKS verify · iss/aud/exp checks ·
              hosted-domain / email / groups gate
                       │
                       ▼
                  authenticated  →  audit_user = JWT email
```

### How it works

The architecture relies on three distinct OAuth/trust relationships. Keeping them straight is what makes the rest of the setup make sense.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                                                                         │
│   Relationship 1: Dex ↔ Google                                          │
│                                                                         │
│   Dex is a confidential OAuth client of Google. Has a client ID +       │
│   secret issued by Google Cloud Console (Web app type). The secret      │
│   lives on Dex's pod, never crosses the wire to end users.              │
│   Google trusts Dex; Dex trusts Google.                                 │
│                                                                         │
│   Relationship 2: Claude Code ↔ Dex                                     │
│                                                                         │
│   Claude Code is a public OAuth client of Dex. Has only a client ID     │
│   ("diagnostic-bot"), no secret. PKCE bears the security. Defined in    │
│   Dex's config under staticClients with public: true.                   │
│   Dex trusts Claude Code (via PKCE proof); Claude Code trusts Dex.      │
│                                                                         │
│   Relationship 3: Bot ↔ Dex                                             │
│                                                                         │
│   The bot is a resource server. It validates JWTs Dex issued by         │
│   checking Dex's signature (JWKS at <dex>/keys), iss, aud, exp.         │
│   No OAuth client relationship — the bot never calls Dex's auth         │
│   endpoints, never holds a credential. Just validates tokens.           │
│   Bot trusts Dex's signing key; Dex doesn't know the bot exists.        │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

Notice: **Google only sees Dex.** Google has no idea diagnostic-bot exists, no idea Claude Code is in the picture. From Google's perspective there's one OAuth app, "Dex," that signs people in. That's what lets you use Workspace policy (MFA, conditional access, device posture) to gate the bot — those policies attach to Dex's OAuth app, and any user reaching Dex is subject to them.

#### First-connection sequence

```
Step 1 — Claude Code hits the bot cold

  Claude Code  ───── GET /mcp ─────►  bot
                                       │
                                       │ no Authorization header → 401
                                       ▼
  Claude Code  ◄──── 401 ──── WWW-Authenticate: Bearer
                                resource_metadata="https://bot.example.com/
                                                   .well-known/oauth-protected-resource"

Step 2 — Claude Code discovers Dex

  Claude Code  ──── GET /.well-known/oauth-protected-resource ────►  bot
  Claude Code  ◄── { "authorization_servers": ["https://dex…"], … }

  Claude Code  ──── GET /.well-known/openid-configuration ────►  Dex
  Claude Code  ◄── { authorization_endpoint, token_endpoint, jwks_uri, … }

Step 3 — Claude Code starts the OAuth dance

  Claude Code spins up a local listener on 127.0.0.1:8080.
  Generates a PKCE code_verifier + code_challenge (SHA256).
  Opens the user's default browser to:

    https://dex…/auth
      ?client_id=diagnostic-bot
      &redirect_uri=http://localhost:8080/callback
      &response_type=code
      &code_challenge=<sha256-of-verifier>
      &code_challenge_method=S256
      &scope=openid+email+profile
      &state=<random>

Step 4 — Dex sees the request, kicks the user to Google

  Dex looks up "diagnostic-bot" in staticClients, confirms public: true,
  confirms the redirect_uri is in the allowed list. Then it has to
  authenticate the user — that's what the connector is for.

  Dex generates an internal auth state, redirects the browser to:

    https://accounts.google.com/o/oauth2/v2/auth
      ?client_id=<dex's Google client id>
      &redirect_uri=https://dex…/callback
      &response_type=code
      &scope=openid email profile
      &state=<dex's internal state>

Step 5 — Google authenticates the human

  User sees Google's sign-in page. Enters email + password + MFA
  + clicks through any Workspace conditional-access prompts.
  Workspace policy enforces: hosted domain, suspended accounts, etc.

  Google → browser → https://dex…/callback?code=<google-code>

Step 6 — Dex exchanges the code with Google, gets the user's identity

  Dex (server-side, with its Google client secret) POSTs to:

    https://oauth2.googleapis.com/token
      code=<google-code>
      client_id=<dex's google client id>
      client_secret=<dex's google client secret>  ◄─── the secret that
      grant_type=authorization_code                    NEVER leaves Dex
      redirect_uri=https://dex…/callback

  Google returns:

    {
      "access_token": "ya29…",
      "id_token": "eyJ…",   ◄── signed JWT with email, sub, hd, name, etc.
      "expires_in": 3599
    }

  Dex verifies the Google ID token's signature using Google's JWKS,
  extracts the email, applies the connector's hostedDomains filter.

Step 7 — Dex issues its own JWT to Claude Code

  Dex maps Google's claims into a fresh JWT it signs with its own key:

    {
      "iss": "https://dex.yourdomain.com",   ◄── Dex's issuer URL,
                                                   matches MCP_OIDC_ISSUER
      "sub": "<dex's internal user id>",
      "aud": "diagnostic-bot",                 ◄── matches MCP_OIDC_AUDIENCE
      "email": "alice@katn-solutions.io",      ◄── propagated from Google
      "email_verified": true,
      "name": "Alice",
      "exp": …, "iat": …,
      // No `hd` claim — Dex doesn't pass that through.
      // No `groups` claim either, unless DWD is configured.
    }

  Dex finishes the original flow by redirecting:

    https://localhost:8080/callback?code=<dex-code>&state=<random>

  Browser hits the loopback. Claude Code's listener catches it.

Step 8 — Claude Code exchanges Dex's code for the JWT

  Claude Code POSTs to Dex's token endpoint with the code AND the original
  PKCE code_verifier. Dex confirms SHA256(verifier) == code_challenge it
  saw earlier — that's PKCE proof. No client secret involved.

  Dex returns:

    {
      "access_token": "<Dex JWT>",
      "id_token":     "<Dex JWT>",
      "token_type":   "Bearer",
      "expires_in":   86400,
      "refresh_token": "<opaque refresh token>"
    }

  Claude Code stores all of this. The browser closes itself.

Step 9 — Claude Code retries the bot with the token

  Claude Code  ──── GET /mcp ────►  bot
                Authorization: Bearer <Dex JWT>
                                     │
                                     │ The bot:
                                     │   1. Parses the JWT
                                     │   2. Fetches Dex's JWKS from
                                     │      https://dex…/keys (cached
                                     │      for MCP_OIDC_JWKS_CACHE_SECONDS)
                                     │   3. Verifies the signature
                                     │   4. Checks iss == MCP_OIDC_ISSUER
                                     │   5. Checks aud == MCP_OIDC_AUDIENCE
                                     │   6. Checks exp
                                     │   7. Splits email on @ → checks
                                     │      domain against
                                     │      MCP_OIDC_ALLOWED_HOSTED_DOMAINS
                                     │   8. Checks email against
                                     │      MCP_OIDC_ALLOWED_EMAILS
                                     │      (or the contents of
                                     │      MCP_OIDC_ALLOWED_EMAILS_FILE,
                                     │      re-read on every request)
                                     │   9. (Optionally) checks groups
                                     ▼
                            request runs, audit identity = "alice@katn-solutions.io"
```

#### Steady state

Claude Code caches the Dex JWT. Every subsequent MCP call just sends:

```
GET /mcp
Authorization: Bearer <cached Dex JWT>
```

Bot validates signature against the cached JWKS (no network call) → done. The whole thing is one signature verify and a few string compares, sub-millisecond.

When the JWT expires (typically 24h), Claude Code refreshes against Dex. Dex hits Google with *its* refresh token to confirm the user still has a valid Workspace session. If Google says "user is suspended / no longer in the org," Dex returns an error and Claude Code is forced into the full re-auth flow — which Google will then refuse, locking the user out.

### Step 1: Create the Google "Web application" OAuth client (one-time)

This is the client *Dex* uses against Google. It is confidential and its secret stays on Dex's pod. End users never see it. Concrete values throughout:

#### 1a. OAuth consent screen

The consent screen must be configured before you can create the client. Settings:

| Field | Value | Notes |
|---|---|---|
| User Type | **Internal** | Restricts sign-in to your Workspace domain. External would also work but exposes the consent screen to arbitrary Google accounts. |
| App name | `Diagnostic Bot (via Dex)` | What users see on the consent screen |
| User support email | `<your support email>` | Must be a Workspace email |
| App logo | optional | |
| **Authorized domains** | `yourdomain.com` | Top-level eTLD+1 of any redirect URI you plan to use. **One entry per top-level domain** — don't list the full URL here. |
| Developer contact info | `<your email>` | Workspace email is fine |
| Scopes | Add `openid`, `.../auth/userinfo.email`, `.../auth/userinfo.profile` | Non-sensitive. No verification needed for Internal apps. |

For an Internal app, no Google review / verification is required even for "sensitive" scopes — but you're not using sensitive ones anyway.

#### 1b. APIs to enable

Going to **APIs & Services → Library**:

- The basic OIDC flow needs **nothing enabled**. `openid` + `email` + `profile` scopes are core OAuth and work without an explicit API enable.
- If you ever want groups via DWD: enable **Admin SDK API**. Skip otherwise.

> **No-groups-via-OAuth warning.** Don't expect group membership to appear in the OAuth flow you just configured. Google's ID tokens do *not* include a `groups` claim — there is no scope, no consent-screen setting, no Console toggle that adds one. Group information lives in the Workspace Admin SDK Directory API and is fetched out-of-band by Dex when you configure DWD on the connector (see Step 2's commented `serviceAccountFilePath` block). If you skip the DWD setup, your tokens carry email + domain but no groups, which is fine for `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` and `MCP_OIDC_ALLOWED_EMAILS` filtering — just don't set `MCP_OIDC_ALLOWED_GROUPS`.

#### 1c. Create the OAuth client

**APIs & Services → Credentials → Create Credentials → OAuth client ID**:

| Field | Value | Notes |
|---|---|---|
| Application type | **Web application** | Required for the Dex-as-confidential-client model. Not "Desktop app" — that's the direct-Google path. |
| Name | `dex-google-upstream` | Internal label; users never see it. |
| **Authorized JavaScript origins** | (none) | Dex doesn't make browser-side JS calls. Leave empty. |
| **Authorized redirect URIs** | `https://dex.yourdomain.com/callback` | Exact match. Must be HTTPS in production. Dex's callback path is `/callback` by default — verify against your Dex deployment's `issuer:` URL. Append more entries if you have multiple Dex environments (e.g. staging + prod). |

After saving, Google shows you the **Client ID** and **Client secret**. Both go straight into Dex's config (Step 2). Save the secret somewhere safe — Google will let you re-fetch it but you should treat it as a credential.

### Step 2: Add the Google connector + a public client for the bot to your Dex config

```yaml
issuer: https://dex.yourdomain.com   # must match MCP_OIDC_ISSUER exactly

connectors:
  # Your existing SSH connector for kubectl, unchanged
  - type: ssh
    id: ssh
    # ...

  # New: Google upstream for browser users (diagnostic-bot, etc.)
  - type: google
    id: google
    name: Google
    config:
      clientID: <dex-google-upstream-client-id>.apps.googleusercontent.com
      clientSecret: <dex-google-upstream-client-secret>   # Dex's secret to Google. Never given to users.
      redirectURI: https://dex.yourdomain.com/callback
      hostedDomains:
        - katn-solutions.io                                # Workspace-only
      # Optional: emit a `groups` claim. Requires a Workspace service
      # account with domain-wide delegation. Skip this whole block if
      # you don't have DWD configured — domain-only allowlisting is fine.
      # serviceAccountFilePath: /etc/dex/google-svc.json
      # adminEmail: dex-admin@katn-solutions.io
      # groups:
      #   - sre@katn-solutions.io
      #   - platform@katn-solutions.io

staticClients:
  - id: diagnostic-bot
    name: Diagnostic Bot MCP
    public: true                                            # ← true public client, PKCE only
    redirectURIs:
      - http://localhost:8080/callback                      # Claude Code's loopback listener
```

The `staticClients[].public: true` is what makes this a real public client — no secret needed on Dex's side either. Authentication is delegated entirely to Google; Dex is essentially proxying.

### Step 3: Bot env vars

Two ways to express the authorization knobs — pick one per axis. The static-env form is fine for a handful of entries that rarely change; the file form lets you edit the list in gitops and have the running pod pick it up without a restart.

**Static env vars (simple, requires pod restart to edit):**

```
MCP_OIDC_ISSUER=https://dex.yourdomain.com
MCP_OIDC_AUDIENCE=diagnostic-bot
MCP_PUBLIC_URL=https://diagnostic-bot.example.com

# Authorization (pick whichever combination fits your team)
MCP_OIDC_ALLOWED_HOSTED_DOMAINS=katn-solutions.io                 # broad
MCP_OIDC_ALLOWED_EMAILS=alice@katn-solutions.io,bob@katn-solutions.io   # narrow, optional
# MCP_OIDC_ALLOWED_GROUPS=sre,platform                            # only if Dex emits groups
```

**File-backed (hot-reload, recommended once the email list grows past two or three entries):**

```
MCP_OIDC_ISSUER=https://dex.yourdomain.com
MCP_OIDC_AUDIENCE=diagnostic-bot
MCP_PUBLIC_URL=https://diagnostic-bot.example.com

MCP_OIDC_ALLOWED_HOSTED_DOMAINS=katn-solutions.io                          # static — domains rarely change
MCP_OIDC_ALLOWED_EMAILS_FILE=/etc/diagnostic-bot/oidc/allowed-emails       # hot-reloadable
# (drop MCP_OIDC_ALLOWED_EMAILS — the file replaces it; if both are set, the file wins)
```

The file approach needs a ConfigMap and a mount — see [Hot-reload allowlists from a ConfigMap](#hot-reload-allowlists-from-a-configmap) below for the full deployment YAML. Axes mix freely: keep `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` as a static env (it almost never changes) and put just the email list in a file.

When `MCP_OIDC_ISSUER` is unset, the OIDC path is off. Both `MCP_OIDC_ISSUER` and `GOOGLE_OAUTH_CLIENT_ID` set simultaneously is a startup error — pick exactly one.

#### Long allowlists: use a YAML block scalar

The allowlist env vars accept **commas or any whitespace** (newlines, tabs, spaces) as entry separators. A single-line comma-separated value is fine for two or three entries; for anything longer, drop in a YAML `|-` block scalar so each entry is on its own line and the diff is reviewable:

```yaml
# kubernetes Deployment manifest, container env block
env:
  - name: MCP_OIDC_ALLOWED_HOSTED_DOMAINS
    value: katn-solutions.io

  - name: MCP_OIDC_ALLOWED_EMAILS
    value: |-
      alice@katn-solutions.io
      bob@katn-solutions.io
      carol@katn-solutions.io
      dave@katn-solutions.io
      eve@katn-solutions.io
```

Both forms parse identically — pick whichever reads better at the size you're at. Mixed forms (commas *and* newlines in the same value) also work, which is convenient when a block scalar leaves stray indentation. Empty entries are dropped.

#### Hot-reload allowlists from a ConfigMap

The env-var approach above is simple, but every team-membership change requires a Deployment edit and pod restart. If you'd rather edit the list in gitops and have the running pod pick it up automatically, point the bot at a file instead:

```yaml
# clusters/<env>/diagnostic-bot/configmap-oidc-allowlist.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: diagnostic-bot-oidc-allowlist
  namespace: diagnostic-bot
data:
  allowed-emails: |-
    alice@katn-solutions.io
    bob@katn-solutions.io
    carol@katn-solutions.io
```

```yaml
# clusters/<env>/diagnostic-bot/deployment.yaml — container env + mount
env:
  - name: MCP_OIDC_ALLOWED_HOSTED_DOMAINS
    value: katn-solutions.io

  - name: MCP_OIDC_ALLOWED_EMAILS_FILE
    value: /etc/diagnostic-bot/oidc/allowed-emails
  # drop MCP_OIDC_ALLOWED_EMAILS — the file replaces it

volumeMounts:
  - name: oidc-allowlist
    mountPath: /etc/diagnostic-bot/oidc
    readOnly: true

volumes:
  - name: oidc-allowlist
    configMap:
      name: diagnostic-bot-oidc-allowlist
```

How the propagation works: edit `data.allowed-emails` in gitops → Flux (or whatever you use) applies the new ConfigMap → kubelet refreshes the mount via its atomic `..data` symlink swap (~60 s) → the bot's next OIDC `Authenticate` call stats the file, sees a new mtime, re-reads, and applies the new list. No pod restart, no rollout. Per-request cost is one `stat` syscall when the file hasn't changed; the parsed list is mtime-cached.

Behavior matrix:

| `…_FILE` env | `…` env | Result |
|---|---|---|
| unset | unset | no restriction on that axis |
| unset | set | static list from env var (legacy path) |
| set, file readable | either | dynamic list from file (file wins, env ignored) |
| set, file readable but empty | either | no restriction on that axis (matches env-var semantic — deleting the ConfigMap data key widens the policy) |
| set, file missing or unreadable | either | **fail closed** on that axis (deny all) + ERROR log per request |

The same `_FILE` suffix works on `MCP_OIDC_ALLOWED_HOSTED_DOMAINS_FILE` and `MCP_OIDC_ALLOWED_GROUPS_FILE`. Mix freely — e.g. keep `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` as a static env (it almost never changes) and put just the email list in a file.

The bot logs the active source on startup so the operator never has to wonder:

```
OIDC auth enabled for MCP HTTP/SSE
  emails_from_file=true emails_file=/etc/diagnostic-bot/oidc/allowed-emails
  hosted_domains_from_file=false allowed_hosted_domains=[katn-solutions.io]
  ...
```

### Step 4: How each user adds the server to Claude Code

```bash
claude mcp add diagnostic-bot https://diagnostic-bot.example.com/mcp \
  --transport http \
  --client-id diagnostic-bot \
  --callback-port 8080
```

No `--client-secret`. The `--client-id` value is just the Dex static client name; it's not a secret. On first MCP call:

1. Claude Code hits `/mcp` with no token. Bot returns `401` with `WWW-Authenticate: Bearer resource_metadata="<bot>/.well-known/oauth-protected-resource"`.
2. Claude Code reads the metadata, discovers Dex as the authorization server.
3. Claude Code spins up a loopback listener on `localhost:8080/callback`, opens the user's browser to `https://dex.yourdomain.com/auth` with PKCE.
4. Dex redirects to Google sign-in. User signs in (password + MFA + whatever Workspace policy demands).
5. Google returns to Dex, Dex issues its own JWT, redirects to the loopback URL with an auth code.
6. Claude Code exchanges the code for the JWT, caches it, sends it on every subsequent request.
7. Bot validates the JWT against Dex's JWKS (cached locally), checks `iss`/`aud`/`exp`, applies the allowlists, stamps the verified `email` on audit logs.

Subsequent invocations of Claude Code reuse the cached token until expiry. Revoking a user means removing them from Workspace; their next token refresh fails at the Google sign-in step.

### Revoking access — what each lever does

| Action | Where | Time-to-effect |
|---|---|---|
| Suspend user in Google Workspace | Workspace Admin → Users → Suspend | Next Dex token refresh (typically ≤ 24h). Instant if you also revoke the user's Dex refresh tokens in Dex's storage. |
| Remove user from the allowed-emails ConfigMap | Edit `diagnostic-bot-oidc-allowlist` data key, commit | ConfigMap mount sync time (~60 s) + next request. No pod restart. This is the everyday "person left the team" workflow when `MCP_OIDC_ALLOWED_EMAILS_FILE` is in use. |
| Remove user from `MCP_OIDC_ALLOWED_EMAILS` | Bot env var, redeploy bot | Next request after pod restart. The static-env-var path, when not using `MCP_OIDC_ALLOWED_EMAILS_FILE`. |
| Pull `diagnostic-bot` from Dex `staticClients` | Edit Dex config, redeploy Dex | Next refresh from any user. Already-issued JWTs stay valid until their `exp`. |
| Revoke Dex's Google OAuth app | Google Cloud Console → Credentials → Delete | Every Dex user locked out instantly. Catastrophic-failure / incident-response lever; not for routine revocation. |
| Tighten `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` | Bot env var, redeploy bot | Next request after pod restart. |
| Kill the bot pod | Kubernetes | The whole HTTP MCP surface goes offline (Slack-bot path keeps working). |

The everyday "person left, lock them out" workflow is **suspend in Workspace Admin**. Everything else is incident-response material.

### Allowlist patterns

Either the static env-var form or the file-backed form expresses each pattern below — they're interchangeable from a policy standpoint. Use the file form (`…_FILE` variant + ConfigMap) for any list that changes more than once a quarter; use the static form for things that effectively never change (the hosted domain).

| Goal | Config |
|---|---|
| Anyone in the Workspace | `MCP_OIDC_ALLOWED_HOSTED_DOMAINS=katn-solutions.io` (one-line static is fine — domains don't churn) |
| A small, stable list of specific humans | `MCP_OIDC_ALLOWED_EMAILS=alice@katn-solutions.io,bob@katn-solutions.io` |
| A growing/churning list of specific humans | `MCP_OIDC_ALLOWED_EMAILS_FILE=/etc/diagnostic-bot/oidc/allowed-emails` backed by a ConfigMap — edit the CM in gitops, no pod restart |
| Anyone in the SRE Google Group, no one else | DWD setup + `MCP_OIDC_ALLOWED_GROUPS=sre@katn-solutions.io` |
| Anyone in Workspace, plus an explicit per-human allowlist on top | Set both `MCP_OIDC_ALLOWED_HOSTED_DOMAINS` (static) and `MCP_OIDC_ALLOWED_EMAILS_FILE` (file-backed) — domain as the broad gate, emails as the narrow one |

### Why this design is clean

1. **One source of truth for identity.** Google Workspace. The bot has no users, no passwords, no per-user key material. Add a person → add to Workspace; remove → suspend in Workspace.
2. **Bot stays simple.** It validates JWTs. Doesn't speak OAuth flow, doesn't redirect users, doesn't host login pages, doesn't store sessions. JWT-in-header, JWKS-from-Dex, done. Sub-millisecond per request after the JWKS is cached.
3. **Authn vs authz, separated.** Workspace decides who's authenticated. The bot's env vars decide who can use *this specific server*. You can run ten MCP servers all backed by the same Dex, each with its own allowlist.
4. **Dex handles what Google OAuth doesn't expose.** The public-client model and group claim emission (when DWD is wired) both live in Dex — see [Why this layer exists at all](#why-this-layer-exists-at-all). The bot just consumes whatever Dex issues; it doesn't care that there's a Workspace Admin SDK call happening on the back end.
5. **The Slack-bot path stays separate.** Slack-bot uses stdio, hits an in-process MCP server, never goes through the HTTP listener that's auth-gated. Investigations triggered via Slack are attributed via the Slack username (existing path). Investigations triggered via Claude Code over HTTP are attributed via the verified Google email. Two paths, two attribution mechanisms, no overlap.

The one trade-off: you have to run Dex. If you're already running it for other purposes (kubectl SSH connector, other internal services), this is free; if not, that's a non-trivial operational ask.

## Direct Google OAuth

Simpler setup, but every user has to put both Google's client ID *and* client secret in their `.mcp.json`. Use this only when you can't run Dex.

### One-time Google Cloud Console setup

#### OAuth consent screen

Same shape as the Dex path's consent screen — set this up first if you haven't:

| Field | Value | Notes |
|---|---|---|
| User Type | **Internal** | Restricts sign-in to your Workspace domain. |
| App name | `Diagnostic Bot` | Shown on the consent screen each user sees. |
| User support email | `<your support email>` | Workspace email. |
| Authorized domains | (none required) | Desktop-app clients use `http://localhost` redirects, no public domain to authorize. |
| Scopes | `openid`, `.../auth/userinfo.email`, `.../auth/userinfo.profile` | Non-sensitive. |

#### OAuth client

**APIs & Services → Credentials → Create Credentials → OAuth client ID**:

| Field | Value | Notes |
|---|---|---|
| Application type | **Desktop app** | Required: this is the type that supports `http://localhost:<port>/callback` loopback redirects, which Claude Code's callback listener uses. Web app type would reject the loopback URL. |
| Name | `diagnostic-bot-direct` | Internal label only. |

After saving, Google shows the **Client ID** and **Client secret**. Both go into every user's `claude mcp add` command below.

The "secret" can't be kept confidential in a distributed CLI — Google's own docs acknowledge this and PKCE provides the actual security. But you do need both pieces in users' config files, and you should treat the client ID + secret pair like an internal credential (i.e. don't paste it into a public Slack channel, don't ship it in a public GitHub repo).

### Bot env vars

Same two styles as the Dex path — pick per axis.

**Static env vars (simple, requires pod restart to edit):**

```
GOOGLE_OAUTH_CLIENT_ID=<your-id>.apps.googleusercontent.com
GOOGLE_ALLOWED_HOSTED_DOMAINS=katn-solutions.io
GOOGLE_ALLOWED_EMAILS=alice@katn-solutions.io,bob@katn-solutions.io   # optional
MCP_PUBLIC_URL=https://diagnostic-bot.example.com
```

**File-backed (hot-reload, recommended once the email list grows):**

```
GOOGLE_OAUTH_CLIENT_ID=<your-id>.apps.googleusercontent.com
GOOGLE_ALLOWED_HOSTED_DOMAINS=katn-solutions.io                            # static — domains rarely change
GOOGLE_ALLOWED_EMAILS_FILE=/etc/diagnostic-bot/oauth/allowed-emails        # hot-reloadable
MCP_PUBLIC_URL=https://diagnostic-bot.example.com
# (drop GOOGLE_ALLOWED_EMAILS — the file replaces it; if both are set, the file wins)
```

The file form needs a ConfigMap and a mount — same shape as the Dex path. New env vars:

| Var | Effect |
|---|---|
| `GOOGLE_ALLOWED_EMAILS_FILE` | Path to a file whose contents replace `GOOGLE_ALLOWED_EMAILS`. Edits to the file (typically a ConfigMap mount) propagate without a pod restart. |
| `GOOGLE_ALLOWED_HOSTED_DOMAINS_FILE` | Same pattern for the hosted-domain axis. |

Same semantics as the OIDC `_FILE` variants: stat-on-every-call + mtime cache + fail-closed on unreadable + empty-file = no restriction + file wins over static when both are set.

**Important architectural note specific to the Google path:** the bot caches Google's userinfo response per token for 5 minutes by default (`CacheTTL`), so on the Direct Google path Authenticate doesn't hit Google's userinfo endpoint on every request. The allowlist check, however, runs on **every** Authenticate against either the cached identity or a fresh one — never against a cached "yes, authorized" verdict. That's what makes the file-backed allowlists actually hot-reload: a user removed from the ConfigMap is denied on their next request, not after their cached userinfo lookup expires. Don't refactor this back to caching the authorized `*Result` — you'd silently break revocation latency.

Example ConfigMap + mount:

```yaml
# clusters/<env>/diagnostic-bot/configmap-google-allowlist.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: diagnostic-bot-google-allowlist
  namespace: diagnostic-bot
data:
  allowed-emails: |-
    alice@katn-solutions.io
    bob@katn-solutions.io
    carol@katn-solutions.io
```

```yaml
# clusters/<env>/diagnostic-bot/deployment.yaml — container env + mount
env:
  - name: GOOGLE_OAUTH_CLIENT_ID
    value: <your-id>.apps.googleusercontent.com
  - name: GOOGLE_ALLOWED_HOSTED_DOMAINS
    value: katn-solutions.io
  - name: GOOGLE_ALLOWED_EMAILS_FILE
    value: /etc/diagnostic-bot/oauth/allowed-emails
  - name: MCP_PUBLIC_URL
    value: https://diagnostic-bot.example.com

volumeMounts:
  - name: google-allowlist
    mountPath: /etc/diagnostic-bot/oauth
    readOnly: true

volumes:
  - name: google-allowlist
    configMap:
      name: diagnostic-bot-google-allowlist
```

The bot logs the active source on startup so the operator never has to wonder:

```
Google OAuth enabled for MCP HTTP/SSE
  client_id=<id>.apps.googleusercontent.com
  emails_from_file=true emails_file=/etc/diagnostic-bot/oauth/allowed-emails
  hosted_domains_from_file=false allowed_hosted_domains=[katn-solutions.io]
  ...
```

### Each user's `.mcp.json` / `claude mcp add`

```bash
claude mcp add diagnostic-bot https://diagnostic-bot.example.com/mcp \
  --transport http \
  --client-id <your-id>.apps.googleusercontent.com \
  --client-secret <google-issued-secret>
```

On first use, Claude Code pops a browser to Google, captures the auth code, exchanges it for an access token, and presents it to the bot. The bot validates each token by calling Google's `userinfo` endpoint (cached for 5 minutes by default), enforces the hosted-domain / email allowlists, stamps the verified email on audit logs.

### Compatibility note for non-Dex / non-Google OIDC providers

The OIDC validator hardcodes `<issuer>/keys` as the JWKS path — Dex's convention. Other IdPs (Okta, Auth0, some Keycloak configurations) serve JWKS under different paths. If you point this at a non-Dex IdP and JWKS discovery fails, generalize `refreshJWKSCache` in `pkg/mcp/auth/oidc.go` to fetch `<issuer>/.well-known/openid-configuration` and read `jwks_uri` from it. Not done by default to avoid widening scope.

## Security

The design goal is to let anyone run investigations safely. The agent is the guardrail: it can only do what its tools allow, and the tools are chosen so the worst case is recoverable.

- **Safe-by-construction toolset.** Everything is read-only except Grafana dashboard management. There is no shell, no filesystem, and no arbitrary-tool access in the Slack agent — only the wired `pkg/mcp` handlers exist, so the universe of possible actions *is* the curated toolset. Kubernetes access is read-only (`get`/`list`); database queries are restricted to `SELECT`/`SHOW`/`DESCRIBE`/`EXPLAIN`.
- **Grafana is the only write surface — deliberately.** Dashboard create/update/patch/delete/restore and folder creation are the sole mutations the agent can make. This is acceptable because Grafana keeps its own dashboard version history *and* is expected to be backed by a database with point-in-time recovery (e.g. CloudNativePG with WAL archiving to object storage). A bad or unwanted dashboard change is therefore reversible — an annoyance, not damage. No other resource the agent can reach is mutable.
- **Global read-only switch.** Set `READ_ONLY=true` to disable *all* writes, including Grafana. In read-only mode the write tools are withheld from the advertised toolset (both doors) and rejected at dispatch as defense in depth — so even a forged or replayed call cannot mutate anything.
- **Inbound defanging.** Tool output is untrusted data, never instructions. Forged conversational control sequences (`human(from …)` envelopes, `assistant:`/`system:` role markers, `<system-reminder>` tags, `[Request interrupted…]` markers) are neutralized before the model sees them, and each trip increments `injection_defangs_total` — a sustained signal there is an active probe, not noise.
- **Outbound secret scrubbing.** Every tool result and the final answer pass a secret/PII scrubber (API keys, tokens, passwords, JWTs, private keys, connection strings, emails, card numbers) before leaving the process.
- **Audit Trail.** All tool dispatches, K8s queries, and Claude API calls are logged as structured JSON, trace-correlated when a span is active.
- **GitOps Principles.** Never suggests direct cluster modifications.

## Deployment

The distribution ships in two forms, both with a Pod/ServiceMonitor:

- **Kustomize example** — [`kubernetes/`](kubernetes/). Renders with:
  ```bash
  kustomize build kubernetes/
  ```
- **Helm chart** — [`charts/diagnostic-bot/`](charts/diagnostic-bot/), also published as an OCI artifact to `oci://ghcr.io/nikogura/charts` on release.
  ```bash
  helm install diagnostic-bot charts/diagnostic-bot \
    --set existingSecret=diagnostic-bot-secrets \
    --set config.LOKI_ENDPOINT=http://loki-gateway.monitoring.svc:80
  # Disable all writes (including Grafana):
  #   --set config.extraEnv.READ_ONLY=true
  # Expose the MCP HTTP door for power users:
  #   --set mcpHttp.enabled=true
  ```

Both render in CI via `make test` (`kustomize build`, `helm lint`, `helm template`).

Deploy as a Kubernetes Deployment with:

- Single replica (in-memory state)
- Non-root container user (UID 1000)
- Resource requests **and** limits set (so the dashboard's % -of-request and % -of-limit panels are meaningful)
- ServiceAccount with minimal RBAC:
  - `get`, `list` on pods, pods/log, configmaps, services
  - `get`, `list` on deployments
  - `get`, `list` on Flux CRDs (gitrepositories, kustomizations)
  - `get`, `list` on Atlas CRDs (atlasmigrations)

### RBAC Requirements

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: diagnostic-bot
rules:
- apiGroups: [""]
  resources: ["pods", "pods/log", "configmaps", "services"]
  verbs: ["get", "list"]
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list"]
- apiGroups: ["source.toolkit.fluxcd.io"]
  resources: ["gitrepositories"]
  verbs: ["get", "list"]
- apiGroups: ["kustomize.toolkit.fluxcd.io"]
  resources: ["kustomizations"]
  verbs: ["get", "list"]
- apiGroups: ["db.atlasgo.io"]
  resources: ["atlasmigrations"]
  verbs: ["get", "list"]
```

## Observability

Instrumentation is OpenTelemetry end to end: metrics are exported in Prometheus format (OTel SDK → Prometheus exporter → `promhttp`), traces over OTLP, and logs are trace-correlated. The metrics + health server listens on its own admin port (`METRICS_PORT`, default `:9090`), always separate from the client-facing MCP HTTP listener.

### Metrics (golden signals + domain)

- `investigations_started_total{type}` - Traffic: investigations initiated by template type
- `investigations_resolved_total{type}` - Investigations completed
- `investigation_duration_seconds{type,status}` - Latency histogram (SLO-budget buckets, in seconds)
- `investigations_in_flight` - Saturation gauge: investigations currently executing
- `tool_executions_total{tool_name,status}` - Tool dispatch counts (errors by status)
- `injection_defangs_total{category}` - Forged control sequences defanged from untrusted output (active-probe signal)
- `claude_api_calls_total{status}` / `claude_api_tokens_total{token_type}` - Claude API calls and token usage
- `k8s_queries_total{namespace,resource_type}` / `loki_queries_total{status}` - Backend query audit
- `conversations_active` - Active Slack conversations (gauge)

### Tracing

OpenTelemetry spans cover the investigation path and each tool dispatch (`investigation`, `tool.<name>`), with W3C trace-context propagation. Export is via OTLP/HTTP, enabled by `OTEL_EXPORTER_OTLP_ENDPOINT`; **no-op when unset**.

### Logging

Structured JSON logs via `log/slog`, decorated with `trace_id`/`span_id` when a span is active so logs, metrics, and traces correlate in the backend.

### Grafana dashboard

A portable dashboard ships at [`dashboards/diagnostic-bot.json`](dashboards/diagnostic-bot.json) with `${prometheus}`/`${loki}`/`${tempo}` datasource template variables (no hardcoded UIDs — imports anywhere). It includes the golden-signal panels (traffic, errors, latency, saturation) and health gauges, a Loki **logs** panel, a Tempo **traces** panel, and Kubernetes resource panels: CPU and memory utilization as **% of limit** *and* **% of request**, plus CPU CFS throttling as **% of periods** (the leading indicator of CPU starvation).

The JSON file is the deliverable; import it through whatever dashboard provisioning you already run (Grafana provisioning files, Terraform/grizzly, the HTTP API, or a sidecar ConfigMap). `make dashboard-check` validates it stays well-formed JSON.

## Container Images

Docker images are automatically built and published to GitHub Container Registry:

```bash
# Pull latest image
docker pull ghcr.io/nikogura/diagnostic-bot:latest

# Pull specific version
docker pull ghcr.io/nikogura/diagnostic-bot:v1.0.0

# Run locally
docker run -e SLACK_BOT_TOKEN=xoxb-... \
           -e SLACK_APP_TOKEN=xapp-... \
           -e ANTHROPIC_API_KEY=sk-... \
           ghcr.io/nikogura/diagnostic-bot:latest
```

## CI/CD

The project uses GitHub Actions for continuous integration and deployment:

- **CI Pipeline** (`.github/workflows/ci.yaml`):
  - Runs on every push and pull request
  - Linting (golangci-lint + namedreturns)
  - Testing with race detection and coverage
  - Binary build
  - Multi-arch Docker image build (amd64, arm64)
  - Security scanning with Trivy
  - Publishes to `ghcr.io/nikogura/diagnostic-bot`

- **Release Pipeline** (`.github/workflows/release.yaml`):
  - Triggered on version tags (`v*`)
  - GoReleaser for multi-platform binaries
  - Docker images with semantic versioning
  - Automated changelog generation
  - GitHub Release creation

- **Dependabot** (`.github/dependabot.yml`):
  - Weekly dependency updates
  - Grouped updates for AWS SDK and Kubernetes

## License

Apache License 2.0

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests and linters: `make test && make lint`
5. Submit a pull request

## Maintainer

[Nik Ogura](https://github.com/nikogura)
