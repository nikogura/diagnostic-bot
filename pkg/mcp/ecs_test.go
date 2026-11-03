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
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockECSClient implements ECSClient for testing.
type mockECSClient struct {
	listClustersFunc           func(ctx context.Context, params *ecs.ListClustersInput, optFns ...func(*ecs.Options)) (*ecs.ListClustersOutput, error)
	listServicesFunc           func(ctx context.Context, params *ecs.ListServicesInput, optFns ...func(*ecs.Options)) (*ecs.ListServicesOutput, error)
	describeServicesFunc       func(ctx context.Context, params *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	listTasksFunc              func(ctx context.Context, params *ecs.ListTasksInput, optFns ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	describeTasksFunc          func(ctx context.Context, params *ecs.DescribeTasksInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	describeTaskDefinitionFunc func(ctx context.Context, params *ecs.DescribeTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

func (m *mockECSClient) ListClusters(ctx context.Context, params *ecs.ListClustersInput, optFns ...func(*ecs.Options)) (result *ecs.ListClustersOutput, err error) {
	if m.listClustersFunc != nil {
		result, err = m.listClustersFunc(ctx, params, optFns...)
		return result, err
	}

	err = errors.New("listClustersFunc not implemented")

	return result, err
}

func (m *mockECSClient) ListServices(ctx context.Context, params *ecs.ListServicesInput, optFns ...func(*ecs.Options)) (result *ecs.ListServicesOutput, err error) {
	if m.listServicesFunc != nil {
		result, err = m.listServicesFunc(ctx, params, optFns...)
		return result, err
	}

	err = errors.New("listServicesFunc not implemented")

	return result, err
}

func (m *mockECSClient) DescribeServices(ctx context.Context, params *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (result *ecs.DescribeServicesOutput, err error) {
	if m.describeServicesFunc != nil {
		result, err = m.describeServicesFunc(ctx, params, optFns...)
		return result, err
	}

	err = errors.New("describeServicesFunc not implemented")

	return result, err
}

func (m *mockECSClient) ListTasks(ctx context.Context, params *ecs.ListTasksInput, optFns ...func(*ecs.Options)) (result *ecs.ListTasksOutput, err error) {
	if m.listTasksFunc != nil {
		result, err = m.listTasksFunc(ctx, params, optFns...)
		return result, err
	}

	err = errors.New("listTasksFunc not implemented")

	return result, err
}

func (m *mockECSClient) DescribeTasks(ctx context.Context, params *ecs.DescribeTasksInput, optFns ...func(*ecs.Options)) (result *ecs.DescribeTasksOutput, err error) {
	if m.describeTasksFunc != nil {
		result, err = m.describeTasksFunc(ctx, params, optFns...)
		return result, err
	}

	err = errors.New("describeTasksFunc not implemented")

	return result, err
}

func (m *mockECSClient) DescribeTaskDefinition(ctx context.Context, params *ecs.DescribeTaskDefinitionInput, optFns ...func(*ecs.Options)) (result *ecs.DescribeTaskDefinitionOutput, err error) {
	if m.describeTaskDefinitionFunc != nil {
		result, err = m.describeTaskDefinitionFunc(ctx, params, optFns...)
		return result, err
	}

	err = errors.New("describeTaskDefinitionFunc not implemented")

	return result, err
}

// ecsFactoryRecorder is an ECSClientFactory that hands out one client and
// records what it was asked for.
type ecsFactoryRecorder struct {
	client  ECSClient
	err     error
	regions []string
	roles   []string
}

func (r *ecsFactoryRecorder) factory(_ context.Context, region string, roleARN string) (client ECSClient, err error) {
	r.regions = append(r.regions, region)
	r.roles = append(r.roles, roleARN)

	client, err = r.client, r.err

	return client, err
}

// newECSTestServer builds a Server whose ECS calls go to the given client.
func newECSTestServer(t *testing.T, client ECSClient) (server *Server, recorder *ecsFactoryRecorder) {
	t.Helper()

	recorder = &ecsFactoryRecorder{client: client}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	server = &Server{
		logger:           logger,
		ecsClientFactory: recorder.factory,
	}

	return server, recorder
}

func TestGetECSTools(t *testing.T) {
	t.Parallel()

	tools := getECSTools()
	require.Len(t, tools, 6)

	required := map[string][]string{
		toolECSListClusters:           nil,
		toolECSListServices:           {"cluster"},
		toolECSDescribeServices:       {"cluster", "services"},
		toolECSListTasks:              {"cluster"},
		toolECSDescribeTasks:          {"cluster", "tasks"},
		toolECSDescribeTaskDefinition: {"task_definition"},
	}

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()

			want, known := required[tool.Name]
			require.True(t, known, "unexpected tool %q", tool.Name)

			assert.NotEmpty(t, tool.Description)
			assert.Equal(t, "object", tool.InputSchema["type"])

			properties, ok := tool.InputSchema["properties"].(map[string]interface{})
			require.True(t, ok, "tool %s should have properties", tool.Name)
			assert.Contains(t, properties, "region", "every ECS tool takes a region")
			assert.Contains(t, properties, "account", "every ECS tool takes an account")

			got, hasRequired := tool.InputSchema["required"].([]string)
			if want == nil {
				assert.False(t, hasRequired, "tool %s should not require arguments", tool.Name)
				return
			}

			require.True(t, hasRequired, "tool %s should require %v", tool.Name, want)
			assert.Equal(t, want, got)
		})
	}
}

func TestECSToolsBelongToTheAWSFamily(t *testing.T) {
	t.Parallel()

	names := toolNameSet(getAWSTools())

	for _, tool := range getECSTools() {
		assert.True(t, names[tool.Name], "%s must be registered with the AWS read tools", tool.Name)
	}
}

func TestResolveECSAccount(t *testing.T) {
	const prodRole = "arn:aws:iam::111111111111:role/diagnostic-bot"

	tests := []struct {
		name        string
		registry    string
		args        map[string]interface{}
		wantAccount string
		wantRole    string
		wantErr     string
	}{
		{name: "no account uses own credentials", registry: `{"prod": "` + prodRole + `"}`, args: map[string]interface{}{}},
		{name: "account without a registry", registry: "", args: map[string]interface{}{"account": "prod"}, wantAccount: "prod", wantErr: envCloudWatchAccounts},
		{name: "unknown account lists the known ones", registry: `{"prod": "` + prodRole + `"}`, args: map[string]interface{}{"account": "staging"}, wantAccount: "staging", wantErr: "unknown account \"staging\"; valid accounts: prod"},
		{name: "known account resolves its role", registry: `{"prod": "` + prodRole + `"}`, args: map[string]interface{}{"account": "prod"}, wantAccount: "prod", wantRole: prodRole},
		{name: "unparsable registry", registry: `{"prod":`, args: map[string]interface{}{"account": "prod"}, wantAccount: "prod", wantErr: "parsing " + envCloudWatchAccounts},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envCloudWatchAccounts, tt.registry)

			account, roleARN, err := resolveECSAccount(tt.args)

			assert.Equal(t, tt.wantAccount, account)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantRole, roleARN)
		})
	}
}

