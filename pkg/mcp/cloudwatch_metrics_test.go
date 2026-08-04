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

// mockCloudWatchMetricsClient implements CloudWatchMetricsClient for testing.
type mockCloudWatchMetricsClient struct {
	listMetricsFunc          func(ctx context.Context, params *cloudwatch.ListMetricsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.ListMetricsOutput, error)
	getMetricStatisticsFunc  func(ctx context.Context, params *cloudwatch.GetMetricStatisticsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricStatisticsOutput, error)
	getMetricDataFunc        func(ctx context.Context, params *cloudwatch.GetMetricDataInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
	describeAlarmsFunc       func(ctx context.Context, params *cloudwatch.DescribeAlarmsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error)
	describeAlarmHistoryFunc func(ctx context.Context, params *cloudwatch.DescribeAlarmHistoryInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmHistoryOutput, error)
}

func (m *mockCloudWatchMetricsClient) ListMetrics(ctx context.Context, params *cloudwatch.ListMetricsInput, optFns ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
	if m.listMetricsFunc != nil {
		result, err = m.listMetricsFunc(ctx, params, optFns...)
		return result, err
	}
	err = errors.New("listMetricsFunc not implemented")
	return result, err
}

func (m *mockCloudWatchMetricsClient) GetMetricStatistics(ctx context.Context, params *cloudwatch.GetMetricStatisticsInput, optFns ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricStatisticsOutput, err error) {
	if m.getMetricStatisticsFunc != nil {
		result, err = m.getMetricStatisticsFunc(ctx, params, optFns...)
		return result, err
	}
	err = errors.New("getMetricStatisticsFunc not implemented")
	return result, err
}

func (m *mockCloudWatchMetricsClient) GetMetricData(ctx context.Context, params *cloudwatch.GetMetricDataInput, optFns ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricDataOutput, err error) {
	if m.getMetricDataFunc != nil {
		result, err = m.getMetricDataFunc(ctx, params, optFns...)
		return result, err
	}
	err = errors.New("getMetricDataFunc not implemented")
	return result, err
}

func (m *mockCloudWatchMetricsClient) DescribeAlarms(ctx context.Context, params *cloudwatch.DescribeAlarmsInput, optFns ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmsOutput, err error) {
	if m.describeAlarmsFunc != nil {
		result, err = m.describeAlarmsFunc(ctx, params, optFns...)
		return result, err
	}
	err = errors.New("describeAlarmsFunc not implemented")
	return result, err
}

func (m *mockCloudWatchMetricsClient) DescribeAlarmHistory(ctx context.Context, params *cloudwatch.DescribeAlarmHistoryInput, optFns ...func(*cloudwatch.Options)) (result *cloudwatch.DescribeAlarmHistoryOutput, err error) {
	if m.describeAlarmHistoryFunc != nil {
		result, err = m.describeAlarmHistoryFunc(ctx, params, optFns...)
		return result, err
	}
	err = errors.New("describeAlarmHistoryFunc not implemented")
	return result, err
}

// TestGetCloudWatchMetricsTools tests the metrics tool definitions.
func TestGetCloudWatchMetricsTools(t *testing.T) {
	t.Parallel()

	tools := getCloudWatchMetricsTools()

	assert.Len(t, tools, 3)

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	assert.True(t, toolNames[toolCloudWatchMetricsList])
	assert.True(t, toolNames[toolCloudWatchMetricsGetStatistics])
	assert.True(t, toolNames[toolCloudWatchMetricsQuery])

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

// TestParseDimensionsArg tests parsing dimensions into sorted SDK dimensions.
func TestParseDimensionsArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]interface{}
		expected map[string]string
	}{
		{
			name:     "missing_dimensions",
			args:     map[string]interface{}{},
			expected: nil,
		},
		{
			name: "single_dimension",
			args: map[string]interface{}{
				"dimensions": map[string]interface{}{"InstanceId": "i-123"},
			},
			expected: map[string]string{"InstanceId": "i-123"},
		},
		{
			name: "multiple_sorted",
			args: map[string]interface{}{
				"dimensions": map[string]interface{}{"Zeta": "z", "Alpha": "a"},
			},
			expected: map[string]string{"Alpha": "a", "Zeta": "z"},
		},
		{
			name: "skips_empty_and_non_string",
			args: map[string]interface{}{
				"dimensions": map[string]interface{}{"Good": "v", "Empty": "", "Num": 5},
			},
			expected: map[string]string{"Good": "v"},
		},
		{
			name: "wrong_type",
			args: map[string]interface{}{
				"dimensions": "not-an-object",
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dims := parseDimensionsArg(tt.args)
			assert.Equal(t, tt.expected, dimensionsToMap(dims))
		})
	}
}

