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
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ECS tool name constants.
const (
	toolECSListClusters           = "ecs_list_clusters"
	toolECSListServices           = "ecs_list_services"
	toolECSDescribeServices       = "ecs_describe_services"
	toolECSListTasks              = "ecs_list_tasks"
	toolECSDescribeTasks          = "ecs_describe_tasks"
	toolECSDescribeTaskDefinition = "ecs_describe_task_definition"
)

const (
	// ecsDescribeServicesBatch is the most services DescribeServices accepts per call.
	ecsDescribeServicesBatch = 10
	// ecsDescribeTasksBatch is the most tasks DescribeTasks accepts per call.
	ecsDescribeTasksBatch = 100
	// defaultECSServiceEvents is how many of a service's most recent events are returned.
	defaultECSServiceEvents = 10
	// defaultECSListTasksLimit bounds how many task ARNs a list returns.
	defaultECSListTasksLimit = 100
	// ecsListPageCap bounds how many pages a list call walks.
	ecsListPageCap = 20
	// ecsRedacted stands in for a value the tool never returns.
	ecsRedacted = "[REDACTED]"
	// descECSCluster describes the cluster argument shared by the ECS tools.
	descECSCluster = "ECS cluster name or ARN"
	// descECSAccount describes the account argument shared by the ECS tools.
	descECSAccount = "Configured AWS account name to query (e.g. 'prod'). Omit to use the bot's own credentials."
)

// ECSClient is the read-only slice of the ECS API the tools use. It mirrors
// CloudWatchMetricsClient and allows for mocking in tests. The surface is
// split into listing and describing, which compose here.
type ECSClient interface {
	ecsLister
	ecsDescriber
}

