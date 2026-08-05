# Bug: CloudWatch Metrics & Alarms tools are unreachable over MCP

**Component:** `pkg/mcp/sdk_server.go` — SDK/HTTP MCP transport
**Introduced in:** v2.3.0 (`9bfca83` — "feat(mcp): add CloudWatch Metrics and Alarms tools")
**Severity:** High — the v2.3.0 feature is non-functional on its primary interface
**Status:** Open

---

## Summary

The five CloudWatch Metrics/Alarms tools added in v2.3.0
(`cloudwatch_metrics_list`, `cloudwatch_metrics_query`,
`cloudwatch_metrics_get_statistics`, `cloudwatch_alarms_list`,
`cloudwatch_alarms_history`) are registered on **every code path except the SDK
MCP transport**. As a result they are **advertised and dispatchable over Slack
and reported by `list_my_tools`, but no MCP client can discover, obtain the
schema for, or call them.** Since the MCP server is the bot's primary interface
(external Claude Code sessions), the feature is effectively dead for its intended
users.

---

## Impact

- MCP clients (Claude Code, IDE integrations, any `tools/list` consumer) never
  see the five tools and cannot call them.
- The Slack interface *can* use them, so the feature appears to work when tested
  from Slack — masking the defect.
- `list_my_tools` lists all five (it reads the legacy path), which actively
  misleads: it reports tools as usable that the MCP transport will not serve.
- All CloudWatch Metrics/Alarms IAM and env wiring (`CLOUDWATCH_ACCOUNTS`,
  cross-account reader roles) is correct but unreachable over MCP until fixed.

---

## How it was observed

1. A reconnected MCP session (Claude Code) called `list_my_tools` → all five new
   tools listed as usable.
2. The same session could **not** obtain a schema for any of them via
   `tools/list`, so it could not call `cloudwatch_metrics_list` etc.
3. `cloudwatch_logs_*` (older tools) work fine from the same session — proving
   the connection, auth, and CloudWatch config are healthy; only the new tools
   are missing from the advertised set.

---

## Root cause

`registerCloudWatchTools()` in `pkg/mcp/sdk_server.go` (line 286) — the function
that registers CloudWatch tools with the SDK MCP server — was **not updated** in
v2.3.0. It still registers only the three **logs** tools:

```go
// pkg/mcp/sdk_server.go:286
func (s *SDKServer) registerCloudWatchTools() {
	if s.legacy.cloudWatchClientFactory == nil {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolCloudWatchLogsQuery:      s.legacy.executeCloudWatchLogsQuery,
		toolCloudWatchLogsListGroups: s.legacy.executeCloudWatchLogsListGroups,
		toolCloudWatchLogsGetEvents:  s.legacy.executeCloudWatchLogsGetEvents,
	}

	for _, t := range getCloudWatchTools() {   // <-- logs getter ONLY
		if h, ok := handlers[t.Name]; ok {
			s.registerTool(t.Name, t.Description, t.InputSchema, h)
		}
	}
}
```

`getCloudWatchMetricsTools()` and `getCloudWatchAlarmsTools()` are never
iterated, and their handlers (`executeCloudWatchMetricsList`,
`executeCloudWatchMetricsGetStatistics`, `executeCloudWatchMetricsQuery`,
`executeCloudWatchAlarmsList`, `executeCloudWatchAlarmsHistory`) are never added
to the map. Tools registered here are what the SDK server advertises via
`tools/list` and can dispatch — so the metrics/alarms tools are absent from both.

### Why every other path works

The new tools *were* correctly added elsewhere, which is why the defect is
partially hidden:

| Path | Wires metrics/alarms? | Location |
|---|---|---|
| Legacy `getToolDefinitions` | ✅ | `server.go:866-867` |
| Legacy `dispatchToolCall` | ✅ | `server.go:1010-1018` |
| Slack tool prose | ✅ | `bot/tools.go:181-184` |
| `list_my_tools` (→ legacy `ToolDefinitions`) | ✅ | `authz.go:140` |
| **SDK MCP `tools/list` + dispatch** | ❌ | **`sdk_server.go:286`** |

`list_my_tools` and the Slack agent both read the **legacy** `ToolDefinitions()`
(which includes the new tools). The MCP transport reads its **own** per-category
registration in `sdk_server.go` (which does not). The two sources diverged.

---

## Fix

Add the two getters and five handlers to `registerCloudWatchTools()`:

```go
func (s *SDKServer) registerCloudWatchTools() {
	if s.legacy.cloudWatchClientFactory == nil {
		return
	}

	handlers := map[string]func(context.Context, map[string]interface{}) (string, error){
		toolCloudWatchLogsQuery:            s.legacy.executeCloudWatchLogsQuery,
		toolCloudWatchLogsListGroups:       s.legacy.executeCloudWatchLogsListGroups,
		toolCloudWatchLogsGetEvents:        s.legacy.executeCloudWatchLogsGetEvents,
		toolCloudWatchMetricsList:          s.legacy.executeCloudWatchMetricsList,
		toolCloudWatchMetricsGetStatistics: s.legacy.executeCloudWatchMetricsGetStatistics,
		toolCloudWatchMetricsQuery:         s.legacy.executeCloudWatchMetricsQuery,
		toolCloudWatchAlarmsList:           s.legacy.executeCloudWatchAlarmsList,
		toolCloudWatchAlarmsHistory:        s.legacy.executeCloudWatchAlarmsHistory,
	}

	getters := [][]MCPTool{
		getCloudWatchTools(),
		getCloudWatchMetricsTools(),
		getCloudWatchAlarmsTools(),
	}
	for _, tools := range getters {
		for _, t := range tools {
			if h, ok := handlers[t.Name]; ok {
				s.registerTool(t.Name, t.Description, t.InputSchema, h)
			}
		}
	}
}
```

(All five `execute*` handlers already exist and are used by the legacy dispatcher
at `server.go:1010-1018`, so no new handler code is required.)

---

## Follow-up (recommended, beyond the immediate fix)

1. **Regression test (TDD).** Assert that the SDK server's advertised tool set
   equals the legacy `getToolDefinitions()` set for a given configuration. This
   is the invariant that was silently broken, and such a test catches this class
   of drift for *every* tool, not just CloudWatch. The existing suite does not
   cover the SDK-vs-legacy advertisement parity.

2. **Eliminate the duplication (true root cause).** `sdk_server.go` maintains a
   parallel per-category handler map instead of deriving registration from
   `getToolDefinitions()` (the single source of truth). Because the two lists are
   maintained by hand in separate places, adding a tool to the legacy list did
   not surface it over MCP. Driving SDK registration from `getToolDefinitions()`
   (with a name→handler lookup) would make this class of bug structurally
   impossible.

3. **`list_my_tools` accuracy.** Consider having `list_my_tools` report from the
   same set the active transport actually advertises, so it can never claim a
   tool is usable that the transport will not serve.

---

## Verification

After the fix, from an MCP client (e.g. Claude Code):

1. `tools/list` includes `cloudwatch_metrics_list`, `cloudwatch_metrics_query`,
   `cloudwatch_metrics_get_statistics`, `cloudwatch_alarms_list`,
   `cloudwatch_alarms_history`.
2. `cloudwatch_metrics_list` (namespace `AWS/EKS`, account `test`) returns metric
   descriptors.
3. `cloudwatch_metrics_query` returns datapoints; `cloudwatch_alarms_list`
   (account `prod`) returns alarm states.
4. The new regression test passes (SDK advertised set == legacy tool set).
