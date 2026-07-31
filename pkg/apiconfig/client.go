package apiconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

const (
	defaultHTTPTimeout = 45 * time.Second
	maxResponseBytes   = 5 * 1024 * 1024 // 5MB
	maxRetryAfter      = 30 * time.Second
	defaultRetryDelay  = 2 * time.Second
)

// APIClient executes requests against a configured third-party API.
type APIClient struct {
	config    *APIConfig
	http      *http.Client
	semaphore chan struct{}
	logger    *slog.Logger
}

// NewAPIClient creates a client for the given API configuration.
func NewAPIClient(config *APIConfig, logger *slog.Logger) (client *APIClient) {
	client = &APIClient{
		config: config,
		http: &http.Client{
			Timeout: defaultHTTPTimeout,
			// Per-client Transport isolates the connection pool from
			// http.DefaultTransport (see pkg/mcp/tempo.go for context).
			Transport: &http.Transport{},
		},
		semaphore: make(chan struct{}, config.RateLimit.MaxConcurrent),
		logger:    logger,
	}

	return client
}

// Execute runs a request against the named endpoint with the given arguments.
func (c *APIClient) Execute(ctx context.Context, endpointName string, args map[string]interface{}) (result string, err error) {
	var endpoint *Endpoint

	endpoint, err = c.findEndpoint(endpointName)
	if err != nil {
		return result, err
	}

	var requestURL string
	var queryParams map[string]string
	var requestBody []byte

	requestURL, queryParams, requestBody, err = validateAndBuildURL(c.config.BaseURL, *endpoint, args)
	if err != nil {
		return result, err
	}

	// Apply limit defaults
	applyLimitDefaults(queryParams, args, c.config.Defaults)

	// Build the full URL with query params
	fullURL := buildFullURL(requestURL, queryParams)

	// Acquire semaphore
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		err = ctx.Err()
		return result, err
	}

	start := time.Now()

	var body []byte
	body, err = c.doRequestWithRetry(ctx, endpoint.Method, fullURL, requestBody, endpoint.Name)

	duration := time.Since(start)
	status := "success"
	if err != nil {
		status = "error"
	}

	metrics.RecordToolExecution(ctx, c.config.Name+"_"+endpointName, status)

	c.logger.InfoContext(ctx, "API request completed",
		slog.String("api", c.config.Name),
		slog.String("endpoint", endpointName),
		slog.String("status", status),
		slog.Duration("duration", duration))

	if err != nil {
		return result, err
	}

	// Redact PII fields
	if len(endpoint.RedactFields) > 0 {
		body, err = redactResponse(body, endpoint.RedactFields)
		if err != nil {
			return result, err
		}
	}

	// Pretty-print if requested
	body = maybePrettyPrint(body, args)

	result = string(body)
	return result, err
}

func (c *APIClient) findEndpoint(name string) (endpoint *Endpoint, err error) {
	for i := range c.config.Endpoints {
		if c.config.Endpoints[i].Name == name {
			endpoint = &c.config.Endpoints[i]
			return endpoint, err
		}
	}

	err = fmt.Errorf("endpoint %q not found in API %q", name, c.config.Name)
	return endpoint, err
}

func (c *APIClient) doRequestWithRetry(ctx context.Context, method, fullURL string, reqBody []byte, endpointName string) (body []byte, err error) {
	for attempt := range c.config.RateLimit.MaxRetries + 1 {
		var requestErr error

		body, requestErr = c.doSingleRequest(ctx, method, fullURL, reqBody)
		if requestErr == nil {
			err = nil
			return body, err
		}

		err = requestErr

		var reqErr *RequestError
		if !isRateLimitError(err, &reqErr) {
			return body, err
		}

		if attempt >= c.config.RateLimit.MaxRetries {
			return body, err
		}

		if !c.config.RateLimit.RetryOn429 {
			return body, err
		}

		delay := parseRetryAfter(reqErr.RetryAfter)

		c.logger.InfoContext(ctx, "Rate limited, retrying",
			slog.String("api", c.config.Name),
			slog.String("endpoint", endpointName),
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay))

		cancelErr := waitForRetry(ctx, delay)
		if cancelErr != nil {
			err = cancelErr
			return body, err
		}
	}

	return body, err
}

func waitForRetry(ctx context.Context, delay time.Duration) (err error) {
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		err = ctx.Err()
	}

	return err
}

