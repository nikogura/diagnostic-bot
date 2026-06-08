package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// defaultWhoisBaseURL is the ip-api.com geolocation endpoint (free, no auth).
const defaultWhoisBaseURL = "http://ip-api.com/json"

// whoisResponseLimit caps how much of the whois response body is read.
const whoisResponseLimit = 64 * 1024

// WhoisClient resolves IP geolocation/ISP/ASN over HTTP. It is independent of
// the Kubernetes client — a whois lookup needs nothing from the cluster.
type WhoisClient struct {
	httpClient *http.Client
	baseURL    string
	logger     *slog.Logger
}

// NewWhoisClient returns a whois client with a sane HTTP timeout.
func NewWhoisClient(logger *slog.Logger) (client *WhoisClient) {
	client = &WhoisClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    defaultWhoisBaseURL,
		logger:     logger,
	}

	return client
}

// Lookup resolves geolocation, ISP, and ASN for an IP address via ip-api.com.
func (c *WhoisClient) Lookup(ctx context.Context, ipAddress string) (result string, err error) {
	c.logger.InfoContext(ctx, "performing whois lookup", slog.String("ip", ipAddress))

	// Validate before building the URL: the IP is model-supplied, and an
	// unvalidated value would let it manipulate the request URL.
	if net.ParseIP(ipAddress) == nil {
		err = fmt.Errorf("invalid IP address: %q", ipAddress)
		return result, err
	}

	url := fmt.Sprintf("%s/%s?fields=status,message,country,countryCode,region,regionName,city,isp,org,as,query",
		c.baseURL, ipAddress)

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if reqErr != nil {
		err = fmt.Errorf("building whois request: %w", reqErr)
		return result, err
	}

	resp, doErr := c.httpClient.Do(req)
	if doErr != nil {
		err = fmt.Errorf("executing whois lookup: %w", doErr)
		return result, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, whoisResponseLimit))
	if readErr != nil {
		err = fmt.Errorf("reading whois response: %w", readErr)
		return result, err
	}

	result, err = formatWhoisResult(ipAddress, body)
	return result, err
}

// formatWhoisResult turns the ip-api JSON response into a readable summary.
func formatWhoisResult(ipAddress string, body []byte) (result string, err error) {
	var data map[string]interface{}

	err = json.Unmarshal(body, &data)
	if err != nil {
		// Surface the raw body alongside the parse error.
		result = string(body)
		err = fmt.Errorf("parsing whois response: %w", err)
		return result, err
	}

	status, _ := data["status"].(string)
	if status != "success" {
		message, _ := data["message"].(string)
		result = fmt.Sprintf("Whois lookup failed for %s: %s\n", ipAddress, message)
		return result, err
	}

	var builder strings.Builder

	fmt.Fprintf(&builder, "Whois lookup for %s:\n\n", ipAddress)
	fmt.Fprintf(&builder, "Country: %s (%s)\n", data["country"], data["countryCode"])
	fmt.Fprintf(&builder, "Region: %s (%s)\n", data["regionName"], data["region"])
	fmt.Fprintf(&builder, "City: %s\n", data["city"])
	fmt.Fprintf(&builder, "ISP: %s\n", data["isp"])
	fmt.Fprintf(&builder, "Organization: %s\n", data["org"])
	fmt.Fprintf(&builder, "ASN: %s\n", data["as"])

	result = builder.String()
	return result, err
}