// TestParseDimensionsArgSorted verifies dimensions are ordered for determinism.
func TestParseDimensionsArgSorted(t *testing.T) {
	t.Parallel()

	args := map[string]interface{}{
		"dimensions": map[string]interface{}{"Zeta": "z", "Alpha": "a", "Mid": "m"},
	}

	dims := parseDimensionsArg(args)

	require.Len(t, dims, 3)
	assert.Equal(t, "Alpha", aws.ToString(dims[0].Name))
	assert.Equal(t, "Mid", aws.ToString(dims[1].Name))
	assert.Equal(t, "Zeta", aws.ToString(dims[2].Name))
}

// TestStandardStatistic tests the statistic string classification.
func TestStandardStatistic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		expectStat cwtypes.Statistic
		expectOK   bool
	}{
		{name: "average", input: "Average", expectStat: cwtypes.StatisticAverage, expectOK: true},
		{name: "avg_alias", input: "avg", expectStat: cwtypes.StatisticAverage, expectOK: true},
		{name: "sum", input: "Sum", expectStat: cwtypes.StatisticSum, expectOK: true},
		{name: "min_alias", input: "min", expectStat: cwtypes.StatisticMinimum, expectOK: true},
		{name: "max_alias", input: "max", expectStat: cwtypes.StatisticMaximum, expectOK: true},
		{name: "sample_count", input: "SampleCount", expectStat: cwtypes.StatisticSampleCount, expectOK: true},
		{name: "percentile", input: "p99", expectOK: false},
		{name: "unknown", input: "bogus", expectOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stat, ok := standardStatistic(tt.input)
			assert.Equal(t, tt.expectOK, ok)
			if tt.expectOK {
				assert.Equal(t, tt.expectStat, stat)
			}
		})
	}
}

// TestParseStatisticsArg tests splitting standard vs extended statistics.
func TestParseStatisticsArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		args           map[string]interface{}
		expectStats    []cwtypes.Statistic
		expectExtended []string
	}{
		{
			name:        "default_average",
			args:        map[string]interface{}{},
			expectStats: []cwtypes.Statistic{cwtypes.StatisticAverage},
		},
		{
			name: "standard_stats",
			args: map[string]interface{}{
				"statistics": []interface{}{"Sum", "Maximum"},
			},
			expectStats: []cwtypes.Statistic{cwtypes.StatisticSum, cwtypes.StatisticMaximum},
		},
		{
			name: "extended_only",
			args: map[string]interface{}{
				"statistics": []interface{}{"p99"},
			},
			expectExtended: []string{"p99"},
		},
		{
			name: "mixed",
			args: map[string]interface{}{
				"statistics": []interface{}{"Average", "p95"},
			},
			expectStats:    []cwtypes.Statistic{cwtypes.StatisticAverage},
			expectExtended: []string{"p95"},
		},
		{
			name: "empty_array_defaults_average",
			args: map[string]interface{}{
				"statistics": []interface{}{},
			},
			expectStats: []cwtypes.Statistic{cwtypes.StatisticAverage},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats, extended := parseStatisticsArg(tt.args)
			assert.Equal(t, tt.expectStats, stats)
			assert.Equal(t, tt.expectExtended, extended)
		})
	}
}

