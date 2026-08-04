package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// CloudWatch Logs tool name constants.
const (
	toolCloudWatchLogsQuery      = "cloudwatch_logs_query"
	toolCloudWatchLogsListGroups = "cloudwatch_logs_list_groups"
	toolCloudWatchLogsGetEvents  = "cloudwatch_logs_get_events"
)

// descCloudWatchRegion is the shared schema description for the AWS region arg.
const descCloudWatchRegion = "AWS region (default: us-east-1)"

// Default values for CloudWatch Logs queries.
const (
	defaultCloudWatchRegion     = "us-east-1"
	defaultCloudWatchLimit      = 100
	defaultCloudWatchMaxWait    = 60 // seconds
	cloudWatchPollInterval      = 500 * time.Millisecond
	cloudWatchMaxResultsPerPoll = 10000
	timeNow                     = "now"
)

// Environment variable for optional IAM role assumption (legacy single-account).
// If set, CloudWatch queries will assume this role instead of using the default credentials.
const envCloudWatchAssumeRole = "CLOUDWATCH_ASSUME_ROLE"

// Environment variable for multi-account CloudWatch access.
// JSON map of friendly name to full role ARN, e.g. {"dev":"arn:...","prod":"arn:..."}.
const envCloudWatchAccounts = "CLOUDWATCH_ACCOUNTS"

// Environment variable for optional external ID when assuming cross-account roles.
// Required when the target role's trust policy includes an external ID condition.
const envCloudWatchExternalID = "CLOUDWATCH_EXTERNAL_ID"

