package authz

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Policy is a live authorization policy, safe for concurrent use. When backed
// by a file it is re-read on change (hot reload); a parse error on reload keeps
// the last good policy rather than failing open.
type Policy struct {
	logger *slog.Logger

	mu    sync.RWMutex
	cfg   *config
	path  string
	mtime time.Time
}

// LoadPolicy builds a Policy from a file path and/or inline YAML. Precedence: a
// non-empty path wins (and hot-reloads); otherwise inline is parsed once. With
// both empty it returns (nil, nil) — authorization is disabled and every tool
// is allowed. A parse failure returns an error; callers that must fail closed
// install DenyAll instead of running open.
func LoadPolicy(path, inline string, logger *slog.Logger) (policy *Policy, err error) {
	if path == "" && inline == "" {
		return policy, err
	}

	if path != "" {
		policy, err = loadFromFile(path, logger)
		return policy, err
	}

	var cfg *config
	cfg, err = parseConfig([]byte(inline))
	if err != nil {
		err = fmt.Errorf("parsing inline authz policy: %w", err)
		return policy, err
	}

	policy = &Policy{logger: logger, cfg: cfg}
	return policy, err
}

// DenyAll returns a policy that denies every tool for everyone. Callers install
// it when an authz file is configured but fails to load, so a broken policy
// fails closed instead of running with no authorization.
func DenyAll(logger *slog.Logger) (policy *Policy) {
	policy = &Policy{logger: logger, cfg: &config{Default: defaultDeny}}
	return policy
}

// loadFromFile reads, parses, and stamps the mtime of a policy file.
func loadFromFile(path string, logger *slog.Logger) (policy *Policy, err error) {
	var data []byte
	data, err = os.ReadFile(path) //nolint:gosec // path is operator-supplied via env var; reading it is the point
	if err != nil {
		err = fmt.Errorf("reading authz policy %q: %w", path, err)
		return policy, err
	}

	var cfg *config
	cfg, err = parseConfig(data)
	if err != nil {
		err = fmt.Errorf("parsing authz policy %q: %w", path, err)
		return policy, err
	}

	var info os.FileInfo
	info, err = os.Stat(path)
	if err != nil {
		err = fmt.Errorf("stat authz policy %q: %w", path, err)
		return policy, err
	}

	policy = &Policy{logger: logger, cfg: cfg, path: path, mtime: info.ModTime()}
	return policy, err
}

// parseConfig unmarshals and validates a policy document.
func parseConfig(data []byte) (cfg *config, err error) {
	var doc document
	err = yaml.Unmarshal(data, &doc)
	if err != nil {
		err = fmt.Errorf("invalid authz YAML: %w", err)
		return cfg, err
	}

	cfg = &doc.Authz
	err = validate(cfg)
	if err != nil {
		cfg = nil
		return cfg, err
	}

	return cfg, err
}

// Evaluate decides whether the principal may invoke tool, reloading the backing
// file first if it changed.
func (pol *Policy) Evaluate(p Principal, tool string) (decision Decision) {
	cfg := pol.current()
	if cfg == nil {
		decision = Decision{Allowed: false, Reason: "no policy loaded"}
		return decision
	}

	decision = evaluate(cfg, p, tool)
	return decision
}

// Capabilities partitions the candidate tool names into what the principal can
// dispatch on its front-end and what its roles grant elsewhere. It shares the
// assess core with Evaluate, so the "here" set matches dispatch exactly — the
// single source of truth for list_my_tools, catalog filtering, and denials.
func (pol *Policy) Capabilities(p Principal, candidates []string) (capability Capability) {
	cfg := pol.current()
	if cfg == nil {
		capability.Elsewhere = map[string][]string{}
		return capability
	}

	capability = capabilities(cfg, p, candidates)
	return capability
}

// current returns the active config, reloading from disk first if the backing
// file changed. On any reload error it logs and keeps the last good config.
func (pol *Policy) current() (cfg *config) {
	pol.mu.RLock()
	path := pol.path
	cached := pol.cfg
	mtime := pol.mtime
	pol.mu.RUnlock()

	if path == "" {
		cfg = cached
		return cfg
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		pol.logger.Warn("authz policy unreadable on reload; keeping last good policy",
			slog.String("path", path), slog.String("error", statErr.Error()))
		cfg = cached
		return cfg
	}

	if !info.ModTime().After(mtime) {
		cfg = cached
		return cfg
	}

	data, readErr := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if readErr != nil {
		pol.logger.Warn("authz policy read failed on reload; keeping last good policy",
			slog.String("path", path), slog.String("error", readErr.Error()))
		cfg = cached
		return cfg
	}

	fresh, parseErr := parseConfig(data)
	if parseErr != nil {
		pol.logger.Error("authz policy parse failed on reload; keeping last good policy",
			slog.String("path", path), slog.String("error", parseErr.Error()))
		cfg = cached
		return cfg
	}

	pol.mu.Lock()
	pol.cfg = fresh
	pol.mtime = info.ModTime()
	pol.mu.Unlock()

	pol.logger.Info("authz policy reloaded", slog.String("path", path))
	cfg = fresh
	return cfg
}
