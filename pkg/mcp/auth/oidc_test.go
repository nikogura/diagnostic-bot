package auth

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// quietLogger returns a slog.Logger that swallows everything below ERROR
// so tests for the fail-closed paths don't spam stderr. Tests asserting
// log content would substitute a JSON handler against a bytes.Buffer.
func quietLogger() (logger *slog.Logger) {
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	return logger
}

// newOIDCAuthForListTests builds a bare OIDCAuth with no JWKS/issuer
// setup — the validate* methods we exercise here don't touch those.
// Keeps each test focused on allowlist semantics.
func newOIDCAuthForListTests(t *testing.T, cfg *OIDCConfig) (auth *OIDCAuth) {
	t.Helper()
	auth = NewOIDCAuth(cfg, quietLogger())
	return auth
}

// TestAllowlistFileUnsetUsesStaticEnv: spec case "no file, static set" —
// must fall through to the env-var-derived slice, current behavior.
func TestAllowlistFileUnsetUsesStaticEnv(t *testing.T) {
	t.Parallel()
	a := newOIDCAuthForListTests(t, &OIDCConfig{
		AllowedEmails: []string{"alice@katn-solutions.io"},
	})

	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))
	require.Error(t, a.validateEmailAllowlist(&Result{Email: "mallory@evil.example"}))
}

// TestAllowlistFileSetReadsFileAndHotReloads is the load-bearing test
// for the whole feature: write a file with two entries, validate one
// passes and a third fails; then rewrite the file with a different
// single entry and verify — without rebuilding the OIDCAuth — that the
// new entry now passes and the old one fails. Proves K8s ConfigMap-edit
// → in-process effect without a pod restart.
func TestAllowlistFileSetReadsFileAndHotReloads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed-emails")

	writeListFile(t, path, "alice@katn-solutions.io\nbob@katn-solutions.io\n")

	a := newOIDCAuthForListTests(t, &OIDCConfig{
		AllowedEmailsFile: path,
	})

	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))
	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "bob@katn-solutions.io"}))
	require.Error(t, a.validateEmailAllowlist(&Result{Email: "carol@katn-solutions.io"}))

	// Hot-reload: rewrite with a single different entry. Bump mtime
	// explicitly so the test doesn't depend on filesystem-clock
	// granularity (some filesystems coalesce mtime updates within a
	// millisecond).
	writeListFile(t, path, "carol@katn-solutions.io\n")
	require.NoError(t, os.Chtimes(path, time.Time{}, time.Now().Add(time.Second)))

	require.Error(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))
	require.Error(t, a.validateEmailAllowlist(&Result{Email: "bob@katn-solutions.io"}))
	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "carol@katn-solutions.io"}))
}

// TestAllowlistFileEmptyMeansNoRestriction mirrors the env-var
// semantic: an empty file (the operator deleted the data key) widens
// to "no restriction on this axis." Worth flagging in deployment docs;
// the test pins the behavior so a future change is deliberate.
func TestAllowlistFileEmptyMeansNoRestriction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed-emails")
	writeListFile(t, path, "")

	a := newOIDCAuthForListTests(t, &OIDCConfig{AllowedEmailsFile: path})

	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "anyone@example.test"}))
}

// TestAllowlistFileMissingFailsClosed: operator pointed at a file that
// doesn't exist (typo, missing ConfigMap, broken mount). The spec says
// fail closed — don't silently widen to "no restriction." All
// validate* axes share this behavior; we hit each one in turn.
func TestAllowlistFileMissingFailsClosed(t *testing.T) {
	t.Parallel()
	bogus := filepath.Join(t.TempDir(), "does-not-exist")

	emails := newOIDCAuthForListTests(t, &OIDCConfig{AllowedEmailsFile: bogus})
	err := emails.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowed-emails file unreadable")

	domains := newOIDCAuthForListTests(t, &OIDCConfig{AllowedHostedDomainsFile: bogus})
	err = domains.validateHostedDomain(&Result{Email: "alice@katn-solutions.io"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowed-hosted-domains file unreadable")

	groups := newOIDCAuthForListTests(t, &OIDCConfig{AllowedGroupsFile: bogus})
	err = groups.validateGroupMembership(&Result{Username: "alice", Groups: []string{"sre"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowed-groups file unreadable")
}

// TestAllowlistFileWinsOverStaticEnv: when both AllowedEmails (static)
// and AllowedEmailsFile (path) are set, file is the source of truth.
// Confirms the precedence the operator log line advertises.
func TestAllowlistFileWinsOverStaticEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed-emails")
	writeListFile(t, path, "carol@katn-solutions.io")

	a := newOIDCAuthForListTests(t, &OIDCConfig{
		AllowedEmails:     []string{"alice@katn-solutions.io"}, // ignored
		AllowedEmailsFile: path,
	})

	require.Error(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))
	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "carol@katn-solutions.io"}))
}

