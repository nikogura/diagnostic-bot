package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AWS tool name constants.
const (
	toolIAMListRoles              = "iam_list_roles"
	toolIAMGetRole                = "iam_get_role"
	toolEC2DescribeVPCs           = "ec2_describe_vpcs"
	toolEC2DescribeSubnets        = "ec2_describe_subnets"
	toolEC2DescribeSecurityGroups = "ec2_describe_security_groups"
	toolEC2DescribeNATGateways    = "ec2_describe_nat_gateways"
	toolRoute53ListHostedZones    = "route53_list_hosted_zones"
	toolRoute53ListRecords        = "route53_list_records"
	toolSTSGetCallerIdentity      = "sts_get_caller_identity"
	toolS3ListBuckets             = "s3_list_buckets"
	toolS3GetBucketPolicy         = "s3_get_bucket_policy"
)

const (
	envAWSAssumeRole   = "AWS_ASSUME_ROLE"
	envAWSExternalID   = "AWS_EXTERNAL_ID"
	defaultAWSRegion   = "us-east-1"
	descAWSRegionQuery = "AWS region to query"
)

// loadAWSConfig creates an AWS config for the given region.
// If AWS_ASSUME_ROLE is set, it configures cross-account role assumption.
func loadAWSConfig(ctx context.Context, region string) (cfg aws.Config, err error) {
	if region == "" {
		region = os.Getenv("AWS_REGION")
		if region == "" {
			region = os.Getenv("AWS_DEFAULT_REGION")
		}
		if region == "" {
			region = defaultAWSRegion
		}
	}

	cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		err = fmt.Errorf("loading AWS config: %w", err)
		return cfg, err
	}

	assumeRoleARN := os.Getenv(envAWSAssumeRole)
	if assumeRoleARN != "" {
		stsClient := sts.NewFromConfig(cfg)
		var opts []func(*stscreds.AssumeRoleOptions)

		externalID := os.Getenv(envAWSExternalID)
		if externalID != "" {
			opts = append(opts, func(o *stscreds.AssumeRoleOptions) {
				o.ExternalID = aws.String(externalID)
			})
		}

		creds := stscreds.NewAssumeRoleProvider(stsClient, assumeRoleARN, opts...)

		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(creds),
		)
		if err != nil {
			err = fmt.Errorf("configuring assume role %s: %w", assumeRoleARN, err)
			return cfg, err
		}
	}

	return cfg, err
}

// awsCredentialsAvailable returns true if AWS credentials can be loaded.
func awsCredentialsAvailable() (available bool) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return available
	}

	_, err = cfg.Credentials.Retrieve(context.Background())
	available = err == nil
	return available
}

// formatAWSJSON marshals a value to indented JSON for tool output.
func formatAWSJSON(v interface{}) (result string, err error) {
	var data []byte
	data, err = json.MarshalIndent(v, "", "  ")
	if err != nil {
		err = fmt.Errorf("formatting response: %w", err)
		return result, err
	}

	result = string(data)
	return result, err
}

// getAWSTools returns all AWS service tool definitions.
func getAWSTools() (result []MCPTool) {
	result = append(result, getSTSTools()...)
	result = append(result, getIAMTools()...)
	result = append(result, getEC2Tools()...)
	result = append(result, getRoute53Tools()...)
	result = append(result, getS3Tools()...)
	return result
}

func getSTSTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolSTSGetCallerIdentity,
			Description: "Show the AWS identity (account, ARN, user ID) the bot is authenticated as. Useful for debugging IAM and credential issues.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
	return result
}

func getIAMTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolIAMListRoles,
			Description: "List IAM roles in the AWS account. Optionally filter by path prefix.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path_prefix": map[string]interface{}{
						"type":        "string",
						"description": "Filter roles by path prefix (e.g., '/service-role/'). Defaults to '/'",
					},
				},
			},
		},
		{
			Name:        toolIAMGetRole,
			Description: "Get details of a specific IAM role including trust policy and attached policies.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"role_name": map[string]interface{}{
						"type":        "string",
						"description": "Name of the IAM role",
					},
				},
				"required": []string{"role_name"},
			},
		},
	}
	return result
}

