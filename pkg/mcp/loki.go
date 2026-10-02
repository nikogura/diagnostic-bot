// Copyright © 2026 Nik Ogura <nik.ogura@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nikogura/diagnostic-bot/pkg/k8s"
)

// Tool name constants for the Loki label-discovery tools. toolLokiQuery is
// declared with the core tool names in server.go.
const (
	toolLokiLabelNames  = "loki_label_names"
	toolLokiLabelValues = "loki_label_values"
)

const (
	// maxLokiLabelItems bounds how many names or values are listed per tenant.
	// A high-cardinality label can carry thousands of values; past this the
	// caller is better served by narrowing with a selector.
	maxLokiLabelItems = 500

	// lokiUntenantedHeading titles the single answer of a Loki with no tenant
	// configuration, where there is no tenant to attribute it to.
	lokiUntenantedHeading = "All streams"

	argTenant = "tenant"
	argStart  = "start"
	argEnd    = "end"
	argQuery  = "query"
	argLabel  = "label"
)

// getLokiTools returns Loki-related tool definitions. When allowedTenants
// is non-empty (multi-tenant Loki, auth_enabled: true), the list is
// appended to each tool description so the calling LLM can discover which
// tenants are queryable, and each schema gains an optional tenant arg.
func getLokiTools(allowedTenants []string) (result []MCPTool) {
	result = []MCPTool{
		getLokiQueryTool(allowedTenants),
		getLokiLabelNamesTool(allowedTenants),
		getLokiLabelValuesTool(allowedTenants),
	}

	return result
}

// getLokiQueryTool defines loki_query.
func getLokiQueryTool(allowedTenants []string) (tool MCPTool) {
	// The description is read literally by the calling model: scoping it to
	// one log kind sends every other log lookup to a system that isn't there.
	description := "Query a Loki log aggregation backend using LogQL. Loki is general-purpose: " +
		"it holds whatever log streams the operator ships — commonly application, infrastructure, " +
		"ingress/WAF, and audit logs — and is not limited to any one kind. Do not infer what a " +
		"tenant holds from its name, and do not conclude a log source is unavailable or lives in " +
		"another system before querying here. When the stream labels are unknown, discover them " +
		"with the label tools instead of guessing, or search with a regex matcher or a line filter " +
		"over a short window. A log query returns the matching lines with their timestamps; a " +
		"metric query (count_over_time, rate, sum by) returns each series under its labels. Loki " +
		"may not be the only log backend on this deployment: when nothing matches, the result " +
		"names the other places you can look."

	properties := map[string]interface{}{
		argQuery: map[string]interface{}{
			"type": "string",
			"description": "LogQL query. Examples: application errors " +
				"'{service_name=\"my-service\"} |= \"error\"'; ingress/WAF 403s " +
				"'{namespace=\"ingress-nginx\"} |~ \"ModSecurity\" | json | transaction_response_http_code=\"403\"'; " +
				"unknown labels '{service_name=~\".*my-service.*\"}' or '{namespace=~\".+\"} |= \"my-service\"'; " +
				"log volume per stream 'sum by (service_name) (count_over_time({namespace=\"apps\"}[5m]))'.",
		},
		argStart: map[string]interface{}{
			"type":        "string",
			"description": "Start time as relative duration (e.g., '1h', '24h') or RFC3339 timestamp",
		},
		argEnd: map[string]interface{}{
			"type":        "string",
			"description": descEndTime,
		},
		"limit": map[string]interface{}{
			"type":        "integer",
			"description": "Maximum number of log entries to return (default: 100, recommended max: 500 to avoid token limits)",
		},
		"step": map[string]interface{}{
			"type":        "string",
			"description": "Resolution of a metric query as a duration (e.g., '30s', '5m'). Defaults to one twentieth of the time range. Ignored by log queries.",
		},
	}

	if len(allowedTenants) > 0 {
		properties[argTenant] = map[string]interface{}{
			"type":        "string",
			"description": "Loki tenant (X-Scope-OrgID) for this query. Pipe-delimited values request a multi-tenant read (e.g. 'monitoring|cloudtrail'). Omit to use the server's default tenant. Allowed values: " + strings.Join(allowedTenants, ", ") + ".",
		}
	}

	tool = MCPTool{
		Name:        toolLokiQuery,
		Description: withAllowedTenants(description, allowedTenants),
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": properties,
			"required":   []string{argQuery, argStart},
		},
	}

	return tool
}

// getLokiLabelNamesTool defines loki_label_names.
func getLokiLabelNamesTool(allowedTenants []string) (tool MCPTool) {
	description := "List the label names present on Loki log streams — how to find out how logs " +
		"are labelled on this deployment before writing a query."

	if len(allowedTenants) > 0 {
		description += " Omitting tenant inspects every allowed tenant and reports each separately, " +
			"which shows how each tenant's logs are labelled without guessing from tenant names."
	}

	tool = MCPTool{
		Name:        toolLokiLabelNames,
		Description: withAllowedTenants(description, allowedTenants),
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": lokiLabelProperties(allowedTenants),
		},
	}

	return tool
}

