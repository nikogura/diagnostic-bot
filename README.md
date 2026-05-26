# Diagnostic Bot

A diagnostic automation platform that integrates Claude Code with Kubernetes, Loki, and observability tools. Provides autonomous investigation capabilities through YAML-based templates and custom MCP tools, delivering professional PDF reports. Includes a Slack bot for conversational access and an MCP server supporting both Streamable HTTP and SSE transports for use with Claude Code, Claude Desktop, OpenAI, Devin, and other MCP-compatible clients.

## Features

- **Claude Code Integration**: Uses Claude Code CLI with MCP server for advanced tool use capabilities
- **MCP Server**: Custom tools for Loki queries, K8s access, GitHub integration, ECR scanning, and PDF generation
- **Third-Party API Integration**: Configuration-driven system for adding read-only API integrations via YAML — no Go code required
- **Investigation Skills**: YAML-based templates define structured investigation workflows
- **Kubernetes Access**: Read-only access to pods, logs, ConfigMaps, Deployments, and Flux CRDs
- **PDF Report Generation**: Automated professional reports with company branding via Pandoc + LaTeX
- **Log Sanitization**: Comprehensive PII and secret redaction (13 regex patterns)
- **Slack Socket Mode**: Supports app mentions, threads, and conversational follow-ups
- **Conversation State**: In-memory tracking with 24-hour expiry and automatic file cleanup
- **Prometheus Metrics**: Full observability with counters for investigations, API calls, K8s queries, and tool usage

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
│   │   ├── claudecode.go            # Claude Code CLI integration
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
│   │   └── sanitizer.go             # Log sanitization
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
│       ├── metrics.go               # Metric definitions
│       └── server.go                # HTTP metrics server
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

**MCP Server Authentication** (multiple methods supported, configure one or more):
- `MCP_AUTH_TOKEN` - Static bearer token for simple authentication (default: empty = no auth)
- `MCP_JWT_SECRET` - JWT signing secret for JWT bearer token authentication
- `MCP_JWT_ALGORITHM` - JWT algorithm (default: `HS256`, also supports `RS256`)
- `MCP_API_KEYS` - API key authentication in format `key1:user1,key2:user2`
- `MCP_OIDC_ISSUER_URL` - OIDC issuer URL for token validation (e.g., Dex endpoint)
- `MCP_OIDC_AUDIENCE` - Expected OIDC audience claim
- `MCP_OIDC_ALLOWED_GROUPS` - Comma-separated list of authorized groups
- `MCP_OIDC_SKIP_ISSUER_VERIFY` - Skip issuer verification (default: `false`, use only for testing)
- `MCP_MTLS_CA_CERT_PATH` - Path to CA certificate for mutual TLS authentication
- `MCP_MTLS_VERIFY_CLIENT` - Verify client certificates against CA (default: `true`)

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

## Security

- **Read-Only Access**: Bot has no write permissions to Kubernetes
- **Log Sanitization**: All logs are sanitized before sending to Claude API
  - API keys, tokens, passwords redacted
  - JWT tokens redacted
  - Email addresses redacted
  - Credit card numbers redacted
  - Private keys redacted
- **Audit Trail**: All K8s queries and Claude API calls are logged
- **GitOps Principles**: Never suggests direct cluster modifications

## Deployment

Deploy as a Kubernetes Deployment with:

- Single replica (in-memory state)
- Non-root container user (UID 1000)
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

The bot exposes Prometheus metrics on port `:9090` for scraping:

### Metrics

- `investigations_started_total{type}` - Total investigations initiated by template type
- `investigations_resolved_total{type}` - Total investigations completed
- `claude_api_calls_total{status}` - Claude API call counts (success/error)
- `claude_api_tokens_total{type="input|output"}` - Token usage tracking
- `k8s_queries_total{namespace,resource_type}` - Kubernetes query audit trail
- `loki_queries_total{status}` - Loki query tracking
- `tool_executions_total{tool_name,status}` - MCP tool usage statistics
- `conversations_active` - Current number of active conversations (gauge)

### Logging

Structured JSON logging via `log/slog` with:
- Severity levels (Debug, Info, Warn, Error)
- Context-aware logging (conversation IDs, user IDs)
- Automatic correlation for investigations

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
