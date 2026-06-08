package bot

import "testing"

func TestDetectPDFCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		message     string
		wantMatched bool
		wantEnable  bool
	}{
		// disable — the user's examples and common variants
		{"disable pdf", true, false},
		{"disable reports", true, false},
		{"disable pdfs", true, false},
		{"stop making reports", true, false},
		{"stop generating pdfs", true, false},
		{"no more reports", true, false},
		{"please turn off the pdf reports", true, false},
		{"suppress the pdf", true, false},
		{"reports off", true, false},
		{"turn the pdf off", true, false},

		// enable
		{"resume reports", true, true},
		{"resume pdfs", true, true},
		{"enable pdf", true, true},
		{"re-enable reports", true, true},
		{"turn the reports back on", true, true},
		{"start generating reports again", true, true},
		{"reports back on", true, true},

		// not a PDF command — must NOT match
		{"investigate why the report service is down", false, false},
		{"what's the report on the outage", false, false},
		{"stop the deployment and check the report", false, false},
		{"why did the modsecurity rule block this request", false, false},
		{"can you summarize the findings", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			t.Parallel()

			matched, enable := detectPDFCommand(tt.message)
			if matched != tt.wantMatched {
				t.Fatalf("detectPDFCommand(%q) matched=%v, want %v", tt.message, matched, tt.wantMatched)
			}

			if matched && enable != tt.wantEnable {
				t.Errorf("detectPDFCommand(%q) enable=%v, want %v", tt.message, enable, tt.wantEnable)
			}
		})
	}
}
