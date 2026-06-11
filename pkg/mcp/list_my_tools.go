package mcp

import (
	"context"
	"sort"
	"strings"
)

// executeListMyTools reports the caller's actual tool access on this front-end —
// the deterministic "what can I do?" answer (sudo -l style). The set it returns
// is the same set dispatch enforces (both go through the shared capability
// core), so the reported and enforced sets cannot drift. Tools scoped to another
// interface are listed separately and labeled, never implied as usable here.
func (s *Server) executeListMyTools(ctx context.Context, _ map[string]interface{}) (result string, err error) {
	capability := s.capabilities(ctx)

	var b strings.Builder

	if len(capability.Here) == 0 {
		b.WriteString("You currently have no tools available on this interface.\n")
	} else {
		b.WriteString("Tools you can use here:\n")
		for _, name := range capability.Here {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteString("\n")
		}
	}

	if len(capability.Elsewhere) > 0 {
		names := make([]string, 0, len(capability.Elsewhere))
		for name := range capability.Elsewhere {
			names = append(names, name)
		}
		sort.Strings(names)

		b.WriteString("\nNot available here, but available on another interface:\n")
		for _, name := range names {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteString(" (available via ")
			b.WriteString(strings.Join(capability.Elsewhere[name], "/"))
			b.WriteString(", not here)\n")
		}
	}

	result = b.String()
	return result, err
}

// listMyToolsDefinition is the always-available capabilities tool, advertised on
// every front-end.
func listMyToolsDefinition() (tool MCPTool) {
	tool = MCPTool{
		Name: metaToolListMyTools,
		Description: "List the tools YOU (the current caller) can actually use on this interface right now, " +
			"plus any tools that exist but are only available on another interface. This is the authoritative, " +
			"per-user answer to \"what can I do?\" — only describe tools it reports as usable here; never offer or " +
			"attempt tools it omits.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
	return tool
}