func TestECSFactoryDefaultsWhenUnset(t *testing.T) {
	t.Parallel()

	server := &Server{}

	assert.NotNil(t, server.ecsFactory(), "a Server built without a factory must still reach ECS")
}

func TestECSToolsValidateBeforeBuildingAClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		call    func(server *Server, ctx context.Context, args map[string]interface{}) (string, error)
		args    map[string]interface{}
		wantErr string
	}{
		{name: "list services needs cluster", call: (*Server).executeECSListServices, args: map[string]interface{}{}, wantErr: "cluster parameter is required"},
		{name: "list tasks needs cluster", call: (*Server).executeECSListTasks, args: map[string]interface{}{}, wantErr: "cluster parameter is required"},
		{name: "describe services needs cluster", call: (*Server).executeECSDescribeServices, args: map[string]interface{}{"services": []interface{}{"web"}}, wantErr: "cluster parameter is required"},
		{name: "describe services needs services", call: (*Server).executeECSDescribeServices, args: map[string]interface{}{"cluster": "prod"}, wantErr: "services parameter is required"},
		{name: "describe tasks needs cluster", call: (*Server).executeECSDescribeTasks, args: map[string]interface{}{"tasks": []interface{}{"abc"}}, wantErr: "cluster parameter is required"},
		{name: "describe tasks needs tasks", call: (*Server).executeECSDescribeTasks, args: map[string]interface{}{"cluster": "prod", "tasks": []interface{}{}}, wantErr: "tasks parameter is required"},
		{name: "describe task definition needs a name", call: (*Server).executeECSDescribeTaskDefinition, args: map[string]interface{}{}, wantErr: "task_definition parameter is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, recorder := newECSTestServer(t, nil)
			recorder.err = errors.New("factory must not be called before arguments are validated")

			_, err := tt.call(server, context.Background(), tt.args)

			require.ErrorContains(t, err, tt.wantErr)
			assert.Empty(t, recorder.regions, "no client should be built for a rejected call")
		})
	}
}

