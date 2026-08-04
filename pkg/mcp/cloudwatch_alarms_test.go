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
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetCloudWatchAlarmsTools tests the alarm tool definitions.
func TestGetCloudWatchAlarmsTools(t *testing.T) {
	t.Parallel()

	tools := getCloudWatchAlarmsTools()

	assert.Len(t, tools, 2)

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	assert.True(t, toolNames[toolCloudWatchAlarmsList])
	assert.True(t, toolNames[toolCloudWatchAlarmsHistory])

	for _, tool := range tools {
		assert.NotEmpty(t, tool.Name)
		assert.NotEmpty(t, tool.Description)
		require.NotNil(t, tool.InputSchema)

		props, ok := tool.InputSchema["properties"].(map[string]interface{})
		require.True(t, ok, "tool %s should have properties", tool.Name)
		_, hasAccounts := props["accounts"]
		assert.True(t, hasAccounts, "tool %s should have accounts property", tool.Name)
	}
}

// TestParseAlarmNamesArg tests the alarm_names argument extraction.
func TestParseAlarmNamesArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]interface{}
		expected []string
	}{
		{name: "missing", args: map[string]interface{}{}, expected: nil},
		{
			name:     "valid",
			args:     map[string]interface{}{"alarm_names": []interface{}{"a", "b"}},
			expected: []string{"a", "b"},
		},
		{
			name:     "filters_empty",
			args:     map[string]interface{}{"alarm_names": []interface{}{"a", "", "b"}},
			expected: []string{"a", "b"},
		},
		{
			name:     "wrong_type",
			args:     map[string]interface{}{"alarm_names": "a"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := parseAlarmNamesArg(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestParseAlarmsLimitArg tests the alarm limit parsing.
func TestParseAlarmsLimitArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]interface{}
		expected int
	}{
		{name: "default", args: map[string]interface{}{}, expected: 100},
		{name: "custom", args: map[string]interface{}{"limit": float64(25)}, expected: 25},
		{name: "zero_falls_back", args: map[string]interface{}{"limit": float64(0)}, expected: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := parseAlarmsLimitArg(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDescribeAlarms tests alarm listing with a mock client.
func TestDescribeAlarms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("metric_and_composite", func(t *testing.T) {
		t.Parallel()

		threshold := 80.0
		updated := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

		mock := &mockCloudWatchMetricsClient{
			describeAlarmsFunc: func(_ context.Context, params *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
				assert.Contains(t, params.AlarmTypes, cwtypes.AlarmTypeMetricAlarm)
				assert.Contains(t, params.AlarmTypes, cwtypes.AlarmTypeCompositeAlarm)

				result = &cloudwatch.DescribeAlarmsOutput{
					MetricAlarms: []cwtypes.MetricAlarm{
						{
							AlarmName:             aws.String("high-cpu"),
							AlarmArn:              aws.String("arn:metric"),
							StateValue:            cwtypes.StateValueAlarm,
							StateReason:           aws.String("threshold crossed"),
							ActionsEnabled:        aws.Bool(true),
							Namespace:             aws.String("AWS/EC2"),
							MetricName:            aws.String("CPUUtilization"),
							Statistic:             cwtypes.StatisticAverage,
							ComparisonOperator:    cwtypes.ComparisonOperatorGreaterThanThreshold,
							Threshold:             &threshold,
							Period:                aws.Int32(300),
							EvaluationPeriods:     aws.Int32(2),
							StateUpdatedTimestamp: aws.Time(updated),
							Dimensions: []cwtypes.Dimension{
								{Name: aws.String("InstanceId"), Value: aws.String("i-123")},
							},
						},
					},
					CompositeAlarms: []cwtypes.CompositeAlarm{
						{
							AlarmName:   aws.String("service-down"),
							AlarmArn:    aws.String("arn:composite"),
							StateValue:  cwtypes.StateValueOk,
							AlarmRule:   aws.String("ALARM(high-cpu)"),
							StateReason: aws.String("all clear"),
						},
					},
				}
				return result, err
			},
		}

		params := alarmsListParams{region: "us-east-1", limit: 100}
		alarms, err := describeAlarms(ctx, mock, params)

		require.NoError(t, err)
		require.Len(t, alarms, 2)

		assert.Equal(t, "high-cpu", alarms[0].Name)
		assert.Equal(t, "metric", alarms[0].Type)
		assert.Equal(t, "ALARM", alarms[0].State)
		assert.Equal(t, "GreaterThanThreshold", alarms[0].ComparisonOperator)
		assert.InDelta(t, 80.0, *alarms[0].Threshold, 0.001)
		assert.Equal(t, "i-123", alarms[0].Dimensions["InstanceId"])
		assert.Equal(t, updated.Format(time.RFC3339), alarms[0].StateUpdated)

		assert.Equal(t, "service-down", alarms[1].Name)
		assert.Equal(t, "composite", alarms[1].Type)
		assert.Equal(t, "ALARM(high-cpu)", alarms[1].AlarmRule)
	})

	t.Run("state_filter_passed", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			describeAlarmsFunc: func(_ context.Context, params *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
				assert.Equal(t, cwtypes.StateValueAlarm, params.StateValue)
				assert.Equal(t, "prod-", aws.ToString(params.AlarmNamePrefix))

				result = &cloudwatch.DescribeAlarmsOutput{}
				return result, err
			},
		}

		params := alarmsListParams{region: "us-east-1", namePrefix: "prod-", stateValue: "alarm", limit: 100}
		_, err := describeAlarms(ctx, mock, params)

		require.NoError(t, err)
	})

	t.Run("pagination", func(t *testing.T) {
		t.Parallel()

		callCount := 0

		mock := &mockCloudWatchMetricsClient{
			describeAlarmsFunc: func(_ context.Context, _ *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
				callCount++

				if callCount == 1 {
					result = &cloudwatch.DescribeAlarmsOutput{
						MetricAlarms: []cwtypes.MetricAlarm{{AlarmName: aws.String("a1")}},
						NextToken:    aws.String("next"),
					}
					return result, err
				}

				result = &cloudwatch.DescribeAlarmsOutput{
					MetricAlarms: []cwtypes.MetricAlarm{{AlarmName: aws.String("a2")}},
				}
				return result, err
			},
		}

		params := alarmsListParams{region: "us-east-1", limit: 100}
		alarms, err := describeAlarms(ctx, mock, params)

		require.NoError(t, err)
		assert.Len(t, alarms, 2)
		assert.Equal(t, 2, callCount)
	})

	t.Run("limit_cap", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			describeAlarmsFunc: func(_ context.Context, _ *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
				result = &cloudwatch.DescribeAlarmsOutput{
					MetricAlarms: []cwtypes.MetricAlarm{
						{AlarmName: aws.String("a1")},
						{AlarmName: aws.String("a2")},
					},
					CompositeAlarms: []cwtypes.CompositeAlarm{
						{AlarmName: aws.String("c1")},
					},
					NextToken: aws.String("more"),
				}
				return result, err
			},
		}

		params := alarmsListParams{region: "us-east-1", limit: 2}
		alarms, err := describeAlarms(ctx, mock, params)

		require.NoError(t, err)
		assert.Len(t, alarms, 2)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			describeAlarmsFunc: func(_ context.Context, _ *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
				err = errors.New("access denied")
				return result, err
			},
		}

		params := alarmsListParams{region: "us-east-1", limit: 100}
		_, err := describeAlarms(ctx, mock, params)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "describing alarms")
	})
}