// TestParseMetricPeriodArg tests period parsing with default.
func TestParseMetricPeriodArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]interface{}
		expected int32
	}{
		{name: "default", args: map[string]interface{}{}, expected: 300},
		{name: "custom", args: map[string]interface{}{"period": float64(60)}, expected: 60},
		{name: "zero_falls_back", args: map[string]interface{}{"period": float64(0)}, expected: 300},
		{name: "wrong_type", args: map[string]interface{}{"period": "60"}, expected: 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := parseMetricPeriodArg(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestParseMetricsListLimitArg tests the metrics list limit parsing.
func TestParseMetricsListLimitArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]interface{}
		expected int
	}{
		{name: "default", args: map[string]interface{}{}, expected: 500},
		{name: "custom", args: map[string]interface{}{"limit": float64(100)}, expected: 100},
		{name: "zero_falls_back", args: map[string]interface{}{"limit": float64(0)}, expected: 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := parseMetricsListLimitArg(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestListMetrics tests metric listing with mock client.
func TestListMetrics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful_list", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			listMetricsFunc: func(_ context.Context, params *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
				assert.Equal(t, "AWS/EC2", aws.ToString(params.Namespace))

				result = &cloudwatch.ListMetricsOutput{
					Metrics: []cwtypes.Metric{
						{
							Namespace:  aws.String("AWS/EC2"),
							MetricName: aws.String("CPUUtilization"),
							Dimensions: []cwtypes.Dimension{
								{Name: aws.String("InstanceId"), Value: aws.String("i-123")},
							},
						},
					},
				}
				return result, err
			},
		}

		metrics, err := listMetrics(ctx, mock, "AWS/EC2", "", nil, 500)

		require.NoError(t, err)
		require.Len(t, metrics, 1)
		assert.Equal(t, "AWS/EC2", metrics[0].Namespace)
		assert.Equal(t, "CPUUtilization", metrics[0].MetricName)
		assert.Equal(t, "i-123", metrics[0].Dimensions["InstanceId"])
	})

	t.Run("dimension_filter_passed", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			listMetricsFunc: func(_ context.Context, params *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
				require.Len(t, params.Dimensions, 1)
				assert.Equal(t, "InstanceId", aws.ToString(params.Dimensions[0].Name))
				assert.Equal(t, "i-123", aws.ToString(params.Dimensions[0].Value))

				result = &cloudwatch.ListMetricsOutput{}
				return result, err
			},
		}

		dims := []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: aws.String("i-123")}}
		_, err := listMetrics(ctx, mock, "AWS/EC2", "CPUUtilization", dims, 500)

		require.NoError(t, err)
	})

	t.Run("pagination", func(t *testing.T) {
		t.Parallel()

		callCount := 0

		mock := &mockCloudWatchMetricsClient{
			listMetricsFunc: func(_ context.Context, _ *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
				callCount++

				if callCount == 1 {
					result = &cloudwatch.ListMetricsOutput{
						Metrics: []cwtypes.Metric{
							{Namespace: aws.String("AWS/EC2"), MetricName: aws.String("m1")},
						},
						NextToken: aws.String("token"),
					}
					return result, err
				}

				result = &cloudwatch.ListMetricsOutput{
					Metrics: []cwtypes.Metric{
						{Namespace: aws.String("AWS/EC2"), MetricName: aws.String("m2")},
					},
				}
				return result, err
			},
		}

		metrics, err := listMetrics(ctx, mock, "", "", nil, 500)

		require.NoError(t, err)
		assert.Len(t, metrics, 2)
		assert.Equal(t, 2, callCount)
	})

	t.Run("limit_cap", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			listMetricsFunc: func(_ context.Context, _ *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
				result = &cloudwatch.ListMetricsOutput{
					Metrics: []cwtypes.Metric{
						{Namespace: aws.String("AWS/EC2"), MetricName: aws.String("m1")},
						{Namespace: aws.String("AWS/EC2"), MetricName: aws.String("m2")},
						{Namespace: aws.String("AWS/EC2"), MetricName: aws.String("m3")},
					},
					NextToken: aws.String("more"),
				}
				return result, err
			},
		}

		metrics, err := listMetrics(ctx, mock, "", "", nil, 2)

		require.NoError(t, err)
		assert.Len(t, metrics, 2)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			listMetricsFunc: func(_ context.Context, _ *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
				err = errors.New("access denied")
				return result, err
			},
		}

		_, err := listMetrics(ctx, mock, "", "", nil, 500)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "listing metrics")
	})
}

