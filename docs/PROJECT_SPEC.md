# Build Slack Diagnostic Bot with Claude Integration

Create a Go-based Slack bot that enables self-service diagnostics for Kubernetes issues by integrating with Claude API. The bot should capture investigation workflows similar to interactive claude-code sessions and make them available to the team via Slack.

## Project Structure
```
diagnostic-bot/
├── cmd/
│   └── bot/
│       └── main.go
├── internal/
│   ├── bot/
│   │   ├── bot.go              # Core bot logic
│   │   ├── handlers.go         # Slack event handlers
│   │   └── state.go            # Conversation state management
│   ├── claude/
│   │   ├── client.go           # Claude API client wrapper
│   │   └── prompts.go          # Prompt construction
│   ├── k8s/
│   │   ├── agent.go            # K8s log/resource fetcher
│   │   └── sanitizer.go        # Log sanitization for PII/secrets
│   └── investigations/
│       ├── template.go         # Investigation template types
│       └── matcher.go          # Message → template matching
├── investigations/
│   ├── modsecurity-block.yaml
│   ├── atlas-migration-failure.yaml
│   └── pod-crashloop.yaml
├── docs/
│   └── CLAUDE.md               # Your engineering standards
├── go.mod
├── go.sum
├── Dockerfile
└── README.md
```

## Core Requirements

### 1. Slack Bot Setup

- Use Slack Socket Mode (simpler than webhooks for internal bots)
- Listen for app mentions and direct messages
- Respond in threads to maintain conversation context
- Support these commands:
  - `@bot help` - List available investigation types
  - `@bot <problem description>` - Start investigation
  - Follow-up messages in thread continue the conversation

### 2. Investigation Templates

Create YAML-based investigation templates:
```yaml
name: "ModSecurity Block Diagnosis"
description: "Diagnoses WAF blocks and provides remediation"
trigger_patterns:
  - "modsec"
  - "modsecurity"
  - "waf blocked"
  - "403.*waf"

initial_prompt: |
  You are diagnosing a ModSecurity WAF block reported by a user.
  
  Your task:
  1. Analyze the modsec audit logs to identify the blocked request
  2. Determine which rule was triggered and why
  3. Assess if this is a legitimate block or false positive
  4. Provide specific remediation (rule tuning, request fix, or whitelist)
  
  Be direct and technical. Provide exact rule IDs and configuration snippets.

kubernetes_resources:
  - type: "logs"
    namespace: "ingress-nginx"
    selector: "app.kubernetes.io/name=ingress-nginx"
    container: "controller"
    since: "1h"
    grep: "ModSecurity"
  
  - type: "configmap"
    namespace: "ingress-nginx"
    name: "modsecurity-rules"


```

### 3. Claude API Integration

- Use `claude-sonnet-4-5-20250929` model
- Maintain conversation history per Slack thread
- System prompt should include:
  - Investigation template's initial prompt
  - CLAUDE.md engineering standards
  - Relevant context documents from RAG (if available)
