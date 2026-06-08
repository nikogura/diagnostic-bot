package mcp

import (
	"strings"
	"testing"

	"github.com/nikogura/diagnostic-bot/pkg/k8s"
)

func TestResolveK8sAgent(t *testing.T) {
	t.Parallel()

	one := &k8s.Agent{}
	single := &Server{k8sClusters: map[string]*k8s.Agent{"in-cluster": one}}
	multi := &Server{k8sClusters: map[string]*k8s.Agent{"a": {}, "b": {}}}
	none := &Server{}

	t.Run("none configured", func(t *testing.T) {
		t.Parallel()
		_, err := none.resolveK8sAgent("")
		if err == nil || !strings.Contains(err.Error(), "no Kubernetes cluster") {
			t.Errorf("expected not-configured error, got: %v", err)
		}
	})

	t.Run("single, no arg returns the one", func(t *testing.T) {
		t.Parallel()
		got, err := single.resolveK8sAgent("")
		if err != nil || got != one {
			t.Errorf("expected the single cluster, got %v err %v", got, err)
		}
	})

	t.Run("single, named", func(t *testing.T) {
		t.Parallel()
		got, err := single.resolveK8sAgent("in-cluster")
		if err != nil || got != one {
			t.Errorf("expected the named cluster, got %v err %v", got, err)
		}
	})

	t.Run("unknown name", func(t *testing.T) {
		t.Parallel()
		_, err := single.resolveK8sAgent("nope")
		if err == nil || !strings.Contains(err.Error(), "unknown cluster") {
			t.Errorf("expected unknown-cluster error, got: %v", err)
		}
	})

	t.Run("multiple requires arg", func(t *testing.T) {
		t.Parallel()
		_, err := multi.resolveK8sAgent("")
		if err == nil || !strings.Contains(err.Error(), "specify the 'cluster'") {
			t.Errorf("expected a must-specify-cluster error, got: %v", err)
		}
	})
}

func TestK8sToolsAreReadOnly(t *testing.T) {
	t.Parallel()

	tools := getK8sTools()

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true

		lower := strings.ToLower(tool.Name)
		for _, verb := range []string{"create", "update", "delete", "patch", "apply", "write", "restore"} {
			if strings.Contains(lower, verb) {
				t.Errorf("k8s tool %q looks like a write tool (contains %q)", tool.Name, verb)
			}
		}
	}

	for _, want := range []string{toolK8sGetResource, toolK8sPodLogs, toolK8sListPods, toolK8sGetEvents} {
		if !names[want] {
			t.Errorf("expected k8s tool %q to be present", want)
		}
	}
}

func TestK8sGetResourceSchemaExcludesSecret(t *testing.T) {
	t.Parallel()

	var getResource MCPTool
	for _, tool := range getK8sTools() {
		if tool.Name == toolK8sGetResource {
			getResource = tool
		}
	}

	props, _ := getResource.InputSchema["properties"].(map[string]interface{})
	rt, _ := props["resource_type"].(map[string]interface{})
	enum, _ := rt["enum"].([]string)

	for _, v := range enum {
		if strings.Contains(strings.ToLower(v), "secret") {
			t.Errorf("resource_type enum must not include a secret type, found %q", v)
		}
	}

	if len(enum) == 0 {
		t.Error("expected a non-empty resource_type enum")
	}
}