// getLokiLabelValuesTool defines loki_label_values.
func getLokiLabelValuesTool(allowedTenants []string) (tool MCPTool) {
	description := "List the values a label takes on Loki log streams — for example every " +
		"service_name or namespace that is shipping logs. Use it to find the exact selector for a " +
		"service before concluding its logs are not here."

	if len(allowedTenants) > 0 {
		description += " Omitting tenant inspects every allowed tenant and reports each separately, " +
			"which shows which tenant holds a given service without guessing from tenant names."
	}

	properties := lokiLabelProperties(allowedTenants)
	properties[argLabel] = map[string]interface{}{
		"type":        "string",
		"description": "Label name to list values for. Example: 'service_name', 'namespace', 'app', 'container'",
	}

	tool = MCPTool{
		Name:        toolLokiLabelValues,
		Description: withAllowedTenants(description, allowedTenants),
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": properties,
			"required":   []string{argLabel},
		},
	}

	return tool
}

// lokiLabelProperties returns the arguments the two label tools share.
func lokiLabelProperties(allowedTenants []string) (properties map[string]interface{}) {
	properties = map[string]interface{}{
		argStart: map[string]interface{}{
			"type":        "string",
			"description": "How far back to look, as a relative duration (e.g., '1h', '24h') or RFC3339 timestamp. Defaults to '" + k8s.DefaultLokiLabelWindow + "'. Widen it for streams that log rarely.",
		},
		argEnd: map[string]interface{}{
			"type":        "string",
			"description": descEndTime,
		},
		argQuery: map[string]interface{}{
			"type":        "string",
			"description": "Optional stream selector limiting which streams are considered. Example: '{namespace=\"apps\"}'",
		},
	}

	if len(allowedTenants) > 0 {
		properties[argTenant] = map[string]interface{}{
			"type":        "string",
			"description": "Loki tenant (X-Scope-OrgID) to inspect. Pipe-delimited values inspect several (e.g. 'monitoring|cloudtrail'). Omit to inspect every allowed tenant. Each tenant is reported separately. Allowed values: " + strings.Join(allowedTenants, ", ") + ".",
		}
	}

	return properties
}

// withAllowedTenants appends the tenant allowlist to a tool description, so a
// calling model can see which tenants exist. With no allowlist the description
// is returned unchanged.
func withAllowedTenants(description string, allowedTenants []string) (result string) {
	result = description

	if len(allowedTenants) > 0 {
		result = fmt.Sprintf("%s Allowed tenants: %s.", description, strings.Join(allowedTenants, ", "))
	}

	return result
}

// dispatchLokiTool routes Loki tools. handled reports whether toolName
// belonged to this family.
func (s *Server) dispatchLokiTool(ctx context.Context, toolName string, args map[string]interface{}) (result string, handled bool, err error) {
	handled = true

	switch toolName {
	case toolLokiQuery:
		result, err = s.executeLokiQuery(ctx, args)
	case toolLokiLabelNames:
		result, err = s.executeLokiLabelNames(ctx, args)
	case toolLokiLabelValues:
		result, err = s.executeLokiLabelValues(ctx, args)
	default:
		handled = false
	}

	return result, handled, err
}

// requireLoki reports an error when no Loki backend is configured.
func (s *Server) requireLoki() (err error) {
	if s.lokiClient == nil {
		err = errors.New("loki is not configured. Set the LOKI_ENDPOINT environment variable")
	}

	return err
}

// executeLokiQuery executes a Loki query.
func (s *Server) executeLokiQuery(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var queryResult k8s.QueryResult

	err = s.requireLoki()
	if err != nil {
		return result, err
	}

	query, _ := args[argQuery].(string)
	start, _ := args[argStart].(string)
	end, _ := args[argEnd].(string)
	step, _ := args["step"].(string)

	limit := 100
	if limitFloat, ok := args["limit"].(float64); ok {
		limit = int(limitFloat)
	}

	// Cap at 500 to avoid overwhelming Claude Code with data
	if limit > 500 {
		limit = 500
	}

	requestedTenant, _ := args[argTenant].(string)

	queryResult, err = s.lokiClient.Query(ctx, k8s.QueryRequest{
		Query:  query,
		Start:  start,
		End:    end,
		Limit:  limit,
		Step:   step,
		Tenant: requestedTenant,
	})
	if err != nil {
		return result, err
	}

	result = queryResult.FormatResultAsText()

	// An empty answer covers one tenant of one backend. Say what it did not
	// cover, so it is not read as "these logs do not exist".
	if queryResult.Empty() {
		result += s.emptyLogSearchNote(ctx, logFamilyLoki, s.lokiQueryLeads(ctx, requestedTenant))
	}

	return result, err
}

