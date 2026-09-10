# Diagnostic Bot

Diagnostic Bot is a collection of curated investigative skills — expert investigation procedures, written once and packaged so anyone can run them. It investigates across logs, metrics, traces, databases, source repositories, and container scans, and returns a written analysis with an optional PDF report.

A skill is reached by describing the problem, not by picking one from a list. The matching skill runs and gathers the data it needs, so someone can get a thorough investigation without direct access to the underlying data sources or familiarity with the query tools. It answers questions such as "why is this service returning 403s", "what changed before this incident", or "explain what this Grafana dashboard is showing".

Access is read-only, with one exception: when asked, it can create or correct Grafana dashboards. Example skills are included; the intent is to write your own for your own infrastructure.

## How It Works

A request is matched to an **investigation skill** — a YAML template that defines the trigger patterns and the prompt. The agent gathers data with the tools that are configured, reasons over the results, and produces findings. Tool output is gathered and analyzed; it is never executed.

There are two ways to reach the same toolset:

- **Slack** — mention the bot with a question. It runs the investigation and replies in the thread, attaching any generated PDF. Threads support follow-up questions. To stop a chatty thread from attaching a report on every reply, say something like "disable reports" (also "stop making pdfs", "no more reports"); "resume reports" / "enable pdf" turns them back on. PDFs can also be disabled globally with `PDF_DISABLED`.
- **MCP server** — exposes the same tools to any MCP client (Claude Code, Claude Desktop, and other MCP-compatible clients) over Streamable HTTP.

## Tools

All tools are read-only except the Grafana dashboard-management tools. Each group is registered only when its backing service is configured.

| Group | Tools | Enabled by |
|-------|-------|------------|
| Logs (Loki) | `loki_query` | `LOKI_ENDPOINT` |
| Logs (CloudWatch) | `cloudwatch_logs_query`, `cloudwatch_logs_list_groups`, `cloudwatch_logs_get_events` | `CLOUDWATCH_ACCOUNTS` or `CLOUDWATCH_ASSUME_ROLE` |
| Metrics (CloudWatch) | `cloudwatch_metrics_list`, `cloudwatch_metrics_get_statistics`, `cloudwatch_metrics_query` | `CLOUDWATCH_ACCOUNTS` or `CLOUDWATCH_ASSUME_ROLE` |
| Alarms (CloudWatch) | `cloudwatch_alarms_list`, `cloudwatch_alarms_history` | `CLOUDWATCH_ACCOUNTS` or `CLOUDWATCH_ASSUME_ROLE` |
| Metrics (Prometheus) | `prometheus_query`, `prometheus_query_range`, `prometheus_series`, `prometheus_label_values`, `prometheus_list_endpoints` | `PROMETHEUS_URL` / `PROMETHEUS_<NAME>_URL` |
| Traces (Tempo) | `tempo_get_trace`, `tempo_search_traces`, `tempo_list_endpoints` | `TEMPO_URL` / `TEMPO_<NAME>_URL` |
| Grafana (read) | `grafana_list_dashboards`, `grafana_get_dashboard`, `grafana_get_dashboard_version` | `GRAFANA_URL` + `GRAFANA_API_KEY` |
| Grafana (write) | `grafana_create_dashboard`, `grafana_update_dashboard`, `grafana_patch_dashboard`, `grafana_restore_dashboard_version`, `grafana_delete_dashboard`, `grafana_create_folder` | `GRAFANA_URL` + `GRAFANA_API_KEY` (disabled by `READ_ONLY`) |
| Database | `database_query` (`SELECT`/`SHOW`/`DESCRIBE`/`EXPLAIN` only), `database_list` | `DATABASE_URL` / `DATABASE_<NAME>_URL` |
| Kubernetes (read-only) | `k8s_get_resource` (configmap/deployment/service/pod/Flux/Atlas CRDs — **never Secrets**), `k8s_pod_logs`, `k8s_list_pods`, `k8s_get_events` | in-cluster ServiceAccount, or `KUBECONFIG` |
| GitHub | `github_get_file`, `github_list_directory`, `github_search_code` | `GITHUB_TOKEN` |
| GitLab | `gitlab_get_file`, `gitlab_list_directory`, `gitlab_search_code` | `GITLAB_TOKEN` |
| AWS (read) | `sts_get_caller_identity`, `iam_list_roles`, `iam_get_role`, `ec2_describe_vpcs`, `ec2_describe_subnets`, `ec2_describe_security_groups`, `ec2_describe_nat_gateways`, `route53_list_hosted_zones`, `route53_list_records`, `s3_list_buckets`, `s3_get_bucket_policy` | `AWS_REGION` / `AWS_DEFAULT_REGION` |
| Container scans (ECR) | `ecr_scan_results` | `AWS_REGION` / `AWS_DEFAULT_REGION` |
| GraphQL | `graphql_query`, `graphql_list_endpoints` | `GRAPHQL_URL` / `GRAPHQL_<NAME>_URL` |
| Utility | `whois_lookup`, `generate_pdf`, `list_my_tools` | always available |
| Third-party APIs | generated per YAML config | `API_CONFIG_DIR` |