// TestConvertDatapoints tests datapoint conversion and timestamp sorting.
func TestConvertDatapoints(t *testing.T) {
	t.Parallel()

	t.Run("sorted_ascending", func(t *testing.T) {
		t.Parallel()

		t1 := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
		t2 := time.Date(2024, 1, 15, 13, 0, 0, 0, time.UTC)
		t3 := time.Date(2024, 1, 15, 14, 0, 0, 0, time.UTC)

		avg := 1.5

		points := []cwtypes.Datapoint{
			{Timestamp: aws.Time(t3), Average: &avg, Unit: cwtypes.StandardUnitPercent},
			{Timestamp: aws.Time(t1), Average: &avg},
			{Timestamp: aws.Time(t2), Average: &avg},
		}

		result := convertDatapoints(points)

		require.Len(t, result, 3)
		assert.Equal(t, t1.Format(time.RFC3339), result[0].Timestamp)
		assert.Equal(t, t2.Format(time.RFC3339), result[1].Timestamp)
		assert.Equal(t, t3.Format(time.RFC3339), result[2].Timestamp)
		assert.Equal(t, "Percent", result[2].Unit)
	})

	t.Run("extended_statistics", func(t *testing.T) {
		t.Parallel()

		points := []cwtypes.Datapoint{
			{
				Timestamp:          aws.Time(time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)),
				ExtendedStatistics: map[string]float64{"p99": 99.9},
			},
		}

		result := convertDatapoints(points)

		require.Len(t, result, 1)
		assert.InDelta(t, 99.9, result[0].Extended["p99"], 0.001)
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		result := convertDatapoints(nil)
		assert.Empty(t, result)
	})
}

// TestGetMetricStatistics tests fetching statistics with a mock client.
func TestGetMetricStatistics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	start := time.Now().Add(-1 * time.Hour)
	end := time.Now()

	t.Run("successful", func(t *testing.T) {
		t.Parallel()

		avg := 42.0

		mock := &mockCloudWatchMetricsClient{
			getMetricStatisticsFunc: func(_ context.Context, params *cloudwatch.GetMetricStatisticsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricStatisticsOutput, err error) {
				assert.Equal(t, "AWS/EC2", aws.ToString(params.Namespace))
				assert.Equal(t, "CPUUtilization", aws.ToString(params.MetricName))
				assert.Equal(t, int32(300), aws.ToInt32(params.Period))
				require.Len(t, params.Statistics, 1)

				result = &cloudwatch.GetMetricStatisticsOutput{
					Label: aws.String("CPUUtilization"),
					Datapoints: []cwtypes.Datapoint{
						{Timestamp: aws.Time(start), Average: &avg, Unit: cwtypes.StandardUnitPercent},
					},
				}
				return result, err
			},
		}

		params := metricStatisticsParams{
			region:     "us-east-1",
			namespace:  "AWS/EC2",
			metricName: "CPUUtilization",
			dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: aws.String("i-123")}},
			statistics: []cwtypes.Statistic{cwtypes.StatisticAverage},
			period:     300,
			startTime:  start,
			endTime:    end,
		}

		result, err := getMetricStatistics(ctx, mock, params)

		require.NoError(t, err)
		assert.Equal(t, "AWS/EC2", result.Namespace)
		assert.Equal(t, "CPUUtilization", result.Label)
		assert.Equal(t, 1, result.Count)
		require.Len(t, result.Datapoints, 1)
		assert.InDelta(t, 42.0, *result.Datapoints[0].Average, 0.001)
		assert.Equal(t, "i-123", result.Dimensions["InstanceId"])
	})

	t.Run("extended_statistics_passed", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			getMetricStatisticsFunc: func(_ context.Context, params *cloudwatch.GetMetricStatisticsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricStatisticsOutput, err error) {
				assert.Empty(t, params.Statistics)
				require.Len(t, params.ExtendedStatistics, 1)
				assert.Equal(t, "p99", params.ExtendedStatistics[0])

				result = &cloudwatch.GetMetricStatisticsOutput{}
				return result, err
			},
		}

		params := metricStatisticsParams{
			region:     "us-east-1",
			namespace:  "AWS/EC2",
			metricName: "CPUUtilization",
			extended:   []string{"p99"},
			period:     300,
			startTime:  start,
			endTime:    end,
		}

		_, err := getMetricStatistics(ctx, mock, params)

		require.NoError(t, err)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			getMetricStatisticsFunc: func(_ context.Context, _ *cloudwatch.GetMetricStatisticsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricStatisticsOutput, err error) {
				err = errors.New("throttled")
				return result, err
			},
		}

		params := metricStatisticsParams{
			namespace:  "AWS/EC2",
			metricName: "CPUUtilization",
			period:     300,
			startTime:  start,
			endTime:    end,
		}

		_, err := getMetricStatistics(ctx, mock, params)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "getting metric statistics")
	})
}

