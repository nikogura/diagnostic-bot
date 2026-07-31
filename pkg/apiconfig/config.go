package apiconfig

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultMethod    = http.MethodGet
	defaultParamType = "string"
)

// APIConfig represents a third-party API configuration loaded from YAML.
type APIConfig struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	BaseURL     string            `yaml:"base_url"`
	Auth        AuthConfig        `yaml:"auth"`
	Endpoints   []Endpoint        `yaml:"endpoints"`
	RateLimit   RateLimitConfig   `yaml:"rate_limit"`
	Defaults    DefaultsConfig    `yaml:"defaults"`
	Headers     map[string]string `yaml:"headers"`
}

// Auth type constants.
const (
	AuthTypeBearer = "bearer"
	AuthTypeHeader = "header"
)

// AuthConfig defines how to authenticate with the API.
type AuthConfig struct {
	Type     string `yaml:"type"`      // "bearer", "header", "none"
	TokenEnv string `yaml:"token_env"` // env var name for bearer token
	Header   string `yaml:"header"`    // custom header name (for type "header")
	Prefix   string `yaml:"prefix"`    // prefix before token value (e.g. "Bearer")
}

// Endpoint defines a single API endpoint.
type Endpoint struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Method       string   `yaml:"method"` // GET only for now
	Path         string   `yaml:"path"`   // e.g. /api/v2/wallet/{wallet_id}
	Params       []Param  `yaml:"params"`
	RedactFields []string `yaml:"redact_fields"`
}

// Param defines a parameter for an endpoint.
type Param struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"` // "string", "integer", "boolean"
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	In          string `yaml:"in"`       // "path", "query" (default: "query")
	Validate    string `yaml:"validate"` // regex pattern for validation
}

// RateLimitConfig controls rate limiting behavior.
type RateLimitConfig struct {
	MaxConcurrent int  `yaml:"max_concurrent"` // semaphore size (default: 5)
	RetryOn429    bool `yaml:"retry_on_429"`   // honor Retry-After (default: true)
	MaxRetries    int  `yaml:"max_retries"`    // max 429 retries (default: 3)
}

// DefaultsConfig provides default values for query parameters.
type DefaultsConfig struct {
	Limit    int `yaml:"limit"`     // default limit for list endpoints
	MaxLimit int `yaml:"max_limit"` // server-enforced max limit
}

// LoadConfigs loads all API configuration YAML files from the given directory.
func LoadConfigs(dir string, logger *slog.Logger) (configs []*APIConfig, err error) {
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			logger.Info("API config directory does not exist, skipping",
				slog.String("dir", dir))
			return configs, err
		}
		err = fmt.Errorf("reading API config directory %s: %w", dir, readErr)
		return configs, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dir, name)

		var config *APIConfig
		config, err = loadSingleConfig(path)
		if err != nil {
			logger.Warn("Failed to load API config",
				slog.String("file", path),
				slog.String("error", err.Error()))
			err = nil // non-fatal, continue loading others
			continue
		}

		// Check if the auth token env var is set
		if config.Auth.TokenEnv != "" && os.Getenv(config.Auth.TokenEnv) == "" {
			logger.Info("API config skipped — auth token env var not set",
				slog.String("api", config.Name),
				slog.String("env_var", config.Auth.TokenEnv))
			continue
		}

		err = validateConfig(config)
		if err != nil {
			logger.Warn("Invalid API config",
				slog.String("file", path),
				slog.String("error", err.Error()))
			err = nil
			continue
		}

		applyDefaults(config)

		logger.Info("Loaded API config",
			slog.String("api", config.Name),
			slog.Int("endpoints", len(config.Endpoints)))

		configs = append(configs, config)
	}

	return configs, err
}

func loadSingleConfig(path string) (config *APIConfig, err error) {
	var data []byte

	data, err = os.ReadFile(path)
	if err != nil {
		err = fmt.Errorf("reading file: %w", err)
		return config, err
	}

	config = &APIConfig{}

	err = yaml.Unmarshal(data, config)
	if err != nil {
		err = fmt.Errorf("parsing YAML: %w", err)
		return config, err
	}

	return config, err
}

func validateConfig(config *APIConfig) (err error) {
	if config.Name == "" {
		err = errors.New("API config missing required field: name")
		return err
	}

	if config.BaseURL == "" {
		err = fmt.Errorf("API config %q missing required field: base_url", config.Name)
		return err
	}

	if len(config.Endpoints) == 0 {
		err = fmt.Errorf("API config %q has no endpoints", config.Name)
		return err
	}

	for i, ep := range config.Endpoints {
		if ep.Name == "" {
			err = fmt.Errorf("API config %q endpoint[%d] missing name", config.Name, i)
			return err
		}
		if ep.Path == "" {
			err = fmt.Errorf("API config %q endpoint %q missing path", config.Name, ep.Name)
			return err
		}
		if ep.Method != "" && !isKnownMethod(strings.ToUpper(ep.Method)) {
			err = fmt.Errorf("API config %q endpoint %q has unsupported method %q (want GET, POST, PUT, PATCH, or DELETE)", config.Name, ep.Name, ep.Method)
			return err
		}
	}

	return err
}

// isKnownMethod reports whether m (already upper-cased) is an HTTP method the
// generic client supports. Whether a non-GET method is actually *permitted* at
// runtime is a separate, deployment-level decision (API_ALLOWED_METHODS); this
// only rejects garbage like "FROBNICATE" at config-load time.
func isKnownMethod(m string) (known bool) {
	switch m {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		known = true
	}

	return known
}

func applyDefaults(config *APIConfig) {
	if config.RateLimit.MaxConcurrent <= 0 {
		config.RateLimit.MaxConcurrent = 5
	}

	if config.RateLimit.MaxRetries <= 0 {
		config.RateLimit.MaxRetries = 3
	}

	if config.Defaults.Limit <= 0 {
		config.Defaults.Limit = 25
	}

	if config.Defaults.MaxLimit <= 0 {
		config.Defaults.MaxLimit = 100
	}

	for i := range config.Endpoints {
		if config.Endpoints[i].Method == "" {
			config.Endpoints[i].Method = defaultMethod
		} else {
			// Normalize so downstream method comparisons (allowlist, write
			// classification, the client's request builder) are case-stable.
			config.Endpoints[i].Method = strings.ToUpper(config.Endpoints[i].Method)
		}
		for j := range config.Endpoints[i].Params {
			if config.Endpoints[i].Params[j].In == "" {
				config.Endpoints[i].Params[j].In = "query"
			}
			if config.Endpoints[i].Params[j].Type == "" {
				config.Endpoints[i].Params[j].Type = defaultParamType
			}
		}
	}
}
