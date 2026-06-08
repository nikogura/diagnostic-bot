package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapToolResult(t *testing.T) {
	t.Parallel()

	t.Run("under the limit is unchanged", func(t *testing.T) {
		t.Parallel()
		in := "small"
		if got := capToolResult(in, 100); got != in {
			t.Errorf("expected unchanged, got %q", got)
		}
	})

	t.Run("over the limit is truncated with a notice", func(t *testing.T) {
		t.Parallel()
		in := strings.Repeat("a", 500)
		got := capToolResult(in, 100)

		if !strings.HasPrefix(got, strings.Repeat("a", 100)) {
			t.Error("expected the first 100 bytes preserved")
		}
		if !strings.Contains(got, "truncated") || !strings.Contains(got, "Narrow the query") {
			t.Errorf("expected a truncation notice, got: %q", got)
		}
	})

	t.Run("zero or negative limit disables the cap", func(t *testing.T) {
		t.Parallel()
		in := strings.Repeat("a", 5000)
		if got := capToolResult(in, 0); got != in {
			t.Error("limit 0 must disable the cap")
		}
		if got := capToolResult(in, -1); got != in {
			t.Error("negative limit must disable the cap")
		}
	})

	t.Run("truncation at a multibyte boundary stays valid UTF-8", func(t *testing.T) {
		t.Parallel()
		in := strings.Repeat("世", 100) // 3 bytes each; limit lands mid-rune
		got := capToolResult(in, 100)

		if !utf8.ValidString(got) {
			t.Error("capped output must be valid UTF-8")
		}
	})
}

func TestResolveMaxToolOutputBytes(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv("MCP_MAX_TOOL_OUTPUT_BYTES", "")
		if got := resolveMaxToolOutputBytes(); got != defaultMaxToolOutputBytes {
			t.Errorf("got %d, want default %d", got, defaultMaxToolOutputBytes)
		}
	})

	t.Run("override when valid", func(t *testing.T) {
		t.Setenv("MCP_MAX_TOOL_OUTPUT_BYTES", "4096")
		if got := resolveMaxToolOutputBytes(); got != 4096 {
			t.Errorf("got %d, want 4096", got)
		}
	})

	t.Run("default when invalid or non-positive", func(t *testing.T) {
		for _, v := range []string{"nonsense", "0", "-5"} {
			t.Setenv("MCP_MAX_TOOL_OUTPUT_BYTES", v)
			if got := resolveMaxToolOutputBytes(); got != defaultMaxToolOutputBytes {
				t.Errorf("value %q: got %d, want default %d", v, got, defaultMaxToolOutputBytes)
			}
		}
	})
}