// TestBuildMetricDataQueries tests parsing the queries argument.
func TestBuildMetricDataQueries(t *testing.T) {
	t.Parallel()

	t.Run("metric_query", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{
					"id":          "m1",
					"namespace":   "AWS/EC2",
					"metric_name": "CPUUtilization",
					"stat":        "Average",
					"period":      float64(60),
					"dimensions":  map[string]interface{}{"InstanceId": "i-123"},
				},
			},
		}

		queries, err := buildMetricDataQueries(args)

		require.NoError(t, err)
		require.Len(t, queries, 1)
		assert.Equal(t, "m1", aws.ToString(queries[0].Id))
		require.NotNil(t, queries[0].MetricStat)
		assert.Equal(t, "AWS/EC2", aws.ToString(queries[0].MetricStat.Metric.Namespace))
		assert.Equal(t, "Average", aws.ToString(queries[0].MetricStat.Stat))
		assert.Equal(t, int32(60), aws.ToInt32(queries[0].MetricStat.Period))
		require.Len(t, queries[0].MetricStat.Metric.Dimensions, 1)
	})

	t.Run("expression_query", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{
					"id":          "e1",
					"expression":  "m1/m2*100",
					"label":       "error rate",
					"return_data": true,
				},
			},
		}

		queries, err := buildMetricDataQueries(args)

		require.NoError(t, err)
		require.Len(t, queries, 1)
		assert.Equal(t, "m1/m2*100", aws.ToString(queries[0].Expression))
		assert.Equal(t, "error rate", aws.ToString(queries[0].Label))
		assert.True(t, aws.ToBool(queries[0].ReturnData))
		assert.Nil(t, queries[0].MetricStat)
	})

	t.Run("default_stat", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{
					"id":          "m1",
					"namespace":   "AWS/EC2",
					"metric_name": "CPUUtilization",
				},
			},
		}

		queries, err := buildMetricDataQueries(args)

		require.NoError(t, err)
		require.NotNil(t, queries[0].MetricStat)
		assert.Equal(t, "Average", aws.ToString(queries[0].MetricStat.Stat))
		assert.Equal(t, int32(300), aws.ToInt32(queries[0].MetricStat.Period))
	})

	t.Run("missing_queries", func(t *testing.T) {
		t.Parallel()

		_, err := buildMetricDataQueries(map[string]interface{}{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "queries parameter is required")
	})

	t.Run("empty_queries", func(t *testing.T) {
		t.Parallel()

		_, err := buildMetricDataQueries(map[string]interface{}{"queries": []interface{}{}})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "queries parameter is required")
	})

	t.Run("missing_id", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{"namespace": "AWS/EC2", "metric_name": "CPUUtilization"},
			},
		}

		_, err := buildMetricDataQueries(args)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("missing_metric_and_expression", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{"id": "m1"},
			},
		}

		_, err := buildMetricDataQueries(args)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires either 'expression'")
	})

	t.Run("query_not_object", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{
			"queries": []interface{}{"not-an-object"},
		}

		_, err := buildMetricDataQueries(args)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be an object")
	})
}

