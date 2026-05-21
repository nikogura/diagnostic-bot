// Package buildconfig validates the repository's build configuration.
//
// These tests guard against silent multi-arch breakage: if Dockerfile ARGs
// like TARGETARCH carry a hard-coded default, a Docker build invoked without
// `docker buildx build --platform ...` will silently produce a binary for
// that default architecture while the manifest still tags variants per
// platform. The result is a published arm64 manifest whose ELF is x86_64
// (see issue #15, v0.0.42).
package buildconfig

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot walks up from the test file directory to find the Dockerfile.
func repoRoot(t *testing.T) (root string) {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for range 6 {
		_, statErr := os.Stat(filepath.Join(dir, "Dockerfile"))
		if statErr == nil {
			root = dir
			return root
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("Dockerfile not found walking up from test directory")
	return root
}

// TestDockerfileTargetArgsHaveNoDefaults asserts that TARGETOS and TARGETARCH
// are declared without default values. With a default, a non-buildx
// `docker build` silently produces a wrong-arch binary that still rides
// inside a multi-platform manifest tag — the v0.0.42 regression.
func TestDockerfileTargetArgsHaveNoDefaults(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dockerfile := filepath.Join(root, "Dockerfile")

	f, err := os.Open(dockerfile)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = f.Close()
	})

	// Match `ARG NAME=value` lines. Captures the value (may be empty).
	argRE := regexp.MustCompile(`^\s*ARG\s+([A-Za-z_][A-Za-z0-9_]*)\s*(=(.*))?\s*$`)

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	require.NoError(t, scanner.Err())

	type argDecl struct {
		name        string
		hasDefault  bool
		defaultText string
		lineNumber  int
	}

	var decls []argDecl
	for i, line := range lines {
		// Strip trailing comments.
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		m := argRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		decls = append(decls, argDecl{
			name:        m[1],
			hasDefault:  m[2] != "",
			defaultText: strings.TrimSpace(m[3]),
			lineNumber:  i + 1,
		})
	}

	targets := map[string]bool{"TARGETOS": false, "TARGETARCH": false}
	for _, d := range decls {
		if _, watched := targets[d.name]; !watched {
			continue
		}
		targets[d.name] = true
		assert.Falsef(t, d.hasDefault,
			"Dockerfile line %d: ARG %s must not have a default (found %q). "+
				"A default lets `docker build` (without buildx --platform) "+
				"silently produce a wrong-arch binary that still ships under "+
				"a multi-arch manifest tag. Drop the default so the build "+
				"fails loudly when invoked outside buildx.",
			d.lineNumber, d.name, d.defaultText)
	}

	for name, found := range targets {
		assert.Truef(t, found, "Dockerfile is expected to declare ARG %s (without a default)", name)
	}
}