// ecsLister covers the ECS list operations.
type ecsLister interface {
	ListClusters(ctx context.Context, params *ecs.ListClustersInput, optFns ...func(*ecs.Options)) (*ecs.ListClustersOutput, error)
	ListServices(ctx context.Context, params *ecs.ListServicesInput, optFns ...func(*ecs.Options)) (*ecs.ListServicesOutput, error)
	ListTasks(ctx context.Context, params *ecs.ListTasksInput, optFns ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
}

// ecsDescriber covers the ECS describe operations.
type ecsDescriber interface {
	DescribeServices(ctx context.Context, params *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTasks(ctx context.Context, params *ecs.DescribeTasksInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	DescribeTaskDefinition(ctx context.Context, params *ecs.DescribeTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

// ECSClientFactory creates an ECS client for a region. An empty roleARN uses
// the bot's own credentials; otherwise the role is assumed first.
type ECSClientFactory func(ctx context.Context, region string, roleARN string) (ECSClient, error)

// ECSListResult is the output of the ECS list tools.
type ECSListResult struct {
	Region      string   `json:"region"`
	Account     string   `json:"account,omitempty"`
	Cluster     string   `json:"cluster,omitempty"`
	Count       int      `json:"count"`
	Truncated   bool     `json:"truncated,omitempty"`
	ClusterARNs []string `json:"cluster_arns,omitempty"`
	ServiceARNs []string `json:"service_arns,omitempty"`
	TaskARNs    []string `json:"task_arns,omitempty"`
}

// ECSDescribeServicesResult is the output of ecs_describe_services.
type ECSDescribeServicesResult struct {
	Region   string              `json:"region"`
	Account  string              `json:"account,omitempty"`
	Cluster  string              `json:"cluster"`
	Services []ECSServiceSummary `json:"services"`
	Failures []ECSFailure        `json:"failures,omitempty"`
}

// ECSServiceSummary is the state of one service: counts, deployments, and the
// most recent events.
type ECSServiceSummary struct {
	ServiceName    string                 `json:"service_name"`
	ServiceARN     string                 `json:"service_arn"`
	Status         string                 `json:"status"`
	TaskDefinition string                 `json:"task_definition"`
	LaunchType     string                 `json:"launch_type,omitempty"`
	DesiredCount   int32                  `json:"desired_count"`
	RunningCount   int32                  `json:"running_count"`
	PendingCount   int32                  `json:"pending_count"`
	Deployments    []ECSDeploymentSummary `json:"deployments"`
	Events         []ECSServiceEvent      `json:"events"`
	EventsTotal    int                    `json:"events_total"`
}

// ECSDeploymentSummary is one deployment of a service.
type ECSDeploymentSummary struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	TaskDefinition     string `json:"task_definition"`
	DesiredCount       int32  `json:"desired_count"`
	RunningCount       int32  `json:"running_count"`
	PendingCount       int32  `json:"pending_count"`
	FailedTasks        int32  `json:"failed_tasks"`
	RolloutState       string `json:"rollout_state,omitempty"`
	RolloutStateReason string `json:"rollout_state_reason,omitempty"`
	CreatedAt          string `json:"created_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
}

// ECSServiceEvent is one entry from a service's event log.
type ECSServiceEvent struct {
	CreatedAt string `json:"created_at"`
	Message   string `json:"message"`
}

// ECSFailure is an ARN the API could not describe, with its reason.
type ECSFailure struct {
	ARN    string `json:"arn"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// ECSDescribeTasksResult is the output of ecs_describe_tasks.
type ECSDescribeTasksResult struct {
	Region   string           `json:"region"`
	Account  string           `json:"account,omitempty"`
	Cluster  string           `json:"cluster"`
	Tasks    []ECSTaskSummary `json:"tasks"`
	Failures []ECSFailure     `json:"failures,omitempty"`
}

// ECSTaskSummary is the state of one task and its containers.
type ECSTaskSummary struct {
	TaskARN              string                `json:"task_arn"`
	TaskDefinitionARN    string                `json:"task_definition_arn"`
	LastStatus           string                `json:"last_status"`
	DesiredStatus        string                `json:"desired_status"`
	HealthStatus         string                `json:"health_status,omitempty"`
	LaunchType           string                `json:"launch_type,omitempty"`
	Group                string                `json:"group,omitempty"`
	StartedBy            string                `json:"started_by,omitempty"`
	AvailabilityZone     string                `json:"availability_zone,omitempty"`
	ContainerInstanceARN string                `json:"container_instance_arn,omitempty"`
	StopCode             string                `json:"stop_code,omitempty"`
	StoppedReason        string                `json:"stopped_reason,omitempty"`
	CreatedAt            string                `json:"created_at,omitempty"`
	StartedAt            string                `json:"started_at,omitempty"`
	StoppingAt           string                `json:"stopping_at,omitempty"`
	StoppedAt            string                `json:"stopped_at,omitempty"`
	Containers           []ECSContainerSummary `json:"containers"`
}

// ECSContainerSummary is the state of one container in a task.
type ECSContainerSummary struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	ImageDigest  string `json:"image_digest,omitempty"`
	LastStatus   string `json:"last_status"`
	HealthStatus string `json:"health_status,omitempty"`
	ExitCode     *int32 `json:"exit_code,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ECSDescribeTaskDefinitionResult is the output of ecs_describe_task_definition.
type ECSDescribeTaskDefinitionResult struct {
	Region         string                   `json:"region"`
	Account        string                   `json:"account,omitempty"`
	TaskDefinition ECSTaskDefinitionSummary `json:"task_definition"`
}

// ECSTaskDefinitionSummary is a task definition with its containers. Values
// that could carry credentials are withheld: environment variables are listed
// by name only, and secrets by name and source reference.
type ECSTaskDefinitionSummary struct {
	TaskDefinitionARN       string                          `json:"task_definition_arn"`
	Family                  string                          `json:"family"`
	Revision                int32                           `json:"revision"`
	Status                  string                          `json:"status"`
	CPU                     string                          `json:"cpu,omitempty"`
	Memory                  string                          `json:"memory,omitempty"`
	NetworkMode             string                          `json:"network_mode,omitempty"`
	RequiresCompatibilities []string                        `json:"requires_compatibilities,omitempty"`
	TaskRoleARN             string                          `json:"task_role_arn,omitempty"`
	ExecutionRoleARN        string                          `json:"execution_role_arn,omitempty"`
	RegisteredAt            string                          `json:"registered_at,omitempty"`
	Containers              []ECSContainerDefinitionSummary `json:"containers"`
}

// ECSContainerDefinitionSummary is one container definition without any
// environment values.
type ECSContainerDefinitionSummary struct {
	Name              string               `json:"name"`
	Image             string               `json:"image"`
	Essential         bool                 `json:"essential"`
	CPU               int32                `json:"cpu,omitempty"`
	Memory            *int32               `json:"memory,omitempty"`
	MemoryReservation *int32               `json:"memory_reservation,omitempty"`
	Command           []string             `json:"command,omitempty"`
	EntryPoint        []string             `json:"entry_point,omitempty"`
	PortMappings      []ECSPortMapping     `json:"port_mappings,omitempty"`
	EnvironmentNames  []string             `json:"environment_names"`
	Secrets           []ECSSecretRef       `json:"secrets,omitempty"`
	LogConfiguration  *ECSLogConfiguration `json:"log_configuration,omitempty"`
	HealthCheck       []string             `json:"health_check,omitempty"`
}

// ECSPortMapping is one container port mapping.
type ECSPortMapping struct {
	ContainerPort *int32 `json:"container_port,omitempty"`
	HostPort      *int32 `json:"host_port,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

// ECSSecretRef names a secret and where ECS reads it from. The value itself
// is never fetched.
type ECSSecretRef struct {
	Name      string `json:"name"`
	ValueFrom string `json:"value_from"`
}

// ECSLogConfiguration is a container's log driver and options.
type ECSLogConfiguration struct {
	LogDriver string            `json:"log_driver"`
	Options   map[string]string `json:"options,omitempty"`
}

// ecsSession is one tool call's resolved target: the client, and the region
// and account it answers for.
type ecsSession struct {
	client  ECSClient
	region  string
	account string
}

// getECSTools returns the ECS tool definitions.
func getECSTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolECSListClusters,
			Description: "List ECS cluster ARNs in a region.",
			InputSchema: ecsInputSchema(map[string]interface{}{}, nil),
		},
		{
			Name:        toolECSListServices,
			Description: "List the service ARNs in an ECS cluster.",
			InputSchema: ecsInputSchema(map[string]interface{}{
				"cluster": ecsStringProperty(descECSCluster),
			}, []string{"cluster"}),
		},
		{
			Name:        toolECSDescribeServices,
			Description: "Describe ECS services: desired/running/pending counts, the live task definition, every deployment with its rollout state, and the most recent service events (placement failures, health-check kills, rollbacks). Answers whether a deployment has settled.",
			InputSchema: ecsInputSchema(map[string]interface{}{
				"cluster": ecsStringProperty(descECSCluster),
				"services": map[string]interface{}{
					"type":        "array",
					"description": "Service names or ARNs to describe",
					"items":       map[string]interface{}{"type": "string"},
				},
				"max_events": map[string]interface{}{
					"type":        "integer",
					"description": "How many of each service's most recent events to return (default: 10)",
				},
			}, []string{"cluster", "services"}),
		},
		{
			Name:        toolECSListTasks,
			Description: "List task ARNs in an ECS cluster, filtered by service, task definition family, or desired status. Use desired_status STOPPED to find recently stopped tasks, then ecs_describe_tasks for why they stopped.",
			InputSchema: ecsInputSchema(map[string]interface{}{
				"cluster":      ecsStringProperty(descECSCluster),
				"service_name": ecsStringProperty("Only tasks belonging to this service (optional)"),
				"family":       ecsStringProperty("Only tasks of this task definition family (optional)"),
				"desired_status": map[string]interface{}{
					"type":        "string",
					"description": "RUNNING (default), PENDING, or STOPPED",
					"enum":        []string{"RUNNING", "PENDING", "STOPPED"},
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of task ARNs to return (default: 100)",
				},
			}, []string{"cluster"}),
		},
		{
			Name:        toolECSDescribeTasks,
			Description: "Describe ECS tasks: last and desired status, health, the task definition revision, stop code and stopped reason, timestamps, and each container's image, status, exit code and reason.",
			InputSchema: ecsInputSchema(map[string]interface{}{
				"cluster": ecsStringProperty(descECSCluster),
				"tasks": map[string]interface{}{
					"type":        "array",
					"description": "Task IDs or ARNs to describe",
					"items":       map[string]interface{}{"type": "string"},
				},
			}, []string{"cluster", "tasks"}),
		},
		{
			Name:        toolECSDescribeTaskDefinition,
			Description: "Describe an ECS task definition: family, revision, CPU/memory, roles, and each container's image, command, ports, log configuration, environment variable NAMES, and secret references. Environment values are never returned.",
			InputSchema: ecsInputSchema(map[string]interface{}{
				"task_definition": ecsStringProperty("Task definition as family, family:revision, or ARN"),
			}, []string{"task_definition"}),
		},
	}

	return result
}

// ecsInputSchema builds a tool schema from the tool's own properties plus the
// region and account arguments every ECS tool takes.
func ecsInputSchema(properties map[string]interface{}, required []string) (schema map[string]interface{}) {
	properties["region"] = ecsStringProperty(descAWSRegionQuery)
	properties["account"] = ecsStringProperty(descECSAccount)

	schema = map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

// ecsStringProperty returns a string schema property with a description.
func ecsStringProperty(description string) (property map[string]interface{}) {
	property = map[string]interface{}{
		"type":        "string",
		"description": description,
	}

	return property
}

// defaultECSClientFactory creates a real ECS client. Without a role it uses
// the same credential chain as the ec2_describe_* tools (including
// AWS_ASSUME_ROLE); with one it assumes that role first.
func defaultECSClientFactory(ctx context.Context, region string, roleARN string) (client ECSClient, err error) {
	var cfg aws.Config

	if roleARN == "" {
		cfg, err = loadAWSConfig(ctx, region)
		if err != nil {
			return client, err
		}

		client = ecs.NewFromConfig(cfg)

		return client, err
	}

	region = resolveAWSRegion(region)

	cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		err = fmt.Errorf("loading AWS config: %w", err)
		return client, err
	}

	cfg, err = configureAssumeRole(ctx, cfg, roleARN, region)
	if err != nil {
		return client, err
	}

	client = ecs.NewFromConfig(cfg)

	return client, err
}

// resolveECSAccount maps the optional account argument to the role the bot
// holds there, from the same per-account registry the CloudWatch tools use.
// With no account named the call runs on the bot's own credentials.
func resolveECSAccount(args map[string]interface{}) (account string, roleARN string, err error) {
	account, _ = args["account"].(string)
	if account == "" {
		return account, roleARN, err
	}

	var accounts []CloudWatchAccountConfig

	accounts, err = loadCloudWatchAccounts()
	if err != nil {
		return account, roleARN, err
	}

	if len(accounts) == 0 {
		err = fmt.Errorf("account %q requested but no accounts are configured; set %s or omit account", account, envCloudWatchAccounts)
		return account, roleARN, err
	}

	var resolved []CloudWatchAccountConfig

	resolved, err = resolveCloudWatchAccounts(accounts, []string{account})
	if err != nil {
		return account, roleARN, err
	}

	roleARN = resolved[0].RoleARN

	return account, roleARN, err
}

// ecsFactory returns the configured client factory, or the real one when a
// Server was built without one.
func (s *Server) ecsFactory() (factory ECSClientFactory) {
	factory = s.ecsClientFactory
	if factory == nil {
		factory = defaultECSClientFactory
	}

	return factory
}

// newECSSession resolves the region and account for a call and builds the client.
func (s *Server) newECSSession(ctx context.Context, args map[string]interface{}) (session ecsSession, err error) {
	session.region = resolveAWSRegion(parseRegionArg(args))

	var roleARN string

	session.account, roleARN, err = resolveECSAccount(args)
	if err != nil {
		return session, err
	}

	session.client, err = s.ecsFactory()(ctx, session.region, roleARN)
	if err != nil {
		err = fmt.Errorf("creating ECS client: %w", err)
		return session, err
	}

	return session, err
}

// parseECSStringListArg reads a string array argument, dropping empties.
func parseECSStringListArg(args map[string]interface{}, key string) (values []string) {
	raw, ok := args[key].([]interface{})
	if !ok {
		return values
	}

	for _, item := range raw {
		value, isString := item.(string)
		if isString && value != "" {
			values = append(values, value)
		}
	}

	return values
}

// parseECSIntArg reads a positive integer argument with a default.
func parseECSIntArg(args map[string]interface{}, key string, fallback int) (value int) {
	value = fallback

	raw, ok := args[key].(float64)
	if ok && int(raw) > 0 {
		value = int(raw)
	}

	return value
}

// chunkStrings splits items into slices of at most size.
func chunkStrings(items []string, size int) (chunks [][]string) {
	for start := 0; start < len(items); start += size {
		end := min(start+size, len(items))
		chunks = append(chunks, items[start:end])
	}

	return chunks
}

// formatECSTime renders an optional timestamp as RFC 3339 UTC.
func formatECSTime(value *time.Time) (formatted string) {
	if value == nil {
		return formatted
	}

	formatted = value.UTC().Format(time.RFC3339)

	return formatted
}

// summarizeECSFailures converts API failures for output.
func summarizeECSFailures(failures []ecstypes.Failure) (result []ECSFailure) {
	for _, failure := range failures {
		result = append(result, ECSFailure{
			ARN:    aws.ToString(failure.Arn),
			Reason: aws.ToString(failure.Reason),
			Detail: aws.ToString(failure.Detail),
		})
	}

	return result
}

// --- List tools ---

func (s *Server) executeECSListClusters(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var session ecsSession

	session, err = s.newECSSession(ctx, args)
	if err != nil {
		return result, err
	}

	var arns []string

	paginator := ecs.NewListClustersPaginator(session.client, &ecs.ListClustersInput{})
	for page := 0; paginator.HasMorePages() && page < ecsListPageCap; page++ {
		var output *ecs.ListClustersOutput

		output, err = paginator.NextPage(ctx)
		if err != nil {
			err = fmt.Errorf("ecs list-clusters: %w", err)
			return result, err
		}

		arns = append(arns, output.ClusterArns...)
	}

	s.logger.InfoContext(ctx, "ecs list-clusters",
		slog.String("region", session.region),
		slog.String("account", session.account),
		slog.Int("count", len(arns)))

	result, err = formatAWSJSON(ECSListResult{
		Region:      session.region,
		Account:     session.account,
		Count:       len(arns),
		Truncated:   paginator.HasMorePages(),
		ClusterARNs: arns,
	})

	return result, err
}

func (s *Server) executeECSListServices(ctx context.Context, args map[string]interface{}) (result string, err error) {
	cluster, _ := args["cluster"].(string)
	if cluster == "" {
		err = errors.New("cluster parameter is required")
		return result, err
	}

	var session ecsSession

	session, err = s.newECSSession(ctx, args)
	if err != nil {
		return result, err
	}

	var arns []string

	paginator := ecs.NewListServicesPaginator(session.client, &ecs.ListServicesInput{Cluster: aws.String(cluster)})
	for page := 0; paginator.HasMorePages() && page < ecsListPageCap; page++ {
		var output *ecs.ListServicesOutput

		output, err = paginator.NextPage(ctx)
		if err != nil {
			err = fmt.Errorf("ecs list-services: %w", err)
			return result, err
		}

		arns = append(arns, output.ServiceArns...)
	}

	s.logger.InfoContext(ctx, "ecs list-services",
		slog.String("region", session.region),
		slog.String("account", session.account),
		slog.String("cluster", cluster),
		slog.Int("count", len(arns)))

	result, err = formatAWSJSON(ECSListResult{
		Region:      session.region,
		Account:     session.account,
		Cluster:     cluster,
		Count:       len(arns),
		Truncated:   paginator.HasMorePages(),
		ServiceARNs: arns,
	})

	return result, err
}

func (s *Server) executeECSListTasks(ctx context.Context, args map[string]interface{}) (result string, err error) {
	cluster, _ := args["cluster"].(string)
	if cluster == "" {
		err = errors.New("cluster parameter is required")
		return result, err
	}

	input := buildECSListTasksInput(cluster, args)
	limit := parseECSIntArg(args, "limit", defaultECSListTasksLimit)

	var session ecsSession

	session, err = s.newECSSession(ctx, args)
	if err != nil {
		return result, err
	}

	var arns []string

	paginator := ecs.NewListTasksPaginator(session.client, input)
	for paginator.HasMorePages() && len(arns) < limit {
		var output *ecs.ListTasksOutput

		output, err = paginator.NextPage(ctx)
		if err != nil {
			err = fmt.Errorf("ecs list-tasks: %w", err)
			return result, err
		}

		arns = append(arns, output.TaskArns...)
	}

	truncated := paginator.HasMorePages() || len(arns) > limit
	if len(arns) > limit {
		arns = arns[:limit]
	}

	s.logger.InfoContext(ctx, "ecs list-tasks",
		slog.String("region", session.region),
		slog.String("account", session.account),
		slog.String("cluster", cluster),
		slog.String("desired_status", string(input.DesiredStatus)),
		slog.Int("count", len(arns)))

	result, err = formatAWSJSON(ECSListResult{
		Region:    session.region,
		Account:   session.account,
		Cluster:   cluster,
		Count:     len(arns),
		Truncated: truncated,
		TaskARNs:  arns,
	})

	return result, err
}

// buildECSListTasksInput applies the optional ListTasks filters. The desired
// status defaults to RUNNING, as the API does.
func buildECSListTasksInput(cluster string, args map[string]interface{}) (input *ecs.ListTasksInput) {
	input = &ecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		DesiredStatus: ecstypes.DesiredStatusRunning,
	}

	if serviceName, ok := args["service_name"].(string); ok && serviceName != "" {
		input.ServiceName = aws.String(serviceName)
	}

	if family, ok := args["family"].(string); ok && family != "" {
		input.Family = aws.String(family)
	}

	switch status, _ := args["desired_status"].(string); strings.ToUpper(status) {
	case string(ecstypes.DesiredStatusPending):
		input.DesiredStatus = ecstypes.DesiredStatusPending
	case string(ecstypes.DesiredStatusStopped):
		input.DesiredStatus = ecstypes.DesiredStatusStopped
	default:
		input.DesiredStatus = ecstypes.DesiredStatusRunning
	}

	return input
}

// --- Describe tools ---

func (s *Server) executeECSDescribeServices(ctx context.Context, args map[string]interface{}) (result string, err error) {
	cluster, _ := args["cluster"].(string)
	if cluster == "" {
		err = errors.New("cluster parameter is required")
		return result, err
	}

	services := parseECSStringListArg(args, "services")
	if len(services) == 0 {
		err = errors.New("services parameter is required and must be a non-empty array")
		return result, err
	}

	maxEvents := parseECSIntArg(args, "max_events", defaultECSServiceEvents)

	var session ecsSession

	session, err = s.newECSSession(ctx, args)
	if err != nil {
		return result, err
	}

	output := ECSDescribeServicesResult{
		Region:   session.region,
		Account:  session.account,
		Cluster:  cluster,
		Services: []ECSServiceSummary{},
	}

	for _, batch := range chunkStrings(services, ecsDescribeServicesBatch) {
		var page *ecs.DescribeServicesOutput

		page, err = session.client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(cluster),
			Services: batch,
		})
		if err != nil {
			err = fmt.Errorf("ecs describe-services: %w", err)
			return result, err
		}

		for _, service := range page.Services {
			output.Services = append(output.Services, summarizeECSService(service, maxEvents))
		}

		output.Failures = append(output.Failures, summarizeECSFailures(page.Failures)...)
	}

	s.logger.InfoContext(ctx, "ecs describe-services",
		slog.String("region", session.region),
		slog.String("account", session.account),
		slog.String("cluster", cluster),
		slog.Int("count", len(output.Services)),
		slog.Int("failures", len(output.Failures)))

	result, err = formatAWSJSON(output)

	return result, err
}