func getEC2Tools() (result []MCPTool) {
	regionProp := map[string]interface{}{
		"type":        "string",
		"description": descAWSRegionQuery,
	}
	vpcIDProp := map[string]interface{}{
		"type":        "string",
		"description": "Filter by VPC ID (optional)",
	}

	result = []MCPTool{
		{
			Name:        toolEC2DescribeVPCs,
			Description: "List VPCs with CIDR blocks, tags, and state.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vpc_id": vpcIDProp,
					"region": regionProp,
				},
			},
		},
		{
			Name:        toolEC2DescribeSubnets,
			Description: "List subnets with CIDR, availability zone, and available IPs. Optionally filter by VPC ID.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vpc_id": vpcIDProp,
					"region": regionProp,
				},
			},
		},
		{
			Name:        toolEC2DescribeSecurityGroups,
			Description: "List security groups with inbound/outbound rules. Filter by VPC ID or group ID.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vpc_id":   vpcIDProp,
					"group_id": map[string]interface{}{"type": "string", "description": "Filter by specific security group ID (optional)"},
					"region":   regionProp,
				},
			},
		},
		{
			Name:        toolEC2DescribeNATGateways,
			Description: "List NAT gateways with state, subnet, and allocated Elastic IPs.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vpc_id": vpcIDProp,
					"region": regionProp,
				},
			},
		},
	}
	return result
}

func getRoute53Tools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolRoute53ListHostedZones,
			Description: "List all Route53 hosted zones with zone ID, name, and record count.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        toolRoute53ListRecords,
			Description: "List DNS records in a Route53 hosted zone.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"zone_id": map[string]interface{}{
						"type":        "string",
						"description": "Hosted zone ID (e.g., 'Z1234567890ABC')",
					},
					"name_filter": map[string]interface{}{
						"type":        "string",
						"description": "Filter records starting with this name (optional)",
					},
				},
				"required": []string{"zone_id"},
			},
		},
	}
	return result
}

func getS3Tools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolS3ListBuckets,
			Description: "List all S3 buckets in the account.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        toolS3GetBucketPolicy,
			Description: "Get the bucket policy for a specific S3 bucket.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"bucket": map[string]interface{}{
						"type":        "string",
						"description": "S3 bucket name",
					},
					"region": map[string]interface{}{
						"type":        "string",
						"description": "AWS region where the bucket is located",
					},
				},
				"required": []string{"bucket"},
			},
		},
	}
	return result
}

// parseRegionArg extracts the region from tool arguments.
func parseRegionArg(args map[string]interface{}) (region string) {
	if r, ok := args["region"].(string); ok && r != "" {
		region = r
		return region
	}

	region = ""
	return region
}

// --- STS ---

func (s *Server) executeSTSGetCallerIdentity(ctx context.Context, _ map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, "")
	if err != nil {
		return result, err
	}

	stsClient := sts.NewFromConfig(cfg)

	var output *sts.GetCallerIdentityOutput
	output, err = stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		err = fmt.Errorf("sts get-caller-identity: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "sts get-caller-identity",
		slog.String("account", aws.ToString(output.Account)),
		slog.String("arn", aws.ToString(output.Arn)))

	result, err = formatAWSJSON(output)
	return result, err
}

// --- IAM ---

func (s *Server) executeIAMListRoles(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, parseRegionArg(args))
	if err != nil {
		return result, err
	}

	iamClient := iam.NewFromConfig(cfg)

	pathPrefix := "/"
	if p, ok := args["path_prefix"].(string); ok && p != "" {
		pathPrefix = p
	}

	var output *iam.ListRolesOutput
	output, err = iamClient.ListRoles(ctx, &iam.ListRolesInput{
		PathPrefix: aws.String(pathPrefix),
	})
	if err != nil {
		err = fmt.Errorf("iam list-roles: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "iam list-roles",
		slog.String("path_prefix", pathPrefix),
		slog.Int("count", len(output.Roles)))

	// Summarize roles
	type roleSummary struct {
		RoleName   string `json:"role_name"`
		RoleID     string `json:"role_id"`
		Arn        string `json:"arn"`
		CreateDate string `json:"create_date"`
		Path       string `json:"path"`
	}

	summaries := make([]roleSummary, 0, len(output.Roles))
	for _, role := range output.Roles {
		summaries = append(summaries, roleSummary{
			RoleName:   aws.ToString(role.RoleName),
			RoleID:     aws.ToString(role.RoleId),
			Arn:        aws.ToString(role.Arn),
			CreateDate: role.CreateDate.String(),
			Path:       aws.ToString(role.Path),
		})
	}

	result, err = formatAWSJSON(summaries)
	return result, err
}

