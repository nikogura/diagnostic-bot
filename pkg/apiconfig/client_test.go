package apiconfig

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestAPIClient_SuccessPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Bearer auth, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"wallets":[]}`))
	}))
	defer server.Close()

	t.Setenv("TEST_TOKEN", "test-token")

	config := &APIConfig{
		Name:    "test",
		BaseURL: server.URL,
		Auth:    AuthConfig{Type: AuthTypeBearer, TokenEnv: "TEST_TOKEN"},
		Endpoints: []Endpoint{
			{Name: "list", Method: "GET", Path: "/wallets"},
		},
		RateLimit: RateLimitConfig{MaxConcurrent: 5, MaxRetries: 3},
		Defaults:  DefaultsConfig{Limit: 25, MaxLimit: 100},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := NewAPIClient(config, logger)

	result, err := client.Execute(context.Background(), "list", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != `{"wallets":[]}` {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestAPIClient_429Retry(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := attempts.Add(1)
		if count <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`rate limited`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	t.Setenv("RETRY_TOKEN", "tok")

	config := &APIConfig{
		Name:    "retrytest",
		BaseURL: server.URL,
		Auth:    AuthConfig{Type: AuthTypeBearer, TokenEnv: "RETRY_TOKEN"},
		Endpoints: []Endpoint{
			{Name: "get", Method: "GET", Path: "/data"},
		},
		RateLimit: RateLimitConfig{MaxConcurrent: 5, RetryOn429: true, MaxRetries: 3},
		Defaults:  DefaultsConfig{Limit: 25, MaxLimit: 100},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := NewAPIClient(config, logger)

	result, err := client.Execute(context.Background(), "get", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}

	if result != `{"ok":true}` {
		t.Errorf("unexpected result: %s", result)
	}

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts (2 retries + 1 success), got %d", attempts.Load())
	}
}

func TestAPIClient_429ExhaustedRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limited`))
	}))
	defer server.Close()

	t.Setenv("EXHAUST_TOKEN", "tok")

	config := &APIConfig{
		Name:    "exhausttest",
		BaseURL: server.URL,
		Auth:    AuthConfig{Type: AuthTypeBearer, TokenEnv: "EXHAUST_TOKEN"},
		Endpoints: []Endpoint{
			{Name: "get", Method: "GET", Path: "/data"},
		},
		RateLimit: RateLimitConfig{MaxConcurrent: 5, RetryOn429: true, MaxRetries: 2},
		Defaults:  DefaultsConfig{Limit: 25, MaxLimit: 100},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := NewAPIClient(config, logger)

	_, err := client.Execute(context.Background(), "get", map[string]interface{}{})
	if err == nil {
		t.Error("expected error after exhausting retries")
	}
}