// summarizeECSService flattens a service to its state, deployments, and the
// maxEvents most recent events. The API returns events newest first.
func summarizeECSService(service ecstypes.Service, maxEvents int) (summary ECSServiceSummary) {
	summary = ECSServiceSummary{
		ServiceName:    aws.ToString(service.ServiceName),
		ServiceARN:     aws.ToString(service.ServiceArn),
		Status:         aws.ToString(service.Status),
		TaskDefinition: aws.ToString(service.TaskDefinition),
		LaunchType:     string(service.LaunchType),
		DesiredCount:   service.DesiredCount,
		RunningCount:   service.RunningCount,
		PendingCount:   service.PendingCount,
		Deployments:    []ECSDeploymentSummary{},
		Events:         []ECSServiceEvent{},
		EventsTotal:    len(service.Events),
	}

	for _, deployment := range service.Deployments {
		summary.Deployments = append(summary.Deployments, ECSDeploymentSummary{
			ID:                 aws.ToString(deployment.Id),
			Status:             aws.ToString(deployment.Status),
			TaskDefinition:     aws.ToString(deployment.TaskDefinition),
			DesiredCount:       deployment.DesiredCount,
			RunningCount:       deployment.RunningCount,
			PendingCount:       deployment.PendingCount,
			FailedTasks:        deployment.FailedTasks,
			RolloutState:       string(deployment.RolloutState),
			RolloutStateReason: aws.ToString(deployment.RolloutStateReason),
			CreatedAt:          formatECSTime(deployment.CreatedAt),
			UpdatedAt:          formatECSTime(deployment.UpdatedAt),
		})
	}

	for index, event := range service.Events {
		if index >= maxEvents {
			break
		}

		summary.Events = append(summary.Events, ECSServiceEvent{
			CreatedAt: formatECSTime(event.CreatedAt),
			Message:   aws.ToString(event.Message),
		})
	}

	return summary
}