// CloudWatchLogsClient defines the interface for CloudWatch Logs operations.
// This allows for easy mocking in tests.
type CloudWatchLogsClient interface {
	StartQuery(ctx context.Context, params *cloudwatchlogs.StartQueryInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error)
	GetQueryResults(ctx context.Context, params *cloudwatchlogs.GetQueryResultsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error)
	DescribeLogGroups(ctx context.Context, params *cloudwatchlogs.DescribeLogGroupsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
	GetLogEvents(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error)
}

// CloudWatchAccountConfig holds the friendly name and role ARN for a CloudWatch account.
type CloudWatchAccountConfig struct {
	Name    string `json:"name"`
	RoleARN string `json:"role_arn"`
}

// CloudWatchClientFactory creates a CloudWatch Logs client for a given region and role ARN.
// This abstraction enables testing without real AWS calls.
type CloudWatchClientFactory func(ctx context.Context, region string, roleARN string) (CloudWatchLogsClient, error)

// CloudWatchQueryResult represents the result of a CloudWatch Logs Insights query.
type CloudWatchQueryResult struct {
	QueryID     string                `json:"query_id"`
	Status      string                `json:"status"`
	Region      string                `json:"region"`
	LogGroups   []string              `json:"log_groups"`
	Query       string                `json:"query"`
	StartTime   string                `json:"start_time"`
	EndTime     string                `json:"end_time"`
	ResultCount int                   `json:"result_count"`
	Results     []map[string]string   `json:"results"`
	Statistics  *CloudWatchQueryStats `json:"statistics,omitempty"`
}

// CloudWatchQueryStats contains statistics about a CloudWatch Logs Insights query.
type CloudWatchQueryStats struct {
	BytesScanned   float64 `json:"bytes_scanned"`
	RecordsMatched float64 `json:"records_matched"`
	RecordsScanned float64 `json:"records_scanned"`
}

// CloudWatchLogGroup represents a CloudWatch log group.
type CloudWatchLogGroup struct {
	Name              string `json:"name"`
	ARN               string `json:"arn"`
	CreationTime      string `json:"creation_time,omitempty"`
	StoredBytes       int64  `json:"stored_bytes"`
	RetentionDays     int32  `json:"retention_days,omitempty"`
	MetricFilterCount int32  `json:"metric_filter_count"`
}

// CloudWatchLogEvent represents a single log event.
type CloudWatchLogEvent struct {
	Timestamp     string `json:"timestamp"`
	Message       string `json:"message"`
	IngestionTime string `json:"ingestion_time,omitempty"`
}

// MultiAccountQueryResult wraps per-account CloudWatch Logs Insights query results.
type MultiAccountQueryResult struct {
	Accounts []AccountQueryResult `json:"accounts"`
}

// AccountQueryResult holds the query result (or error) for a single account.
type AccountQueryResult struct {
	Account string                 `json:"account"`
	Error   string                 `json:"error,omitempty"`
	Result  *CloudWatchQueryResult `json:"result,omitempty"`
}

// MultiAccountListGroupsResult wraps per-account log group listing results.
type MultiAccountListGroupsResult struct {
	Accounts []AccountListGroupsResult `json:"accounts"`
}

// AccountListGroupsResult holds the log groups (or error) for a single account.
type AccountListGroupsResult struct {
	Account   string               `json:"account"`
	Region    string               `json:"region"`
	Prefix    string               `json:"prefix,omitempty"`
	Error     string               `json:"error,omitempty"`
	Count     int                  `json:"count"`
	LogGroups []CloudWatchLogGroup `json:"log_groups,omitempty"`
}

// MultiAccountGetEventsResult wraps per-account log event results.
type MultiAccountGetEventsResult struct {
	Accounts []AccountGetEventsResult `json:"accounts"`
}

// AccountGetEventsResult holds the log events (or error) for a single account.
type AccountGetEventsResult struct {
	Account   string               `json:"account"`
	Region    string               `json:"region"`
	LogGroup  string               `json:"log_group"`
	LogStream string               `json:"log_stream"`
	Error     string               `json:"error,omitempty"`
	Count     int                  `json:"count"`
	Events    []CloudWatchLogEvent `json:"events,omitempty"`
}

// cloudWatchAccountsProperty returns the shared "accounts" property for tool schemas.
func cloudWatchAccountsProperty() (result map[string]interface{}) {
	result = map[string]interface{}{
		"type":        "array",
		"description": "AWS accounts to query by name (e.g. 'dev','stage','prod'). Defaults to all configured accounts.",
		"items": map[string]interface{}{
			"type": "string",
		},
	}

	return result
}

// getCloudWatchTools returns CloudWatch Logs tool definitions.
func getCloudWatchTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolCloudWatchLogsQuery,
			Description: "Execute a CloudWatch Logs Insights query across one or more log groups. Returns structured results from the query. Useful for searching application logs, error patterns, and time-series analysis.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "CloudWatch Logs Insights query string. Example: 'fields @timestamp, @message | filter @message like /ERROR/ | sort @timestamp desc | limit 100'",
					},
					"log_groups": map[string]interface{}{
						"type":        "array",
						"description": "List of log group names to query. Example: ['/aws/lambda/my-function', '/ecs/my-service']",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
					"start_time": map[string]interface{}{
						"type":        "string",
						"description": "Start time as relative duration (e.g., '1h', '24h', '7d') or RFC3339 timestamp",
					},
					"end_time": map[string]interface{}{
						"type":        "string",
						"description": descEndTime,
					},
					"region": map[string]interface{}{
						"type":        "string",
						"description": descCloudWatchRegion,
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return (default: 100, max: 10000)",
					},
					"accounts": cloudWatchAccountsProperty(),
				},
				"required": []string{"query", "log_groups", "start_time"},
			},
		},
		{
			Name:        toolCloudWatchLogsListGroups,
			Description: "List CloudWatch log groups in an AWS region. Useful for discovering available log groups before running queries.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"prefix": map[string]interface{}{
						"type":        "string",
						"description": "Filter log groups by prefix (e.g., '/aws/lambda/', '/ecs/')",
					},
					"region": map[string]interface{}{
						"type":        "string",
						"description": descCloudWatchRegion,
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of log groups to return (default: 50)",
					},
					"accounts": cloudWatchAccountsProperty(),
				},
			},
		},
		{
			Name:        toolCloudWatchLogsGetEvents,
			Description: "Get log events from a specific CloudWatch log stream. Useful for detailed investigation of a specific container, instance, or request.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"log_group": map[string]interface{}{
						"type":        "string",
						"description": "Log group name",
					},
					"log_stream": map[string]interface{}{
						"type":        "string",
						"description": "Log stream name",
					},
					"start_time": map[string]interface{}{
						"type":        "string",
						"description": "Start time as relative duration (e.g., '1h') or RFC3339 timestamp",
					},
					"end_time": map[string]interface{}{
						"type":        "string",
						"description": descEndTime,
					},
					"region": map[string]interface{}{
						"type":        "string",
						"description": descCloudWatchRegion,
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of events to return (default: 100)",
					},
					"accounts": cloudWatchAccountsProperty(),
				},
				"required": []string{"log_group", "log_stream"},
			},
		},
	}

	return result
}

