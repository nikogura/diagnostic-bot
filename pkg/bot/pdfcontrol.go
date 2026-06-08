package bot

import "regexp"

// pdfCommandPattern is a regex that, when it matches a message, signals a
// PDF on/off command.
type pdfCommandPattern struct {
	re     *regexp.Regexp
	enable bool
}

// pdfCommandPatterns recognize natural-language requests to turn PDF report
// generation off or back on in a thread. They require a disable/enable verb to
// sit close to a PDF noun (pdf/pdfs/report/reports) so an ordinary
// investigation request that merely mentions "report" doesn't trip them.
// Disable patterns are listed first and win on overlap.
//
//nolint:gochecknoglobals // compiled-once command patterns
var pdfCommandPatterns = []pdfCommandPattern{
	// "disable reports", "stop generating pdfs", "no more reports", "suppress the pdf"
	{regexp.MustCompile(`(?i)\b(disable|stop|suppress|pause|skip|cancel|kill|halt|no more|turn off|don'?t (?:make|generate|create|produce|want|send))\b[^.!?\n]{0,15}\b(pdfs?|reports?)\b`), false},
	// "turn the pdf off"
	{regexp.MustCompile(`(?i)\bturn\b[^.!?\n]{0,15}\b(pdfs?|reports?)\b[^.!?\n]{0,8}\boff\b`), false},
	// "reports off", "pdf disabled"
	{regexp.MustCompile(`(?i)\b(pdfs?|reports?)\b[ \t]+(off|disabled)\b`), false},
	// "resume reports", "enable pdf", "re-enable pdfs", "start making reports"
	{regexp.MustCompile(`(?i)\b(enable|resume|re-?enable|reinstate|unpause|turn on|start (?:making|generating|sending)?)\b[^.!?\n]{0,15}\b(pdfs?|reports?)\b`), true},
	// "turn the reports back on"
	{regexp.MustCompile(`(?i)\bturn\b[^.!?\n]{0,15}\b(pdfs?|reports?)\b[^.!?\n]{0,10}\b(on|back on)\b`), true},
	// "reports back on", "pdf enabled", "reports on again"
	{regexp.MustCompile(`(?i)\b(pdfs?|reports?)\b[ \t]+(back on|enabled|on again)\b`), true},
}

// detectPDFCommand reports whether a message is a PDF on/off command, and if so
// whether it enables (true) or disables (false) reports. It is intentionally
// fuzzy — "disable reports", "stop making pdfs", "resume reports", and
// "turn the pdf back on" all match.
func detectPDFCommand(message string) (matched bool, enable bool) {
	for _, pattern := range pdfCommandPatterns {
		if pattern.re.MatchString(message) {
			matched = true
			enable = pattern.enable
			return matched, enable
		}
	}

	return matched, enable
}
