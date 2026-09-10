package investigations

import (
	"os"
	"path/filepath"
	"testing"
)

func checkModSecurityTemplate(t *testing.T, tmpl *InvestigationSkill) {
	t.Helper()
	if tmpl.Name != "ModSecurity Test" {
		t.Errorf("Name = %q, want %q", tmpl.Name, "ModSecurity Test")
	}
	if len(tmpl.TriggerPatterns) != 2 {
		t.Errorf("TriggerPatterns length = %d, want 2", len(tmpl.TriggerPatterns))
	}
	if tmpl.InitialPrompt != "Test prompt" {
		t.Errorf("InitialPrompt = %q, want %q", tmpl.InitialPrompt, "Test prompt")
	}
	if len(tmpl.KubernetesResources) != 1 {
		t.Errorf("KubernetesResources length = %d, want 1", len(tmpl.KubernetesResources))
	}
}

func checkEmptyTriggerPatterns(t *testing.T, tmpl *InvestigationSkill) {
	t.Helper()
	if len(tmpl.TriggerPatterns) != 0 {
		t.Errorf("TriggerPatterns length = %d, want 0", len(tmpl.TriggerPatterns))
	}
}

func checkEmptyTemplate(t *testing.T, tmpl *InvestigationSkill) {
	t.Helper()
	if tmpl.Name != "" {
		t.Errorf("Name = %q, want empty string", tmpl.Name)
	}
}

func runTemplateTest(t *testing.T, tmpFile string, wantErr bool, checkFunc func(*testing.T, *InvestigationSkill)) {
	t.Helper()

	// Load skill
	tmpl, err := LoadSkill(tmpFile)

	if wantErr {
		if err == nil {
			t.Errorf("LoadSkill() error = nil, wantErr %v", wantErr)
		}
		return
	}

	if err != nil {
		t.Errorf("LoadSkill() error = %v, wantErr %v", err, wantErr)
		return
	}

	if checkFunc != nil {
		checkFunc(t, tmpl)
	}
}

func TestLoadSkill(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		wantErr   bool
		checkFunc func(*testing.T, *InvestigationSkill)
	}{
		{
			name: "valid modsecurity skill",
			yaml: `name: "ModSecurity Test"
description: "Test skill"
trigger_patterns:
  - "modsec"
  - "waf"
initial_prompt: "Test prompt"
kubernetes_resources:
  - type: "logs"
    namespace: "test"
`,
			wantErr:   false,
			checkFunc: checkModSecurityTemplate,
		},
		{
			name: "empty trigger patterns",
			yaml: `name: "Test"
description: "Test"
trigger_patterns: []
initial_prompt: "Test"
`,
			wantErr:   false,
			checkFunc: checkEmptyTriggerPatterns,
		},
		{
			name:    "invalid yaml",
			yaml:    `invalid: yaml: structure: here`,
			wantErr: true, // YAML parsing will error on malformed syntax
		},
		{
			name:      "empty file",
			yaml:      "",
			wantErr:   false,
			checkFunc: checkEmptyTemplate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create temporary file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "test.yaml")
			err := os.WriteFile(tmpFile, []byte(tt.yaml), 0600)
			if err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}

			runTemplateTest(t, tmpFile, tt.wantErr, tt.checkFunc)
		})
	}
}

func TestLoadSkillFileNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadSkill("/nonexistent/file.yaml")
	if err == nil {
		t.Error("LoadSkill() with nonexistent file should return error")
	}
}