func (s *Server) executeECSDescribeTasks(ctx context.Context, args map[string]interface{}) (result string, err error) {
	cluster, _ := args["cluster"].(string)
	if cluster == "" {
		err = errors.New("cluster parameter is required")
		return result, err
	}

	tasks := parseECSStringListArg(args, "tasks")
	if len(tasks) == 0 {
		err = errors.New("tasks parameter is required and must be a non-empty array")
		return result, err
	}

	var session ecsSession

	session, err = s.newECSSession(ctx, args)
	if err != nil {
		return result, err
	}

	output := ECSDescribeTasksResult{
		Region:  session.region,
		Account: session.account,
		Cluster: cluster,
		Tasks:   []ECSTaskSummary{},
	}

	for _, batch := range chunkStrings(tasks, ecsDescribeTasksBatch) {
		var page *ecs.DescribeTasksOutput

		page, err = session.client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   batch,
		})
		if err != nil {
			err = fmt.Errorf("ecs describe-tasks: %w", err)
			return result, err
		}

		for _, task := range page.Tasks {
			output.Tasks = append(output.Tasks, summarizeECSTask(task))
		}

		output.Failures = append(output.Failures, summarizeECSFailures(page.Failures)...)
	}

	s.logger.InfoContext(ctx, "ecs describe-tasks",
		slog.String("region", session.region),
		slog.String("account", session.account),
		slog.String("cluster", cluster),
		slog.Int("count", len(output.Tasks)),
		slog.Int("failures", len(output.Failures)))

	result, err = formatAWSJSON(output)

	return result, err
}