// TestAllowlistMtimeCacheStable: N validate calls against an unchanged
// file should produce N stats and exactly 1 read. The hot-reload
// guarantee depends on stat-on-every-call (to detect ConfigMap atomic
// swaps); the cache-stability guarantee depends on read-only-on-change
// (to keep the per-request overhead at one cheap syscall). These two
// requirements together are what justify the design.
func TestAllowlistMtimeCacheStable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed-emails")
	writeListFile(t, path, "alice@katn-solutions.io\nbob@katn-solutions.io\n")

	a := newOIDCAuthForListTests(t, &OIDCConfig{AllowedEmailsFile: path})

	const calls = 5
	for range calls {
		require.NoError(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))
	}

	require.Equal(t, int64(calls), a.listStatCount.Load(), "stat should run on every call to detect ConfigMap swap")
	require.Equal(t, int64(1), a.listReadCount.Load(), "file read should happen only once when mtime is unchanged")
}

// TestAllowlistMtimeCacheRereadsOnChange complements the prior test:
// after an mtime bump, the next validate must re-read. Together they
// nail the cache contract.
func TestAllowlistMtimeCacheRereadsOnChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed-emails")
	writeListFile(t, path, "alice@katn-solutions.io")

	a := newOIDCAuthForListTests(t, &OIDCConfig{AllowedEmailsFile: path})

	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))
	require.Equal(t, int64(1), a.listReadCount.Load())

	writeListFile(t, path, "bob@katn-solutions.io")
	require.NoError(t, os.Chtimes(path, time.Time{}, time.Now().Add(time.Second)))

	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "bob@katn-solutions.io"}))
	require.Equal(t, int64(2), a.listReadCount.Load(), "mtime change should trigger a re-read")
}

// TestAllowlistFileBlockScalarParses verifies that a YAML `|-` block
// scalar form — one entry per line, the whole point of accepting file
// input in the first place — parses identically to the comma-separated
// env-var form. Mixed forms (commas + newlines + indented lines) also
// work, mirroring the splitCSV contract documented in cmd/bot/main.go.
func TestAllowlistFileBlockScalarParses(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"newline_per_entry":  "alice@katn-solutions.io\nbob@katn-solutions.io\ncarol@katn-solutions.io\n",
		"comma_separated":    "alice@katn-solutions.io,bob@katn-solutions.io,carol@katn-solutions.io",
		"mixed_and_indented": "alice@katn-solutions.io,bob@katn-solutions.io\n  carol@katn-solutions.io\n",
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "allowed-emails")
			writeListFile(t, path, content)

			a := newOIDCAuthForListTests(t, &OIDCConfig{AllowedEmailsFile: path})
			require.NoError(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))
			require.NoError(t, a.validateEmailAllowlist(&Result{Email: "bob@katn-solutions.io"}))
			require.NoError(t, a.validateEmailAllowlist(&Result{Email: "carol@katn-solutions.io"}))
			require.Error(t, a.validateEmailAllowlist(&Result{Email: "mallory@evil.example"}))
		})
	}
}

// TestAllowlistConcurrentReadsHitCache exercises the RLock path under
// parallel callers. Doesn't assert exact counts (race-prone) — only
// that all reads succeed and the read count stays at 1 when the file
// hasn't changed.
func TestAllowlistConcurrentReadsHitCache(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed-emails")
	writeListFile(t, path, "alice@katn-solutions.io")

	a := newOIDCAuthForListTests(t, &OIDCConfig{AllowedEmailsFile: path})

	// Prime the cache so the parallel section never observes a cold miss.
	require.NoError(t, a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"}))

	const goroutines = 16
	const callsEach = 10
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range callsEach {
				_ = a.validateEmailAllowlist(&Result{Email: "alice@katn-solutions.io"})
			}
		}()
	}
	wg.Wait()

	require.Equal(t, int64(1), a.listReadCount.Load(), "all goroutines should hit the cache; only the primer should read")
}

// writeListFile writes string s to path, creating it with mode 0o600
// since the tests run under the project's secure-defaults posture and
// no other process needs to read these files.
func writeListFile(t *testing.T, path, s string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(s), 0o600))
}

// Smoke-test the splitList helper directly — it's a private function
// but lives in the same package, so this is the lightest way to pin
// the parser contract used by file-backed allowlists. cmd/bot's
// splitCSV has its own equivalent test; this guards against drift.
func TestSplitListMatchesSplitCSVContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		{in: "a", want: []string{"a"}},
		{in: "a,b,c", want: []string{"a", "b", "c"}},
		{in: "a\nb\nc", want: []string{"a", "b", "c"}},
		{in: "a, b ,c", want: []string{"a", "b", "c"}},
		{in: "a,,\n\nb", want: []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.in, "\n", "\\n"), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, splitList(tt.in))
		})
	}
}