// isCloudWatchConfigured returns true if either multi-account or legacy CloudWatch config is present.
func isCloudWatchConfigured() (configured bool) {
	configured = os.Getenv(envCloudWatchAccounts) != "" || os.Getenv(envCloudWatchAssumeRole) != ""

	return configured
}

// loadCloudWatchAccounts parses the CLOUDWATCH_ACCOUNTS env var into account configs.
// Returns nil (not an error) if the env var is unset, allowing fallback to legacy mode.
func loadCloudWatchAccounts() (accounts []CloudWatchAccountConfig, err error) {
	raw := os.Getenv(envCloudWatchAccounts)
	if raw == "" {
		return accounts, err
	}

	var nameToARN map[string]string

	err = json.Unmarshal([]byte(raw), &nameToARN)
	if err != nil {
		err = fmt.Errorf("parsing %s: %w", envCloudWatchAccounts, err)
		return accounts, err
	}

	if len(nameToARN) == 0 {
		err = fmt.Errorf("%s is set but contains no accounts", envCloudWatchAccounts)
		return accounts, err
	}

	// Sort keys for deterministic ordering
	names := make([]string, 0, len(nameToARN))
	for name := range nameToARN {
		names = append(names, name)
	}

	sort.Strings(names)

	accounts = make([]CloudWatchAccountConfig, 0, len(names))

	for _, name := range names {
		accounts = append(accounts, CloudWatchAccountConfig{
			Name:    name,
			RoleARN: nameToARN[name],
		})
	}

	return accounts, err
}

// resolveCloudWatchAccounts filters the full account list to only the requested names.
// If requested is empty, all accounts are returned.
func resolveCloudWatchAccounts(
	all []CloudWatchAccountConfig,
	requested []string,
) (resolved []CloudWatchAccountConfig, err error) {
	if len(requested) == 0 {
		resolved = all
		return resolved, err
	}

	byName := make(map[string]CloudWatchAccountConfig, len(all))
	for _, acct := range all {
		byName[acct.Name] = acct
	}

	for _, name := range requested {
		acct, exists := byName[name]
		if !exists {
			validNames := make([]string, 0, len(all))
			for _, a := range all {
				validNames = append(validNames, a.Name)
			}

			err = fmt.Errorf("unknown account %q; valid accounts: %s", name, strings.Join(validNames, ", "))

			return resolved, err
		}

		resolved = append(resolved, acct)
	}

	return resolved, err
}

// parseAccountsToolArg extracts the optional "accounts" string array from tool args.
func parseAccountsToolArg(args map[string]interface{}) (accounts []string) {
	raw, ok := args["accounts"].([]interface{})
	if !ok {
		return accounts
	}

	for _, item := range raw {
		s, strOK := item.(string)
		if strOK && s != "" {
			accounts = append(accounts, s)
		}
	}

	return accounts
}

// defaultCloudWatchClientFactory creates a real CloudWatch Logs client via assume-role.
func defaultCloudWatchClientFactory(ctx context.Context, region string, roleARN string) (client CloudWatchLogsClient, err error) {
	var cfg aws.Config

	cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		err = fmt.Errorf("loading AWS config: %w", err)
		return client, err
	}

	cfg, err = configureAssumeRole(ctx, cfg, roleARN, region)
	if err != nil {
		return client, err
	}

	client = cloudwatchlogs.NewFromConfig(cfg)

	return client, err
}