func TestExecuteECSFactoryErrorIsWrapped(t *testing.T) {
	t.Parallel()

	server, recorder := newECSTestServer(t, nil)
	recorder.err = errors.New("no credentials")

	_, err := server.executeECSListClusters(context.Background(), map[string]interface{}{})

	require.ErrorContains(t, err, "creating ECS client")
	require.ErrorContains(t, err, "no credentials")
}

func TestExecuteECSListClustersWalksPages(t *testing.T) {
	t.Parallel()

	var tokens []*string

	client := &mockECSClient{
		listClustersFunc: func(_ context.Context, params *ecs.ListClustersInput, _ ...func(*ecs.Options)) (output *ecs.ListClustersOutput, err error) {
			tokens = append(tokens, params.NextToken)

			if params.NextToken == nil {
				output = &ecs.ListClustersOutput{
					ClusterArns: []string{"arn:aws:ecs:us-east-1:111111111111:cluster/prod"},
					NextToken:   aws.String("page-2"),
				}

				return output, err
			}

			output = &ecs.ListClustersOutput{
				ClusterArns: []string{"arn:aws:ecs:us-east-1:111111111111:cluster/stage"},
			}

			return output, err
		},
	}

	server, recorder := newECSTestServer(t, client)

	raw, err := server.executeECSListClusters(context.Background(), map[string]interface{}{"region": "eu-west-1"})
	require.NoError(t, err)

	var result ECSListResult

	require.NoError(t, json.Unmarshal([]byte(raw), &result))

	assert.Equal(t, "eu-west-1", result.Region)
	assert.Equal(t, []string{"eu-west-1"}, recorder.regions)
	assert.Equal(t, []string{""}, recorder.roles, "no account means no role to assume")
	assert.Equal(t, 2, result.Count)
	assert.False(t, result.Truncated)
	assert.Len(t, result.ClusterARNs, 2)
	assert.Contains(t, result.ClusterARNs, "arn:aws:ecs:us-east-1:111111111111:cluster/stage")

	require.Len(t, tokens, 2)
	assert.Nil(t, tokens[0])
	assert.Equal(t, "page-2", aws.ToString(tokens[1]))
}

func TestExecuteECSListServices(t *testing.T) {
	t.Parallel()

	var cluster string

	client := &mockECSClient{
		listServicesFunc: func(_ context.Context, params *ecs.ListServicesInput, _ ...func(*ecs.Options)) (output *ecs.ListServicesOutput, err error) {
			cluster = aws.ToString(params.Cluster)
			output = &ecs.ListServicesOutput{ServiceArns: []string{"arn:aws:ecs:us-east-1:111111111111:service/prod/web"}}

			return output, err
		},
	}

	server, _ := newECSTestServer(t, client)

	raw, err := server.executeECSListServices(context.Background(), map[string]interface{}{"cluster": "prod"})
	require.NoError(t, err)

	var result ECSListResult

	require.NoError(t, json.Unmarshal([]byte(raw), &result))

	assert.Equal(t, "prod", cluster)
	assert.Equal(t, "prod", result.Cluster)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, []string{"arn:aws:ecs:us-east-1:111111111111:service/prod/web"}, result.ServiceARNs)
}

func TestBuildECSListTasksInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        map[string]interface{}
		wantStatus  ecstypes.DesiredStatus
		wantService string
		wantFamily  string
	}{
		{name: "defaults to running", args: map[string]interface{}{}, wantStatus: ecstypes.DesiredStatusRunning},
		{name: "stopped, any case", args: map[string]interface{}{"desired_status": "stopped"}, wantStatus: ecstypes.DesiredStatusStopped},
		{name: "pending", args: map[string]interface{}{"desired_status": "PENDING"}, wantStatus: ecstypes.DesiredStatusPending},
		{name: "unknown status falls back to running", args: map[string]interface{}{"desired_status": "weird"}, wantStatus: ecstypes.DesiredStatusRunning},
		{name: "service and family filters", args: map[string]interface{}{"service_name": "web", "family": "web-td"}, wantStatus: ecstypes.DesiredStatusRunning, wantService: "web", wantFamily: "web-td"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := buildECSListTasksInput("prod", tt.args)

			assert.Equal(t, "prod", aws.ToString(input.Cluster))
			assert.Equal(t, tt.wantStatus, input.DesiredStatus)
			assert.Equal(t, tt.wantService, aws.ToString(input.ServiceName))
			assert.Equal(t, tt.wantFamily, aws.ToString(input.Family))
		})
	}
}

func TestExecuteECSListTasksHonorsLimit(t *testing.T) {
	t.Parallel()

	client := &mockECSClient{
		listTasksFunc: func(_ context.Context, _ *ecs.ListTasksInput, _ ...func(*ecs.Options)) (output *ecs.ListTasksOutput, err error) {
			output = &ecs.ListTasksOutput{TaskArns: []string{"task-1", "task-2", "task-3"}}

			return output, err
		},
	}

	server, _ := newECSTestServer(t, client)

	raw, err := server.executeECSListTasks(context.Background(), map[string]interface{}{
		"cluster":        "prod",
		"desired_status": "STOPPED",
		"limit":          float64(2),
	})
	require.NoError(t, err)

	var result ECSListResult

	require.NoError(t, json.Unmarshal([]byte(raw), &result))

	assert.Equal(t, 2, result.Count)
	assert.True(t, result.Truncated, "dropping ARNs to meet the limit must be reported")
	assert.Equal(t, []string{"task-1", "task-2"}, result.TaskARNs)
}

