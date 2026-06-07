package k8s

import (
	"regexp"
)

// InboundSanitizer defangs forged conversational control sequences in
// untrusted data (tool results, fetched logs, API responses) before that data
// is surfaced to a model. Tool output is DATA, never instructions: an attacker
// who can land text in a log line, a dashboard title, or an API payload must
// not be able to forge a turn boundary or an operator message and have the
// agent act on it.
//
// This is the inbound counterpart to Sanitizer, which scrubs *outbound*
// secrets. The two are deliberately separate concerns: one keeps secrets from
// leaving, the other keeps injected instructions from arriving.
type InboundSanitizer struct {
	patterns []*defangPattern
}

type defangPattern struct {
	regex       *regexp.Regexp
	replacement string
	description string
}

// NewInboundSanitizer creates an inbound sanitizer with the forged-control
// patterns called out by the GeoIP prompt-injection incident analysis:
// human(from …) operator envelopes, bare role labels, <system-reminder>
// tags, and [Request interrupted…] markers.
func NewInboundSanitizer() (result *InboundSanitizer) {
	patterns := []*defangPattern{
		// Operator-impersonation envelope, e.g. "human(from Vayde): do X".
		// This is the exact tradecraft of incident injections #1 and #2.
		{
			regex:       regexp.MustCompile(`(?i)human\s*\(\s*from[^)]*\)`),
			replacement: `[defanged-role-envelope]`,
			description: "forged human(from ...) operator envelope",
		},
		// Line-leading role labels: "assistant:", "system:", "human:",
		// "user:" — a classic attempt to forge a turn boundary. Anchored to
		// line start (optionally behind quote/space) to avoid mangling prose
		// like "the user: see docs".
		{
			regex:       regexp.MustCompile(`(?im)^[ \t>]*(assistant|system|human|user)[ \t]*:`),
			replacement: `[defanged-role-label]:`,
			description: "forged role label",
		},
		// Forged harness control tags.
		{
			regex:       regexp.MustCompile(`(?i)<\s*/?\s*system-reminder\s*>`),
			replacement: `[defanged-system-reminder]`,
			description: "forged <system-reminder> tag",
		},
		// Forged interrupt marker the real harness emits, e.g.
		// "[Request interrupted by user]".
		{
			regex:       regexp.MustCompile(`(?i)\[\s*request interrupted[^\]]*\]`),
			replacement: `[defanged-interrupt-marker]`,
			description: "forged [Request interrupted...] marker",
		},
		// Generic forged role/turn XML-ish tags, e.g. "<system>", "</user>".
		{
			regex:       regexp.MustCompile(`(?i)<\s*/?\s*(system|user|assistant|human)\s*>`),
			replacement: `[defanged-role-tag]`,
			description: "forged role tag",
		},
	}

	result = &InboundSanitizer{
		patterns: patterns,
	}

	return result
}

// Defang neutralizes forged control sequences in the input and reports which
// pattern categories tripped. An empty hits slice means the input was clean.
// Callers should treat a non-empty hits slice as an active-probe signal worth
// an event/metric, not merely a transformed string.
func (s *InboundSanitizer) Defang(input string) (sanitized string, hits []string) {
	sanitized = input

	for _, pattern := range s.patterns {
		if pattern.regex.MatchString(sanitized) {
			hits = append(hits, pattern.description)
			sanitized = pattern.regex.ReplaceAllString(sanitized, pattern.replacement)
		}
	}

	return sanitized, hits
}
