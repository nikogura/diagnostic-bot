package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	pdf "github.com/stephenafamo/goldmark-pdf"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// imageMarkdownPattern matches inline image syntax ![alt](src).
var imageMarkdownPattern = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)

// pdfRenderMu serializes PDF rendering. goldmark-pdf keeps table-layout state in
// package-level globals, so concurrent renders race and corrupt output;
// investigations can finish concurrently, so the renders must be serialized.
// Rendering is fast and infrequent (one report per investigation), so a single
// global lock is fine.
//
//nolint:gochecknoglobals // required to serialize a thread-unsafe dependency
var pdfRenderMu sync.Mutex

// pdfBodyFont resolves the report font from the PDF_FONT env var, defaulting to
// Helvetica. Only the three core PDF fonts are offered — they need no font
// files or network access, which keeps the renderer (and the image) self
// contained. Unknown values fall back to the default.
func pdfBodyFont() (font pdf.Font) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PDF_FONT"))) {
	case "times":
		font = pdf.FontTimes
	case "courier":
		font = pdf.FontCourier
	default:
		font = pdf.FontHelvetica
	}

	return font
}

// neutralizeImages replaces inline image markdown with its alt text. Report
// content is model-influenced, and the PDF renderer would otherwise HTTP-fetch
// any image URL (an SSRF / file-read vector), so images are rendered as plain
// text instead of fetched.
func neutralizeImages(markdown string) (out string) {
	out = imageMarkdownPattern.ReplaceAllString(markdown, "$1")
	return out
}

// composePDFMarkdown prepends an optional title and company line to the report
// body before rendering.
func composePDFMarkdown(markdown string, title string, companyName string) (out string) {
	var builder strings.Builder

	if title != "" {
		fmt.Fprintf(&builder, "# %s\n\n", title)
	}

	if companyName != "" {
		fmt.Fprintf(&builder, "*%s*\n\n", companyName)
	}

	builder.WriteString(neutralizeImages(markdown))

	out = builder.String()
	return out
}

// renderMarkdownPDF renders Markdown to PDF bytes with a pure-Go pipeline
// (goldmark + goldmark-pdf): no shell, no LaTeX, no external binaries. Content
// is parsed and typeset as data, so there is no template or raw-TeX injection
// surface. The heading/body font is configurable via PDF_FONT (default
// Helvetica); code is always monospace.
func renderMarkdownPDF(ctx context.Context, markdown string, title string, companyName string) (data []byte, err error) {
	document := composePDFMarkdown(markdown, title, companyName)

	bodyFont := pdfBodyFont()
	renderer := pdf.New(
		pdf.WithContext(ctx),
		pdf.WithHeadingFont(bodyFont),
		pdf.WithBodyFont(bodyFont),
		pdf.WithCodeFont(pdf.FontCourier),
	)

	converter := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRenderer(renderer),
	)

	var buf bytes.Buffer

	// goldmark-pdf is not concurrency-safe (package-global table state).
	pdfRenderMu.Lock()
	err = converter.Convert([]byte(document), &buf)
	pdfRenderMu.Unlock()

	if err != nil {
		err = fmt.Errorf("rendering markdown to PDF: %w", err)
		return data, err
	}

	data = buf.Bytes()
	return data, err
}