func TestExecuteECSDescribeServicesBatchesAndTrimsEvents(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	var batches [][]string

	client := &mockECSClient{
		describeServicesFunc: func(_ context.Context, params *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (output *ecs.DescribeServicesOutput, err error) {
			batches = append(batches, params.Services)

			output = &ecs.DescribeServicesOutput{}

			for _, name := range params.Services {
				if name == "missing" {
					output.Failures = append(output.Failures, ecstypes.Failure{
						Arn:    aws.String("arn:aws:ecs:us-east-1:111111111111:service/prod/missing"),
						Reason: aws.String("MISSING"),
					})

					continue
				}

				output.Services = append(output.Services, ecstypes.Service{
					ServiceName:    aws.String(name),
					ServiceArn:     aws.String("arn:aws:ecs:us-east-1:111111111111:service/prod/" + name),
					Status:         aws.String("ACTIVE"),
					TaskDefinition: aws.String("arn:aws:ecs:us-east-1:111111111111:task-definition/web:42"),
					LaunchType:     ecstypes.LaunchTypeFargate,
					DesiredCount:   3,
					RunningCount:   2,
					PendingCount:   1,
					Deployments: []ecstypes.Deployment{{
						Id:                 aws.String("ecs-svc/1"),
						Status:             aws.String("PRIMARY"),
						TaskDefinition:     aws.String("arn:aws:ecs:us-east-1:111111111111:task-definition/web:42"),
						DesiredCount:       3,
						RunningCount:       2,
						PendingCount:       1,
						FailedTasks:        4,
						RolloutState:       ecstypes.DeploymentRolloutStateInProgress,
						RolloutStateReason: aws.String("ECS deployment ecs-svc/1 in progress."),
						CreatedAt:          aws.Time(now),
						UpdatedAt:          aws.Time(now.Add(time.Minute)),
					}},
					Events: []ecstypes.ServiceEvent{
						{CreatedAt: aws.Time(now.Add(2 * time.Minute)), Message: aws.String("newest")},
						{CreatedAt: aws.Time(now.Add(time.Minute)), Message: aws.String("middle")},
						{CreatedAt: aws.Time(now), Message: aws.String("oldest")},
					},
				})
			}

			return output, err
		},
	}

	services := make([]interface{}, 0, 12)
	for _, name := range []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9", "s10", "s11", "missing"} {
		services = append(services, name)
	}

	server, _ := newECSTestServer(t, client)

	raw, err := server.executeECSDescribeServices(context.Background(), map[string]interface{}{
		"cluster":    "prod",
		"services":   services,
		"max_events": float64(2),
	})
	require.NoError(t, err)

	require.Len(t, batches, 2, "twelve services must go to the API in two calls")
	assert.Len(t, batches[0], ecsDescribeServicesBatch)
	assert.Len(t, batches[1], 2)

	var result ECSDescribeServicesResult

	require.NoError(t, json.Unmarshal([]byte(raw), &result))

	assert.Equal(t, "prod", result.Cluster)
	require.Len(t, result.Services, 11)
	require.Len(t, result.Failures, 1)
	assert.Equal(t, "MISSING", result.Failures[0].Reason)

	first := result.Services[0]
	assert.Equal(t, "s1", first.ServiceName)
	assert.Equal(t, "FARGATE", first.LaunchType)
	assert.Equal(t, int32(3), first.DesiredCount)
	assert.Equal(t, int32(2), first.RunningCount)
	assert.Equal(t, int32(1), first.PendingCount)

	require.Len(t, first.Deployments, 1)
	assert.Equal(t, "IN_PROGRESS", first.Deployments[0].RolloutState)
	assert.Equal(t, int32(4), first.Deployments[0].FailedTasks)
	assert.Equal(t, "2026-11-01T12:00:00Z", first.Deployments[0].CreatedAt)

	assert.Equal(t, 3, first.EventsTotal)
	require.Len(t, first.Events, 2, "max_events must trim the event log")
	assert.Equal(t, "newest", first.Events[0].Message)
	assert.Equal(t, "middle", first.Events[1].Message)
}

