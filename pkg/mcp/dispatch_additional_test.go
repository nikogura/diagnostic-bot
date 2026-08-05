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
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatchRoutesAdditionalFamilies verifies the legacy dispatch path routes
// the GitLab, Tempo, and AWS read tools — the families that used to be
// registered only on the SDK transport. Routing (not the tool's success) is
// what is asserted: with no backing clients the handlers error, but they must
// NOT return "unknown tool", which would mean the case is missing.
func TestDispatchRoutesAdditionalFamilies(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()
	server := &Server{logger: logger}

	routed := []string{
		toolGitLabGetFile,
		toolGitLabListDirectory,
		toolGitLabSearchCode,
		toolTempoGetTrace,
		toolTempoSearchTraces,
		toolTempoListEndpoints,
		toolSTSGetCallerIdentity,
		toolIAMListRoles,
		toolIAMGetRole,
		toolEC2DescribeVPCs,
		toolEC2DescribeSubnets,
		toolEC2DescribeSecurityGroups,
		toolEC2DescribeNATGateways,
		toolRoute53ListHostedZones,
		toolRoute53ListRecords,
		toolS3ListBuckets,
		toolS3GetBucketPolicy,
	}

	for _, name := range routed {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := server.dispatchToolCall(ctx, name, map[string]interface{}{})
			if err != nil {
				assert.NotContains(t, err.Error(), "unknown tool",
					"%q must be routed by the legacy dispatcher, not fall through as unknown", name)
			}
		})
	}
}

// TestDispatchUnknownToolStillErrors is the control: a genuinely unknown tool
// name must still be reported as unknown after the additional-family routing.
func TestDispatchUnknownToolStillErrors(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()
	server := &Server{logger: logger}

	_, err := server.dispatchToolCall(ctx, "definitely_not_a_real_tool", map[string]interface{}{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool",
		"an unrecognized tool must still be reported as unknown")
}