- Handle multi-turn conversations for follow-ups
- Implement token limit awareness (don't overflow context)

### 4. Kubernetes Access

**Security requirements:**
- Read-only access only (use ServiceAccount with minimal RBAC)
- Never write to cluster
- Sanitize logs before sending to Claude API:
  - Remove tokens, API keys, passwords (regex-based)
  - Redact IP addresses if sensitive
  - Strip customer PII
- Log all k8s queries for audit trail

**Capabilities needed:**
- Fetch pod logs with filtering (namespace, label selector, time range, grep)
- Get ConfigMaps, Deployments, Services (basic YAML dump)
- List pods in namespace
- Get pod status and events

### 5. Conversation State Management
```go
type Conversation struct {
    ID              string
    InvestigationType string
    SlackThreadTS   string
    UserID          string
    StartedAt       time.Time
    MessageHistory  []anthropic.Message
    KubernetesContext map[string]interface{}  // Cached logs, resources
    State           ConversationState  // Active, Resolved, Abandoned
}
```

Store in-memory initially (can migrate to Redis later). Auto-expire conversations after 24 hours of inactivity.

### 6. Response Formatting

- Use Slack's Block Kit for rich formatting
- Code blocks for configs/logs
- Collapsible sections for long outputs
- Emoji indicators: 🔍 (investigating), ✅ (resolved), ⚠️ (needs attention), ❌ (error)
- Add reaction emojis to original message to show bot is working

## Implementation Details

### Template Matching

Use fuzzy keyword matching to select investigation template:
1. Check for exact trigger pattern matches in user message
2. If multiple matches, ask user to clarify
3. If no matches, respond with "Unknown issue type" and list available investigations

### Prompt Construction
```go
func buildSystemPrompt(template InvestigationTemplate, claudeMD string, docs []string) string {
    return fmt.Sprintf(`%s

# Engineering Standards
%s

# Reference Documentation
%s

# Available Tools
You can request additional information by asking me to:
- Fetch logs from specific pods/namespaces
- Get Kubernetes resource configurations
- Search for related error patterns

Be concise but thorough. Provide actionable recommendations.`,
        template.InitialPrompt,
        claudeMD,
        strings.Join(docs, "\n\n"),
    )
}
```

### Log Fetching
```go
type LogRequest struct {
    Namespace     string
    LabelSelector string
    Container     string
    Since         time.Duration
    TailLines     int
    Grep          string  // Optional filter
}

func (a *K8sAgent) FetchLogs(req LogRequest) (string, error) {
    // Get matching pods
    // Stream logs from each
    // Apply grep filter if specified
    // Sanitize output
    // Truncate if too large (max ~50KB for Claude context)
    // Return aggregated logs
}
```

### Follow-up Detection

When a message arrives in an existing thread:
1. Load conversation state from memory
2. Detect if user is asking for more logs/info
3. If so, fetch new data and append to conversation
4. Otherwise, just continue conversation with Claude

## Configuration

Use environment variables:
- `SLACK_BOT_TOKEN` - Bot OAuth token
- `SLACK_APP_TOKEN` - App-level token (for Socket Mode)
- `ANTHROPIC_API_KEY` - Claude API key
- `KUBECONFIG` - Path to kubeconfig (or use in-cluster config)
- `INVESTIGATION_DIR` - Path to investigation skills and reference documents (default: ./investigations). Loads `.yaml`, `.yml`, `.md` and `.markdown`; Markdown files are reference documents with no trigger patterns.
- Investigations are authorized under an `investigation:<name>` prefix in the authz policy. This filters visibility, not just execution: a caller without the permission does not see the investigation listed, cannot be routed to it, and receives an identical error for a denied and a nonexistent name.
- `CONTEXT_DOCUMENTS` - Comma- or newline-separated names of reference documents (not procedures) carrying environment knowledge. Delivered to BOTH front-ends: inlined into MCP `instructions` at connect, and prepended to every Slack investigation prompt. Names are file stems.
- `SLACK_RETRY_INITIAL` - First delay before reconnecting after a fatal Slack error (default: 1s)
- `SLACK_RETRY_MAX` - Ceiling on the Slack reconnection backoff (default: 5m)
- `CLAUDE_MD_PATH` - Path to CLAUDE.md (default: ./docs/CLAUDE.md)

## Error Handling

- If k8s query fails: Tell user in Slack, don't fail silently
- If Claude API fails: Retry once, then inform user
- If template not found: List available investigation types
- Rate limiting: Track per-user requests, limit to 10/hour initially

## Logging & Observability

- Log all Slack interactions (user, message, investigation type)
- Log all k8s queries (namespace, resource type, timestamp)
- Log Claude API calls (tokens used, latency)
- Emit Prometheus metrics:
  - investigations_started_total{type}
  - investigations_resolved_total{type}
  - claude_api_calls_total{status}
  - k8s_queries_total{namespace,type}

## Testing Strategy

1. Unit tests for template matching
2. Unit tests for log sanitization (ensure secrets removed)
3. Integration test with mock Slack/Claude/K8s
4. Manual testing in dev Slack workspace before production

## Deployment

- Build Docker image (multi-stage build)
- Deploy as Deployment in k8s
- Single replica (state is in-memory)
- ReadOnly filesystem
- ServiceAccount with RBAC:
```yaml
  rules:
  - apiGroups: [""]
    resources: ["pods", "pods/log", "configmaps", "services"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list"]
```

## Initial Investigation Templates to Create

1. **modsecurity-block.yaml** - WAF diagnosis
2. **atlas-migration-failure.yaml** - Database migration issues
3. **pod-crashloop.yaml** - Generic pod crash investigation

Each should follow the same structure with appropriate k8s resources and prompts.

## Success Criteria

- User can report issue in Slack and get diagnosis within 30 seconds
- Bot correctly identifies investigation type 90%+ of the time
- Follow-up questions work and maintain context
- No secrets/PII leaked to Claude API
- All k8s access is read-only and logged

## Development Notes

- Start with the Slack bot scaffolding and basic message handling
- Add template system next
- Integrate k8s access
- Add Claude API last (can stub initially)
- Test each component independently before integration

## Future Enhancements (Not MVP)

- RAG integration with your Elasticsearch codebase index
- Automatic golangci-lint validation on any code suggestions
- Investigation analytics (which types most common, resolution rates)
- Multi-cluster support
- Persistent state storage (PostgreSQL/Redis)
- Approval workflow for production log access

---

Build this incrementally. Start with basic Slack message handling and template matching, then add k8s and Claude integration. Focus on making the modsecurity investigation work end-to-end before adding more investigation types.

The goal: democratize your diagnostic expertise so the team can self-serve without needing you in the loop, while maintaining your security standards and read-only access principles.
￼
￼
￼
￼Retry
To run code, enable code execution and file creation in Settings > Capabilities.