func (c *APIClient) doSingleRequest(ctx context.Context, method, fullURL string, reqBody []byte) (body []byte, err error) {
	if method == "" {
		method = http.MethodGet
	}

	var req *http.Request
	var bodyReader io.Reader
	if reqBody != nil {
		bodyReader = bytes.NewReader(reqBody)
	}

	req, err = http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		err = fmt.Errorf("building request: %w", err)
		return body, err
	}

	c.applyAuth(req)
	c.applyHeaders(req)

	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	var resp *http.Response

	resp, err = c.http.Do(req)
	if err != nil {
		err = fmt.Errorf("executing request: %w", err)
		return body, err
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		err = fmt.Errorf("reading response: %w", err)
		return body, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		err = &RequestError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
		return body, err
	}

	return body, err
}

func (c *APIClient) applyAuth(req *http.Request) {
	switch c.config.Auth.Type {
	case AuthTypeBearer:
		token := os.Getenv(c.config.Auth.TokenEnv)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	case AuthTypeHeader:
		token := os.Getenv(c.config.Auth.TokenEnv)
		if token != "" {
			header := c.config.Auth.Header
			if header == "" {
				header = "Authorization"
			}
			prefix := c.config.Auth.Prefix
			if prefix != "" {
				token = prefix + " " + token
			}
			req.Header.Set(header, token)
		}
	}
}

func (c *APIClient) applyHeaders(req *http.Request) {
	for k, v := range c.config.Headers {
		// Expand env vars in header values
		expanded := os.ExpandEnv(v)
		req.Header.Set(k, expanded)
	}

	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
}

// RequestError represents an HTTP error response.
type RequestError struct {
	StatusCode int
	Body       string
	RetryAfter string
}

func (e *RequestError) Error() (msg string) {
	msg = fmt.Sprintf("API returned HTTP %d: %s", e.StatusCode, truncateBody(e.Body))
	return msg
}

func isRateLimitError(err error, target **RequestError) (isRateLimit bool) {
	reqErr, ok := err.(*RequestError)
	if !ok {
		return isRateLimit
	}

	*target = reqErr
	isRateLimit = reqErr.StatusCode == http.StatusTooManyRequests

	return isRateLimit
}

func parseRetryAfter(header string) (delay time.Duration) {
	delay = defaultRetryDelay

	if header == "" {
		return delay
	}

	// Try parsing as seconds
	seconds, parseErr := strconv.Atoi(header)
	if parseErr == nil {
		delay = time.Duration(seconds) * time.Second
		if delay > maxRetryAfter {
			delay = maxRetryAfter
		}
		return delay
	}

	// Try parsing as HTTP-date
	t, dateErr := http.ParseTime(header)
	if dateErr == nil {
		delay = time.Until(t)
		if delay < 0 {
			delay = defaultRetryDelay
		}
		if delay > maxRetryAfter {
			delay = maxRetryAfter
		}
	}

	return delay
}

func applyLimitDefaults(queryParams map[string]string, args map[string]interface{}, defaults DefaultsConfig) {
	_, exists := queryParams["limit"]
	if exists {
		// Enforce max limit
		limitVal, parseErr := strconv.Atoi(queryParams["limit"])
		if parseErr == nil && limitVal > defaults.MaxLimit {
			queryParams["limit"] = strconv.Itoa(defaults.MaxLimit)
		}

		return
	}

	// Check if limit was passed as a float64 from JSON
	if limitRaw, ok := args["limit"]; ok {
		if limitFloat, fOk := limitRaw.(float64); fOk {
			limit := int(limitFloat)
			if limit > defaults.MaxLimit {
				limit = defaults.MaxLimit
			}
			queryParams["limit"] = strconv.Itoa(limit)
			return
		}
	}

	// Apply default
	if defaults.Limit > 0 {
		queryParams["limit"] = strconv.Itoa(defaults.Limit)
	}
}

func buildFullURL(baseURL string, queryParams map[string]string) (fullURL string) {
	if len(queryParams) == 0 {
		fullURL = baseURL
		return fullURL
	}

	values := url.Values{}
	for k, v := range queryParams {
		values.Set(k, v)
	}

	fullURL = baseURL + "?" + values.Encode()
	return fullURL
}

func maybePrettyPrint(body []byte, args map[string]interface{}) (result []byte) {
	result = body

	prettyRaw, ok := args["pretty"]
	if !ok {
		return result
	}

	pretty, boolOk := prettyRaw.(bool)
	if !boolOk || !pretty {
		return result
	}

	var raw interface{}

	parseErr := json.Unmarshal(body, &raw)
	if parseErr != nil {
		return result
	}

	prettyBuf, marshalErr := json.MarshalIndent(raw, "", "  ")
	if marshalErr != nil {
		return result
	}

	result = prettyBuf

	return result
}

func truncateBody(body string) (truncated string) {
	truncated = strings.TrimSpace(body)

	if len(truncated) > 200 {
		truncated = truncated[:200] + "..."
	}

	return truncated
}
