package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nikogura/diagnostic-bot/pkg/k8s"
)

// Kubernetes read-only tool names.
const (
	toolK8sGetResource   = "k8s_get_resource"
	toolK8sListResources = "k8s_list_resources"
	toolK8sPodLogs       = "k8s_pod_logs"
	toolK8sListPods      = "k8s_list_pods"
	toolK8sGetEvents     = "k8s_get_events"
)

// defaultClusterName is the registry key used for the bot's own cluster.
const defaultClusterName = "in-cluster"

// loadK8sClusters builds the cluster registry. Today it holds at most one entry
// — the cluster the bot runs in (in-cluster ServiceAccount) or the one named by
// KUBECONFIG. The map shape leaves room for future multi-cluster access
// (additional named contexts) without changing the tool surface. Construction
// is best-effort: if no cluster is reachable (e.g. running outside a cluster
// with no KUBECONFIG), the Kubernetes tools are simply not registered.
func loadK8sClusters(logger *slog.Logger) (clusters map[string]*k8s.Agent) {
	clusters = make(map[string]*k8s.Agent)

	if strings.EqualFold(os.Getenv("K8S_ENABLED"), "false") {
		logger.Info("K8S_ENABLED=false - Kubernetes tools disabled")
		return clusters
	}

	name := os.Getenv("K8S_CLUSTER_NAME")
	if name == "" {
		name = defaultClusterName
	}

	agent, err := k8s.NewAgent(os.Getenv("KUBECONFIG"), logger)
	if err != nil {
		logger.Info("Kubernetes access not available - k8s tools disabled",
			slog.String("error", err.Error()))
		return clusters
	}

	clusters[name] = agent
	logger.Info("Kubernetes cluster registered (read-only)", slog.String("cluster", name))

	return clusters
}

// resolveK8sAgent selects the cluster a tool call targets. With one cluster
// configured the cluster argument is optional; with several it is required.
func (s *Server) resolveK8sAgent(cluster string) (agent *k8s.Agent, err error) {
	switch {
	case len(s.k8sClusters) == 0:
		err = errors.New("no Kubernetes cluster is configured")

	case cluster != "":
		found, ok := s.k8sClusters[cluster]
		if !ok {
			err = fmt.Errorf("unknown cluster %q", cluster)
			return agent, err
		}

		agent = found

	case len(s.k8sClusters) == 1:
		for _, found := range s.k8sClusters {
			agent = found
		}

	default:
		err = errors.New("multiple clusters configured; specify the 'cluster' argument")
	}

	return agent, err
}

// clusterProperty is the shared optional 'cluster' argument schema.
func clusterProperty() (prop map[string]interface{}) {
	prop = map[string]interface{}{
		"type":        "string",
		"description": "Cluster name to target. Optional when a single cluster is configured.",
	}
	return prop
}

// getK8sTools returns the read-only Kubernetes tool definitions. Every tool is
// read-only (get/list); none can mutate state and none can read Secrets.
func getK8sTools() (result []MCPTool) {
	result = []MCPTool{
		{
			Name:        toolK8sGetResource,
			Description: "Read a single Kubernetes resource by name (read-only). Covers core/apps, Ingress, the Gateway API (gateways, httproutes, grpcroutes, etc.), Envoy Gateway, cert-manager (certificates, certificaterequests, issuers — never the private key), and Flux/Atlas CRDs. Secrets cannot be read. Output is JSON, secret-scrubbed.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cluster": clusterProperty(),
					"resource_type": map[string]interface{}{
						"type":        "string",
						"description": "The resource type to read (see enum). Secrets are not available.",
						"enum":        k8s.AllowedResourceTypes(),
					},
					"namespace": map[string]interface{}{"type": "string", "description": "Resource namespace (omit for cluster-scoped types)"},
					"name":      map[string]interface{}{"type": "string", "description": "Resource name"},
				},
				"required": []string{"resource_type", "name"},
			},
		},
		{
			Name:        toolK8sListResources,
			Description: "List Kubernetes resources of a type (read-only), returning name, namespace, spec, and status per item. Omit namespace to list across ALL namespaces — use this to diff, e.g., which httproutes attach to which gateway. Same resource types as k8s_get_resource; Secrets cannot be listed.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cluster": clusterProperty(),
					"resource_type": map[string]interface{}{
						"type":        "string",
						"description": "The resource type to list (see enum). Secrets are not available.",
						"enum":        k8s.AllowedResourceTypes(),
					},
					"namespace":      map[string]interface{}{"type": "string", "description": "Namespace to list in; omit for all namespaces"},
					"label_selector": map[string]interface{}{"type": "string", "description": "Optional label selector"},
				},
				"required": []string{"resource_type"},
			},
		},
		{
			Name:        toolK8sPodLogs,
			Description: "Fetch pod logs from the Kubernetes API (read-only). Specify pod_name or label_selector. Output is secret-scrubbed.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cluster":        clusterProperty(),
					"namespace":      map[string]interface{}{"type": "string", "description": "Pod namespace"},
					"pod_name":       map[string]interface{}{"type": "string", "description": "Specific pod name (optional if label_selector given)"},
					"label_selector": map[string]interface{}{"type": "string", "description": "Label selector (optional if pod_name given)"},
					"container":      map[string]interface{}{"type": "string", "description": "Container name (defaults to first)"},
					"since":          map[string]interface{}{"type": "string", "description": "Duration to look back, e.g. '1h', '15m' (default 1h)"},
					"tail_lines":     map[string]interface{}{"type": "integer", "description": "Number of trailing lines (default 100)"},
					"grep":           map[string]interface{}{"type": "string", "description": "Case-insensitive substring filter (optional)"},
				},
				"required": []string{"namespace"},
			},
		},
		{
			Name:        toolK8sListPods,
			Description: "List pods in a namespace with status and restart counts (read-only).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cluster":        clusterProperty(),
					"namespace":      map[string]interface{}{"type": "string", "description": "Namespace to list pods in"},
					"label_selector": map[string]interface{}{"type": "string", "description": "Label selector (optional)"},
				},
				"required": []string{"namespace"},
			},
		},
		{
			Name:        toolK8sGetEvents,
			Description: "List Kubernetes events (read-only). Omit namespace for cluster-wide events.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cluster":        clusterProperty(),
					"namespace":      map[string]interface{}{"type": "string", "description": "Namespace (optional; omit for all namespaces)"},
					"field_selector": map[string]interface{}{"type": "string", "description": "Field selector (optional)"},
					"limit":          map[string]interface{}{"type": "integer", "description": "Max events to return (default 50)"},
				},
			},
		},
	}

	return result
}