// summarizeECSTask flattens a task and its containers to the fields that
// explain its state.
func summarizeECSTask(task ecstypes.Task) (summary ECSTaskSummary) {
	summary = ECSTaskSummary{
		TaskARN:              aws.ToString(task.TaskArn),
		TaskDefinitionARN:    aws.ToString(task.TaskDefinitionArn),
		LastStatus:           aws.ToString(task.LastStatus),
		DesiredStatus:        aws.ToString(task.DesiredStatus),
		HealthStatus:         string(task.HealthStatus),
		LaunchType:           string(task.LaunchType),
		Group:                aws.ToString(task.Group),
		StartedBy:            aws.ToString(task.StartedBy),
		AvailabilityZone:     aws.ToString(task.AvailabilityZone),
		ContainerInstanceARN: aws.ToString(task.ContainerInstanceArn),
		StopCode:             string(task.StopCode),
		StoppedReason:        aws.ToString(task.StoppedReason),
		CreatedAt:            formatECSTime(task.CreatedAt),
		StartedAt:            formatECSTime(task.StartedAt),
		StoppingAt:           formatECSTime(task.StoppingAt),
		StoppedAt:            formatECSTime(task.StoppedAt),
		Containers:           []ECSContainerSummary{},
	}

	for _, container := range task.Containers {
		summary.Containers = append(summary.Containers, ECSContainerSummary{
			Name:         aws.ToString(container.Name),
			Image:        aws.ToString(container.Image),
			ImageDigest:  aws.ToString(container.ImageDigest),
			LastStatus:   aws.ToString(container.LastStatus),
			HealthStatus: string(container.HealthStatus),
			ExitCode:     container.ExitCode,
			Reason:       aws.ToString(container.Reason),
		})
	}

	return summary
}