func TestAPIClient_PathParamValidation(t *testing.T) {
	t.Parallel()

	config := &APIConfig{
		Name:    "validtest",
		BaseURL: "https://example.com",
		Endpoints: []Endpoint{
			{
				Name:   "get_wallet",
				Method: "GET",
				Path:   "/wallet/{wallet_id}",
				Params: []Param{
					{Name: "wallet_id", Required: true, In: "path", Validate: "[a-f0-9]{24,}"},
				},
			},
		},
		RateLimit: RateLimitConfig{MaxConcurrent: 5, MaxRetries: 1},
		Defaults:  DefaultsConfig{Limit: 25, MaxLimit: 100},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := NewAPIClient(config, logger)

	// Path traversal should fail before any HTTP request
	_, err := client.Execute(context.Background(), "get_wallet", map[string]interface{}{
		"wallet_id": "../etc/passwd",
	})
	if err == nil {
		t.Error("expected error for path traversal")
	}

	// Invalid format should fail
	_, err = client.Execute(context.Background(), "get_wallet", map[string]interface{}{
		"wallet_id": "UPPERCASE_NOT_ALLOWED",
	})
	if err == nil {
		t.Error("expected error for invalid wallet_id format")
	}
}

func TestAPIClient_LimitEnforcement(t *testing.T) {
	var receivedLimit string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedLimit = r.URL.Query().Get("limit")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	t.Setenv("LIMIT_TOKEN", "tok")

	config := &APIConfig{
		Name:    "limittest",
		BaseURL: server.URL,
		Auth:    AuthConfig{Type: AuthTypeBearer, TokenEnv: "LIMIT_TOKEN"},
		Endpoints: []Endpoint{
			{Name: "list", Method: "GET", Path: "/items"},
		},
		RateLimit: RateLimitConfig{MaxConcurrent: 5, MaxRetries: 1},
		Defaults:  DefaultsConfig{Limit: 25, MaxLimit: 50},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := NewAPIClient(config, logger)

	// Default limit applied
	_, err := client.Execute(context.Background(), "list", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedLimit != "25" {
		t.Errorf("expected default limit 25, got %q", receivedLimit)
	}

	// Excessive limit capped
	_, err = client.Execute(context.Background(), "list", map[string]interface{}{
		"limit": float64(999),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	limitInt, _ := strconv.Atoi(receivedLimit)
	if limitInt > 50 {
		t.Errorf("expected limit capped at 50, got %d", limitInt)
	}
}

func TestAPIClient_RedactFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"email":"secret@test.com","id":"abc"}`))
	}))
	defer server.Close()

	t.Setenv("REDACT_TOKEN", "tok")

	config := &APIConfig{
		Name:    "redacttest",
		BaseURL: server.URL,
		Auth:    AuthConfig{Type: AuthTypeBearer, TokenEnv: "REDACT_TOKEN"},
		Endpoints: []Endpoint{
			{Name: "get", Method: "GET", Path: "/data", RedactFields: []string{"email"}},
		},
		RateLimit: RateLimitConfig{MaxConcurrent: 5, MaxRetries: 1},
		Defaults:  DefaultsConfig{Limit: 25, MaxLimit: 100},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := NewAPIClient(config, logger)

	result, err := client.Execute(context.Background(), "get", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == "" {
		t.Fatal("empty result")
	}

	// Should contain [redacted] for email
	if !contains(result, "[redacted]") {
		t.Errorf("expected [redacted] in result, got: %s", result)
	}

	// Should preserve non-sensitive fields
	if !contains(result, "abc") {
		t.Errorf("expected id 'abc' preserved in result, got: %s", result)
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		header  string
		wantMin int // minimum seconds
		wantMax int // maximum seconds
	}{
		{"empty", "", 1, 3},
		{"zero-seconds", "0", 0, 1},
		{"five-seconds", "5", 4, 6},
		{"clamped-to-max", "999", 29, 31},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			delay := parseRetryAfter(tt.header)
			seconds := int(delay.Seconds())
			if seconds < tt.wantMin || seconds > tt.wantMax {
				t.Errorf("parseRetryAfter(%q) = %v, expected %d-%ds", tt.header, delay, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func contains(s string, sub string) (found bool) {
	found = len(s) >= len(sub)
	if !found {
		return found
	}

	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			found = true
			return found
		}
	}

	found = false
	return found
}

// TestNewAPIClientIsolatesTransport guards the per-client Transport
// pattern. nil falls back to http.DefaultTransport (package-global) and
// parallel tests can yank idle connections from unrelated requests —
// see pkg/mcp/tempo.go's NewTempoClient comment for the original failure.
func TestNewAPIClientIsolatesTransport(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := &APIConfig{
		Endpoints: []Endpoint{{Name: "ping", Method: "GET", Path: "/ping"}},
		RateLimit: RateLimitConfig{MaxConcurrent: 1, MaxRetries: 0},
		Defaults:  DefaultsConfig{Limit: 1, MaxLimit: 1},
	}
	client := NewAPIClient(cfg, logger)
	if client.http.Transport == nil {
		t.Fatal("client must have its own Transport; nil falls back to http.DefaultTransport")
	}
}
