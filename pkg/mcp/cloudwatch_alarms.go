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
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// CloudWatch Alarms tool name constants.
const (
	toolCloudWatchAlarmsList    = "cloudwatch_alarms_list"
	toolCloudWatchAlarmsHistory = "cloudwatch_alarms_history"
)

// Default values for CloudWatch Alarms operations. The AWS Describe* APIs cap
// MaxRecords at 100 per page; we paginate up to the caller's limit.
const (
	defaultAlarmsLimit   = 100
	alarmsMaxRecordsPage = 100
	alarmTypeMetric      = "metric"
	alarmTypeComposite   = "composite"
)

// CloudWatchAlarm represents a metric or composite CloudWatch alarm.
type CloudWatchAlarm struct {
	Name               string            `json:"name"`
	ARN                string            `json:"arn,omitempty"`
	Type               string            `json:"type"`
	State              string            `json:"state"`
	StateReason        string            `json:"state_reason,omitempty"`
	StateUpdated       string            `json:"state_updated,omitempty"`
	ActionsEnabled     bool              `json:"actions_enabled"`
	Namespace          string            `json:"namespace,omitempty"`
	MetricName         string            `json:"metric_name,omitempty"`
	Statistic          string            `json:"statistic,omitempty"`
	ExtendedStatistic  string            `json:"extended_statistic,omitempty"`
	Dimensions         map[string]string `json:"dimensions,omitempty"`
	Period             int32             `json:"period_seconds,omitempty"`
	EvaluationPeriods  int32             `json:"evaluation_periods,omitempty"`
	ComparisonOperator string            `json:"comparison_operator,omitempty"`
	Threshold          *float64          `json:"threshold,omitempty"`
	AlarmRule          string            `json:"alarm_rule,omitempty"`
}

// CloudWatchAlarmsResult wraps the result of a DescribeAlarms call.
type CloudWatchAlarmsResult struct {
	Region string            `json:"region"`
	Count  int               `json:"count"`
	Alarms []CloudWatchAlarm `json:"alarms"`
}

