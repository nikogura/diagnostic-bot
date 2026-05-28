package auth

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// listSource is the file-backed allowlist helper shared by OIDCAuth and
// GoogleAuth. It holds the mtime-keyed parse cache plus the test-visible
// hit/miss counters and exposes a single `current` method.
//
// Both Auth structs embed this anonymously, so the cache fields and
// counters surface as promoted fields (a.listCache, a.listStatCount,
// etc.) and existing tests reach them without an extra accessor hop.
//
// Why embedded rather than a free function on (cache, static, path):
// keeps the per-Auth process state (cache + counters) bundled with the
// method that mutates it, and keeps the call sites in validate*
// readable — `a.current(path, static)` rather than threading the cache
// as an argument.
type listSource struct {
	listCacheMu sync.RWMutex
	listCache   map[string]listCacheEntry

	// listStatCount and listReadCount are test-visible accounting for
	// the cache. Production code never reads them; the per-package
	// _test.go reads them to verify the (stat-on-every-call,
	// read-only-on-mtime-change) contract.
	listStatCount atomic.Int64
	listReadCount atomic.Int64
}

// listCacheEntry memoizes a parsed file. mtime is the value reported
// by os.Stat at parse time; equality against a fresh stat tells us
// whether to re-read.
type listCacheEntry struct {
	mtime time.Time
	list  []string
}

// current returns the active allowlist for one axis. If filePath is
// empty, returns the static slice (env-var path, unchanged behavior).
// Otherwise it stats the file: on unchanged mtime returns the parsed
// cache entry; on changed mtime re-reads and re-parses. A file that
// can't be stat'd or read returns the underlying error so the caller
// can fail closed and emit an ERROR log. An empty file returns a nil
// list (caller treats as "no restriction on this axis", matching the
// env-var semantic).
//
// Threading: the stat happens every call to detect ConfigMap atomic
// swaps within ~60s of edit. A RLock protects the common cache-hit
// path so concurrent Authenticate calls don't serialize. The full
// Lock is only taken on a real mtime change (rare, after a ConfigMap
// update).
func (s *listSource) current(filePath string, static []string) (list []string, err error) {
	if filePath == "" {
		list = static
		return list, err
	}

	s.listStatCount.Add(1)
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		err = statErr
		return list, err
	}
	mtime := info.ModTime()

	s.listCacheMu.RLock()
	entry, hit := s.listCache[filePath]
	s.listCacheMu.RUnlock()
	if hit && entry.mtime.Equal(mtime) {
		list = entry.list
		return list, err
	}

	s.listReadCount.Add(1)
	data, readErr := os.ReadFile(filePath) //nolint:gosec // filePath is operator-supplied via env var; opening it is the point
	if readErr != nil {
		err = readErr
		return list, err
	}
	list = splitList(string(data))

	s.listCacheMu.Lock()
	s.listCache[filePath] = listCacheEntry{mtime: mtime, list: list}
	s.listCacheMu.Unlock()
	return list, err
}

// splitList parses an allowlist value with the same separator rules as
// cmd/bot's splitCSV: commas or any Unicode whitespace separate
// entries, empties are dropped. Lifted here so file contents (newline-
// per-entry from a YAML `|-` block scalar in a ConfigMap) and env-var
// contents (comma-separated) parse identically.
func splitList(v string) (out []string) {
	out = strings.FieldsFunc(v, isListSeparator)
	if len(out) == 0 {
		out = nil
	}
	return out
}

// isListSeparator is the splitList predicate, extracted so namedreturns
// doesn't flag an inline closure with an unnamed bool return.
func isListSeparator(r rune) (isSep bool) {
	isSep = r == ',' || unicode.IsSpace(r)
	return isSep
}