func (s *Server) executeIAMGetRole(ctx context.Context, args map[string]interface{}) (result string, err error) {
	roleName, _ := args["role_name"].(string)
	if roleName == "" {
		err = errors.New("role_name parameter is required")
		return result, err
	}

	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, parseRegionArg(args))
	if err != nil {
		return result, err
	}

	iamClient := iam.NewFromConfig(cfg)

	var roleOutput *iam.GetRoleOutput
	roleOutput, err = iamClient.GetRole(ctx, &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		err = fmt.Errorf("iam get-role: %w", err)
		return result, err
	}

	// Also get attached policies
	var policiesOutput *iam.ListAttachedRolePoliciesOutput
	policiesOutput, err = iamClient.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		err = fmt.Errorf("iam list-attached-role-policies: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "iam get-role",
		slog.String("role_name", roleName))

	response := map[string]interface{}{
		"role":              roleOutput.Role,
		"attached_policies": policiesOutput.AttachedPolicies,
	}

	result, err = formatAWSJSON(response)
	return result, err
}

// --- EC2 / VPC ---

func (s *Server) executeEC2DescribeVPCs(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, parseRegionArg(args))
	if err != nil {
		return result, err
	}

	ec2Client := ec2.NewFromConfig(cfg)

	input := &ec2.DescribeVpcsInput{}
	if vpcID, ok := args["vpc_id"].(string); ok && vpcID != "" {
		input.VpcIds = []string{vpcID}
	}

	var output *ec2.DescribeVpcsOutput
	output, err = ec2Client.DescribeVpcs(ctx, input)
	if err != nil {
		err = fmt.Errorf("ec2 describe-vpcs: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "ec2 describe-vpcs",
		slog.Int("count", len(output.Vpcs)))

	result, err = formatAWSJSON(output.Vpcs)
	return result, err
}

//nolint:dupl // Structurally similar to DescribeNATGateways but different AWS types and API calls
func (s *Server) executeEC2DescribeSubnets(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, parseRegionArg(args))
	if err != nil {
		return result, err
	}

	ec2Client := ec2.NewFromConfig(cfg)

	input := &ec2.DescribeSubnetsInput{}
	if vpcID, ok := args["vpc_id"].(string); ok && vpcID != "" {
		input.Filters = []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		}
	}

	var output *ec2.DescribeSubnetsOutput
	output, err = ec2Client.DescribeSubnets(ctx, input)
	if err != nil {
		err = fmt.Errorf("ec2 describe-subnets: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "ec2 describe-subnets",
		slog.Int("count", len(output.Subnets)))

	result, err = formatAWSJSON(output.Subnets)
	return result, err
}

func (s *Server) executeEC2DescribeSecurityGroups(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, parseRegionArg(args))
	if err != nil {
		return result, err
	}

	ec2Client := ec2.NewFromConfig(cfg)

	input := &ec2.DescribeSecurityGroupsInput{}

	var filters []ec2types.Filter
	if vpcID, ok := args["vpc_id"].(string); ok && vpcID != "" {
		filters = append(filters, ec2types.Filter{
			Name:   aws.String("vpc-id"),
			Values: []string{vpcID},
		})
	}
	if groupID, ok := args["group_id"].(string); ok && groupID != "" {
		input.GroupIds = []string{groupID}
	}

	if len(filters) > 0 {
		input.Filters = filters
	}

	var output *ec2.DescribeSecurityGroupsOutput
	output, err = ec2Client.DescribeSecurityGroups(ctx, input)
	if err != nil {
		err = fmt.Errorf("ec2 describe-security-groups: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "ec2 describe-security-groups",
		slog.Int("count", len(output.SecurityGroups)))

	result, err = formatAWSJSON(output.SecurityGroups)
	return result, err
}

