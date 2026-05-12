package mcp

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServerForAWS(t *testing.T) (mcpServer *Server) {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mcpServer = &Server{
		logger:      logger,
		companyName: "TestCorp",
	}

	return mcpServer
}

func TestGetAWSTools(t *testing.T) {
	t.Parallel()

	tools := getAWSTools()
	require.Len(t, tools, 11)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	assert.True(t, names[toolSTSGetCallerIdentity])
	assert.True(t, names[toolIAMListRoles])
	assert.True(t, names[toolIAMGetRole])
	assert.True(t, names[toolEC2DescribeVPCs])
	assert.True(t, names[toolEC2DescribeSubnets])
	assert.True(t, names[toolEC2DescribeSecurityGroups])
	assert.True(t, names[toolEC2DescribeNATGateways])
	assert.True(t, names[toolRoute53ListHostedZones])
	assert.True(t, names[toolRoute53ListRecords])
	assert.True(t, names[toolS3ListBuckets])
	assert.True(t, names[toolS3GetBucketPolicy])
}

func TestParseRegionArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]interface{}
		expected string
	}{
		{"empty args", map[string]interface{}{}, ""},
		{"with region", map[string]interface{}{"region": "eu-west-2"}, "eu-west-2"},
		{"empty region string", map[string]interface{}{"region": ""}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseRegionArg(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatAWSJSON(t *testing.T) {
	t.Parallel()

	input := map[string]string{"account": "123456789012", "arn": "arn:aws:iam::123456789012:user/test"}
	result, err := formatAWSJSON(input)

	require.NoError(t, err)
	assert.Contains(t, result, "123456789012")
	assert.Contains(t, result, "arn:aws:iam")
}

func TestFormatAWSJSONInvalidInput(t *testing.T) {
	t.Parallel()

	// Channels can't be marshaled to JSON
	_, err := formatAWSJSON(make(chan int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "formatting response")
}

func TestExecuteIAMGetRoleMissingName(t *testing.T) {
	t.Parallel()

	server := newTestServerForAWS(t)
	ctx := context.Background()

	_, err := server.executeIAMGetRole(ctx, map[string]interface{}{})
	require.ErrorContains(t, err, "role_name parameter is required")
}

func TestExecuteRoute53ListRecordsMissingZoneID(t *testing.T) {
	t.Parallel()

	server := newTestServerForAWS(t)
	ctx := context.Background()

	_, err := server.executeRoute53ListRecords(ctx, map[string]interface{}{})
	require.ErrorContains(t, err, "zone_id parameter is required")
}

func TestExecuteS3GetBucketPolicyMissingBucket(t *testing.T) {
	t.Parallel()

	server := newTestServerForAWS(t)
	ctx := context.Background()

	_, err := server.executeS3GetBucketPolicy(ctx, map[string]interface{}{})
	require.ErrorContains(t, err, "bucket parameter is required")
}

func TestAWSToolSchemas(t *testing.T) {
	t.Parallel()

	tools := getAWSTools()

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()

			assert.NotEmpty(t, tool.Name, "tool name should not be empty")
			assert.NotEmpty(t, tool.Description, "tool description should not be empty")
			assert.NotNil(t, tool.InputSchema, "tool input schema should not be nil")

			schema, ok := tool.InputSchema["type"].(string)
			require.True(t, ok, "input schema should have a type field")
			assert.Equal(t, "object", schema, "input schema type should be object")
		})
	}
}

func TestAWSToolsRequiredFields(t *testing.T) {
	t.Parallel()

	// Tools that require specific fields
	requiresField := map[string][]string{
		toolIAMGetRole:         {"role_name"},
		toolRoute53ListRecords: {"zone_id"},
		toolS3GetBucketPolicy:  {"bucket"},
	}

	tools := getAWSTools()

	for _, tool := range tools {
		expected, hasRequired := requiresField[tool.Name]
		if !hasRequired {
			continue
		}

		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()

			required, ok := tool.InputSchema["required"].([]string)
			require.True(t, ok, "tool %s should have required field", tool.Name)
			assert.Equal(t, expected, required)
		})
	}
}