func (s *Server) executeECSDescribeTaskDefinition(ctx context.Context, args map[string]interface{}) (result string, err error) {
	taskDefinition, _ := args["task_definition"].(string)
	if taskDefinition == "" {
		err = errors.New("task_definition parameter is required")
		return result, err
	}

	var session ecsSession

	session, err = s.newECSSession(ctx, args)
	if err != nil {
		return result, err
	}

	var output *ecs.DescribeTaskDefinitionOutput

	output, err = session.client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(taskDefinition),
	})
	if err != nil {
		err = fmt.Errorf("ecs describe-task-definition: %w", err)
		return result, err
	}

	if output.TaskDefinition == nil {
		err = fmt.Errorf("ecs describe-task-definition: no task definition returned for %q", taskDefinition)
		return result, err
	}

	summary := summarizeECSTaskDefinition(*output.TaskDefinition)

	s.logger.InfoContext(ctx, "ecs describe-task-definition",
		slog.String("region", session.region),
		slog.String("account", session.account),
		slog.String("task_definition", summary.TaskDefinitionARN),
		slog.Int("containers", len(summary.Containers)))

	result, err = formatAWSJSON(ECSDescribeTaskDefinitionResult{
		Region:         session.region,
		Account:        session.account,
		TaskDefinition: summary,
	})

	return result, err
}