// executeCloudWatchLogsQuery executes a CloudWatch Logs Insights query.
func (s *Server) executeCloudWatchLogsQuery(ctx context.Context, args map[string]interface{}) (result string, err error) {
	// Parse arguments
	query, _ := args["query"].(string)
	if query == "" {
		err = errors.New("query parameter is required")
		return result, err
	}

	var logGroups []string
	logGroups, err = parseLogGroupsArg(args)
	if err != nil {
		return result, err
	}

	startTimeStr, _ := args["start_time"].(string)
	if startTimeStr == "" {
		err = errors.New("start_time parameter is required")
		return result, err
	}

	endTimeStr := parseEndTimeArg(args)
	region := parseCloudWatchRegionArg(args)
	limit := parseCloudWatchLimitArg(args)

	// Parse time ranges
	var startTime time.Time
	startTime, err = parseTimeArg(startTimeStr)
	if err != nil {
		err = fmt.Errorf("parsing start_time: %w", err)
		return result, err
	}

	var endTime time.Time
	endTime, err = parseTimeArg(endTimeStr)
	if err != nil {
		err = fmt.Errorf("parsing end_time: %w", err)
		return result, err
	}

	// Check for multi-account mode
	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountQuery(ctx, accounts, args, logGroups, query, startTime, endTime, limit, region)
		return result, err
	}

	// Legacy single-account path
	s.logger.InfoContext(ctx, "executing CloudWatch Logs Insights query",
		"region", region,
		"log_groups", logGroups,
		"query", query,
		"start_time", startTime.Format(time.RFC3339),
		"end_time", endTime.Format(time.RFC3339),
		"limit", limit)

	// Create CloudWatch Logs client
	var client *cloudwatchlogs.Client
	client, err = createCloudWatchClient(ctx, region)
	if err != nil {
		return result, err
	}

	// Execute the query
	var queryResult CloudWatchQueryResult
	queryResult, err = runCloudWatchQuery(ctx, client, logGroups, query, startTime, endTime, limit, region)
	if err != nil {
		return result, err
	}

	// Format result as JSON
	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(queryResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting query result: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executeMultiAccountQuery runs a CloudWatch query across multiple accounts.
func (s *Server) executeMultiAccountQuery(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	logGroups []string,
	query string,
	startTime time.Time,
	endTime time.Time,
	limit int,
	region string,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "executing multi-account CloudWatch Logs Insights query",
		"region", region,
		"accounts", len(resolved),
		"log_groups", logGroups,
		"query", query)

	multiResult := MultiAccountQueryResult{
		Accounts: make([]AccountQueryResult, 0, len(resolved)),
	}

	for _, acct := range resolved {
		acctResult := AccountQueryResult{Account: acct.Name}

		client, clientErr := s.cloudWatchClientFactory(ctx, region, acct.RoleARN)
		if clientErr != nil {
			acctResult.Error = clientErr.Error()
			multiResult.Accounts = append(multiResult.Accounts, acctResult)

			continue
		}

		queryResult, queryErr := runCloudWatchQuery(ctx, client, logGroups, query, startTime, endTime, limit, region)
		if queryErr != nil {
			acctResult.Error = queryErr.Error()
		} else {
			acctResult.Result = &queryResult
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(multiResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting multi-account query result: %w", err)
		return result, err
	}

	result = string(resultBytes)

	return result, err
}

// executeCloudWatchLogsListGroups lists CloudWatch log groups.
func (s *Server) executeCloudWatchLogsListGroups(ctx context.Context, args map[string]interface{}) (result string, err error) {
	region := parseCloudWatchRegionArg(args)
	prefix, _ := args["prefix"].(string)
	limit := parseCloudWatchListLimitArg(args)

	// Check for multi-account mode
	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountListGroups(ctx, accounts, args, prefix, limit, region)
		return result, err
	}

	// Legacy single-account path
	s.logger.InfoContext(ctx, "listing CloudWatch log groups",
		"region", region,
		"prefix", prefix,
		"limit", limit)

	// Create CloudWatch Logs client
	var client *cloudwatchlogs.Client
	client, err = createCloudWatchClient(ctx, region)
	if err != nil {
		return result, err
	}

	// List log groups
	var logGroups []CloudWatchLogGroup
	logGroups, err = listLogGroups(ctx, client, prefix, limit)
	if err != nil {
		return result, err
	}

	// Format result
	output := struct {
		Region    string               `json:"region"`
		Prefix    string               `json:"prefix,omitempty"`
		Count     int                  `json:"count"`
		LogGroups []CloudWatchLogGroup `json:"log_groups"`
	}{
		Region:    region,
		Prefix:    prefix,
		Count:     len(logGroups),
		LogGroups: logGroups,
	}

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(output, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting log groups: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executeMultiAccountListGroups lists log groups across multiple accounts.
func (s *Server) executeMultiAccountListGroups(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	prefix string,
	limit int,
	region string,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "listing CloudWatch log groups across accounts",
		"region", region,
		"accounts", len(resolved),
		"prefix", prefix)

	multiResult := MultiAccountListGroupsResult{
		Accounts: make([]AccountListGroupsResult, 0, len(resolved)),
	}

	for _, acct := range resolved {
		acctResult := AccountListGroupsResult{
			Account: acct.Name,
			Region:  region,
			Prefix:  prefix,
		}

		client, clientErr := s.cloudWatchClientFactory(ctx, region, acct.RoleARN)
		if clientErr != nil {
			acctResult.Error = clientErr.Error()
			multiResult.Accounts = append(multiResult.Accounts, acctResult)

			continue
		}

		logGroups, listErr := listLogGroups(ctx, client, prefix, limit)
		if listErr != nil {
			acctResult.Error = listErr.Error()
		} else {
			acctResult.Count = len(logGroups)
			acctResult.LogGroups = logGroups
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(multiResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting multi-account log groups: %w", err)
		return result, err
	}

	result = string(resultBytes)

	return result, err
}

// executeCloudWatchLogsGetEvents gets log events from a specific log stream.
func (s *Server) executeCloudWatchLogsGetEvents(ctx context.Context, args map[string]interface{}) (result string, err error) {
	logGroup, _ := args["log_group"].(string)
	if logGroup == "" {
		err = errors.New("log_group parameter is required")
		return result, err
	}

	logStream, _ := args["log_stream"].(string)
	if logStream == "" {
		err = errors.New("log_stream parameter is required")
		return result, err
	}

	region := parseCloudWatchRegionArg(args)
	limit := parseCloudWatchLimitArg(args)

	// Parse time ranges
	var startTime, endTime *time.Time

	startTimeStr, _ := args["start_time"].(string)
	if startTimeStr != "" {
		parsed, parseErr := parseTimeArg(startTimeStr)
		if parseErr != nil {
			err = fmt.Errorf("parsing start_time: %w", parseErr)
			return result, err
		}
		startTime = &parsed
	}

	endTimeStr := parseEndTimeArg(args)
	if endTimeStr != timeNow {
		parsed, parseErr := parseTimeArg(endTimeStr)
		if parseErr != nil {
			err = fmt.Errorf("parsing end_time: %w", parseErr)
			return result, err
		}
		endTime = &parsed
	}

	// Check for multi-account mode
	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountGetEvents(ctx, accounts, args, logGroup, logStream, startTime, endTime, limit, region)
		return result, err
	}

	// Legacy single-account path
	s.logger.InfoContext(ctx, "getting CloudWatch log events",
		"region", region,
		"log_group", logGroup,
		"log_stream", logStream,
		"limit", limit)

	// Create CloudWatch Logs client
	var client *cloudwatchlogs.Client
	client, err = createCloudWatchClient(ctx, region)
	if err != nil {
		return result, err
	}

	// Get log events
	var events []CloudWatchLogEvent
	events, err = getLogEvents(ctx, client, logGroup, logStream, startTime, endTime, limit)
	if err != nil {
		return result, err
	}

	// Format result
	output := struct {
		Region    string               `json:"region"`
		LogGroup  string               `json:"log_group"`
		LogStream string               `json:"log_stream"`
		Count     int                  `json:"count"`
		Events    []CloudWatchLogEvent `json:"events"`
	}{
		Region:    region,
		LogGroup:  logGroup,
		LogStream: logStream,
		Count:     len(events),
		Events:    events,
	}

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(output, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting log events: %w", err)
		return result, err
	}

	result = string(resultBytes)
	return result, err
}

// executeMultiAccountGetEvents gets log events across multiple accounts.
func (s *Server) executeMultiAccountGetEvents(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	logGroup string,
	logStream string,
	startTime *time.Time,
	endTime *time.Time,
	limit int,
	region string,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "getting CloudWatch log events across accounts",
		"region", region,
		"accounts", len(resolved),
		"log_group", logGroup,
		"log_stream", logStream)

	multiResult := MultiAccountGetEventsResult{
		Accounts: make([]AccountGetEventsResult, 0, len(resolved)),
	}

	for _, acct := range resolved {
		acctResult := AccountGetEventsResult{
			Account:   acct.Name,
			Region:    region,
			LogGroup:  logGroup,
			LogStream: logStream,
		}

		client, clientErr := s.cloudWatchClientFactory(ctx, region, acct.RoleARN)
		if clientErr != nil {
			acctResult.Error = clientErr.Error()
			multiResult.Accounts = append(multiResult.Accounts, acctResult)

			continue
		}

		events, eventsErr := getLogEvents(ctx, client, logGroup, logStream, startTime, endTime, limit)
		if eventsErr != nil {
			acctResult.Error = eventsErr.Error()
		} else {
			acctResult.Count = len(events)
			acctResult.Events = events
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	var resultBytes []byte
	resultBytes, err = json.MarshalIndent(multiResult, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting multi-account log events: %w", err)
		return result, err
	}

	result = string(resultBytes)

	return result, err
}

// createCloudWatchClient creates a CloudWatch Logs client for the specified region.
// If CLOUDWATCH_ASSUME_ROLE is set, the client will assume that role for all operations.
// Otherwise, it uses the default credential chain (IRSA, instance profile, etc.).
func createCloudWatchClient(ctx context.Context, region string) (client *cloudwatchlogs.Client, err error) {
	var cfg aws.Config

	cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		err = fmt.Errorf("loading AWS config: %w", err)
		return client, err
	}

	// Check if we need to assume a role for CloudWatch access
	assumeRoleARN := os.Getenv(envCloudWatchAssumeRole)
	if assumeRoleARN != "" {
		cfg, err = configureAssumeRole(ctx, cfg, assumeRoleARN, region)
		if err != nil {
			return client, err
		}
	}

	client = cloudwatchlogs.NewFromConfig(cfg)
	return client, err
}

// configureAssumeRole creates a new AWS config that assumes the specified role.
// If CLOUDWATCH_EXTERNAL_ID is set, it is passed to the STS AssumeRole call.
func configureAssumeRole(ctx context.Context, baseCfg aws.Config, roleARN string, region string) (cfg aws.Config, err error) {
	// Create STS client from base config
	stsClient := sts.NewFromConfig(baseCfg)

	// Build assume role options
	var opts []func(*stscreds.AssumeRoleOptions)

	externalID := os.Getenv(envCloudWatchExternalID)
	if externalID != "" {
		opts = append(opts, func(o *stscreds.AssumeRoleOptions) {
			o.ExternalID = aws.String(externalID)
		})
	}

	// Create credentials provider that assumes the role
	creds := stscreds.NewAssumeRoleProvider(stsClient, roleARN, opts...)

	// Load new config with the assume role credentials
	cfg, err = config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(creds),
	)
	if err != nil {
		err = fmt.Errorf("configuring assume role %s: %w", roleARN, err)
		return cfg, err
	}

	return cfg, err
}

// runCloudWatchQuery executes a CloudWatch Logs Insights query and waits for results.
func runCloudWatchQuery(
	ctx context.Context,
	client CloudWatchLogsClient,
	logGroups []string,
	query string,
	startTime time.Time,
	endTime time.Time,
	limit int,
	region string,
) (result CloudWatchQueryResult, err error) {
	// Ensure limit is within CloudWatch's constraints
	if limit > cloudWatchMaxResultsPerPoll {
		limit = cloudWatchMaxResultsPerPoll
	}

	// Start the query
	var startOutput *cloudwatchlogs.StartQueryOutput
	startOutput, err = client.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupNames: logGroups,
		QueryString:   aws.String(query),
		StartTime:     aws.Int64(startTime.Unix()),
		EndTime:       aws.Int64(endTime.Unix()),
		Limit:         aws.Int32(int32(limit)),
	})
	if err != nil {
		err = fmt.Errorf("starting query: %w", err)
		return result, err
	}

	queryID := aws.ToString(startOutput.QueryId)

	result = CloudWatchQueryResult{
		QueryID:   queryID,
		Region:    region,
		LogGroups: logGroups,
		Query:     query,
		StartTime: startTime.Format(time.RFC3339),
		EndTime:   endTime.Format(time.RFC3339),
	}

	// Poll for results
	result, err = pollQueryResults(ctx, client, queryID, result)

	return result, err
}

