// Copyright 2025 Nik Ogura
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// CloudWatch Metrics tool name constants.
const (
	toolCloudWatchMetricsList          = "cloudwatch_metrics_list"
	toolCloudWatchMetricsGetStatistics = "cloudwatch_metrics_get_statistics"
	toolCloudWatchMetricsQuery         = "cloudwatch_metrics_query"
)

// Default values for CloudWatch Metrics operations.
const (
	defaultMetricPeriodSeconds = 300
	defaultMetricsListLimit    = 500
	defaultStatistic           = "Average"
)

// CloudWatchMetricsClient defines the interface for the CloudWatch (non-Logs)
// service: metric queries AND metric/composite alarm reads. It mirrors
// CloudWatchLogsClient and allows for easy mocking in tests. Alarm operations
// share this client because they live on the same underlying cloudwatch.Client.
// The surface is split into two focused sub-interfaces (metric querying vs
// alarm reading) that compose here.
type CloudWatchMetricsClient interface {
	cloudWatchMetricQuerier
	cloudWatchAlarmReader
}

// cloudWatchMetricQuerier covers CloudWatch metric read operations.
type cloudWatchMetricQuerier interface {
	ListMetrics(ctx context.Context, params *cloudwatch.ListMetricsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.ListMetricsOutput, error)
	GetMetricStatistics(ctx context.Context, params *cloudwatch.GetMetricStatisticsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricStatisticsOutput, error)
	GetMetricData(ctx context.Context, params *cloudwatch.GetMetricDataInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// cloudWatchAlarmReader covers CloudWatch alarm read operations.
type cloudWatchAlarmReader interface {
	DescribeAlarms(ctx context.Context, params *cloudwatch.DescribeAlarmsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error)
	DescribeAlarmHistory(ctx context.Context, params *cloudwatch.DescribeAlarmHistoryInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmHistoryOutput, error)
}

// CloudWatchMetricsClientFactory creates a CloudWatch Metrics client for a given
// region and role ARN. This abstraction enables testing without real AWS calls.
type CloudWatchMetricsClientFactory func(ctx context.Context, region string, roleARN string) (CloudWatchMetricsClient, error)

// CloudWatchMetric represents a single CloudWatch metric definition.
type CloudWatchMetric struct {
	Namespace  string            `json:"namespace"`
	MetricName string            `json:"metric_name"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
}

// CloudWatchListMetricsResult wraps the result of a ListMetrics call.
type CloudWatchListMetricsResult struct {
	Region     string             `json:"region"`
	Namespace  string             `json:"namespace,omitempty"`
	MetricName string             `json:"metric_name,omitempty"`
	Count      int                `json:"count"`
	Metrics    []CloudWatchMetric `json:"metrics"`
}

// CloudWatchDatapoint represents a single GetMetricStatistics datapoint.
type CloudWatchDatapoint struct {
	Timestamp   string             `json:"timestamp"`
	Unit        string             `json:"unit,omitempty"`
	Average     *float64           `json:"average,omitempty"`
	Sum         *float64           `json:"sum,omitempty"`
	Maximum     *float64           `json:"maximum,omitempty"`
	Minimum     *float64           `json:"minimum,omitempty"`
	SampleCount *float64           `json:"sample_count,omitempty"`
	Extended    map[string]float64 `json:"extended_statistics,omitempty"`
}

// CloudWatchStatisticsResult wraps the result of a GetMetricStatistics call.
type CloudWatchStatisticsResult struct {
	Region     string                `json:"region"`
	Namespace  string                `json:"namespace"`
	MetricName string                `json:"metric_name"`
	Dimensions map[string]string     `json:"dimensions,omitempty"`
	Label      string                `json:"label,omitempty"`
	Period     int32                 `json:"period_seconds"`
	StartTime  string                `json:"start_time"`
	EndTime    string                `json:"end_time"`
	Count      int                   `json:"count"`
	Datapoints []CloudWatchDatapoint `json:"datapoints"`
}

// CloudWatchMetricDataPoint is a single timestamp/value pair in a data series.
type CloudWatchMetricDataPoint struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}

// CloudWatchMetricDataSeries is one query's worth of GetMetricData results.
type CloudWatchMetricDataSeries struct {
	ID         string                      `json:"id"`
	Label      string                      `json:"label,omitempty"`
	StatusCode string                      `json:"status_code,omitempty"`
	Count      int                         `json:"count"`
	Values     []CloudWatchMetricDataPoint `json:"values"`
}

// CloudWatchMetricDataResult wraps the result of a GetMetricData call.
type CloudWatchMetricDataResult struct {
	Region    string                       `json:"region"`
	StartTime string                       `json:"start_time"`
	EndTime   string                       `json:"end_time"`
	Results   []CloudWatchMetricDataSeries `json:"results"`
}

// MultiAccountListMetricsResult wraps per-account ListMetrics results.
type MultiAccountListMetricsResult struct {
	Accounts []AccountListMetricsResult `json:"accounts"`
}

// AccountListMetricsResult holds the metrics (or error) for a single account.
type AccountListMetricsResult struct {
	Account string             `json:"account"`
	Region  string             `json:"region"`
	Error   string             `json:"error,omitempty"`
	Count   int                `json:"count"`
	Metrics []CloudWatchMetric `json:"metrics,omitempty"`
}

// MultiAccountStatisticsResult wraps per-account GetMetricStatistics results.
type MultiAccountStatisticsResult struct {
	Accounts []AccountStatisticsResult `json:"accounts"`
}

// AccountStatisticsResult holds the statistics (or error) for a single account.
type AccountStatisticsResult struct {
	Account string                      `json:"account"`
	Error   string                      `json:"error,omitempty"`
	Result  *CloudWatchStatisticsResult `json:"result,omitempty"`
}

// MultiAccountMetricDataResult wraps per-account GetMetricData results.
type MultiAccountMetricDataResult struct {
	Accounts []AccountMetricDataResult `json:"accounts"`
}

// AccountMetricDataResult holds the data series (or error) for a single account.
type AccountMetricDataResult struct {
	Account string                       `json:"account"`
	Region  string                       `json:"region"`
	Error   string                       `json:"error,omitempty"`
	Count   int                          `json:"count"`
	Results []CloudWatchMetricDataSeries `json:"results,omitempty"`
}

// metricStatisticsParams bundles the parsed arguments for a GetMetricStatistics
// call so they can be threaded through the legacy and multi-account paths
// without an unwieldy argument list.
type metricStatisticsParams struct {
	region     string
	namespace  string
	metricName string
	dimensions []cwtypes.Dimension
	statistics []cwtypes.Statistic
	extended   []string
	period     int32
	startTime  time.Time
	endTime    time.Time
}

// getCloudWatchMetricsTools returns CloudWatch Metrics tool definitions.
func getCloudWatchMetricsTools() (result []MCPTool) {
	result = []MCPTool{
		cloudWatchMetricsListTool(),
		cloudWatchMetricsStatisticsTool(),
		cloudWatchMetricsQueryTool(),
	}

	return result
}

// cloudWatchMetricsListTool returns the schema for the metrics discovery tool.
func cloudWatchMetricsListTool() (tool MCPTool) {
	tool = MCPTool{
		Name:        toolCloudWatchMetricsList,
		Description: "List CloudWatch metrics (namespaces, metric names, and dimensions). Useful for discovering what metrics exist before fetching statistics. Filter by namespace (e.g. 'AWS/EC2', 'AWS/RDS'), metric name, and/or dimensions.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"namespace": map[string]interface{}{
					"type":        "string",
					"description": "CloudWatch namespace to filter by (e.g. 'AWS/EC2', 'AWS/RDS', 'AWS/ApplicationELB')",
				},
				"metric_name": map[string]interface{}{
					"type":        "string",
					"description": "Metric name to filter by (e.g. 'CPUUtilization')",
				},
				"dimensions": cloudWatchDimensionsProperty(),
				"region":     cloudWatchRegionProperty(),
				"limit":      cloudWatchMetricsLimitProperty(),
				"accounts":   cloudWatchAccountsProperty(),
			},
		},
	}

	return tool
}

// cloudWatchMetricsStatisticsTool returns the schema for the GetMetricStatistics tool.
func cloudWatchMetricsStatisticsTool() (tool MCPTool) {
	tool = MCPTool{
		Name:        toolCloudWatchMetricsGetStatistics,
		Description: "Fetch time-series statistics for a single CloudWatch metric (GetMetricStatistics). This is the common case: pick a namespace, metric name, and dimensions, then get Average/Sum/Maximum/Minimum/SampleCount (or percentiles like 'p99') over a time range at a given period.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"namespace": map[string]interface{}{
					"type":        "string",
					"description": "CloudWatch namespace (e.g. 'AWS/EC2', 'AWS/RDS')",
				},
				"metric_name": map[string]interface{}{
					"type":        "string",
					"description": "Metric name (e.g. 'CPUUtilization', 'FreeableMemory')",
				},
				"dimensions": cloudWatchDimensionsProperty(),
				"statistics": map[string]interface{}{
					"type":        "array",
					"description": "Statistics to fetch: 'Average', 'Sum', 'Maximum', 'Minimum', 'SampleCount', or percentiles like 'p95'/'p99'. Defaults to ['Average'].",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
				"period": map[string]interface{}{
					"type":        "integer",
					"description": "Granularity in seconds (must be a multiple of 60). Default: 300 (5 minutes).",
				},
				"start_time": cloudWatchStartTimeProperty(),
				"end_time": map[string]interface{}{
					"type":        "string",
					"description": descEndTime,
				},
				"region":   cloudWatchRegionProperty(),
				"accounts": cloudWatchAccountsProperty(),
			},
			"required": []string{"namespace", "metric_name", "start_time"},
		},
	}

	return tool
}

// cloudWatchMetricsQueryTool returns the schema for the GetMetricData tool.
func cloudWatchMetricsQueryTool() (tool MCPTool) {
	tool = MCPTool{
		Name:        toolCloudWatchMetricsQuery,
		Description: "Run one or more CloudWatch metric queries with optional metric math (GetMetricData). Each query either references a metric (namespace + metric_name + stat) or is a math expression (e.g. 'errors/requests*100') over other query IDs. Use this for computed metrics, batching many metrics in one call, or high-resolution retrieval.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"queries": map[string]interface{}{
					"type":        "array",
					"description": "Metric data queries. Each item needs a unique 'id'. Provide either 'expression' (metric math over other ids) OR 'namespace' + 'metric_name' (+ optional 'stat', 'period', 'dimensions').",
					"items": map[string]interface{}{
						"type":       "object",
						"properties": cloudWatchMetricQueryItemProperties(),
						"required":   []string{"id"},
					},
				},
				"start_time": cloudWatchStartTimeProperty(),
				"end_time": map[string]interface{}{
					"type":        "string",
					"description": descEndTime,
				},
				"region":   cloudWatchRegionProperty(),
				"accounts": cloudWatchAccountsProperty(),
			},
			"required": []string{"queries", "start_time"},
		},
	}

	return tool
}

// cloudWatchMetricQueryItemProperties returns the per-query schema for GetMetricData.
func cloudWatchMetricQueryItemProperties() (result map[string]interface{}) {
	result = map[string]interface{}{
		"id": map[string]interface{}{
			"type":        "string",
			"description": "Unique identifier for this query, referenceable by math expressions (e.g. 'm1').",
		},
		"expression": map[string]interface{}{
			"type":        "string",
			"description": "Metric math expression over other query ids (e.g. 'm1/m2*100'). Mutually exclusive with namespace/metric_name.",
		},
		"namespace": map[string]interface{}{
			"type":        "string",
			"description": "CloudWatch namespace (required unless 'expression' is set).",
		},
		"metric_name": map[string]interface{}{
			"type":        "string",
			"description": "Metric name (required unless 'expression' is set).",
		},
		"dimensions": cloudWatchDimensionsProperty(),
		"stat": map[string]interface{}{
			"type":        "string",
			"description": "Statistic to apply (e.g. 'Average', 'Sum', 'p99'). Default: 'Average'.",
		},
		"period": map[string]interface{}{
			"type":        "integer",
			"description": "Granularity in seconds (multiple of 60). Default: 300.",
		},
		"label": map[string]interface{}{
			"type":        "string",
			"description": "Human-readable label for the returned series.",
		},
		"return_data": map[string]interface{}{
			"type":        "boolean",
			"description": "Whether to return this series' data. Set false for intermediate math inputs. Default: true.",
		},
	}

	return result
}

// cloudWatchDimensionsProperty returns the shared "dimensions" schema property.
func cloudWatchDimensionsProperty() (result map[string]interface{}) {
	result = map[string]interface{}{
		"type":        "object",
		"description": "CloudWatch dimensions as key-value pairs (e.g. {'InstanceId': 'i-1234567890abcdef0'}).",
		"additionalProperties": map[string]interface{}{
			"type": "string",
		},
	}

	return result
}

// cloudWatchRegionProperty returns the shared "region" schema property.
func cloudWatchRegionProperty() (result map[string]interface{}) {
	result = map[string]interface{}{
		"type":        "string",
		"description": descCloudWatchRegion,
	}

	return result
}

// cloudWatchStartTimeProperty returns the shared "start_time" schema property.
func cloudWatchStartTimeProperty() (result map[string]interface{}) {
	result = map[string]interface{}{
		"type":        "string",
		"description": "Start time as relative duration (e.g., '1h', '24h', '7d') or RFC3339 timestamp",
	}

	return result
}

// cloudWatchMetricsLimitProperty returns the "limit" schema property for metric listing.
func cloudWatchMetricsLimitProperty() (result map[string]interface{}) {
	result = map[string]interface{}{
		"type":        "integer",
		"description": "Maximum number of metrics to return (default: 500)",
	}

	return result
}

// parseDimensionsArg parses the "dimensions" argument into sorted CloudWatch
// dimensions. Sorting yields deterministic output and API requests.
func parseDimensionsArg(args map[string]interface{}) (dimensions []cwtypes.Dimension) {
	raw, ok := args["dimensions"].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return dimensions
	}

	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		value, valOK := raw[name].(string)
		if !valOK || value == "" {
			continue
		}

		dimensions = append(dimensions, cwtypes.Dimension{
			Name:  aws.String(name),
			Value: aws.String(value),
		})
	}

	return dimensions
}

// dimensionsToMap converts CloudWatch dimensions back into a plain map for output.
func dimensionsToMap(dimensions []cwtypes.Dimension) (result map[string]string) {
	if len(dimensions) == 0 {
		return result
	}

	result = make(map[string]string, len(dimensions))
	for _, dim := range dimensions {
		result[aws.ToString(dim.Name)] = aws.ToString(dim.Value)
	}

	return result
}

// standardStatistic maps a statistic string to a CloudWatch Statistic enum,
// reporting whether it is one of the five standard statistics.
func standardStatistic(value string) (stat cwtypes.Statistic, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "average", "avg":
		stat, ok = cwtypes.StatisticAverage, true
	case "sum":
		stat, ok = cwtypes.StatisticSum, true
	case "minimum", "min":
		stat, ok = cwtypes.StatisticMinimum, true
	case "maximum", "max":
		stat, ok = cwtypes.StatisticMaximum, true
	case "samplecount", "sample_count":
		stat, ok = cwtypes.StatisticSampleCount, true
	}

	return stat, ok
}

// parseStatisticsArg splits the "statistics" argument into standard statistics
// and extended statistics (percentiles). Defaults to Average when none given.
func parseStatisticsArg(args map[string]interface{}) (stats []cwtypes.Statistic, extended []string) {
	raw, ok := args["statistics"].([]interface{})
	if !ok || len(raw) == 0 {
		stats = []cwtypes.Statistic{cwtypes.StatisticAverage}
		return stats, extended
	}

	for _, item := range raw {
		value, strOK := item.(string)
		if !strOK || value == "" {
			continue
		}

		standard, isStandard := standardStatistic(value)
		if isStandard {
			stats = append(stats, standard)
			continue
		}

		extended = append(extended, value)
	}

	if len(stats) == 0 && len(extended) == 0 {
		stats = []cwtypes.Statistic{cwtypes.StatisticAverage}
	}

	return stats, extended
}

// parseMetricPeriodArg parses the "period" argument with a sane default.
func parseMetricPeriodArg(args map[string]interface{}) (period int32) {
	period = defaultMetricPeriodSeconds

	value, ok := args["period"].(float64)
	if ok && int32(value) > 0 {
		period = int32(value)
	}

	return period
}

// parseMetricsListLimitArg parses the "limit" argument for metric listing.
func parseMetricsListLimitArg(args map[string]interface{}) (limit int) {
	limit = defaultMetricsListLimit

	value, ok := args["limit"].(float64)
	if ok && int(value) > 0 {
		limit = int(value)
	}

	return limit
}

// defaultCloudWatchMetricsClientFactory creates a real CloudWatch Metrics client
// via assume-role. It mirrors defaultCloudWatchClientFactory for the Logs client.
func defaultCloudWatchMetricsClientFactory(ctx context.Context, region string, roleARN string) (client CloudWatchMetricsClient, err error) {
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

	client = cloudwatch.NewFromConfig(cfg)

	return client, err
}

// createCloudWatchMetricsClient creates a CloudWatch Metrics client for the
// specified region, honoring the legacy CLOUDWATCH_ASSUME_ROLE env var.
func createCloudWatchMetricsClient(ctx context.Context, region string) (client *cloudwatch.Client, err error) {
	var cfg aws.Config

	cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		err = fmt.Errorf("loading AWS config: %w", err)
		return client, err
	}

	assumeRoleARN := os.Getenv(envCloudWatchAssumeRole)
	if assumeRoleARN != "" {
		cfg, err = configureAssumeRole(ctx, cfg, assumeRoleARN, region)
		if err != nil {
			return client, err
		}
	}

	client = cloudwatch.NewFromConfig(cfg)

	return client, err
}

// marshalCloudWatchMetrics renders a metrics result to indented JSON.
func marshalCloudWatchMetrics(value interface{}, what string) (result string, err error) {
	var resultBytes []byte

	resultBytes, err = json.MarshalIndent(value, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting %s: %w", what, err)
		return result, err
	}

	result = string(resultBytes)

	return result, err
}

// executeCloudWatchMetricsList lists CloudWatch metrics matching the filters.
func (s *Server) executeCloudWatchMetricsList(ctx context.Context, args map[string]interface{}) (result string, err error) {
	region := parseCloudWatchRegionArg(args)
	namespace, _ := args["namespace"].(string)
	metricName, _ := args["metric_name"].(string)
	dimensions := parseDimensionsArg(args)
	limit := parseMetricsListLimitArg(args)

	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountListMetrics(ctx, accounts, args, namespace, metricName, dimensions, limit, region)
		return result, err
	}

	s.logger.InfoContext(ctx, "listing CloudWatch metrics",
		"region", region,
		"namespace", namespace,
		"metric_name", metricName)

	var client *cloudwatch.Client
	client, err = createCloudWatchMetricsClient(ctx, region)
	if err != nil {
		return result, err
	}

	var metrics []CloudWatchMetric
	metrics, err = listMetrics(ctx, client, namespace, metricName, dimensions, limit)
	if err != nil {
		return result, err
	}

	output := CloudWatchListMetricsResult{
		Region:     region,
		Namespace:  namespace,
		MetricName: metricName,
		Count:      len(metrics),
		Metrics:    metrics,
	}

	result, err = marshalCloudWatchMetrics(output, "metrics list")

	return result, err
}

// executeMultiAccountListMetrics lists metrics across multiple accounts.
func (s *Server) executeMultiAccountListMetrics(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	namespace string,
	metricName string,
	dimensions []cwtypes.Dimension,
	limit int,
	region string,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "listing CloudWatch metrics across accounts",
		"region", region,
		"accounts", len(resolved),
		"namespace", namespace)

	multiResult := MultiAccountListMetricsResult{
		Accounts: make([]AccountListMetricsResult, 0, len(resolved)),
	}

	for _, acct := range resolved {
		acctResult := AccountListMetricsResult{Account: acct.Name, Region: region}

		client, clientErr := s.cloudWatchMetricsClientFactory(ctx, region, acct.RoleARN)
		if clientErr != nil {
			acctResult.Error = clientErr.Error()
			multiResult.Accounts = append(multiResult.Accounts, acctResult)

			continue
		}

		metrics, listErr := listMetrics(ctx, client, namespace, metricName, dimensions, limit)
		if listErr != nil {
			acctResult.Error = listErr.Error()
		} else {
			acctResult.Count = len(metrics)
			acctResult.Metrics = metrics
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	result, err = marshalCloudWatchMetrics(multiResult, "multi-account metrics list")

	return result, err
}

// executeCloudWatchMetricsGetStatistics fetches statistics for a single metric.
func (s *Server) executeCloudWatchMetricsGetStatistics(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var params metricStatisticsParams
	params, err = parseMetricStatisticsParams(args)
	if err != nil {
		return result, err
	}

	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountGetMetricStatistics(ctx, accounts, args, params)
		return result, err
	}

	s.logger.InfoContext(ctx, "getting CloudWatch metric statistics",
		"region", params.region,
		"namespace", params.namespace,
		"metric_name", params.metricName,
		"period", params.period)

	var client *cloudwatch.Client
	client, err = createCloudWatchMetricsClient(ctx, params.region)
	if err != nil {
		return result, err
	}

	var statsResult CloudWatchStatisticsResult
	statsResult, err = getMetricStatistics(ctx, client, params)
	if err != nil {
		return result, err
	}

	result, err = marshalCloudWatchMetrics(statsResult, "metric statistics")

	return result, err
}

// parseMetricStatisticsParams validates and parses the GetMetricStatistics args.
func parseMetricStatisticsParams(args map[string]interface{}) (params metricStatisticsParams, err error) {
	namespace, _ := args["namespace"].(string)
	if namespace == "" {
		err = errors.New("namespace parameter is required")
		return params, err
	}

	metricName, _ := args["metric_name"].(string)
	if metricName == "" {
		err = errors.New("metric_name parameter is required")
		return params, err
	}

	startTimeStr, _ := args["start_time"].(string)
	if startTimeStr == "" {
		err = errors.New("start_time parameter is required")
		return params, err
	}

	var startTime time.Time
	startTime, err = parseTimeArg(startTimeStr)
	if err != nil {
		err = fmt.Errorf("parsing start_time: %w", err)
		return params, err
	}

	var endTime time.Time
	endTime, err = parseTimeArg(parseEndTimeArg(args))
	if err != nil {
		err = fmt.Errorf("parsing end_time: %w", err)
		return params, err
	}

	stats, extended := parseStatisticsArg(args)

	params = metricStatisticsParams{
		region:     parseCloudWatchRegionArg(args),
		namespace:  namespace,
		metricName: metricName,
		dimensions: parseDimensionsArg(args),
		statistics: stats,
		extended:   extended,
		period:     parseMetricPeriodArg(args),
		startTime:  startTime,
		endTime:    endTime,
	}

	return params, err
}

// executeMultiAccountGetMetricStatistics fetches statistics across accounts.
func (s *Server) executeMultiAccountGetMetricStatistics(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	params metricStatisticsParams,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "getting CloudWatch metric statistics across accounts",
		"region", params.region,
		"accounts", len(resolved),
		"namespace", params.namespace,
		"metric_name", params.metricName)

	multiResult := MultiAccountStatisticsResult{
		Accounts: make([]AccountStatisticsResult, 0, len(resolved)),
	}

	for _, acct := range resolved {
		acctResult := AccountStatisticsResult{Account: acct.Name}

		client, clientErr := s.cloudWatchMetricsClientFactory(ctx, params.region, acct.RoleARN)
		if clientErr != nil {
			acctResult.Error = clientErr.Error()
			multiResult.Accounts = append(multiResult.Accounts, acctResult)

			continue
		}

		statsResult, statsErr := getMetricStatistics(ctx, client, params)
		if statsErr != nil {
			acctResult.Error = statsErr.Error()
		} else {
			acctResult.Result = &statsResult
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	result, err = marshalCloudWatchMetrics(multiResult, "multi-account metric statistics")

	return result, err
}

// executeCloudWatchMetricsQuery runs one or more GetMetricData queries.
func (s *Server) executeCloudWatchMetricsQuery(ctx context.Context, args map[string]interface{}) (result string, err error) {
	region := parseCloudWatchRegionArg(args)

	var queries []cwtypes.MetricDataQuery
	queries, err = buildMetricDataQueries(args)
	if err != nil {
		return result, err
	}

	startTimeStr, _ := args["start_time"].(string)
	if startTimeStr == "" {
		err = errors.New("start_time parameter is required")
		return result, err
	}

	var startTime time.Time
	startTime, err = parseTimeArg(startTimeStr)
	if err != nil {
		err = fmt.Errorf("parsing start_time: %w", err)
		return result, err
	}

	var endTime time.Time
	endTime, err = parseTimeArg(parseEndTimeArg(args))
	if err != nil {
		err = fmt.Errorf("parsing end_time: %w", err)
		return result, err
	}

	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountGetMetricData(ctx, accounts, args, queries, startTime, endTime, region)
		return result, err
	}

	s.logger.InfoContext(ctx, "querying CloudWatch metric data",
		"region", region,
		"queries", len(queries))

	var client *cloudwatch.Client
	client, err = createCloudWatchMetricsClient(ctx, region)
	if err != nil {
		return result, err
	}

	var dataResult CloudWatchMetricDataResult
	dataResult, err = getMetricData(ctx, client, region, queries, startTime, endTime)
	if err != nil {
		return result, err
	}

	result, err = marshalCloudWatchMetrics(dataResult, "metric data")

	return result, err
}

// executeMultiAccountGetMetricData runs GetMetricData across multiple accounts.
func (s *Server) executeMultiAccountGetMetricData(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	queries []cwtypes.MetricDataQuery,
	startTime time.Time,
	endTime time.Time,
	region string,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "querying CloudWatch metric data across accounts",
		"region", region,
		"accounts", len(resolved),
		"queries", len(queries))

	multiResult := MultiAccountMetricDataResult{
		Accounts: make([]AccountMetricDataResult, 0, len(resolved)),
	}

	for _, acct := range resolved {
		acctResult := AccountMetricDataResult{Account: acct.Name, Region: region}

		client, clientErr := s.cloudWatchMetricsClientFactory(ctx, region, acct.RoleARN)
		if clientErr != nil {
			acctResult.Error = clientErr.Error()
			multiResult.Accounts = append(multiResult.Accounts, acctResult)

			continue
		}

		dataResult, dataErr := getMetricData(ctx, client, region, queries, startTime, endTime)
		if dataErr != nil {
			acctResult.Error = dataErr.Error()
		} else {
			acctResult.Count = len(dataResult.Results)
			acctResult.Results = dataResult.Results
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	result, err = marshalCloudWatchMetrics(multiResult, "multi-account metric data")

	return result, err
}

// listMetrics retrieves CloudWatch metric definitions, paginating up to limit.
func listMetrics(
	ctx context.Context,
	client CloudWatchMetricsClient,
	namespace string,
	metricName string,
	dimensions []cwtypes.Dimension,
	limit int,
) (metrics []CloudWatchMetric, err error) {
	input := &cloudwatch.ListMetricsInput{}

	if namespace != "" {
		input.Namespace = aws.String(namespace)
	}

	if metricName != "" {
		input.MetricName = aws.String(metricName)
	}

	for _, dim := range dimensions {
		input.Dimensions = append(input.Dimensions, cwtypes.DimensionFilter{
			Name:  dim.Name,
			Value: dim.Value,
		})
	}

	var nextToken *string

	for len(metrics) < limit {
		input.NextToken = nextToken

		var output *cloudwatch.ListMetricsOutput
		output, err = client.ListMetrics(ctx, input)
		if err != nil {
			err = fmt.Errorf("listing metrics: %w", err)
			return metrics, err
		}

		for _, metric := range output.Metrics {
			if len(metrics) >= limit {
				break
			}

			metrics = append(metrics, CloudWatchMetric{
				Namespace:  aws.ToString(metric.Namespace),
				MetricName: aws.ToString(metric.MetricName),
				Dimensions: dimensionsToMap(metric.Dimensions),
			})
		}

		if output.NextToken == nil || len(metrics) >= limit {
			break
		}

		nextToken = output.NextToken
	}

	return metrics, err
}

// getMetricStatistics fetches datapoints for a single metric and sorts them.
func getMetricStatistics(
	ctx context.Context,
	client CloudWatchMetricsClient,
	params metricStatisticsParams,
) (result CloudWatchStatisticsResult, err error) {
	input := &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String(params.namespace),
		MetricName: aws.String(params.metricName),
		StartTime:  aws.Time(params.startTime),
		EndTime:    aws.Time(params.endTime),
		Period:     aws.Int32(params.period),
		Dimensions: params.dimensions,
	}

	if len(params.statistics) > 0 {
		input.Statistics = params.statistics
	}

	if len(params.extended) > 0 {
		input.ExtendedStatistics = params.extended
	}

	var output *cloudwatch.GetMetricStatisticsOutput
	output, err = client.GetMetricStatistics(ctx, input)
	if err != nil {
		err = fmt.Errorf("getting metric statistics: %w", err)
		return result, err
	}

	datapoints := convertDatapoints(output.Datapoints)

	result = CloudWatchStatisticsResult{
		Region:     params.region,
		Namespace:  params.namespace,
		MetricName: params.metricName,
		Dimensions: dimensionsToMap(params.dimensions),
		Label:      aws.ToString(output.Label),
		Period:     params.period,
		StartTime:  params.startTime.Format(time.RFC3339),
		EndTime:    params.endTime.Format(time.RFC3339),
		Count:      len(datapoints),
		Datapoints: datapoints,
	}

	return result, err
}

// convertDatapoints converts SDK datapoints to our type, sorted by timestamp.
func convertDatapoints(points []cwtypes.Datapoint) (result []CloudWatchDatapoint) {
	result = make([]CloudWatchDatapoint, 0, len(points))

	for _, point := range points {
		datapoint := CloudWatchDatapoint{
			Unit:        string(point.Unit),
			Average:     point.Average,
			Sum:         point.Sum,
			Maximum:     point.Maximum,
			Minimum:     point.Minimum,
			SampleCount: point.SampleCount,
		}

		if point.Timestamp != nil {
			datapoint.Timestamp = point.Timestamp.Format(time.RFC3339)
		}

		if len(point.ExtendedStatistics) > 0 {
			datapoint.Extended = point.ExtendedStatistics
		}

		result = append(result, datapoint)
	}

	sort.Slice(result, func(i, j int) (less bool) {
		less = result[i].Timestamp < result[j].Timestamp
		return less
	})

	return result
}

// buildMetricDataQueries builds the SDK query list from the "queries" argument.
func buildMetricDataQueries(args map[string]interface{}) (queries []cwtypes.MetricDataQuery, err error) {
	raw, ok := args["queries"].([]interface{})
	if !ok || len(raw) == 0 {
		err = errors.New("queries parameter is required and must be a non-empty array")
		return queries, err
	}

	for i, item := range raw {
		obj, objOK := item.(map[string]interface{})
		if !objOK {
			err = fmt.Errorf("queries[%d] must be an object", i)
			return queries, err
		}

		var query cwtypes.MetricDataQuery
		query, err = buildMetricDataQuery(obj, i)
		if err != nil {
			return queries, err
		}

		queries = append(queries, query)
	}

	return queries, err
}

// buildMetricDataQuery builds a single MetricDataQuery from a parsed object.
func buildMetricDataQuery(obj map[string]interface{}, index int) (query cwtypes.MetricDataQuery, err error) {
	id, _ := obj["id"].(string)
	if id == "" {
		err = fmt.Errorf("queries[%d].id is required", index)
		return query, err
	}

	query.Id = aws.String(id)

	label, hasLabel := obj["label"].(string)
	if hasLabel && label != "" {
		query.Label = aws.String(label)
	}

	returnData, hasReturnData := obj["return_data"].(bool)
	if hasReturnData {
		query.ReturnData = aws.Bool(returnData)
	}

	expression, _ := obj["expression"].(string)
	if expression != "" {
		query.Expression = aws.String(expression)
		return query, err
	}

	query.MetricStat, err = buildMetricStat(obj, index)

	return query, err
}

// buildMetricStat builds the MetricStat for a query that references a metric.
func buildMetricStat(obj map[string]interface{}, index int) (metricStat *cwtypes.MetricStat, err error) {
	namespace, _ := obj["namespace"].(string)
	metricName, _ := obj["metric_name"].(string)

	if namespace == "" || metricName == "" {
		err = fmt.Errorf("queries[%d] requires either 'expression' or both 'namespace' and 'metric_name'", index)
		return metricStat, err
	}

	stat, _ := obj["stat"].(string)
	if stat == "" {
		stat = defaultStatistic
	}

	period := int32(defaultMetricPeriodSeconds)

	value, ok := obj["period"].(float64)
	if ok && int32(value) > 0 {
		period = int32(value)
	}

	metricStat = &cwtypes.MetricStat{
		Metric: &cwtypes.Metric{
			Namespace:  aws.String(namespace),
			MetricName: aws.String(metricName),
			Dimensions: parseDimensionsArg(obj),
		},
		Period: aws.Int32(period),
		Stat:   aws.String(stat),
	}

	return metricStat, err
}

// getMetricData runs GetMetricData, paginating and merging series by id.
func getMetricData(
	ctx context.Context,
	client CloudWatchMetricsClient,
	region string,
	queries []cwtypes.MetricDataQuery,
	startTime time.Time,
	endTime time.Time,
) (result CloudWatchMetricDataResult, err error) {
	input := &cloudwatch.GetMetricDataInput{
		MetricDataQueries: queries,
		StartTime:         aws.Time(startTime),
		EndTime:           aws.Time(endTime),
	}

	result = CloudWatchMetricDataResult{
		Region:    region,
		StartTime: startTime.Format(time.RFC3339),
		EndTime:   endTime.Format(time.RFC3339),
	}

	seriesByID := make(map[string]*CloudWatchMetricDataSeries)

	var order []string
	var nextToken *string

	for {
		input.NextToken = nextToken

		var output *cloudwatch.GetMetricDataOutput
		output, err = client.GetMetricData(ctx, input)
		if err != nil {
			err = fmt.Errorf("getting metric data: %w", err)
			return result, err
		}

		order = mergeMetricDataResults(seriesByID, order, output.MetricDataResults)

		if output.NextToken == nil {
			break
		}

		nextToken = output.NextToken
	}

	for _, id := range order {
		series := seriesByID[id]
		series.Count = len(series.Values)
		result.Results = append(result.Results, *series)
	}

	return result, err
}

// mergeMetricDataResults folds a page of SDK results into the series map,
// appending values to existing series and preserving first-seen id order.
func mergeMetricDataResults(
	seriesByID map[string]*CloudWatchMetricDataSeries,
	order []string,
	page []cwtypes.MetricDataResult,
) (updatedOrder []string) {
	updatedOrder = order

	for _, item := range page {
		id := aws.ToString(item.Id)

		series, exists := seriesByID[id]
		if !exists {
			series = &CloudWatchMetricDataSeries{
				ID:         id,
				Label:      aws.ToString(item.Label),
				StatusCode: string(item.StatusCode),
			}
			seriesByID[id] = series
			updatedOrder = append(updatedOrder, id)
		} else if item.StatusCode != "" {
			series.StatusCode = string(item.StatusCode)
		}

		series.Values = appendMetricDataPoints(series.Values, item.Timestamps, item.Values)
	}

	return updatedOrder
}

// appendMetricDataPoints zips the parallel timestamp/value arrays into points.
func appendMetricDataPoints(
	existing []CloudWatchMetricDataPoint,
	timestamps []time.Time,
	values []float64,
) (result []CloudWatchMetricDataPoint) {
	result = existing

	count := len(timestamps)
	if len(values) < count {
		count = len(values)
	}

	for i := range count {
		result = append(result, CloudWatchMetricDataPoint{
			Timestamp: timestamps[i].Format(time.RFC3339),
			Value:     values[i],
		})
	}

	return result
}