// lokiQueryLeads lists what an empty loki_query has not yet tried within Loki:
// the other allowed tenants, and discovering how the logs are labelled.
func (s *Server) lokiQueryLeads(ctx context.Context, requestedTenant string) (leads []string) {
	unsearched := s.lokiClient.UnsearchedTenants(requestedTenant)
	if len(unsearched) > 0 {
		leads = append(leads, fmt.Sprintf(
			"the other Loki tenants, which this query did not search: %s (pass tenant, joining several with '|')",
			strings.Join(unsearched, ", ")))
	}

	labelTools := s.permittedTools(ctx, toolLokiLabelNames, toolLokiLabelValues)
	if len(labelTools) > 0 {
		leads = append(leads, fmt.Sprintf("how these logs are actually labelled (%s)", strings.Join(labelTools, ", ")))
	}

	return leads
}

// executeLokiLabelNames lists the label names on Loki streams.
func (s *Server) executeLokiLabelNames(ctx context.Context, args map[string]interface{}) (result string, err error) {
	result, err = s.lokiLabelLookup(ctx, args, "")
	return result, err
}

// executeLokiLabelValues lists the values of one label on Loki streams.
func (s *Server) executeLokiLabelValues(ctx context.Context, args map[string]interface{}) (result string, err error) {
	label, _ := args[argLabel].(string)
	if label == "" {
		err = errors.New("label parameter is required")
		return result, err
	}

	result, err = s.lokiLabelLookup(ctx, args, label)

	return result, err
}

// lokiLabelLookup answers both label tools: label names when label is empty,
// that label's values otherwise.
//
// Each tenant is asked separately and reported under its own heading. A
// combined multi-tenant read would return one merged list, which cannot say
// which tenant a service's logs are in — the thing the caller is here to learn.
func (s *Server) lokiLabelLookup(ctx context.Context, args map[string]interface{}, label string) (result string, err error) {
	err = s.requireLoki()
	if err != nil {
		return result, err
	}

	requestedTenant, _ := args[argTenant].(string)

	var tenants []string

	tenants, err = s.lokiClient.DiscoveryTenants(requestedTenant)
	if err != nil {
		return result, err
	}

	req := k8s.LabelRequest{}
	req.Start, _ = args[argStart].(string)
	req.End, _ = args[argEnd].(string)
	req.Query, _ = args[argQuery].(string)

	window := req.Start
	if window == "" {
		window = k8s.DefaultLokiLabelWindow
	}

	subject := "label names"
	if label != "" {
		subject = fmt.Sprintf("values of label %q", label)
	}

	var builder strings.Builder

	fmt.Fprintf(&builder, "Loki %s (start: %s):\n\n", subject, window)

	found := 0
	failures := make([]string, 0, len(tenants))

	for _, tenantID := range tenants {
		req.Tenant = tenantID

		var items []string
		var lookupErr error

		if label == "" {
			items, lookupErr = s.lokiClient.LabelNames(ctx, req)
		} else {
			items, lookupErr = s.lokiClient.LabelValues(ctx, label, req)
		}

		if lookupErr != nil {
			failures = append(failures, lookupErr.Error())
		}

		found += len(items)

		builder.WriteString(formatLokiLabelSection(tenantID, items, lookupErr))
	}

	// One tenant failing is reported in place beside the ones that answered;
	// every tenant failing is a failed call.
	if len(failures) == len(tenants) {
		err = fmt.Errorf("loki label lookup failed: %s", strings.Join(failures, "; "))
		return result, err
	}

	result = builder.String()

	if found == 0 {
		// Every requested tenant was inspected, so the remaining leads are a
		// wider window and the other log backends.
		result += s.emptyLogSearchNote(ctx, logFamilyLoki,
			[]string{"a wider window (start), since streams that have not logged within it are not listed"})
	}

	return result, err
}

// formatLokiLabelSection renders one tenant's answer to a label lookup.
func formatLokiLabelSection(tenantID string, items []string, lookupErr error) (section string) {
	heading := lokiUntenantedHeading
	if tenantID != "" {
		heading = "Tenant " + tenantID
	}

	if lookupErr != nil {
		section = fmt.Sprintf("%s: error: %s\n", heading, lookupErr.Error())
		return section
	}

	if len(items) == 0 {
		section = heading + ": none\n"
		return section
	}

	shown := items
	suffix := ""

	if len(items) > maxLokiLabelItems {
		shown = items[:maxLokiLabelItems]
		suffix = fmt.Sprintf(" … (showing %d of %d; narrow with query)", maxLokiLabelItems, len(items))
	}

	section = fmt.Sprintf("%s (%d): %s%s\n", heading, len(items), strings.Join(shown, ", "), suffix)

	return section
}