// TestDescribeAlarmHistory tests alarm history retrieval with a mock client.
func TestDescribeAlarmHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful", func(t *testing.T) {
		t.Parallel()

		ts := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

		mock := &mockCloudWatchMetricsClient{
			describeAlarmHistoryFunc: func(_ context.Context, params *cloudwatch.DescribeAlarmHistoryInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmHistoryOutput, err error) {
				assert.Equal(t, cwtypes.ScanByTimestampDescending, params.ScanBy)
				assert.Equal(t, "high-cpu", aws.ToString(params.AlarmName))

				result = &cloudwatch.DescribeAlarmHistoryOutput{
					AlarmHistoryItems: []cwtypes.AlarmHistoryItem{
						{
							AlarmName:       aws.String("high-cpu"),
							AlarmType:       cwtypes.AlarmTypeMetricAlarm,
							HistoryItemType: cwtypes.HistoryItemTypeStateUpdate,
							HistorySummary:  aws.String("OK -> ALARM"),
							Timestamp:       aws.Time(ts),
						},
					},
				}
				return result, err
			},
		}

		params := alarmHistoryParams{region: "us-east-1", alarmName: "high-cpu", limit: 100}
		items, err := describeAlarmHistory(ctx, mock, params)

		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "high-cpu", items[0].AlarmName)
		assert.Equal(t, "StateUpdate", items[0].ItemType)
		assert.Equal(t, "OK -> ALARM", items[0].Summary)
		assert.Equal(t, ts.Format(time.RFC3339), items[0].Timestamp)
	})

	t.Run("item_type_filter_passed", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			describeAlarmHistoryFunc: func(_ context.Context, params *cloudwatch.DescribeAlarmHistoryInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmHistoryOutput, err error) {
				assert.Equal(t, cwtypes.HistoryItemTypeStateUpdate, params.HistoryItemType)

				result = &cloudwatch.DescribeAlarmHistoryOutput{}
				return result, err
			},
		}

		params := alarmHistoryParams{region: "us-east-1", itemType: "StateUpdate", limit: 100}
		_, err := describeAlarmHistory(ctx, mock, params)

		require.NoError(t, err)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			describeAlarmHistoryFunc: func(_ context.Context, _ *cloudwatch.DescribeAlarmHistoryInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmHistoryOutput, err error) {
				err = errors.New("throttled")
				return result, err
			},
		}

		params := alarmHistoryParams{region: "us-east-1", limit: 100}
		_, err := describeAlarmHistory(ctx, mock, params)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "describing alarm history")
	})
}