Database access is restricted to read-only statements. The Kubernetes tools are read-only (`get`/`list`) and target only the cluster the bot runs in (or the one named by `KUBECONFIG`); they cannot read Secrets — `secret` is excluded from the resource allowlist, the supplied RBAC withholds the `secrets` verb, and all output is secret-scrubbed. Setting `READ_ONLY=true` removes every write tool from the toolset and rejects any write call at dispatch.

## Investigation Skills

Skills are YAML files in `INVESTIGATION_DIR` (default `./investigations`). A message is routed to the first skill whose `trigger_patterns` match.

```yaml
name: "Investigation Name"
description: "What this investigation does"
trigger_patterns:
  - "pattern1"
  - "pattern2.*regex"
initial_prompt: |
  The prompt that defines the investigation: role, methodology, which tools to
  use, and the expected output format.
```

| Field | Purpose |
|-------|---------|
| `name` | Display name |
| `description` | Short summary |
| `trigger_patterns` | Regexes matched against the request to select the skill |
| `initial_prompt` | Investigation instructions and methodology |

A skill is identified by its file name — `waf-block.yaml` is the investigation `waf-block` — and that name is what the `get_investigation` tool takes. Two files that resolve to the same name are a startup error rather than a silent overwrite.

Four example skills ship in `investigations/`: `modsecurity-block`, `atlas-migration`, `ecr-vulnerability-scan`, and `general-diagnostic`. They contain substitution placeholders for adapting to a specific environment.

### Reference Documents

Plain Markdown files in the same directory are loaded as reference material. Drop `environment-mapping-table.md` in beside the skills and it is available as the document `environment-mapping-table` — no YAML wrapper, no `initial_prompt` block. The first `#` heading becomes its title and the first prose line its summary.

Documents carry no `trigger_patterns`, so they never start an investigation. They exist to be preloaded into MCP clients or fetched by name. This is for the knowledge an operator already keeps as a document: environment maps, account topology, naming conventions, the traps that catch newcomers.

## Slack Resilience

A fatal Slack error — a rejected handshake, a bad token, a sustained outage — does not end the process. The connection is retried with exponential backoff between `SLACK_RETRY_INITIAL` and `SLACK_RETRY_MAX`, the backoff resets once a connection has held for two minutes so a later outage starts fresh, and `slack_reconnects_total` counts the attempts.

Meanwhile the MCP tool surface and the metrics endpoint keep serving: neither depends on Slack. Readiness reports the socket as disconnected throughout, so the pod is honestly degraded rather than pretending. Exiting instead would drop two healthy listeners and land the pod in CrashLoopBackOff, which fixes nothing.

## Context Documents

Some knowledge applies to every question asked of this deployment: which AWS account holds staging, which cluster serves a realm, what the humans here call each environment. A playbook that says "query staging" is ambiguous without it, and guessing wrong is the failure that knowledge exists to prevent.

A **context document** is reference material rather than a procedure. It carries no `trigger_patterns`, so it is never matched or run — it is simply present, so that whatever is asked, the model already knows which account "staging" means. That is the opposite of an investigation, which is a procedure fetched only when it applies.