//nolint:dupl // Structurally similar to DescribeSubnets but different AWS types and API calls
func (s *Server) executeEC2DescribeNATGateways(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, parseRegionArg(args))
	if err != nil {
		return result, err
	}

	ec2Client := ec2.NewFromConfig(cfg)

	input := &ec2.DescribeNatGatewaysInput{}
	if vpcID, ok := args["vpc_id"].(string); ok && vpcID != "" {
		input.Filter = []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		}
	}

	var output *ec2.DescribeNatGatewaysOutput
	output, err = ec2Client.DescribeNatGateways(ctx, input)
	if err != nil {
		err = fmt.Errorf("ec2 describe-nat-gateways: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "ec2 describe-nat-gateways",
		slog.Int("count", len(output.NatGateways)))

	result, err = formatAWSJSON(output.NatGateways)
	return result, err
}

// --- Route53 ---

func (s *Server) executeRoute53ListHostedZones(ctx context.Context, _ map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, "")
	if err != nil {
		return result, err
	}

	r53Client := route53.NewFromConfig(cfg)

	var output *route53.ListHostedZonesOutput
	output, err = r53Client.ListHostedZones(ctx, &route53.ListHostedZonesInput{})
	if err != nil {
		err = fmt.Errorf("route53 list-hosted-zones: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "route53 list-hosted-zones",
		slog.Int("count", len(output.HostedZones)))

	result, err = formatAWSJSON(output.HostedZones)
	return result, err
}

func (s *Server) executeRoute53ListRecords(ctx context.Context, args map[string]interface{}) (result string, err error) {
	zoneID, _ := args["zone_id"].(string)
	if zoneID == "" {
		err = errors.New("zone_id parameter is required")
		return result, err
	}

	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, "")
	if err != nil {
		return result, err
	}

	r53Client := route53.NewFromConfig(cfg)

	input := &route53.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	}

	if nameFilter, ok := args["name_filter"].(string); ok && nameFilter != "" {
		input.StartRecordName = aws.String(nameFilter)
	}

	var output *route53.ListResourceRecordSetsOutput
	output, err = r53Client.ListResourceRecordSets(ctx, input)
	if err != nil {
		err = fmt.Errorf("route53 list-resource-record-sets: %w", err)
		return result, err
	}

	// Filter by name prefix if specified
	nameFilter, _ := args["name_filter"].(string)
	if nameFilter != "" {
		var filtered []interface{}
		for _, record := range output.ResourceRecordSets {
			if strings.HasPrefix(aws.ToString(record.Name), nameFilter) {
				filtered = append(filtered, record)
			}
		}

		s.logger.InfoContext(ctx, "route53 list-records",
			slog.String("zone_id", zoneID),
			slog.String("name_filter", nameFilter),
			slog.Int("count", len(filtered)))

		result, err = formatAWSJSON(filtered)
		return result, err
	}

	s.logger.InfoContext(ctx, "route53 list-records",
		slog.String("zone_id", zoneID),
		slog.Int("count", len(output.ResourceRecordSets)))

	result, err = formatAWSJSON(output.ResourceRecordSets)
	return result, err
}

// --- S3 ---

func (s *Server) executeS3ListBuckets(ctx context.Context, _ map[string]interface{}) (result string, err error) {
	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, "")
	if err != nil {
		return result, err
	}

	s3Client := s3.NewFromConfig(cfg)

	var output *s3.ListBucketsOutput
	output, err = s3Client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		err = fmt.Errorf("s3 list-buckets: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "s3 list-buckets",
		slog.Int("count", len(output.Buckets)))

	result, err = formatAWSJSON(output.Buckets)
	return result, err
}

func (s *Server) executeS3GetBucketPolicy(ctx context.Context, args map[string]interface{}) (result string, err error) {
	bucket, _ := args["bucket"].(string)
	if bucket == "" {
		err = errors.New("bucket parameter is required")
		return result, err
	}

	var cfg aws.Config
	cfg, err = loadAWSConfig(ctx, parseRegionArg(args))
	if err != nil {
		return result, err
	}

	s3Client := s3.NewFromConfig(cfg)

	var output *s3.GetBucketPolicyOutput
	output, err = s3Client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		err = fmt.Errorf("s3 get-bucket-policy: %w", err)
		return result, err
	}

	s.logger.InfoContext(ctx, "s3 get-bucket-policy",
		slog.String("bucket", bucket))

	// Pretty-print the policy JSON
	policyStr := aws.ToString(output.Policy)

	var parsed interface{}
	err = json.Unmarshal([]byte(policyStr), &parsed)
	if err != nil {
		// Return raw policy string if not valid JSON
		result = policyStr
		return result, err
	}

	result, err = formatAWSJSON(parsed)
	return result, err
}