// TestAppendMetricDataPoints tests zipping parallel timestamp/value arrays.
func TestAppendMetricDataPoints(t *testing.T) {
	t.Parallel()

	t.Run("equal_lengths", func(t *testing.T) {
		t.Parallel()

		ts := []time.Time{
			time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
			time.Date(2024, 1, 15, 13, 0, 0, 0, time.UTC),
		}
		values := []float64{1.0, 2.0}

		result := appendMetricDataPoints(nil, ts, values)

		require.Len(t, result, 2)
		assert.InDelta(t, 1.0, result[0].Value, 0.001)
		assert.InDelta(t, 2.0, result[1].Value, 0.001)
	})

	t.Run("mismatched_lengths_uses_min", func(t *testing.T) {
		t.Parallel()

		ts := []time.Time{
			time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
			time.Date(2024, 1, 15, 13, 0, 0, 0, time.UTC),
		}
		values := []float64{1.0}

		result := appendMetricDataPoints(nil, ts, values)

		require.Len(t, result, 1)
		assert.InDelta(t, 1.0, result[0].Value, 0.001)
	})
}

// TestGetMetricData tests GetMetricData with mock client, including pagination merge.
func TestGetMetricData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	start := time.Now().Add(-1 * time.Hour)
	end := time.Now()

	queries := []cwtypes.MetricDataQuery{
		{Id: aws.String("m1")},
	}

	t.Run("single_page", func(t *testing.T) {
		t.Parallel()

		ts := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

		mock := &mockCloudWatchMetricsClient{
			getMetricDataFunc: func(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricDataOutput, err error) {
				result = &cloudwatch.GetMetricDataOutput{
					MetricDataResults: []cwtypes.MetricDataResult{
						{
							Id:         aws.String("m1"),
							Label:      aws.String("CPU"),
							StatusCode: cwtypes.StatusCodeComplete,
							Timestamps: []time.Time{ts},
							Values:     []float64{55.5},
						},
					},
				}
				return result, err
			},
		}

		result, err := getMetricData(ctx, mock, "us-east-1", queries, start, end)

		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Equal(t, "m1", result.Results[0].ID)
		assert.Equal(t, "CPU", result.Results[0].Label)
		assert.Equal(t, 1, result.Results[0].Count)
		assert.InDelta(t, 55.5, result.Results[0].Values[0].Value, 0.001)
	})

	t.Run("pagination_merges_series", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		t1 := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
		t2 := time.Date(2024, 1, 15, 12, 5, 0, 0, time.UTC)

		mock := &mockCloudWatchMetricsClient{
			getMetricDataFunc: func(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricDataOutput, err error) {
				callCount++

				if callCount == 1 {
					result = &cloudwatch.GetMetricDataOutput{
						MetricDataResults: []cwtypes.MetricDataResult{
							{Id: aws.String("m1"), Timestamps: []time.Time{t1}, Values: []float64{1.0}},
						},
						NextToken: aws.String("next"),
					}
					return result, err
				}

				result = &cloudwatch.GetMetricDataOutput{
					MetricDataResults: []cwtypes.MetricDataResult{
						{Id: aws.String("m1"), Timestamps: []time.Time{t2}, Values: []float64{2.0}},
					},
				}
				return result, err
			},
		}

		result, err := getMetricData(ctx, mock, "us-east-1", queries, start, end)

		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Equal(t, 2, result.Results[0].Count)
		assert.Equal(t, 2, callCount)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		mock := &mockCloudWatchMetricsClient{
			getMetricDataFunc: func(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricDataOutput, err error) {
				err = errors.New("invalid expression")
				return result, err
			},
		}

		_, err := getMetricData(ctx, mock, "us-east-1", queries, start, end)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "getting metric data")
	})
}