`CONTEXT_DOCUMENTS` names the documents to load. **Both front-ends receive them**, rendered from one place so they cannot drift:

- **MCP clients** get it in the server's `instructions`, which clients fold into the model's context during `initialize`. No slash command, no tool call, no user action — a locally started agent arrives already oriented instead of rediscovering the environment every session.
- **Slack investigations** get it prepended to the system prompt, ahead of the matched playbook, so the playbook's references resolve against real accounts and clusters.

A question asked in Slack and the same question asked through MCP are therefore answered against identical facts.

```bash
CONTEXT_DOCUMENTS=environment-mapping-table,account-topology
```

```yaml
# Accepts commas AND newlines, so a block scalar stays readable.
CONTEXT_DOCUMENTS: |
  environment-mapping-table
  account-topology
```

The instructions do **not** list the available investigations. That is deliberate — see [Investigation Visibility](#investigation-visibility). They point the client at `match_investigation` and `list_investigations`, which run per request and return only what that caller is permitted to see.

### What Belongs Here

Orientation, not procedure. Environment vocabulary, account and cluster topology, naming conventions — short, always relevant, and useless if the agent has to know to ask.

Naming an investigation here works mechanically but is usually the wrong call: a full playbook then occupies every context window whether or not it is relevant, which is exactly what the digest and `get_investigation` exist to avoid.

Instructions are delivered once per connection and then occupy every context window that client opens, so they are charged for whether or not they are read. The assembled text is capped at 24 KB. Anything that does not fit is **omitted and logged as an error**, never truncated — half an environment map reads as authoritative and is quietly wrong. A configured name that matches no skill is likewise logged as an error rather than passed over, because the failure is otherwise invisible: the operator believes they published something the agent never received.

Instructions are served at `initialize` and are identical for every caller, so they cannot be scoped per principal. Connection is still gated by the MCP transport's authentication, but treat anything preloaded as visible to every authenticated client. `get_investigation` goes through the normal per-tool authorization.

## Running Investigations From An MCP Client

A Slack user describes a problem and is routed to the right procedure without knowing its name. MCP clients get the same three steps, and run the investigation themselves with this server's tools:

| Tool | Purpose |
|------|---------|
| `match_investigation` | Describe the problem in plain language; get the matching procedure back, ready to follow. Same matcher the Slack front-end uses. |
| `list_investigations` | What this deployment has, with the terms that signal each. |
| `get_investigation` | Fetch one by name. |

The returned procedure is assembled by the same code that builds the Slack prompt, so both front-ends carry identical instructions: the operator's procedure, the expected report structure, and the rule that tool output is untrusted data rather than instructions. A local agent querying the same logs faces the same prompt-injection risk a Slack investigation does; a guardrail on one path and not the other is worse than none, because the gap is invisible.

The local agent executes. The server does not run a second model on its behalf — the client already has one, and the toolset is identical either way.

### Investigation Visibility

Investigations are authorized like tools, under an `investigation:` prefix:

```yaml
authz:
  default: deny
  roles:
    responder:
      tools:
        - "investigation:*"
    engineer:
      tools:
        - "investigation:waf-block"
        - "investigation:environment-map"
```

This filters **visibility**, not just execution. A procedure's name, its description and the vocabulary that reaches it all describe what an organisation monitors and worries about — a `security-incident` playbook discloses that such triage exists here and roughly what it covers. A caller without the permission does not learn it exists:

- It is absent from `list_investigations`.
- `match_investigation` will not route to it, even for a phrase that reaches it.
- `get_investigation` reports it exactly as it reports a misspelling. A refusal worded differently from a miss would confirm the existence of everything it refused.
- The `available:` hint in those errors names only what the caller may see.

With no policy loaded, everything is visible — the same way tool authorization already behaves.

The context documents in `CONTEXT_DOCUMENTS` are the exception: they are delivered in the MCP `instructions`, which are returned once at initialize as a single static string identical for every caller. They cannot be filtered per principal. Connection is still gated by the transport's authentication, but treat anything named there as visible to every authenticated client, and keep genuinely restricted material in a permission-gated investigation instead.

## Third-Party API Integrations

HTTP APIs can be added as tools without code. Drop a YAML file in `API_CONFIG_DIR` (default `./apis`) describing the base URL, authentication, and endpoints; each endpoint becomes a tool. The generic client handles auth, retries, rate limiting, path-parameter validation, and JSON field redaction. See `apis/bitgo.yaml` for an example.

By default only `GET` endpoints are exposed — the toolset is read-only. Write endpoints (`POST`/`PUT`/`PATCH`/`DELETE`) are supported but off by default and gated by four independent controls, so no single misstep enables them: the endpoint declares its `method` and its request-body fields as params with `in: body`; the deployment opts in by listing the verb in `API_ALLOWED_METHODS` (GET is always allowed); `READ_ONLY=true` withholds and rejects every write regardless; and the tool-authorization policy scopes each write tool (e.g. `jira_add_comment`) to specific roles — put writes `via: ["mcp"]` so they're only reachable over the authenticated transport, never broadcast to a Slack channel. A method that isn't permitted is withheld from the toolset and rejected at dispatch, and every write is audit-logged with the caller's identity.

In-cluster, provide the configs as a mounted ConfigMap, the same way as the authorization policy. **Helm:** set `apiConfigs.configs` (a map of filename → YAML document) or point `apiConfigs.existingConfigMap` at an existing one — the chart mounts it and sets `API_CONFIG_DIR` for you. **Kustomize:** uncomment the `API_CONFIG_DIR` env and the `api-configs` volume/mount in `kubernetes/deployment.yaml`, and create the ConfigMap from your configs (`kubectl create configmap diagnostic-bot-apis --from-file=apis/`). Each config's auth token is read from the environment by its `token_env` and comes from the Secret, never the ConfigMap.

## Configuration

All configuration is via environment variables.

### Required

| Variable | Purpose |
|----------|---------|
| `SLACK_BOT_TOKEN` | Slack bot OAuth token (`xoxb-…`) |
| `SLACK_APP_TOKEN` | Slack app-level token for Socket Mode (`xapp-…`) |
| `ANTHROPIC_API_KEY` | Claude API key (`sk-ant-…`) |

### Core

| Variable | Default | Purpose |
|----------|---------|---------|
| `CLAUDE_MODEL` | `claude-sonnet-4-5-20250929` | Model used for investigations |
| `READ_ONLY` | `false` | When `true`/`1`/`yes`/`on`, disables all write tools (Grafana) on both interfaces |
| `INVESTIGATION_DIR` | `./investigations` | Skill and reference-document directory (`.yaml`, `.yml`, `.md`, `.markdown`) |
| `CONTEXT_DOCUMENTS` | *(none)* | Reference material always present in both front-ends: inlined into MCP client context at connect, and prepended to every Slack investigation. Names are file stems. Accepts commas **and** newlines. See [Context Documents](#context-documents) |
| `COMPANY_NAME` | `Company` | Branding on PDF reports |
| `FILE_RETENTION` | `24h` | Generated-file cleanup interval |
| `SLACK_RETRY_INITIAL` | `1s` | First delay before reconnecting after a fatal Slack error |
| `SLACK_RETRY_MAX` | `5m` | Ceiling on the Slack reconnection backoff |
| `API_CONFIG_DIR` | `./apis` | Third-party API config directory |
| `API_ALLOWED_METHODS` | `GET` | HTTP methods third-party API tools may use. GET is always allowed; add write verbs (e.g. `POST,PATCH`) to enable write endpoints. Accepts commas **and** newlines. `READ_ONLY` still overrides. |
| `PDF_FONT` | `helvetica` | Report font: `helvetica`, `times`, or `courier` (code blocks are always monospace) |
| `PDF_DISABLED` | `false` | Globally disable PDF report generation (text-only responses) |

### MCP Server

| Variable | Default | Purpose |
|----------|---------|---------|
| `MCP_HTTP_ENABLED` | `false` | Serve the MCP HTTP (Streamable) endpoint |
| `MCP_HTTP_PORT` | `8090` | Port for the MCP HTTP server |
| `MCP_PUBLIC_URL` | — | Externally reachable base URL (required for Google OAuth) |
| `MCP_AUDIT_USER` | OS user | Identity stamped on Grafana writes |
| `MCP_MAX_TOOL_OUTPUT_BYTES` | `1000000` | Cap on a single tool result returned to any caller; oversized output is truncated with a notice |
| `MCP_AUTHZ_FILE` | — | Path to a YAML role-based tool-authorization policy (hot-reloaded). See [Tool Authorization](#tool-authorization-rbac). Unset = disabled (all tools available to all callers) |
| `MCP_AUTHZ` | — | Inline YAML policy, as an alternative to `MCP_AUTHZ_FILE` (the file wins if both are set) |

### Observability

| Variable | Default | Purpose |
|----------|---------|---------|
| `METRICS_PORT` | `9090` | Admin port for `/metrics` and health probes (always separate from the MCP port) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | OTLP endpoint for traces; tracing is a no-op when unset |

### Backing Services

| Variable | Enables |
|----------|---------|
| `LOKI_ENDPOINT`, `LOKI_DEFAULT_ORG_ID`, `LOKI_ORG_IDS` | Loki log queries |
| `CLOUDWATCH_ACCOUNTS` or `CLOUDWATCH_ASSUME_ROLE`, `CLOUDWATCH_EXTERNAL_ID` | CloudWatch Logs, Metrics, and Alarms |
| `PROMETHEUS_URL` / `PROMETHEUS_<NAME>_URL` | Prometheus queries |
| `TEMPO_URL` / `TEMPO_<NAME>_URL` | Tempo trace lookups |
| `GRAFANA_URL`, `GRAFANA_API_KEY` | Grafana dashboard tools |
| `DATABASE_URL` / `DATABASE_<NAME>_URL` | Read-only SQL |
| in-cluster ServiceAccount or `KUBECONFIG` (+ optional `K8S_ENABLED`=`false` to disable, `K8S_CLUSTER_NAME` to name it) | Read-only Kubernetes tools for the bot's own cluster |
| `GITHUB_TOKEN` | GitHub file/search tools |
| `GITLAB_TOKEN`, `GITLAB_URL` | GitLab file/search tools |
| `AWS_REGION` / `AWS_DEFAULT_REGION` + AWS credentials | ECR scans and AWS read tools |
| `GRAPHQL_URL` (+ `GRAPHQL_TOKEN` or `GRAPHQL_CLIENT_ID`/`GRAPHQL_CLIENT_SECRET`/`GRAPHQL_AUTH_URL`/`GRAPHQL_AUDIENCE`) | GraphQL queries |

List-valued variables (e.g. `LOKI_ORG_IDS`) accept commas **and** newlines, so a YAML block scalar stays readable:

```yaml
- name: LOKI_ORG_IDS
  value: |-
    monitoring
    cloudtrail
    self-monitoring
```

### AWS IAM Permissions

All AWS-backed tools are read-only. Grant only the actions for the families you
enable, on the target account's role (assumed via `CLOUDWATCH_ASSUME_ROLE` /
`CLOUDWATCH_ACCOUNTS` / `AWS_ASSUME_ROLE`, or directly on the pod's IRSA role).
Scope `Resource` as tightly as your environment allows:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "DiagnosticBotCloudWatchReadOnly",
      "Effect": "Allow",
      "Action": [
        "logs:StartQuery",
        "logs:GetQueryResults",
        "logs:DescribeLogGroups",
        "logs:GetLogEvents",
        "cloudwatch:ListMetrics",
        "cloudwatch:GetMetricStatistics",
        "cloudwatch:GetMetricData",
        "cloudwatch:DescribeAlarms",
        "cloudwatch:DescribeAlarmHistory"
      ],
      "Resource": "*"
    },
    {
      "Sid": "DiagnosticBotAWSReadOnly",
      "Effect": "Allow",
      "Action": [
        "sts:GetCallerIdentity",
        "iam:ListRoles",
        "iam:GetRole",
        "ec2:DescribeVpcs",
        "ec2:DescribeSubnets",
        "ec2:DescribeSecurityGroups",
        "ec2:DescribeNatGateways",
        "route53:ListHostedZones",
        "route53:ListResourceRecordSets",
        "s3:ListAllMyBuckets",
        "s3:GetBucketPolicy",
        "ecr:DescribeRepositories",
        "ecr:DescribeImages",
        "ecr:DescribeImageScanFindings"
      ],
      "Resource": "*"
    }
  ]
}
```

The CloudWatch statement backs the Logs/Metrics/Alarms tools; the second backs
the AWS read tools (`sts_*`, `iam_*`, `ec2_describe_*`, `route53_*`, `s3_*`) and
ECR scans. The ECR and AWS read tools are gated on `AWS_REGION` /
`AWS_DEFAULT_REGION` being set, so set one of those in the pod even when
credentials come from IRSA.

In EKS, attach the role to the bot's ServiceAccount with an
`eks.amazonaws.com/role-arn` annotation (IRSA). See the Kustomize
`kubernetes/serviceaccount.yaml` and the chart's `serviceAccount.annotations`.

## MCP Server Authentication

The MCP HTTP endpoint is unauthenticated by default (intended for VPC-internal use). One or more of the following methods can be enabled. OIDC and Google OAuth are mutually exclusive — setting both is a startup error.

| Method | Key variables |
|--------|---------------|
| Static bearer token | `MCP_AUTH_TOKEN` |
| JWT | `MCP_JWT_SECRET`, `MCP_JWT_ALGORITHM` (`HS256`/`RS256`) |
| API keys | `MCP_API_KEYS` (`key1:user1,key2:user2`) |
| OIDC / JWKS | `MCP_OIDC_ISSUER`, `MCP_OIDC_AUDIENCE`, `MCP_OIDC_ALLOWED_GROUPS`, `MCP_OIDC_ALLOWED_HOSTED_DOMAINS`, `MCP_OIDC_ALLOWED_EMAILS` (+ `_FILE` variants), `MCP_OIDC_JWKS_CACHE_SECONDS` |
| Google OAuth | `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_ALLOWED_HOSTED_DOMAINS`, `GOOGLE_ALLOWED_EMAILS` (+ `_FILE` variants), `MCP_PUBLIC_URL` |
| Mutual TLS | `MCP_MTLS_CA_CERT_PATH`, `MCP_MTLS_VERIFY_CLIENT` |

For OIDC and Google, the allowlist variables restrict access by group, hosted domain, and email. The `_FILE` variants read the allowlist from a file (typically a mounted ConfigMap), re-read per request with an mtime cache, so edits take effect without a restart.

## Tool Authorization (RBAC)

Authentication answers *can you use the bot at all*. Authorization answers *which tools you may invoke*. With a policy configured (`MCP_AUTHZ_FILE` or `MCP_AUTHZ`), each tool call is checked against the requester's role before it runs; with neither set, authorization is disabled and every caller gets the full tool surface (backward compatible).

This is application-layer filtering: the bot's own credentials are unchanged — the policy only decides which of the bot's tools a given requester may call. Enforcement is server-side, at the single dispatch boundary each front-end shares, not advisory to the model. A configured-but-unparseable policy **fails closed** (deny all) so a broken file never silently disables authorization.

When a request is denied, the requester is told — on Slack and on the MCP client alike — that they're not allowed and **why** (wrong interface, no granting role, or unresolved identity), along with **what they can** run. Denials deliberately never name roles or groups, so the message can't be used to enumerate the policy.

Capability reporting is per-caller and per-interface, and matches dispatch exactly. The `list_my_tools` tool (always available) is the authoritative "what can I do?" answer — it lists only the tools that would actually dispatch for *this* caller on *this* front-end, and labels any tools that are available only on the other interface ("available via mcp, not here"). The same allowed-set also filters the model's callable catalog and the system-prompt tool descriptions on Slack, and the advertised `tools/list` on MCP — so the bot never describes, offers, or advertises a tool the caller can't use. `list_my_tools`, the catalog filter, and the denial message all share one decision core, so they cannot disagree with enforcement.

Each front-end is matched by its **hardened identifier**: the MCP HTTP path matches by the **email** (and `groups`) from the verified OIDC/Google token; the Slack path matches by the user's **immutable Slack user ID** (`U…`), never the user-editable profile email. A `users[]` entry lists both so the same person is recognized on both front-ends. Roles are **additive** — a requester holds the union of every role bound to their identifiers and groups — and anything no held role grants falls to the configured `default` (`deny` or `allow`).

```yaml
authz:
  default: deny                 # deny | allow — outcome when no role grants the tool
  roles:
    read-only:
      tools: ["k8s_*", "loki_*", "prometheus_*", "cloudwatch_*"]   # prefix globs or exact names
      # via omitted → applies on both Slack and MCP
    platform:
      tools: ["k8s_*", "ec2_describe_*", "cloudwatch_*"]
    grafana-read:
      tools: ["grafana_get_*", "grafana_list_*"]
    grafana-write:                # dashboard/folder CRUD
      tools: ["grafana_*"]
      via: ["mcp"]                # mutations only over authenticated MCP, never broadcast to a Slack channel
    security:
      tools: ["ecr_*", "iam_*", "sts_*"]
      via: ["mcp"]
  users:
    - name: "Alice"                                         # for readability only; not used in matching
      emails: ["alice@corp.com", "alice@contractor.com"]    # MCP identity (verified token)
      slack_ids: ["U01ALICE"]                               # Slack identity (immutable user ID)
      roles: ["security", "platform"]                       # additive: union of both roles' grants
    - name: "Bob"
      emails: ["bob@corp.com"]
      slack_ids: ["U02BOB"]
      roles: ["read-only"]
  groups:                         # optional: OIDC/Google group → roles.
                                  # MCP path only — Slack principals carry no
                                  # groups, so use slack_ids for Slack access.
    sre-team: ["platform"]
    sec-team: ["security"]