// pollQueryResults polls for CloudWatch Logs Insights query results until complete.
func pollQueryResults(
	ctx context.Context,
	client CloudWatchLogsClient,
	queryID string,
	result CloudWatchQueryResult,
) (updatedResult CloudWatchQueryResult, err error) {
	updatedResult = result

	deadline := time.Now().Add(time.Duration(defaultCloudWatchMaxWait) * time.Second)

	for time.Now().Before(deadline) {
		var resultsOutput *cloudwatchlogs.GetQueryResultsOutput
		resultsOutput, err = client.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{
			QueryId: aws.String(queryID),
		})
		if err != nil {
			err = fmt.Errorf("getting query results: %w", err)
			return updatedResult, err
		}

		updatedResult.Status = string(resultsOutput.Status)

		switch resultsOutput.Status {
		case types.QueryStatusComplete:
			updatedResult = parseQueryResults(resultsOutput, updatedResult)
			return updatedResult, err

		case types.QueryStatusFailed, types.QueryStatusCancelled, types.QueryStatusTimeout:
			err = fmt.Errorf("query %s: %s", updatedResult.Status, queryID)
			return updatedResult, err

		case types.QueryStatusRunning, types.QueryStatusScheduled, types.QueryStatusUnknown:
			// Continue polling
			select {
			case <-ctx.Done():
				err = ctx.Err()
				return updatedResult, err
			case <-time.After(cloudWatchPollInterval):
				// Continue
			}
		}
	}

	err = fmt.Errorf("query timed out after %d seconds", defaultCloudWatchMaxWait)
	return updatedResult, err
}