// TestServerExecuteCloudWatchMetricsGetStatistics tests argument validation.
func TestServerExecuteCloudWatchMetricsGetStatistics(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	server := &Server{logger: logger}

	t.Run("missing_namespace", func(t *testing.T) {
		_, err := server.executeCloudWatchMetricsGetStatistics(ctx, map[string]interface{}{
			"metric_name": "CPUUtilization",
			"start_time":  "1h",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "namespace parameter is required")
	})

	t.Run("missing_metric_name", func(t *testing.T) {
		_, err := server.executeCloudWatchMetricsGetStatistics(ctx, map[string]interface{}{
			"namespace":  "AWS/EC2",
			"start_time": "1h",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "metric_name parameter is required")
	})

	t.Run("missing_start_time", func(t *testing.T) {
		_, err := server.executeCloudWatchMetricsGetStatistics(ctx, map[string]interface{}{
			"namespace":   "AWS/EC2",
			"metric_name": "CPUUtilization",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start_time parameter is required")
	})

	t.Run("invalid_start_time", func(t *testing.T) {
		_, err := server.executeCloudWatchMetricsGetStatistics(ctx, map[string]interface{}{
			"namespace":   "AWS/EC2",
			"metric_name": "CPUUtilization",
			"start_time":  "bogus",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing start_time")
	})
}

// TestServerExecuteCloudWatchMetricsQuery tests argument validation.
func TestServerExecuteCloudWatchMetricsQuery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	server := &Server{logger: logger}

	t.Run("missing_queries", func(t *testing.T) {
		_, err := server.executeCloudWatchMetricsQuery(ctx, map[string]interface{}{
			"start_time": "1h",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "queries parameter is required")
	})

	t.Run("missing_start_time", func(t *testing.T) {
		_, err := server.executeCloudWatchMetricsQuery(ctx, map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{"id": "m1", "namespace": "AWS/EC2", "metric_name": "CPUUtilization"},
			},
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "start_time parameter is required")
	})
}

// TestExecuteMultiAccountListMetrics tests multi-account metric listing.
func TestExecuteMultiAccountListMetrics(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, `{"dev":"arn:dev","prod":"arn:prod"}`)
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("results_labeled_per_account", func(t *testing.T) {
		server := &Server{
			logger: logger,
			cloudWatchMetricsClientFactory: func(_ context.Context, _ string, roleARN string) (client CloudWatchMetricsClient, err error) {
				metricName := "dev-metric"
				if roleARN == "arn:prod" {
					metricName = "prod-metric"
				}

				client = &mockCloudWatchMetricsClient{
					listMetricsFunc: func(_ context.Context, _ *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
						result = &cloudwatch.ListMetricsOutput{
							Metrics: []cwtypes.Metric{
								{Namespace: aws.String("AWS/EC2"), MetricName: aws.String(metricName)},
							},
						}
						return result, err
					},
				}
				return client, err
			},
		}

		result, err := server.executeCloudWatchMetricsList(ctx, map[string]interface{}{})

		require.NoError(t, err)
		assert.Contains(t, result, `"account": "dev"`)
		assert.Contains(t, result, `"account": "prod"`)
		assert.Contains(t, result, "dev-metric")
		assert.Contains(t, result, "prod-metric")
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
					listMetricsFunc: func(_ context.Context, _ *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
						result = &cloudwatch.ListMetricsOutput{
							Metrics: []cwtypes.Metric{
								{Namespace: aws.String("AWS/EC2"), MetricName: aws.String("prod-metric")},
							},
						}
						return result, err
					},
				}
				return client, err
			},
		}

		result, err := server.executeCloudWatchMetricsList(ctx, map[string]interface{}{})

		require.NoError(t, err)
		assert.Contains(t, result, "assume role failed for dev")
		assert.Contains(t, result, "prod-metric")
	})

	t.Run("filter_by_account_name", func(t *testing.T) {
		factoryCalled := make(map[string]bool)

		server := &Server{
			logger: logger,
			cloudWatchMetricsClientFactory: func(_ context.Context, _ string, roleARN string) (client CloudWatchMetricsClient, err error) {
				factoryCalled[roleARN] = true

				client = &mockCloudWatchMetricsClient{
					listMetricsFunc: func(_ context.Context, _ *cloudwatch.ListMetricsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.ListMetricsOutput, err error) {
						result = &cloudwatch.ListMetricsOutput{
							Metrics: []cwtypes.Metric{
								{Namespace: aws.String("AWS/EC2"), MetricName: aws.String("prod-metric")},
							},
						}
						return result, err
					},
				}
				return client, err
			},
		}

		result, err := server.executeCloudWatchMetricsList(ctx, map[string]interface{}{
			"accounts": []interface{}{"prod"},
		})

		require.NoError(t, err)
		assert.True(t, factoryCalled["arn:prod"])
		assert.False(t, factoryCalled["arn:dev"])
		assert.Contains(t, result, `"account": "prod"`)
	})
}

// TestExecuteMultiAccountGetMetricStatistics tests multi-account statistics.
func TestExecuteMultiAccountGetMetricStatistics(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, `{"dev":"arn:dev","prod":"arn:prod"}`)
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	now := time.Now()

	server := &Server{
		logger: logger,
		cloudWatchMetricsClientFactory: func(_ context.Context, _ string, roleARN string) (client CloudWatchMetricsClient, err error) {
			value := 10.0
			if roleARN == "arn:prod" {
				value = 90.0
			}

			client = &mockCloudWatchMetricsClient{
				getMetricStatisticsFunc: func(_ context.Context, _ *cloudwatch.GetMetricStatisticsInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricStatisticsOutput, err error) {
					result = &cloudwatch.GetMetricStatisticsOutput{
						Datapoints: []cwtypes.Datapoint{
							{Timestamp: aws.Time(now), Average: &value},
						},
					}
					return result, err
				},
			}
			return client, err
		},
	}

	result, err := server.executeCloudWatchMetricsGetStatistics(ctx, map[string]interface{}{
		"namespace":   "AWS/EC2",
		"metric_name": "CPUUtilization",
		"start_time":  "1h",
	})

	require.NoError(t, err)
	assert.Contains(t, result, `"account": "dev"`)
	assert.Contains(t, result, `"account": "prod"`)
	assert.Contains(t, result, "90")
}