```

Tool patterns are simple globs: `*` (any tool), `prefix_*` (prefix match), or an exact tool name. A role's `via` restricts where its grants apply (`slack`, `mcp`); omit it to apply everywhere. The policy file is re-read on change (mtime-cached), so edits take effect without a restart; a parse error on reload keeps the last good policy.

**Channel egress is the requester's responsibility, by design.** Authorization is on *who asks*, not *who can see the Slack thread* — if a privileged user invokes a permitted tool in an open channel, the result lands in that channel. Scope sensitive tools to `via: ["mcp"]` (a private, authenticated 1:1 transport) when broadcasting their output would be unacceptable.

**Trust assumptions (Slack vs MCP).** The two front-ends do not have equal identity assurance. The MCP path verifies a cryptographically signed, audience-bound OIDC/Google token. The Slack path trusts that Slack vouches for the user behind a Slack user ID. Binding Slack to the **immutable user ID** (not the editable email) means a user cannot escalate by changing their email — but it still rests on your workspace's account integrity (ideally SSO/SCIM-provisioned). Because Slack assurance is inherently weaker, **scope your most sensitive roles to `via: ["mcp"]`** so they can only be exercised over the authenticated MCP transport. Slack principals also carry no `groups`, so group-to-role mappings never apply on the Slack path.

An example policy ships at [`examples/authz.yaml`](examples/authz.yaml). Every decision is recorded on the `authz_decisions_total` metric (`decision`, `source`) and surfaced on the dashboard; denials are logged.

## Observability

Instrumentation is OpenTelemetry: metrics are exported in Prometheus format, traces over OTLP, and logs are JSON with trace correlation. The metrics and health server listens on `METRICS_PORT` (default `:9090`), separate from the MCP HTTP port.

Endpoints: `/metrics`, `/healthz` (liveness), `/readyz` (readiness), `/health` (detailed).

Metrics:

| Metric | Type | Labels |
|--------|------|--------|
| `investigations_started_total` | counter | `type` |
| `investigations_resolved_total` | counter | `type` |
| `investigation_duration_seconds` | histogram | `type`, `status` |
| `investigations_in_flight` | gauge | — |
| `tool_executions_total` | counter | `tool_name`, `status` |
| `injection_defangs_total` | counter | `category` |
| `claude_api_calls_total` | counter | `status` |
| `claude_api_tokens_total` | counter | `token_type` |
| `k8s_queries_total` | counter | `namespace`, `resource_type` |
| `loki_queries_total` | counter | `status` |
| `conversations_active` | gauge | — |
| `authz_decisions_total` | counter | `decision`, `source` |
| `panics_recovered_total` | counter | `site` |

Traces cover the investigation path and each tool call (`investigation`, `tool.<name>`), with W3C trace-context propagation, exported via OTLP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Logs are emitted as JSON and carry `trace_id`/`span_id` when a span is active.

A Grafana dashboard is provided at [`dashboards/diagnostic-bot.json`](dashboards/diagnostic-bot.json). It uses `${prometheus}`/`${loki}`/`${tempo}` datasource variables (no hardcoded UIDs) and includes traffic, error, latency, and saturation panels, an authorization-decisions panel, a logs panel, a traces panel, and Kubernetes resource panels (CPU and memory as a percentage of both request and limit, plus CPU throttling). Import it with whatever dashboard provisioning you use. `make dashboard-check` validates it is well-formed JSON.

## Deployment

Run as a single-replica Deployment (state is held in memory) under a non-root user. Both distribution forms ship a PodMonitor, a ServiceMonitor, and — for the in-cluster Kubernetes tools — a **read-only** ClusterRole (`get`/`list`/`watch` on diagnostic resources, **no `secrets`, no write verbs**) bound to the ServiceAccount. The RBAC is gated by `rbac.create` in the chart; remove `clusterrole.yaml`/`clusterrolebinding.yaml` from the Kustomize example if the bot has no Kubernetes tools.

- **Kustomize** — [`kubernetes/`](kubernetes/): `kustomize build kubernetes/`
- **Helm** — [`charts/diagnostic-bot/`](charts/diagnostic-bot/), also published to `oci://ghcr.io/nikogura/charts` on release:

  ```bash
  helm install diagnostic-bot charts/diagnostic-bot \
    --set existingSecret=diagnostic-bot-secrets \
    --set config.LOKI_ENDPOINT=http://loki-gateway.monitoring.svc:80
  ```

  Set `--set mcpHttp.enabled=true` to serve the MCP HTTP endpoint, and `--set config.extraEnv.READ_ONLY=true` to disable writes.