// executeK8sGetResource handles the k8s_get_resource tool.
func (s *Server) executeK8sGetResource(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var agent *k8s.Agent

	agent, err = s.resolveK8sAgent(optStringArg(args, "cluster"))
	if err != nil {
		return result, err
	}

	result, err = agent.GetResource(ctx,
		optStringArg(args, "resource_type"),
		optStringArg(args, "namespace"),
		optStringArg(args, "name"),
		"")

	return result, err
}

// executeK8sListResources handles the k8s_list_resources tool.
func (s *Server) executeK8sListResources(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var agent *k8s.Agent

	agent, err = s.resolveK8sAgent(optStringArg(args, "cluster"))
	if err != nil {
		return result, err
	}

	result, err = agent.ListResources(ctx,
		optStringArg(args, "resource_type"),
		optStringArg(args, "namespace"),
		optStringArg(args, "label_selector"))

	return result, err
}

// executeK8sPodLogs handles the k8s_pod_logs tool.
func (s *Server) executeK8sPodLogs(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var agent *k8s.Agent

	agent, err = s.resolveK8sAgent(optStringArg(args, "cluster"))
	if err != nil {
		return result, err
	}

	req := k8s.LogRequest{
		Namespace:     optStringArg(args, "namespace"),
		PodName:       optStringArg(args, "pod_name"),
		LabelSelector: optStringArg(args, "label_selector"),
		Container:     optStringArg(args, "container"),
		Grep:          optStringArg(args, "grep"),
		TailLines:     optIntArg(args, "tail_lines"),
	}

	since := optStringArg(args, "since")
	if since != "" {
		duration, parseErr := time.ParseDuration(since)
		if parseErr != nil {
			err = fmt.Errorf("invalid 'since' duration %q: %w", since, parseErr)
			return result, err
		}

		req.Since = duration
	}

	result, err = agent.FetchLogs(ctx, req)
	return result, err
}

// executeK8sListPods handles the k8s_list_pods tool.
func (s *Server) executeK8sListPods(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var agent *k8s.Agent

	agent, err = s.resolveK8sAgent(optStringArg(args, "cluster"))
	if err != nil {
		return result, err
	}

	result, err = agent.ListPods(ctx, optStringArg(args, "namespace"), optStringArg(args, "label_selector"))
	return result, err
}

// executeK8sGetEvents handles the k8s_get_events tool.
func (s *Server) executeK8sGetEvents(ctx context.Context, args map[string]interface{}) (result string, err error) {
	var agent *k8s.Agent

	agent, err = s.resolveK8sAgent(optStringArg(args, "cluster"))
	if err != nil {
		return result, err
	}

	result, err = agent.GetEvents(ctx,
		optStringArg(args, "namespace"),
		optStringArg(args, "field_selector"),
		optIntArg(args, "limit"))

	return result, err
}

// optStringArg returns a string argument or "".
func optStringArg(args map[string]interface{}, key string) (val string) {
	val, _ = args[key].(string)
	return val
}

// optIntArg returns an integer argument or 0 (JSON numbers arrive as float64).
func optIntArg(args map[string]interface{}, key string) (val int) {
	f, ok := args[key].(float64)
	if ok {
		val = int(f)
	}

	return val
}