// TestExecuteMultiAccountGetMetricData tests multi-account GetMetricData.
func TestExecuteMultiAccountGetMetricData(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, `{"dev":"arn:dev","prod":"arn:prod"}`)
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	now := time.Now()

	server := &Server{
		logger: logger,
		cloudWatchMetricsClientFactory: func(_ context.Context, _ string, roleARN string) (client CloudWatchMetricsClient, err error) {
			label := "dev-series"
			if roleARN == "arn:prod" {
				label = "prod-series"
			}

			client = &mockCloudWatchMetricsClient{
				getMetricDataFunc: func(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (result *cloudwatch.GetMetricDataOutput, err error) {
					result = &cloudwatch.GetMetricDataOutput{
						MetricDataResults: []cwtypes.MetricDataResult{
							{Id: aws.String("m1"), Label: aws.String(label), Timestamps: []time.Time{now}, Values: []float64{1.0}},
						},
					}
					return result, err
				},
			}
			return client, err
		},
	}

	result, err := server.executeCloudWatchMetricsQuery(ctx, map[string]interface{}{
		"queries": []interface{}{
			map[string]interface{}{"id": "m1", "namespace": "AWS/EC2", "metric_name": "CPUUtilization"},
		},
		"start_time": "1h",
		"accounts":   []interface{}{"prod"},
	})

	require.NoError(t, err)
	assert.Contains(t, result, `"account": "prod"`)
	assert.Contains(t, result, "prod-series")
	assert.NotContains(t, result, "dev-series")
}
