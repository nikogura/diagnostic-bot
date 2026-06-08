package k8s

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestWhoisClient(server *httptest.Server) (client *WhoisClient) {
	client = &WhoisClient{
		httpClient: server.Client(),
		baseURL:    server.URL,
		logger:     slog.New(slog.DiscardHandler),
	}
	return client
}

func TestWhoisLookupFormatsSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"status":"success","country":"United Kingdom","countryCode":"GB",
			"regionName":"England","region":"ENG","city":"London",
			"isp":"Amazon.com","org":"AWS EC2 eu-west-2","as":"AS16509 Amazon","query":"13.40.254.161"
		}`))
	}))
	t.Cleanup(server.Close)

	result, err := newTestWhoisClient(server).Lookup(context.Background(), "13.40.254.161")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	for _, want := range []string{"United Kingdom", "GB", "London", "Amazon.com", "AWS EC2 eu-west-2", "AS16509 Amazon"} {
		if !strings.Contains(result, want) {
			t.Errorf("result missing %q\ngot:\n%s", want, result)
		}
	}
}

func TestWhoisLookupReportsFailedStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"fail","message":"reserved range","query":"10.0.0.1"}`))
	}))
	t.Cleanup(server.Close)

	result, err := newTestWhoisClient(server).Lookup(context.Background(), "10.0.0.1")
	if err != nil {
		t.Fatalf("a failed-status payload is not a transport error: %v", err)
	}

	if !strings.Contains(result, "Whois lookup failed") || !strings.Contains(result, "reserved range") {
		t.Errorf("expected a failed-lookup message, got: %q", result)
	}
}

func TestWhoisLookupRejectsInvalidIP(t *testing.T) {
	t.Parallel()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	t.Cleanup(server.Close)

	_, err := newTestWhoisClient(server).Lookup(context.Background(), "not-an-ip; rm -rf /")
	if err == nil {
		t.Fatal("expected an error for an invalid IP address")
	}

	if called {
		t.Error("invalid IP must be rejected before any HTTP request is made")
	}
}
