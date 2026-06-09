package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pdf "github.com/stephenafamo/goldmark-pdf"
)

// sampleReport is a representative investigation report exercising the markdown
// features reports actually use: headings, bold/italic, lists, a fenced code
// block, and a GFM table.
const sampleReport = `# ModSecurity Block Investigation

**Severity:** Medium  **Status:** Resolved

## Executive Summary

A legitimate API client was blocked by the WAF after a deploy changed the
request content type — the triggering rule was a false positive against JSON
payloads. Flow: client → gateway → WAF → upstream. The user asked “why is my
traffic dropping?” … remediation below.

## Key Findings

- Blocks began at *14:02 UTC*, immediately after the ` + "`api-gateway`" + ` deploy.
- All blocks share OWASP CRS rule **942100** (SQL injection heuristic).
- The payloads are valid JSON; the rule misfires on the ` + "`$where`" + ` key.

## Blocked Requests

| Time (UTC) | Source IP       | Rule  | Path           |
|------------|-----------------|-------|----------------|
| 14:02:11   | 13.40.254.161   | 942100 | /v1/orders     |
| 14:02:13   | 13.40.254.161   | 942100 | /v1/orders     |
| 14:03:01   | 52.18.4.22      | 942100 | /v1/inventory  |

## Suggested Rule Exclusion

` + "```" + `
SecRule REQUEST_URI "@beginsWith /v1/" \
  "id:1000100,phase:1,pass,nolog,ctl:ruleRemoveById=942100"
` + "```" + `

## Recommendations

1. Apply the exclusion above scoped to the ` + "`/v1/`" + ` prefix.
2. Add a synthetic monitor asserting a JSON POST to ` + "`/v1/orders`" + ` returns 200.
3. Review CRS paranoia level for this listener.
`

func TestRenderMarkdownPDFProducesValidPDF(t *testing.T) {
	t.Parallel()

	data, err := renderMarkdownPDF(context.Background(), sampleReport, "ModSecurity Block Investigation", "Acme Corp")
	if err != nil {
		t.Fatalf("renderMarkdownPDF: %v", err)
	}

	if !bytes.HasPrefix(data, []byte("%PDF")) {
		t.Fatalf("output is not a PDF (missing %%PDF header); first bytes: %q", data[:min(8, len(data))])
	}

	if len(data) < 1000 {
		t.Errorf("PDF suspiciously small (%d bytes) — content likely did not render", len(data))
	}
}

// TestGeneratePDFPreviewArtifact renders the sample report and writes it to
// testdata/sample-report.pdf so the output can be opened and reviewed. The path
// is logged; open it to preview what shipped PDFs look like.
func TestGeneratePDFPreviewArtifact(t *testing.T) {
	t.Parallel()

	data, err := renderMarkdownPDF(context.Background(), sampleReport, "ModSecurity Block Investigation", "Acme Corp")
	if err != nil {
		t.Fatalf("renderMarkdownPDF: %v", err)
	}

	dir := "testdata"
	mkErr := os.MkdirAll(dir, 0o755)
	if mkErr != nil {
		t.Fatalf("creating testdata dir: %v", mkErr)
	}

	out := filepath.Join(dir, "sample-report.pdf")
	writeErr := os.WriteFile(out, data, 0o644)
	if writeErr != nil {
		t.Fatalf("writing preview PDF: %v", writeErr)
	}

	abs, _ := filepath.Abs(out)
	t.Logf("PDF preview written to %s (%d bytes) — open it to review the rendering", abs, len(data))
}

func TestFoldToASCII(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain ascii":          "plain ascii",
		"em — dash":            "em -- dash",
		"arrow → here":         "arrow -> here",
		"curly “quotes”":       "curly \"quotes\"",
		"apostrophe’s":         "apostrophe's",
		"ellipsis…":            "ellipsis...",
		"entity &quot;x&quot;": "entity \"x\"",
	}

	for in, want := range cases {
		got := foldToASCII(in)
		if got != want {
			t.Errorf("foldToASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFoldToASCIIIsPureASCII(t *testing.T) {
	t.Parallel()

	// Typography that previously mojibaked, plus an accented name and an emoji.
	in := "Façade — café → “résumé” • 日本語 🚀 …"
	got := foldToASCII(in)

	for i := range len(got) {
		if got[i] >= 128 {
			t.Fatalf("foldToASCII left a non-ASCII byte at %d in %q", i, got)
		}
	}
}

func TestNeutralizeImagesReplacesWithAltText(t *testing.T) {
	t.Parallel()

	in := "before ![a diagram](http://internal.example/secret.png) after"
	out := neutralizeImages(in)

	if strings.Contains(out, "http") {
		t.Errorf("image URL must not survive: %q", out)
	}

	if out != "before a diagram after" {
		t.Errorf("expected alt text inline, got %q", out)
	}
}

func TestPDFBodyFontSelection(t *testing.T) {
	tests := []struct {
		env  string
		want string
	}{
		{"", pdf.FontHelvetica.Family},
		{"helvetica", pdf.FontHelvetica.Family},
		{"HELVETICA", pdf.FontHelvetica.Family},
		{"times", pdf.FontTimes.Family},
		{"courier", pdf.FontCourier.Family},
		{"nonsense", pdf.FontHelvetica.Family},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("PDF_FONT", tt.env)

			got := pdfBodyFont().Family
			if got != tt.want {
				t.Errorf("PDF_FONT=%q: got font %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}