func TestExecuteECSDescribeTasksReportsWhyATaskStopped(t *testing.T) {
	t.Parallel()

	stoppedAt := time.Date(2026, 11, 1, 3, 14, 0, 0, time.UTC)

	client := &mockECSClient{
		describeTasksFunc: func(_ context.Context, params *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (output *ecs.DescribeTasksOutput, err error) {
			assert.Equal(t, "prod", aws.ToString(params.Cluster))
			assert.Equal(t, []string{"abc123", "gone"}, params.Tasks)

			output = &ecs.DescribeTasksOutput{
				Tasks: []ecstypes.Task{{
					TaskArn:           aws.String("arn:aws:ecs:us-east-1:111111111111:task/prod/abc123"),
					TaskDefinitionArn: aws.String("arn:aws:ecs:us-east-1:111111111111:task-definition/web:42"),
					LastStatus:        aws.String("STOPPED"),
					DesiredStatus:     aws.String("STOPPED"),
					HealthStatus:      ecstypes.HealthStatusUnhealthy,
					LaunchType:        ecstypes.LaunchTypeFargate,
					Group:             aws.String("service:web"),
					StopCode:          ecstypes.TaskStopCodeEssentialContainerExited,
					StoppedReason:     aws.String("Essential container in task exited"),
					StoppedAt:         aws.Time(stoppedAt),
					Containers: []ecstypes.Container{{
						Name:       aws.String("web"),
						Image:      aws.String("111111111111.dkr.ecr.us-east-1.amazonaws.com/web:1.2.3"),
						LastStatus: aws.String("STOPPED"),
						ExitCode:   aws.Int32(137),
						Reason:     aws.String("OutOfMemoryError: Container killed due to memory usage"),
					}},
				}},
				Failures: []ecstypes.Failure{{
					Arn:    aws.String("arn:aws:ecs:us-east-1:111111111111:task/prod/gone"),
					Reason: aws.String("MISSING"),
				}},
			}

			return output, err
		},
	}

	server, _ := newECSTestServer(t, client)

	raw, err := server.executeECSDescribeTasks(context.Background(), map[string]interface{}{
		"cluster": "prod",
		"tasks":   []interface{}{"abc123", "gone"},
	})
	require.NoError(t, err)

	var result ECSDescribeTasksResult

	require.NoError(t, json.Unmarshal([]byte(raw), &result))

	require.Len(t, result.Tasks, 1)
	task := result.Tasks[0]
	assert.Equal(t, "STOPPED", task.LastStatus)
	assert.Equal(t, "UNHEALTHY", task.HealthStatus)
	assert.Equal(t, "EssentialContainerExited", task.StopCode)
	assert.Equal(t, "Essential container in task exited", task.StoppedReason)
	assert.Equal(t, "2026-11-01T03:14:00Z", task.StoppedAt)
	assert.Empty(t, task.StartedAt, "an unset timestamp must stay empty, not render as the zero time")

	require.Len(t, task.Containers, 1)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(137), *task.Containers[0].ExitCode)
	assert.Contains(t, task.Containers[0].Reason, "OutOfMemoryError")

	require.Len(t, result.Failures, 1)
	assert.Equal(t, "MISSING", result.Failures[0].Reason)
}

