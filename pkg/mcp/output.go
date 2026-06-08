package mcp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// defaultMaxToolOutputBytes bounds a single tool result returned to any caller
// (the in-process agent loop and external MCP clients alike). It is a backstop
// against a tool with no internal limit — a broad prometheus_query_range, a
// large k8s_get_resource dump, an unfiltered query — emitting a pathologically
// huge payload (a ~27MB result is ~6.9M tokens, far past a model's context).
// Override with MCP_MAX_TOOL_OUTPUT_BYTES.
const defaultMaxToolOutputBytes = 1_000_000

// resolveMaxToolOutputBytes reads the cap from the environment, falling back to
// the default when unset or invalid.
func resolveMaxToolOutputBytes() (limit int) {
	limit = defaultMaxToolOutputBytes

	value := os.Getenv("MCP_MAX_TOOL_OUTPUT_BYTES")
	if value == "" {
		return limit
	}

	parsed, err := strconv.Atoi(value)
	if err == nil && parsed > 0 {
		limit = parsed
	}

	return limit
}

// capToolResult truncates an oversized tool result at a UTF-8 boundary and
// appends a notice. Applied at the dispatch boundary so every tool is bounded
// regardless of whether it limits its own output. A limit <= 0 disables the cap.
func capToolResult(result string, limit int) (capped string) {
	if limit <= 0 || len(result) <= limit {
		capped = result
		return capped
	}

	truncated := strings.ToValidUTF8(result[:limit], "")
	capped = fmt.Sprintf("%s\n\n[result truncated: %d of %d bytes shown. Narrow the query — a tighter time range, more specific filters, or a smaller scope — to retrieve the rest.]",
		truncated, len(truncated), len(result))

	return capped
}