// CloudWatchAlarmHistoryItem represents a single alarm history entry.
type CloudWatchAlarmHistoryItem struct {
	Timestamp string `json:"timestamp"`
	AlarmName string `json:"alarm_name,omitempty"`
	AlarmType string `json:"alarm_type,omitempty"`
	ItemType  string `json:"history_item_type,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// CloudWatchAlarmHistoryResult wraps the result of a DescribeAlarmHistory call.
type CloudWatchAlarmHistoryResult struct {
	Region    string                       `json:"region"`
	AlarmName string                       `json:"alarm_name,omitempty"`
	Count     int                          `json:"count"`
	Items     []CloudWatchAlarmHistoryItem `json:"items"`
}

// MultiAccountAlarmsResult wraps per-account DescribeAlarms results.
type MultiAccountAlarmsResult struct {
	Accounts []AccountAlarmsResult `json:"accounts"`
}

// AccountAlarmsResult holds the alarms (or error) for a single account.
type AccountAlarmsResult struct {
	Account string            `json:"account"`
	Region  string            `json:"region"`
	Error   string            `json:"error,omitempty"`
	Count   int               `json:"count"`
	Alarms  []CloudWatchAlarm `json:"alarms,omitempty"`
}

// MultiAccountAlarmHistoryResult wraps per-account DescribeAlarmHistory results.
type MultiAccountAlarmHistoryResult struct {
	Accounts []AccountAlarmHistoryResult `json:"accounts"`
}

// AccountAlarmHistoryResult holds the alarm history (or error) for one account.
type AccountAlarmHistoryResult struct {
	Account string                       `json:"account"`
	Region  string                       `json:"region"`
	Error   string                       `json:"error,omitempty"`
	Count   int                          `json:"count"`
	Items   []CloudWatchAlarmHistoryItem `json:"items,omitempty"`
}

// alarmsListParams bundles the parsed arguments for a DescribeAlarms call.
type alarmsListParams struct {
	region     string
	alarmNames []string
	namePrefix string
	stateValue string
	limit      int
}

// alarmHistoryParams bundles the parsed arguments for a DescribeAlarmHistory call.
type alarmHistoryParams struct {
	region    string
	alarmName string
	itemType  string
	startTime *time.Time
	endTime   *time.Time
	limit     int
}

// getCloudWatchAlarmsTools returns CloudWatch Alarms tool definitions.
func getCloudWatchAlarmsTools() (result []MCPTool) {
	result = []MCPTool{
		cloudWatchAlarmsListTool(),
		cloudWatchAlarmsHistoryTool(),
	}

	return result
}

// cloudWatchAlarmsListTool returns the schema for the alarm listing tool.
func cloudWatchAlarmsListTool() (tool MCPTool) {
	tool = MCPTool{
		Name:        toolCloudWatchAlarmsList,
		Description: "List CloudWatch alarms and their current state (DescribeAlarms). Returns metric and composite alarms with state (OK/ALARM/INSUFFICIENT_DATA), the reason, and — for metric alarms — the watched metric, statistic, comparison, and threshold. Filter by name, name prefix, or state to find what is currently firing.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"alarm_names": map[string]interface{}{
					"type":        "array",
					"description": "Exact alarm names to fetch. Omit to list all (subject to prefix/state filters).",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
				"alarm_name_prefix": map[string]interface{}{
					"type":        "string",
					"description": "Only return alarms whose name starts with this prefix. Mutually exclusive with alarm_names.",
				},
				"state_value": map[string]interface{}{
					"type":        "string",
					"description": "Filter by current alarm state.",
					"enum":        []string{"OK", "ALARM", "INSUFFICIENT_DATA"},
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of alarms to return (default: 100).",
				},
				"region":   cloudWatchRegionProperty(),
				"accounts": cloudWatchAccountsProperty(),
			},
		},
	}

	return tool
}

// cloudWatchAlarmsHistoryTool returns the schema for the alarm history tool.
func cloudWatchAlarmsHistoryTool() (tool MCPTool) {
	tool = MCPTool{
		Name:        toolCloudWatchAlarmsHistory,
		Description: "Retrieve CloudWatch alarm history (DescribeAlarmHistory): state transitions, configuration changes, and actions over time, most recent first. Use this to see when an alarm flapped or fired and why — the key signal for post-incident diagnosis.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"alarm_name": map[string]interface{}{
					"type":        "string",
					"description": "Alarm name to fetch history for. Omit to fetch across all alarms.",
				},
				"history_item_type": map[string]interface{}{
					"type":        "string",
					"description": "Filter the kind of history entry.",
					"enum":        []string{"StateUpdate", "ConfigurationUpdate", "Action"},
				},
				"start_time": map[string]interface{}{
					"type":        "string",
					"description": "Start time as relative duration (e.g., '1h', '24h', '7d') or RFC3339 timestamp (optional).",
				},
				"end_time": map[string]interface{}{
					"type":        "string",
					"description": descEndTime,
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of history items to return (default: 100).",
				},
				"region":   cloudWatchRegionProperty(),
				"accounts": cloudWatchAccountsProperty(),
			},
		},
	}

	return tool
}

// parseAlarmNamesArg extracts the optional "alarm_names" string array.
func parseAlarmNamesArg(args map[string]interface{}) (names []string) {
	raw, ok := args["alarm_names"].([]interface{})
	if !ok {
		return names
	}

	for _, item := range raw {
		value, strOK := item.(string)
		if strOK && value != "" {
			names = append(names, value)
		}
	}

	return names
}

// parseAlarmsLimitArg parses the "limit" argument for alarm operations.
func parseAlarmsLimitArg(args map[string]interface{}) (limit int) {
	limit = defaultAlarmsLimit

	value, ok := args["limit"].(float64)
	if ok && int(value) > 0 {
		limit = int(value)
	}

	return limit
}

// executeCloudWatchAlarmsList lists CloudWatch alarms matching the filters.
func (s *Server) executeCloudWatchAlarmsList(ctx context.Context, args map[string]interface{}) (result string, err error) {
	params := alarmsListParams{
		region:     parseCloudWatchRegionArg(args),
		alarmNames: parseAlarmNamesArg(args),
		limit:      parseAlarmsLimitArg(args),
	}
	params.namePrefix, _ = args["alarm_name_prefix"].(string)
	params.stateValue, _ = args["state_value"].(string)

	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountAlarmsList(ctx, accounts, args, params)
		return result, err
	}

	s.logger.InfoContext(ctx, "listing CloudWatch alarms",
		"region", params.region,
		"prefix", params.namePrefix,
		"state", params.stateValue)

	var client *cloudwatch.Client
	client, err = createCloudWatchMetricsClient(ctx, params.region)
	if err != nil {
		return result, err
	}

	var alarms []CloudWatchAlarm
	alarms, err = describeAlarms(ctx, client, params)
	if err != nil {
		return result, err
	}

	output := CloudWatchAlarmsResult{
		Region: params.region,
		Count:  len(alarms),
		Alarms: alarms,
	}

	result, err = marshalCloudWatchMetrics(output, "alarms")

	return result, err
}

// cloudWatchAccountOutcome pairs an account name with the typed result of
// running an operation against it, or the error encountered.
type cloudWatchAccountOutcome[T any] struct {
	account string
	err     string
	value   T
	ok      bool
}

// fanOutCloudWatchAccounts runs op against each resolved account's client,
// isolating per-account failures so one account's error never blocks another.
// This is the shared fan-out skeleton behind the multi-account alarm tools.
func fanOutCloudWatchAccounts[T any](
	ctx context.Context,
	factory CloudWatchMetricsClientFactory,
	resolved []CloudWatchAccountConfig,
	region string,
	op func(ctx context.Context, client CloudWatchMetricsClient) (T, error),
) (outcomes []cloudWatchAccountOutcome[T]) {
	outcomes = make([]cloudWatchAccountOutcome[T], 0, len(resolved))

	for _, acct := range resolved {
		outcome := cloudWatchAccountOutcome[T]{account: acct.Name}

		client, clientErr := factory(ctx, region, acct.RoleARN)
		if clientErr != nil {
			outcome.err = clientErr.Error()
			outcomes = append(outcomes, outcome)

			continue
		}

		value, opErr := op(ctx, client)
		if opErr != nil {
			outcome.err = opErr.Error()
		} else {
			outcome.value = value
			outcome.ok = true
		}

		outcomes = append(outcomes, outcome)
	}

	return outcomes
}

// executeMultiAccountAlarmsList lists alarms across multiple accounts.
//
//nolint:dupl // Parallel multi-account wrapper to executeMultiAccountAlarmsHistory; the substantive fan-out is shared via fanOutCloudWatchAccounts, and the residual glue differs by result type (Alarms vs Items) and cannot be merged without erasing the semantic JSON field names.
func (s *Server) executeMultiAccountAlarmsList(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	params alarmsListParams,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "listing CloudWatch alarms across accounts",
		"region", params.region,
		"accounts", len(resolved),
		"state", params.stateValue)

	op := func(opCtx context.Context, client CloudWatchMetricsClient) (alarms []CloudWatchAlarm, opErr error) {
		alarms, opErr = describeAlarms(opCtx, client, params)
		return alarms, opErr
	}
	outcomes := fanOutCloudWatchAccounts(ctx, s.cloudWatchMetricsClientFactory, resolved, params.region, op)

	multiResult := MultiAccountAlarmsResult{
		Accounts: make([]AccountAlarmsResult, 0, len(outcomes)),
	}

	for _, outcome := range outcomes {
		acctResult := AccountAlarmsResult{Account: outcome.account, Region: params.region, Error: outcome.err}
		if outcome.ok {
			acctResult.Count = len(outcome.value)
			acctResult.Alarms = outcome.value
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	result, err = marshalCloudWatchMetrics(multiResult, "multi-account alarms")

	return result, err
}

// executeCloudWatchAlarmsHistory retrieves alarm history entries.
func (s *Server) executeCloudWatchAlarmsHistory(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var params alarmHistoryParams
	params, err = parseAlarmHistoryParams(args)
	if err != nil {
		return result, err
	}

	var accounts []CloudWatchAccountConfig
	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return result, err
	}

	if len(accounts) > 0 {
		result, err = s.executeMultiAccountAlarmsHistory(ctx, accounts, args, params)
		return result, err
	}

	s.logger.InfoContext(ctx, "getting CloudWatch alarm history",
		"region", params.region,
		"alarm_name", params.alarmName,
		"item_type", params.itemType)

	var client *cloudwatch.Client
	client, err = createCloudWatchMetricsClient(ctx, params.region)
	if err != nil {
		return result, err
	}

	var items []CloudWatchAlarmHistoryItem
	items, err = describeAlarmHistory(ctx, client, params)
	if err != nil {
		return result, err
	}

	output := CloudWatchAlarmHistoryResult{
		Region:    params.region,
		AlarmName: params.alarmName,
		Count:     len(items),
		Items:     items,
	}

	result, err = marshalCloudWatchMetrics(output, "alarm history")

	return result, err
}

// parseAlarmHistoryParams validates and parses the DescribeAlarmHistory args.
func parseAlarmHistoryParams(args map[string]interface{}) (params alarmHistoryParams, err error) {
	params = alarmHistoryParams{
		region: parseCloudWatchRegionArg(args),
		limit:  parseAlarmsLimitArg(args),
	}
	params.alarmName, _ = args["alarm_name"].(string)
	params.itemType, _ = args["history_item_type"].(string)

	startTimeStr, _ := args["start_time"].(string)
	if startTimeStr != "" {
		var parsed time.Time
		parsed, err = parseTimeArg(startTimeStr)
		if err != nil {
			err = fmt.Errorf("parsing start_time: %w", err)
			return params, err
		}
		params.startTime = &parsed
	}

	endTimeStr, _ := args["end_time"].(string)
	if endTimeStr != "" && endTimeStr != timeNow {
		var parsed time.Time
		parsed, err = parseTimeArg(endTimeStr)
		if err != nil {
			err = fmt.Errorf("parsing end_time: %w", err)
			return params, err
		}
		params.endTime = &parsed
	}

	return params, err
}

// executeMultiAccountAlarmsHistory retrieves alarm history across accounts.
//
//nolint:dupl // Parallel multi-account wrapper to executeMultiAccountAlarmsList; the substantive fan-out is shared via fanOutCloudWatchAccounts, and the residual glue differs by result type (Items vs Alarms) and cannot be merged without erasing the semantic JSON field names.
func (s *Server) executeMultiAccountAlarmsHistory(
	ctx context.Context,
	allAccounts []CloudWatchAccountConfig,
	args map[string]interface{},
	params alarmHistoryParams,
) (result string, err error) {
	requested := parseAccountsToolArg(args)

	var resolved []CloudWatchAccountConfig
	resolved, err = resolveCloudWatchAccounts(allAccounts, requested)
	if err != nil {
		return result, err
	}

	s.logger.InfoContext(ctx, "getting CloudWatch alarm history across accounts",
		"region", params.region,
		"accounts", len(resolved),
		"alarm_name", params.alarmName)

	op := func(opCtx context.Context, client CloudWatchMetricsClient) (items []CloudWatchAlarmHistoryItem, opErr error) {
		items, opErr = describeAlarmHistory(opCtx, client, params)
		return items, opErr
	}
	outcomes := fanOutCloudWatchAccounts(ctx, s.cloudWatchMetricsClientFactory, resolved, params.region, op)

	multiResult := MultiAccountAlarmHistoryResult{
		Accounts: make([]AccountAlarmHistoryResult, 0, len(outcomes)),
	}

	for _, outcome := range outcomes {
		acctResult := AccountAlarmHistoryResult{Account: outcome.account, Region: params.region, Error: outcome.err}
		if outcome.ok {
			acctResult.Count = len(outcome.value)
			acctResult.Items = outcome.value
		}

		multiResult.Accounts = append(multiResult.Accounts, acctResult)
	}

	result, err = marshalCloudWatchMetrics(multiResult, "multi-account alarm history")

	return result, err
}

// describeAlarms fetches metric and composite alarms, paginating up to limit.
func describeAlarms(
	ctx context.Context,
	client CloudWatchMetricsClient,
	params alarmsListParams,
) (alarms []CloudWatchAlarm, err error) {
	input := &cloudwatch.DescribeAlarmsInput{
		AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeMetricAlarm, cwtypes.AlarmTypeCompositeAlarm},
		MaxRecords: aws.Int32(alarmsMaxRecordsPage),
	}

	if len(params.alarmNames) > 0 {
		input.AlarmNames = params.alarmNames
	}

	if params.namePrefix != "" {
		input.AlarmNamePrefix = aws.String(params.namePrefix)
	}

	if params.stateValue != "" {
		input.StateValue = cwtypes.StateValue(strings.ToUpper(params.stateValue))
	}

	var nextToken *string

	for len(alarms) < params.limit {
		input.NextToken = nextToken

		var output *cloudwatch.DescribeAlarmsOutput
		output, err = client.DescribeAlarms(ctx, input)
		if err != nil {
			err = fmt.Errorf("describing alarms: %w", err)
			return alarms, err
		}

		alarms = appendMetricAlarms(alarms, output.MetricAlarms, params.limit)
		alarms = appendCompositeAlarms(alarms, output.CompositeAlarms, params.limit)

		if output.NextToken == nil || len(alarms) >= params.limit {
			break
		}

		nextToken = output.NextToken
	}

	return alarms, err
}

// appendMetricAlarms converts and appends metric alarms up to limit.
func appendMetricAlarms(existing []CloudWatchAlarm, source []cwtypes.MetricAlarm, limit int) (result []CloudWatchAlarm) {
	result = existing

	for _, alarm := range source {
		if len(result) >= limit {
			break
		}

		result = append(result, convertMetricAlarm(alarm))
	}

	return result
}

// appendCompositeAlarms converts and appends composite alarms up to limit.
func appendCompositeAlarms(existing []CloudWatchAlarm, source []cwtypes.CompositeAlarm, limit int) (result []CloudWatchAlarm) {
	result = existing

	for _, alarm := range source {
		if len(result) >= limit {
			break
		}

		result = append(result, convertCompositeAlarm(alarm))
	}

	return result
}

// convertMetricAlarm maps an SDK metric alarm to our output type.
func convertMetricAlarm(alarm cwtypes.MetricAlarm) (result CloudWatchAlarm) {
	result = CloudWatchAlarm{
		Name:               aws.ToString(alarm.AlarmName),
		ARN:                aws.ToString(alarm.AlarmArn),
		Type:               alarmTypeMetric,
		State:              string(alarm.StateValue),
		StateReason:        aws.ToString(alarm.StateReason),
		ActionsEnabled:     aws.ToBool(alarm.ActionsEnabled),
		Namespace:          aws.ToString(alarm.Namespace),
		MetricName:         aws.ToString(alarm.MetricName),
		Statistic:          string(alarm.Statistic),
		ExtendedStatistic:  aws.ToString(alarm.ExtendedStatistic),
		Dimensions:         dimensionsToMap(alarm.Dimensions),
		Period:             aws.ToInt32(alarm.Period),
		EvaluationPeriods:  aws.ToInt32(alarm.EvaluationPeriods),
		ComparisonOperator: string(alarm.ComparisonOperator),
		Threshold:          alarm.Threshold,
	}

	if alarm.StateUpdatedTimestamp != nil {
		result.StateUpdated = alarm.StateUpdatedTimestamp.Format(time.RFC3339)
	}

	return result
}

// convertCompositeAlarm maps an SDK composite alarm to our output type.
func convertCompositeAlarm(alarm cwtypes.CompositeAlarm) (result CloudWatchAlarm) {
	result = CloudWatchAlarm{
		Name:           aws.ToString(alarm.AlarmName),
		ARN:            aws.ToString(alarm.AlarmArn),
		Type:           alarmTypeComposite,
		State:          string(alarm.StateValue),
		StateReason:    aws.ToString(alarm.StateReason),
		ActionsEnabled: aws.ToBool(alarm.ActionsEnabled),
		AlarmRule:      aws.ToString(alarm.AlarmRule),
	}

	if alarm.StateUpdatedTimestamp != nil {
		result.StateUpdated = alarm.StateUpdatedTimestamp.Format(time.RFC3339)
	}

	return result
}

// describeAlarmHistory fetches alarm history, most recent first, up to limit.
func describeAlarmHistory(
	ctx context.Context,
	client CloudWatchMetricsClient,
	params alarmHistoryParams,
) (items []CloudWatchAlarmHistoryItem, err error) {
	input := &cloudwatch.DescribeAlarmHistoryInput{
		MaxRecords: aws.Int32(alarmsMaxRecordsPage),
		ScanBy:     cwtypes.ScanByTimestampDescending,
	}

	if params.alarmName != "" {
		input.AlarmName = aws.String(params.alarmName)
	}

	if params.itemType != "" {
		input.HistoryItemType = cwtypes.HistoryItemType(params.itemType)
	}

	if params.startTime != nil {
		input.StartDate = aws.Time(*params.startTime)
	}

	if params.endTime != nil {
		input.EndDate = aws.Time(*params.endTime)
	}

	var nextToken *string

	for len(items) < params.limit {
		input.NextToken = nextToken

		var output *cloudwatch.DescribeAlarmHistoryOutput
		output, err = client.DescribeAlarmHistory(ctx, input)
		if err != nil {
			err = fmt.Errorf("describing alarm history: %w", err)
			return items, err
		}

		items = appendAlarmHistoryItems(items, output.AlarmHistoryItems, params.limit)

		if output.NextToken == nil || len(items) >= params.limit {
			break
		}

		nextToken = output.NextToken
	}

	return items, err
}

// appendAlarmHistoryItems converts and appends history items up to limit.
func appendAlarmHistoryItems(existing []CloudWatchAlarmHistoryItem, source []cwtypes.AlarmHistoryItem, limit int) (result []CloudWatchAlarmHistoryItem) {
	result = existing

	for _, item := range source {
		if len(result) >= limit {
			break
		}

		historyItem := CloudWatchAlarmHistoryItem{
			AlarmName: aws.ToString(item.AlarmName),
			AlarmType: string(item.AlarmType),
			ItemType:  string(item.HistoryItemType),
			Summary:   aws.ToString(item.HistorySummary),
		}

		if item.Timestamp != nil {
			historyItem.Timestamp = item.Timestamp.Format(time.RFC3339)
		}

		result = append(result, historyItem)
	}

	return result
}