// parseQueryResults parses CloudWatch Logs Insights query results.
func parseQueryResults(
	output *cloudwatchlogs.GetQueryResultsOutput,
	result CloudWatchQueryResult,
) (updatedResult CloudWatchQueryResult) {
	updatedResult = result

	// Parse statistics
	if output.Statistics != nil {
		updatedResult.Statistics = &CloudWatchQueryStats{
			BytesScanned:   output.Statistics.BytesScanned,
			RecordsMatched: output.Statistics.RecordsMatched,
			RecordsScanned: output.Statistics.RecordsScanned,
		}
	}

	// Parse results
	updatedResult.ResultCount = len(output.Results)
	updatedResult.Results = make([]map[string]string, 0, len(output.Results))

	for _, row := range output.Results {
		rowMap := make(map[string]string)
		for _, field := range row {
			if field.Field != nil && field.Value != nil {
				rowMap[*field.Field] = *field.Value
			}
		}
		updatedResult.Results = append(updatedResult.Results, rowMap)
	}

	return updatedResult
}

// listLogGroups lists CloudWatch log groups with optional prefix filtering.
func listLogGroups(
	ctx context.Context,
	client CloudWatchLogsClient,
	prefix string,
	limit int,
) (logGroups []CloudWatchLogGroup, err error) {
	input := &cloudwatchlogs.DescribeLogGroupsInput{
		Limit: aws.Int32(int32(limit)),
	}

	if prefix != "" {
		input.LogGroupNamePrefix = aws.String(prefix)
	}

	var nextToken *string
	collected := 0

	for collected < limit {
		input.NextToken = nextToken

		var output *cloudwatchlogs.DescribeLogGroupsOutput
		output, err = client.DescribeLogGroups(ctx, input)
		if err != nil {
			err = fmt.Errorf("describing log groups: %w", err)
			return logGroups, err
		}

		for _, lg := range output.LogGroups {
			if collected >= limit {
				break
			}

			group := CloudWatchLogGroup{
				Name:              aws.ToString(lg.LogGroupName),
				ARN:               aws.ToString(lg.Arn),
				StoredBytes:       aws.ToInt64(lg.StoredBytes),
				MetricFilterCount: aws.ToInt32(lg.MetricFilterCount),
			}

			if lg.CreationTime != nil {
				group.CreationTime = time.UnixMilli(*lg.CreationTime).Format(time.RFC3339)
			}

			if lg.RetentionInDays != nil {
				group.RetentionDays = *lg.RetentionInDays
			}

			logGroups = append(logGroups, group)
			collected++
		}

		if output.NextToken == nil || collected >= limit {
			break
		}
		nextToken = output.NextToken
	}

	return logGroups, err
}

