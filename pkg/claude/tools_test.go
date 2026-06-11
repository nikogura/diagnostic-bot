package claude

import (
	"encoding/json"
	"testing"
)

func TestGetInvestigationTools(t *testing.T) {
	t.Parallel()

	tools := GetInvestigationTools()

	// Should have exactly 6 tools
	expectedCount := 6
	if len(tools) != expectedCount {
		t.Errorf("GetInvestigationTools() returned %d tools, want %d", len(tools), expectedCount)
	}

	// Expected tool names
	expectedTools := map[string]bool{
		ToolLokiQuery:      true,
		ToolGetK8sPodLogs:  true,
		ToolGetK8sResource: true,
		ToolListK8sPods:    true,
		ToolGetK8sEvents:   true,
		ToolWhoisLookup:    true,
	}

	// Verify each tool
	for _, tool := range tools {
		// Check tool name is expected
		if !expectedTools[tool.Name] {
			t.Errorf("Unexpected tool name: %s", tool.Name)
		}

		// Check required fields are present
		if tool.Name == "" {
			t.Error("Tool has empty name")
		}
		if tool.Description == "" {
			t.Errorf("Tool %s has empty description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("Tool %s has nil InputSchema", tool.Name)
		}

		// Verify InputSchema can be marshaled to JSON
		_, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("Tool %s InputSchema cannot be marshaled: %v", tool.Name, err)
		}
	}

	// Verify no duplicate tool names
	seen := make(map[string]bool)
	for _, tool := range tools {
		if seen[tool.Name] {
			t.Errorf("Duplicate tool name: %s", tool.Name)
		}
		seen[tool.Name] = true
	}
}

func TestToolDefinitionsAreValid(t *testing.T) {
	t.Parallel()

	tools := GetInvestigationTools()

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()

			// Verify tool can be serialized to JSON (Anthropic API requirement)
			jsonBytes, err := json.Marshal(tool)
			if err != nil {
				t.Fatalf("Tool %s cannot be marshaled to JSON: %v", tool.Name, err)
			}

			// Verify JSON is not empty
			if len(jsonBytes) == 0 {
				t.Errorf("Tool %s marshaled to empty JSON", tool.Name)
			}
		})
	}
}
