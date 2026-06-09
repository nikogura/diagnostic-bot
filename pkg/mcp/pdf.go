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

// asciiReplacer maps common non-ASCII typography (and a few HTML entities) to
// ASCII equivalents. The goldmark-pdf core fonts are Latin-1 and render UTF-8
// bytes as mojibake (an em-dash "—" shows as "â€"), so the document is folded
// to ASCII before rendering.
//
//nolint:gochecknoglobals // compiled-once replacement table
var asciiReplacer = strings.NewReplacer(
	"—", "--", // em dash
	"–", "-", // en dash
	"−", "-", // minus sign
	"→", "->", // right arrow
	"←", "<-", // left arrow
	"↔", "<->", // left-right arrow
	"⇒", "=>", // rightwards double arrow
	"“", "\"", // left double quote
	"”", "\"", // right double quote
	"„", "\"", // low double quote
	"‘", "'", // left single quote
	"’", "'", // right single quote / apostrophe
	"•", "-", // bullet
	"·", "-", // middle dot
	"…", "...", // ellipsis
	" ", " ", // non-breaking space
	"×", "x", // multiplication sign
	" ", " ", // thin space
	"\u200b", "", // zero-width space
	"&quot;", "\"",
	"&apos;", "'",
	"&lt;", "<",
	"&gt;", ">",
	"&#39;", "'",
)

// foldToASCII converts text to pure ASCII so the Latin-1 core fonts render it
// correctly. Known typography is transliterated; any remaining non-ASCII rune
// becomes '?' rather than mojibake.
func foldToASCII(s string) (out string) {
	s = asciiReplacer.Replace(s)

	var builder strings.Builder

	builder.Grow(len(s))

	for _, r := range s {
		if r < 128 {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('?')
		}
	}

	out = builder.String()
	return out
}

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
	// Fold to ASCII: the core fonts are Latin-1 and would mojibake any UTF-8
	// typography (em-dashes, arrows, curly quotes) otherwise.
	document := foldToASCII(composePDFMarkdown(markdown, title, companyName))

	bodyFont := pdfBodyFont()
	renderer := pdf.New(
		pdf.WithContext(ctx),
		pdf.WithHeadingFont(bodyFont),
		pdf.WithBodyFont(bodyFont),
		pdf.WithCodeFont(pdf.FontCourier),
		// Don't HTML-escape text — a literal quote must render as " not &quot;.
		pdf.WithEscapeHTML(false),
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