// getLogEvents retrieves log events from a specific log stream.
func getLogEvents(
	ctx context.Context,
	client CloudWatchLogsClient,
	logGroup string,
	logStream string,
	startTime *time.Time,
	endTime *time.Time,
	limit int,
) (events []CloudWatchLogEvent, err error) {
	input := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String(logStream),
		Limit:         aws.Int32(int32(limit)),
		StartFromHead: aws.Bool(false), // Get most recent events first
	}

	if startTime != nil {
		input.StartTime = aws.Int64(startTime.UnixMilli())
	}

	if endTime != nil {
		input.EndTime = aws.Int64(endTime.UnixMilli())
	}

	var output *cloudwatchlogs.GetLogEventsOutput
	output, err = client.GetLogEvents(ctx, input)
	if err != nil {
		err = fmt.Errorf("getting log events: %w", err)
		return events, err
	}

	events = make([]CloudWatchLogEvent, 0, len(output.Events))

	for _, event := range output.Events {
		logEvent := CloudWatchLogEvent{
			Message: aws.ToString(event.Message),
		}

		if event.Timestamp != nil {
			logEvent.Timestamp = time.UnixMilli(*event.Timestamp).Format(time.RFC3339Nano)
		}

		if event.IngestionTime != nil {
			logEvent.IngestionTime = time.UnixMilli(*event.IngestionTime).Format(time.RFC3339Nano)
		}

		events = append(events, logEvent)
	}

	return events, err
}