func TestExecuteECSDescribeTasksBatches(t *testing.T) {
	t.Parallel()

	var batchSizes []int

	client := &mockECSClient{
		describeTasksFunc: func(_ context.Context, params *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (output *ecs.DescribeTasksOutput, err error) {
			batchSizes = append(batchSizes, len(params.Tasks))
			output = &ecs.DescribeTasksOutput{}

			return output, err
		},
	}

	tasks := make([]interface{}, 0, ecsDescribeTasksBatch+1)
	for range ecsDescribeTasksBatch + 1 {
		tasks = append(tasks, "task")
	}

	server, _ := newECSTestServer(t, client)

	_, err := server.executeECSDescribeTasks(context.Background(), map[string]interface{}{"cluster": "prod", "tasks": tasks})
	require.NoError(t, err)

	assert.Equal(t, []int{ecsDescribeTasksBatch, 1}, batchSizes)
}

func TestExecuteECSDescribeTaskDefinitionWithholdsValues(t *testing.T) {
	t.Parallel()

	client := &mockECSClient{
		describeTaskDefinitionFunc: func(_ context.Context, params *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (output *ecs.DescribeTaskDefinitionOutput, err error) {
			assert.Equal(t, "web:42", aws.ToString(params.TaskDefinition))

			output = &ecs.DescribeTaskDefinitionOutput{
				TaskDefinition: &ecstypes.TaskDefinition{
					TaskDefinitionArn:       aws.String("arn:aws:ecs:us-east-1:111111111111:task-definition/web:42"),
					Family:                  aws.String("web"),
					Revision:                42,
					Status:                  ecstypes.TaskDefinitionStatusActive,
					Cpu:                     aws.String("512"),
					Memory:                  aws.String("1024"),
					NetworkMode:             ecstypes.NetworkModeAwsvpc,
					RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
					ContainerDefinitions: []ecstypes.ContainerDefinition{{
						Name:      aws.String("web"),
						Image:     aws.String("111111111111.dkr.ecr.us-east-1.amazonaws.com/web:1.2.3"),
						Essential: aws.Bool(true),
						Environment: []ecstypes.KeyValuePair{
							{Name: aws.String("LOG_LEVEL"), Value: aws.String("info")},
							{Name: aws.String("DB_PASSWORD"), Value: aws.String("hunter2")},
						},
						Secrets: []ecstypes.Secret{{
							Name:      aws.String("API_KEY"),
							ValueFrom: aws.String("arn:aws:secretsmanager:us-east-1:111111111111:secret:web/api-key"),
						}},
						LogConfiguration: &ecstypes.LogConfiguration{
							LogDriver: ecstypes.LogDriverSplunk,
							Options: map[string]string{
								"splunk-url":   "https://splunk.example.com",
								"splunk-token": "0f3a-secret-token",
							},
						},
						PortMappings: []ecstypes.PortMapping{{ContainerPort: aws.Int32(8080), Protocol: ecstypes.TransportProtocolTcp}},
						HealthCheck:  &ecstypes.HealthCheck{Command: []string{"CMD-SHELL", "curl -f http://localhost:8080/healthz"}},
					}},
				},
			}

			return output, err
		},
	}

	server, _ := newECSTestServer(t, client)

	raw, err := server.executeECSDescribeTaskDefinition(context.Background(), map[string]interface{}{"task_definition": "web:42"})
	require.NoError(t, err)

	assert.NotContains(t, raw, "hunter2", "environment values must never be returned")
	assert.NotContains(t, raw, "0f3a-secret-token", "log driver credentials must never be returned")
	assert.NotContains(t, raw, `"info"`, "no environment value is returned, sensitive-looking or not")

	var result ECSDescribeTaskDefinitionResult

	require.NoError(t, json.Unmarshal([]byte(raw), &result))

	definition := result.TaskDefinition
	assert.Equal(t, "web", definition.Family)
	assert.Equal(t, int32(42), definition.Revision)
	assert.Equal(t, "ACTIVE", definition.Status)
	assert.Equal(t, []string{"FARGATE"}, definition.RequiresCompatibilities)

	require.Len(t, definition.Containers, 1)
	container := definition.Containers[0]
	assert.True(t, container.Essential)
	assert.Equal(t, []string{"DB_PASSWORD", "LOG_LEVEL"}, container.EnvironmentNames, "names are kept, sorted, without values")

	require.Len(t, container.Secrets, 1)
	assert.Equal(t, "API_KEY", container.Secrets[0].Name)
	assert.Contains(t, container.Secrets[0].ValueFrom, "secretsmanager")

	require.NotNil(t, container.LogConfiguration)
	assert.Equal(t, "splunk", container.LogConfiguration.LogDriver)
	assert.Equal(t, "https://splunk.example.com", container.LogConfiguration.Options["splunk-url"])
	assert.Equal(t, ecsRedacted, container.LogConfiguration.Options["splunk-token"])

	require.Len(t, container.PortMappings, 1)
	assert.Equal(t, int32(8080), aws.ToInt32(container.PortMappings[0].ContainerPort))
	assert.Equal(t, []string{"CMD-SHELL", "curl -f http://localhost:8080/healthz"}, container.HealthCheck)
}

func TestExecuteECSDescribeTaskDefinitionEmptyResponse(t *testing.T) {
	t.Parallel()

	client := &mockECSClient{
		describeTaskDefinitionFunc: func(_ context.Context, _ *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (output *ecs.DescribeTaskDefinitionOutput, err error) {
			output = &ecs.DescribeTaskDefinitionOutput{}

			return output, err
		},
	}

	server, _ := newECSTestServer(t, client)

	_, err := server.executeECSDescribeTaskDefinition(context.Background(), map[string]interface{}{"task_definition": "web:42"})

	require.ErrorContains(t, err, "no task definition returned")
}

func TestExecuteECSAPIErrorsAreWrapped(t *testing.T) {
	t.Parallel()

	boom := errors.New("AccessDeniedException")

	client := &mockECSClient{
		listClustersFunc: func(_ context.Context, _ *ecs.ListClustersInput, _ ...func(*ecs.Options)) (output *ecs.ListClustersOutput, err error) {
			err = boom
			return output, err
		},
		describeServicesFunc: func(_ context.Context, _ *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (output *ecs.DescribeServicesOutput, err error) {
			err = boom
			return output, err
		},
	}

	server, _ := newECSTestServer(t, client)

	_, err := server.executeECSListClusters(context.Background(), map[string]interface{}{})
	require.ErrorContains(t, err, "ecs list-clusters")
	require.ErrorIs(t, err, boom)

	_, err = server.executeECSDescribeServices(context.Background(), map[string]interface{}{"cluster": "prod", "services": []interface{}{"web"}})
	require.ErrorContains(t, err, "ecs describe-services")
	require.ErrorIs(t, err, boom)
}

func TestNewECSSessionAssumesTheAccountRole(t *testing.T) {
	const prodRole = "arn:aws:iam::111111111111:role/diagnostic-bot"

	t.Setenv(envCloudWatchAccounts, `{"prod": "`+prodRole+`", "stage": "arn:aws:iam::222222222222:role/diagnostic-bot"}`)

	client := &mockECSClient{
		listClustersFunc: func(_ context.Context, _ *ecs.ListClustersInput, _ ...func(*ecs.Options)) (output *ecs.ListClustersOutput, err error) {
			output = &ecs.ListClustersOutput{ClusterArns: []string{"arn:aws:ecs:eu-west-1:111111111111:cluster/prod"}}

			return output, err
		},
	}

	server, recorder := newECSTestServer(t, client)

	raw, err := server.executeECSListClusters(context.Background(), map[string]interface{}{"account": "prod", "region": "eu-west-1"})
	require.NoError(t, err)

	assert.Equal(t, []string{prodRole}, recorder.roles, "the account's role must be the one assumed")
	assert.Equal(t, []string{"eu-west-1"}, recorder.regions)

	var result ECSListResult

	require.NoError(t, json.Unmarshal([]byte(raw), &result))
	assert.Equal(t, "prod", result.Account)

	_, err = server.executeECSListClusters(context.Background(), map[string]interface{}{"account": "dev-1"})
	require.ErrorContains(t, err, "unknown account \"dev-1\"; valid accounts: prod, stage")
	assert.Len(t, recorder.roles, 1, "an unknown account must not build a client")
}

func TestChunkStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []string
		size  int
		want  [][]string
	}{
		{name: "empty", items: nil, size: 10, want: nil},
		{name: "under one chunk", items: []string{"a", "b"}, size: 10, want: [][]string{{"a", "b"}}},
		{name: "exact chunks", items: []string{"a", "b", "c", "d"}, size: 2, want: [][]string{{"a", "b"}, {"c", "d"}}},
		{name: "remainder", items: []string{"a", "b", "c"}, size: 2, want: [][]string{{"a", "b"}, {"c"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, chunkStrings(tt.items, tt.size))
		})
	}
}

func TestIsECSSensitiveOptionKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want bool
	}{
		{key: "awslogs-group", want: false},
		{key: "awslogs-stream-prefix", want: false},
		{key: "splunk-token", want: true},
		{key: "Splunk-Token", want: true},
		{key: "fluentd-password", want: true},
		{key: "gelf-api_key", want: true},
		{key: "client-secret", want: true},
		{key: "aws-credentials", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, isECSSensitiveOptionKey(tt.key))
		})
	}
}

func TestParseECSIntArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]interface{}
		want int
	}{
		{name: "missing uses default", args: map[string]interface{}{}, want: 7},
		{name: "positive value", args: map[string]interface{}{"n": float64(3)}, want: 3},
		{name: "zero uses default", args: map[string]interface{}{"n": float64(0)}, want: 7},
		{name: "negative uses default", args: map[string]interface{}{"n": float64(-2)}, want: 7},
		{name: "wrong type uses default", args: map[string]interface{}{"n": "3"}, want: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, parseECSIntArg(tt.args, "n", 7))
		})
	}
}