func TestTemplateMatches(t *testing.T) {
	t.Parallel()

	tmpl, err := LoadSkill("../../investigations/modsecurity-block.yaml")
	if err != nil {
		t.Skipf("Skipping test - modsecurity skill not found: %v", err)
		return
	}

	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{
			name:    "matches modsec",
			message: "I'm seeing modsec blocks",
			want:    true,
		},
		{
			name:    "matches modsecurity",
			message: "ModSecurity is blocking",
			want:    true,
		},
		{
			name:    "no match",
			message: "Something else is happening",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tmpl.Matches(tt.message)
			if got != tt.want {
				t.Errorf("Matches(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestInvestigationType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		it   InvestigationType
		want string
	}{
		{
			name: "modsecurity",
			it:   InvestigationTypeModSecurity,
			want: "modsecurity",
		},
		{
			name: "atlas",
			it:   InvestigationTypeAtlas,
			want: "atlas-migration",
		},
		{
			name: "pod_crash",
			it:   InvestigationTypePodCrash,
			want: "pod-crashloop",
		},
		{
			name: "unknown",
			it:   InvestigationTypeUnknown,
			want: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := string(tt.it)
			if got != tt.want {
				t.Errorf("InvestigationType = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchWithSpecificity(t *testing.T) {
	t.Parallel()

	// Create test skills with different specificity levels
	tmpDir := t.TempDir()

	// Specific pattern skill
	specificYAML := `name: "Specific Test"
description: "Specific skill"
trigger_patterns:
  - "database.*migration"
initial_prompt: "Specific prompt"
`
	specificFile := filepath.Join(tmpDir, "specific.yaml")
	err := os.WriteFile(specificFile, []byte(specificYAML), 0600)
	if err != nil {
		t.Fatalf("Failed to write specific skill: %v", err)
	}

	// Generic pattern skill
	genericYAML := `name: "Generic Test"
description: "Generic skill"
trigger_patterns:
  - "issue"
initial_prompt: "Generic prompt"
`
	genericFile := filepath.Join(tmpDir, "generic.yaml")
	err = os.WriteFile(genericFile, []byte(genericYAML), 0600)
	if err != nil {
		t.Fatalf("Failed to write generic skill: %v", err)
	}

	specificTmpl, err := LoadSkill(specificFile)
	if err != nil {
		t.Fatalf("Failed to load specific skill: %v", err)
	}

	genericTmpl, err := LoadSkill(genericFile)
	if err != nil {
		t.Fatalf("Failed to load generic skill: %v", err)
	}

	// Test message that matches both patterns
	message := "database migration issue in example-repo"

	specificMatched, specificScore, specificPattern := specificTmpl.MatchWithSpecificity(message)
	genericMatched, genericScore, genericPattern := genericTmpl.MatchWithSpecificity(message)

	// Both should match
	if !specificMatched {
		t.Error("Specific skill should match the message")
	}

	if !genericMatched {
		t.Error("Generic skill should match the message")
	}

	// Specific skill should have higher score
	if specificScore <= genericScore {
		t.Errorf("Specific pattern score (%d) should be higher than generic pattern score (%d)",
			specificScore, genericScore)
	}

	t.Logf("Specific: matched=%v, score=%d, pattern=%q",
		specificMatched, specificScore, specificPattern)
	t.Logf("Generic: matched=%v, score=%d, pattern=%q",
		genericMatched, genericScore, genericPattern)
}

func TestFindMatchingSkillSpecificity(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create multiple skills with overlapping patterns
	skills := []struct {
		filename string
		yaml     string
	}{
		{
			filename: "atlas-migration.yaml",
			yaml: `name: "Database Migration"
description: "Database migration investigation"
trigger_patterns:
  - "database.*migration"
  - "atlas.*migration"
initial_prompt: "Database migration prompt"
`,
		},
		{
			filename: "general.yaml",
			yaml: `name: "General"
description: "General investigation"
trigger_patterns:
  - "issue"
  - "problem"
  - "error"
initial_prompt: "General prompt"
`,
		},
	}

	for _, skill := range skills {
		filePath := filepath.Join(tmpDir, skill.filename)
		err := os.WriteFile(filePath, []byte(skill.yaml), 0600)
		if err != nil {
			t.Fatalf("Failed to write %s: %v", skill.filename, err)
		}
	}

	// Load skill library
	lib, err := NewSkillLibrary(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create skill library: %v", err)
	}

	// Test that specific pattern wins over generic
	message := "database migration issue in prod"

	// Debug: Check all skills and their specificity scores
	for invType, skill := range lib.skills {
		matched, specificity, pattern := skill.MatchWithSpecificity(message)
		t.Logf("Skill %q (type=%s): matched=%v, specificity=%d, pattern=%q",
			skill.Name, invType, matched, specificity, pattern)
	}

	skill, invType, err := lib.FindMatchingSkill(message)
	if err != nil {
		t.Fatalf("FindMatchingSkill() error = %v", err)
	}

	if skill.Name != "Database Migration" {
		t.Errorf("FindMatchingSkill() returned %q, want %q",
			skill.Name, "Database Migration")
	}

	t.Logf("Matched investigation type: %s", invType)
}