// parseLogGroupsArg parses the log_groups argument from the args map.
func parseLogGroupsArg(args map[string]interface{}) (logGroups []string, err error) {
	logGroupsRaw, ok := args["log_groups"].([]interface{})
	if !ok || len(logGroupsRaw) == 0 {
		err = errors.New("log_groups parameter is required and must be a non-empty array")
		return logGroups, err
	}

	for _, lg := range logGroupsRaw {
		lgStr, strOK := lg.(string)
		if strOK && lgStr != "" {
			logGroups = append(logGroups, lgStr)
		}
	}

	if len(logGroups) == 0 {
		err = errors.New("log_groups must contain at least one valid log group name")
		return logGroups, err
	}

	return logGroups, err
}

// parseCloudWatchRegionArg parses the region argument with default.
func parseCloudWatchRegionArg(args map[string]interface{}) (region string) {
	region = defaultCloudWatchRegion

	regionStr, ok := args["region"].(string)
	if ok && regionStr != "" {
		region = regionStr
	}

	return region
}

// parseCloudWatchLimitArg parses the limit argument with default.
func parseCloudWatchLimitArg(args map[string]interface{}) (limit int) {
	limit = defaultCloudWatchLimit

	limitFloat, ok := args["limit"].(float64)
	if ok {
		limit = int(limitFloat)
	}

	// Cap at CloudWatch's maximum
	if limit > cloudWatchMaxResultsPerPoll {
		limit = cloudWatchMaxResultsPerPoll
	}

	return limit
}

// parseCloudWatchListLimitArg parses the limit argument for list operations with default.
func parseCloudWatchListLimitArg(args map[string]interface{}) (limit int) {
	limit = 50 // Default for list operations

	limitFloat, ok := args["limit"].(float64)
	if ok {
		limit = int(limitFloat)
	}

	return limit
}

// parseEndTimeArg parses the end_time argument with default of "now".
func parseEndTimeArg(args map[string]interface{}) (endTime string) {
	endTime = timeNow

	endTimeStr, ok := args["end_time"].(string)
	if ok && endTimeStr != "" {
		endTime = endTimeStr
	}

	return endTime
}

// parseTimeArg parses a time argument that can be a relative duration or RFC3339 timestamp.
func parseTimeArg(timeStr string) (result time.Time, err error) {
	if timeStr == "" || timeStr == timeNow {
		result = time.Now()
		return result, err
	}

	// Try parsing as RFC3339 first
	result, err = time.Parse(time.RFC3339, timeStr)
	if err != nil {
		// RFC3339 failed, try parsing as relative duration (e.g., "1h", "24h", "7d")
		result, err = parseRelativeDurationAsTime(timeStr)
	}

	return result, err
}

// parseRelativeDurationAsTime parses a relative duration and returns the time result.
func parseRelativeDurationAsTime(timeStr string) (result time.Time, err error) {
	var duration time.Duration
	duration, err = parseRelativeDuration(timeStr)
	if err != nil {
		err = fmt.Errorf("invalid time format: %s (expected RFC3339 or relative duration like '1h', '24h', '7d')", timeStr)
		return result, err
	}

	result = time.Now().Add(-duration)
	return result, err
}

// parseRelativeDuration parses a relative duration string like "1h", "24h", "7d".
func parseRelativeDuration(durationStr string) (duration time.Duration, err error) {
	durationStr = strings.TrimSpace(durationStr)
	if durationStr == "" {
		err = errors.New("empty duration string")
		return duration, err
	}

	// Check for day suffix
	if strings.HasSuffix(durationStr, "d") {
		daysStr := strings.TrimSuffix(durationStr, "d")
		var days int
		_, err = fmt.Sscanf(daysStr, "%d", &days)
		if err != nil {
			err = fmt.Errorf("invalid days format: %s", durationStr)
			return duration, err
		}
		duration = time.Duration(days) * 24 * time.Hour
		return duration, err
	}

	// Try standard Go duration parsing (handles "1h", "30m", "1h30m", etc.)
	duration, err = time.ParseDuration(durationStr)
	if err != nil {
		err = fmt.Errorf("invalid duration format: %s", durationStr)
		return duration, err
	}

	return duration, err
}