// summarizeECSTaskDefinition flattens a task definition, withholding every
// environment value.
func summarizeECSTaskDefinition(definition ecstypes.TaskDefinition) (summary ECSTaskDefinitionSummary) {
	summary = ECSTaskDefinitionSummary{
		TaskDefinitionARN: aws.ToString(definition.TaskDefinitionArn),
		Family:            aws.ToString(definition.Family),
		Revision:          definition.Revision,
		Status:            string(definition.Status),
		CPU:               aws.ToString(definition.Cpu),
		Memory:            aws.ToString(definition.Memory),
		NetworkMode:       string(definition.NetworkMode),
		TaskRoleARN:       aws.ToString(definition.TaskRoleArn),
		ExecutionRoleARN:  aws.ToString(definition.ExecutionRoleArn),
		RegisteredAt:      formatECSTime(definition.RegisteredAt),
		Containers:        []ECSContainerDefinitionSummary{},
	}

	for _, compatibility := range definition.RequiresCompatibilities {
		summary.RequiresCompatibilities = append(summary.RequiresCompatibilities, string(compatibility))
	}

	for _, container := range definition.ContainerDefinitions {
		summary.Containers = append(summary.Containers, summarizeECSContainerDefinition(container))
	}

	return summary
}

// summarizeECSContainerDefinition flattens one container definition. Only the
// names of environment variables are kept; secrets keep their name and the
// reference ECS resolves them from, never a value.
func summarizeECSContainerDefinition(container ecstypes.ContainerDefinition) (summary ECSContainerDefinitionSummary) {
	summary = ECSContainerDefinitionSummary{
		Name:              aws.ToString(container.Name),
		Image:             aws.ToString(container.Image),
		Essential:         aws.ToBool(container.Essential),
		CPU:               container.Cpu,
		Memory:            container.Memory,
		MemoryReservation: container.MemoryReservation,
		Command:           container.Command,
		EntryPoint:        container.EntryPoint,
		EnvironmentNames:  []string{},
	}

	for _, mapping := range container.PortMappings {
		summary.PortMappings = append(summary.PortMappings, ECSPortMapping{
			ContainerPort: mapping.ContainerPort,
			HostPort:      mapping.HostPort,
			Protocol:      string(mapping.Protocol),
		})
	}

	for _, pair := range container.Environment {
		summary.EnvironmentNames = append(summary.EnvironmentNames, aws.ToString(pair.Name))
	}

	sort.Strings(summary.EnvironmentNames)

	for _, secret := range container.Secrets {
		summary.Secrets = append(summary.Secrets, ECSSecretRef{
			Name:      aws.ToString(secret.Name),
			ValueFrom: aws.ToString(secret.ValueFrom),
		})
	}

	if container.LogConfiguration != nil {
		summary.LogConfiguration = &ECSLogConfiguration{
			LogDriver: string(container.LogConfiguration.LogDriver),
			Options:   redactECSLogOptions(container.LogConfiguration.Options),
		}
	}

	if container.HealthCheck != nil {
		summary.HealthCheck = container.HealthCheck.Command
	}

	return summary
}

// redactECSLogOptions copies log driver options, replacing any value whose key
// names a credential (log drivers such as splunk and fluentd take tokens as
// plain options).
func redactECSLogOptions(options map[string]string) (redacted map[string]string) {
	if len(options) == 0 {
		return redacted
	}

	redacted = make(map[string]string, len(options))

	for key, value := range options {
		if isECSSensitiveOptionKey(key) {
			redacted[key] = ecsRedacted
			continue
		}

		redacted[key] = value
	}

	return redacted
}

// isECSSensitiveOptionKey reports whether a log option key names a credential.
func isECSSensitiveOptionKey(key string) (sensitive bool) {
	lower := strings.ToLower(key)

	for _, marker := range []string{"token", "password", "secret", "credential", "apikey", "api-key", "api_key"} {
		if strings.Contains(lower, marker) {
			sensitive = true
			return sensitive
		}
	}

	return sensitive
}