## Building

```bash
make build    # compile cmd/bot into bin/
make lint     # namedreturns + golangci-lint
make test     # unit tests (race + coverage) and Kustomize/Helm renders
```

The binary is a pure-Go static build with no external-binary dependencies — whois and PDF rendering are in-process — so the image is distroless (`gcr.io/distroless/static-debian12`) and multi-architecture (amd64, arm64):

```bash
docker pull ghcr.io/nikogura/diagnostic-bot:latest
```

## CI/CD

GitHub Actions (`.github/workflows/ci.yml`) runs on every push and pull request: it installs the lint and render tooling (namedreturns, golangci-lint, kustomize, helm), then runs `make lint` and `make test`. On a push to `main`, the publish job builds and pushes the multi-arch image, packages and pushes the Helm chart to GHCR, tags the repository, and creates a GitHub Release.

## Project Structure

```
cmd/
  bot/                 # Slack bot + optional MCP HTTP server + metrics server
pkg/
  bot/                 # Slack handling and the investigation agent loop
  claude/              # Anthropic API client and prompt construction
  investigations/      # skill loading and request matching
  k8s/                 # Loki and whois clients, input/output sanitizers
  mcp/                 # tool definitions, handlers, transports, auth
  metrics/             # OpenTelemetry instruments
  observability/       # OTel metrics/tracing/log-correlation setup
  apiconfig/           # third-party API tool generation
dashboards/            # Grafana dashboard JSON
kubernetes/            # Kustomize deployment
charts/diagnostic-bot/ # Helm chart
investigations/        # example skills
apis/                  # example third-party API configs
```

## License

See [LICENSE](LICENSE).

## Contributing

All code must pass `make lint` (namedreturns + golangci-lint) and `make test`. New features require tests.

## Maintainer

Nik Ogura
