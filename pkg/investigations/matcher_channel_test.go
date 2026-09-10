package investigations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchWithChannel(t *testing.T) {
	// Create temporary directory for test skills
	tmpDir := t.TempDir()

	// Create test investigation skills
	atlasYAML := `name: "Atlas Migration Test"
description: "Test skill for atlas migrations"
trigger_patterns:
  - "migration.*fail"
  - "database.*migration"
initial_prompt: "Test prompt for atlas migrations"
kubernetes_resources: []
`

	modsecYAML := `name: "ModSecurity Test"
description: "Test skill for modsecurity"
trigger_patterns:
  - "waf.*block"
  - "modsec.*fail"
initial_prompt: "Test prompt for modsecurity"
kubernetes_resources: []
`

	podYAML := `name: "Pod Crash Test"
description: "Test skill for pod crashes"
trigger_patterns:
  - "pod.*crash"
  - "crashloop"
initial_prompt: "Test prompt for pod crashes"
kubernetes_resources: []
`

	generalYAML := `name: "General Diagnostic Test"
description: "Test skill for general diagnostics"
trigger_patterns:
  - "investigate"
  - "diagnostic"
  - "diagnose"
  - "issue"
  - "problem"
initial_prompt: "Test prompt for general diagnostics"
kubernetes_resources: []
`

	// Write test skills
	err := os.WriteFile(filepath.Join(tmpDir, "atlas-test.yaml"), []byte(atlasYAML), 0600)
	if err != nil {
		t.Fatalf("failed to write atlas test skill: %v", err)
	}

	err = os.WriteFile(filepath.Join(tmpDir, "modsec-test.yaml"), []byte(modsecYAML), 0600)
	if err != nil {
		t.Fatalf("failed to write modsec test skill: %v", err)
	}

	err = os.WriteFile(filepath.Join(tmpDir, "pod-crash-test.yaml"), []byte(podYAML), 0600)
	if err != nil {
		t.Fatalf("failed to write pod crash test skill: %v", err)
	}

	err = os.WriteFile(filepath.Join(tmpDir, "general-diagnostic.yaml"), []byte(generalYAML), 0600)
	if err != nil {
		t.Fatalf("failed to write general diagnostic test skill: %v", err)
	}

	// Load skills
	library, err := NewSkillLibrary(tmpDir)
	if err != nil {
		t.Fatalf("failed to load skill library: %v", err)
	}

	matcher := NewMatcher(library)

	tests := []struct {
		name            string
		message         string
		channelName     string
		wantMatched     bool
		wantType        InvestigationType
		wantDescription string
	}{
		{
			name:            "explicit pattern match overrides fallback",
			message:         "migration fail",
			channelName:     "sec",
			wantMatched:     true,
			wantType:        InvestigationTypeAtlas,
			wantDescription: "Pattern match should take precedence",
		},
		{
			name:            "fallback to general diagnostic when no pattern match",
			message:         "something is broken",
			channelName:     "data",
			wantMatched:     true,
			wantType:        InvestigationTypeGeneralDiagnostic,
			wantDescription: "Should fall back to general-diagnostic when message doesn't match specific patterns",
		},
		{
			name:            "fallback to general diagnostic in any channel",
			message:         "help needed",
			channelName:     "security",
			wantMatched:     true,
			wantType:        InvestigationTypeGeneralDiagnostic,
			wantDescription: "Should fall back to general-diagnostic regardless of channel",
		},
		{
			name:            "fallback to general diagnostic for infrastructure questions",
			message:         "service down",
			channelName:     "k8s",
			wantMatched:     true,
			wantType:        InvestigationTypeGeneralDiagnostic,
			wantDescription: "Should fall back to general-diagnostic for non-specific messages",
		},
		{
			name:            "fallback to general diagnostic in general channel",
			message:         "random issue",
			channelName:     "general",
			wantMatched:     true,
			wantType:        InvestigationTypeGeneralDiagnostic,
			wantDescription: "Should fall back to general-diagnostic in any channel",
		},
		{
			name:            "fallback to general diagnostic without channel",
			message:         "random issue",
			channelName:     "",
			wantMatched:     true,
			wantType:        InvestigationTypeGeneralDiagnostic,
			wantDescription: "Should fall back to general-diagnostic even without channel name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matcher.MatchWithChannel(tt.message, tt.channelName)

			if result.Matched != tt.wantMatched {
				t.Errorf("MatchWithChannel(%q, %q).Matched = %v, want %v\n%s",
					tt.message, tt.channelName, result.Matched, tt.wantMatched, tt.wantDescription)
			}

			if result.InvestigationType != tt.wantType {
				t.Errorf("MatchWithChannel(%q, %q).InvestigationType = %v, want %v\n%s",
					tt.message, tt.channelName, result.InvestigationType, tt.wantType, tt.wantDescription)
			}
		})
	}
}
