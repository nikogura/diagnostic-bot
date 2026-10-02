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
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikogura/diagnostic-bot/pkg/k8s"
)

// cloudWatchRowsByRole returns a client factory whose queries answer with the
// rows configured for the role being assumed; a role with none matches nothing.
func cloudWatchRowsByRole(rows map[string][]string) (factory CloudWatchClientFactory) {
	factory = func(_ context.Context, _ string, roleARN string) (client CloudWatchLogsClient, err error) {
		client = &mockCloudWatchLogsClient{
			startQueryFunc: func(_ context.Context, _ *cloudwatchlogs.StartQueryInput, _ ...func(*cloudwatchlogs.Options)) (result *cloudwatchlogs.StartQueryOutput, err error) {
				result = &cloudwatchlogs.StartQueryOutput{QueryId: aws.String("qid-" + roleARN)}
				return result, err
			},
			getQueryResultsFunc: func(_ context.Context, _ *cloudwatchlogs.GetQueryResultsInput, _ ...func(*cloudwatchlogs.Options)) (result *cloudwatchlogs.GetQueryResultsOutput, err error) {
				result = &cloudwatchlogs.GetQueryResultsOutput{Status: types.QueryStatusComplete}

				for _, message := range rows[roleARN] {
					result.Results = append(result.Results, []types.ResultField{
						{Field: aws.String("@message"), Value: aws.String(message)},
					})
				}

				return result, err
			},
		}

		return client, err
	}

	return factory
}

// TestEmptyCloudWatchQueryNamesWhereElseToLook is the CloudWatch half of the
// cross-backend rule: a query that matches nothing says which accounts and log
// groups it did not cover, and which other log backends this deployment has.
func TestEmptyCloudWatchQueryNamesWhereElseToLook(t *testing.T) {
	t.Setenv(envCloudWatchAccounts, `{"dev":"arn:dev","prod":"arn:prod"}`)
	t.Setenv(envCloudWatchAssumeRole, "")

	logger := slog.New(slog.DiscardHandler)

	args := map[string]interface{}{
		"query":      "fields @message",
		"log_groups": []interface{}{"/ecs/my-service"},
		"start_time": "1h",
		"accounts":   []interface{}{"dev"},
	}

	t.Run("empty", func(t *testing.T) {
		server := &Server{
			logger:                  logger,
			lokiClient:              k8s.NewLokiClient("http://loki.example:3100", logger),
			cloudWatchClientFactory: cloudWatchRowsByRole(nil),
		}

		result, err := server.executeCloudWatchLogsQuery(context.Background(), args)
		require.NoError(t, err)

		var parsed MultiAccountQueryResult
		require.NoError(t, json.Unmarshal([]byte(result), &parsed), "the note must leave the result valid JSON")

		assert.Contains(t, parsed.Note, "does not show these logs do not exist")
		assert.Contains(t, parsed.Note, "accounts this query did not cover: prod")
		assert.Contains(t, parsed.Note, "cloudwatch_logs_list_groups")
		assert.Contains(t, parsed.Note, "Loki (loki_label_names, loki_label_values, loki_query)")
		assert.NotContains(t, parsed.Note, "Kubernetes", "an unconfigured backend is not suggested")
	})

	t.Run("matched", func(t *testing.T) {
		server := &Server{
			logger:                  logger,
			lokiClient:              k8s.NewLokiClient("http://loki.example:3100", logger),
			cloudWatchClientFactory: cloudWatchRowsByRole(map[string][]string{"arn:dev": {"a log line"}}),
		}

		result, err := server.executeCloudWatchLogsQuery(context.Background(), args)
		require.NoError(t, err)

		var parsed MultiAccountQueryResult
		require.NoError(t, json.Unmarshal([]byte(result), &parsed))

		assert.Empty(t, parsed.Note, "a query that found rows carries no note")
		assert.NotContains(t, result, `"note"`)
	})
}

func TestUnqueriedCloudWatchAccounts(t *testing.T) {
	t.Parallel()

	all := []CloudWatchAccountConfig{{Name: "dev"}, {Name: "stage"}, {Name: "prod"}}

	assert.Equal(t, []string{"stage", "prod"}, unqueriedCloudWatchAccounts(all, []CloudWatchAccountConfig{{Name: "dev"}}))
	assert.Empty(t, unqueriedCloudWatchAccounts(all, all))
}