// TestParseAlarmHistoryParams tests validation of the history arguments.
func TestParseAlarmHistoryParams(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()

		params, err := parseAlarmHistoryParams(map[string]interface{}{})

		require.NoError(t, err)
		assert.Equal(t, "us-east-1", params.region)
		assert.Equal(t, 100, params.limit)
		assert.Nil(t, params.startTime)
		assert.Nil(t, params.endTime)
	})

	t.Run("parses_times", func(t *testing.T) {
		t.Parallel()

		params, err := parseAlarmHistoryParams(map[string]interface{}{
			"start_time": "1h",
			"end_time":   "2024-01-15T12:00:00Z",
		})

		require.NoError(t, err)
		require.NotNil(t, params.startTime)
		require.NotNil(t, params.endTime)
	})

	t.Run("invalid_start_time", func(t *testing.T) {
		t.Parallel()

		_, err := parseAlarmHistoryParams(map[string]interface{}{"start_time": "bogus"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing start_time")
	})

	t.Run("invalid_end_time", func(t *testing.T) {
		t.Parallel()

		_, err := parseAlarmHistoryParams(map[string]interface{}{"end_time": "bogus"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing end_time")
	})
}

// TestServerExecuteCloudWatchAlarmsHistoryValidation tests server-level validation.
func TestServerExecuteCloudWatchAlarmsHistoryValidation(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, "")
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	server := &Server{logger: logger}

	_, err := server.executeCloudWatchAlarmsHistory(ctx, map[string]interface{}{
		"start_time": "bogus",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing start_time")
}

// TestExecuteMultiAccountAlarmsList tests multi-account alarm listing.
func TestExecuteMultiAccountAlarmsList(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, `{"dev":"arn:dev","prod":"arn:prod"}`)
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("results_labeled_per_account", func(t *testing.T) {
		server := &Server{
			logger: logger,
			cloudWatchMetricsClientFactory: func(_ context.Context, _ string, roleARN string) (client CloudWatchMetricsClient, err error) {
				alarmName := "dev-alarm"
				if roleARN == "arn:prod" {
					alarmName = "prod-alarm"
				}

				client = &mockCloudWatchMetricsClient{
					describeAlarmsFunc: func(_ context.Context, _ *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
						result = &cloudwatch.DescribeAlarmsOutput{
							MetricAlarms: []cwtypes.MetricAlarm{
								{AlarmName: aws.String(alarmName), StateValue: cwtypes.StateValueOk},
							},
						}
						return result, err
					},
				}
				return client, err
			},
		}

		result, err := server.executeCloudWatchAlarmsList(ctx, map[string]interface{}{})

		require.NoError(t, err)
		assert.Contains(t, result, `"account": "dev"`)
		assert.Contains(t, result, `"account": "prod"`)
		assert.Contains(t, result, "dev-alarm")
		assert.Contains(t, result, "prod-alarm")
	})

	t.Run("one_account_error_doesnt_block_others", func(t *testing.T) {
		server := &Server{
			logger: logger,
			cloudWatchMetricsClientFactory: func(_ context.Context, _ string, roleARN string) (client CloudWatchMetricsClient, err error) {
				if roleARN == "arn:dev" {
					err = errors.New("assume role failed for dev")
					return client, err
				}

				client = &mockCloudWatchMetricsClient{
					describeAlarmsFunc: func(_ context.Context, _ *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
						result = &cloudwatch.DescribeAlarmsOutput{
							MetricAlarms: []cwtypes.MetricAlarm{
								{AlarmName: aws.String("prod-alarm"), StateValue: cwtypes.StateValueAlarm},
							},
						}
						return result, err
					},
				}
				return client, err
			},
		}

		result, err := server.executeCloudWatchAlarmsList(ctx, map[string]interface{}{})

		require.NoError(t, err)
		assert.Contains(t, result, "assume role failed for dev")
		assert.Contains(t, result, "prod-alarm")
	})
}

// TestExecuteMultiAccountAlarmsHistory tests multi-account alarm history.
func TestExecuteMultiAccountAlarmsHistory(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, `{"dev":"arn:dev","prod":"arn:prod"}`)
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	now := time.Now()

	server := &Server{
		logger: logger,
		cloudWatchMetricsClientFactory: func(_ context.Context, _ string, roleARN string) (client CloudWatchMetricsClient, err error) {
			summary := "dev transition"
			if roleARN == "arn:prod" {
				summary = "prod transition"
			}

			client = &mockCloudWatchMetricsClient{
				describeAlarmHistoryFunc: func(_ context.Context, _ *cloudwatch.DescribeAlarmHistoryInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmHistoryOutput, err error) {
					result = &cloudwatch.DescribeAlarmHistoryOutput{
						AlarmHistoryItems: []cwtypes.AlarmHistoryItem{
							{HistorySummary: aws.String(summary), Timestamp: aws.Time(now)},
						},
					}
					return result, err
				},
			}
			return client, err
		},
	}

	result, err := server.executeCloudWatchAlarmsHistory(ctx, map[string]interface{}{
		"alarm_name": "high-cpu",
		"accounts":   []interface{}{"prod"},
	})

	require.NoError(t, err)
	assert.Contains(t, result, `"account": "prod"`)
	assert.Contains(t, result, "prod transition")
	assert.NotContains(t, result, "dev transition")
}
