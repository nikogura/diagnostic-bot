package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

const (
	// MaxLogSize is the maximum size of logs to return (50KB).
	MaxLogSize = 50 * 1024

	// DefaultTailLines is the default number of log lines to tail.
	DefaultTailLines = 100
)

// Agent provides Kubernetes cluster access for investigations.
type Agent struct {
	clientset     *kubernetes.Clientset
	dynamicClient dynamic.Interface
	logger        *slog.Logger
	sanitizer     *Sanitizer
}

// NewAgent creates a new Kubernetes agent.
func NewAgent(kubeconfig string, logger *slog.Logger) (result *Agent, err error) {
	var config *rest.Config
	var clientset *kubernetes.Clientset
	var dynamicClient dynamic.Interface

	if kubeconfig != "" {
		// Use kubeconfig file
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			err = fmt.Errorf("building config from kubeconfig: %w", err)
			return result, err
		}
	} else {
		// Use in-cluster config
		config, err = rest.InClusterConfig()
		if err != nil {
			err = fmt.Errorf("building in-cluster config: %w", err)
			return result, err
		}
	}

	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		err = fmt.Errorf("creating Kubernetes clientset: %w", err)
		return result, err
	}

	dynamicClient, err = dynamic.NewForConfig(config)
	if err != nil {
		err = fmt.Errorf("creating dynamic client: %w", err)
		return result, err
	}

	result = &Agent{
		clientset:     clientset,
		dynamicClient: dynamicClient,
		logger:        logger,
		sanitizer:     NewSanitizer(),
	}

	return result, err
}

// LogRequest represents a request to fetch pod logs.
type LogRequest struct {
	Namespace     string
	LabelSelector string
	PodName       string
	Container     string
	Since         time.Duration
	TailLines     int
	Grep          string
}

// FetchLogs retrieves logs from Kubernetes pods.
//
//nolint:gocognit,funlen // Log fetching with sanitization and filtering is inherently complex
func (a *Agent) FetchLogs(ctx context.Context, req LogRequest) (result string, err error) {
	if req.TailLines == 0 {
		req.TailLines = DefaultTailLines
	}

	if req.Since == 0 {
		req.Since = 1 * time.Hour
	}

	a.logger.InfoContext(ctx, "fetching Kubernetes logs",
		slog.String("namespace", req.Namespace),
		slog.String("label_selector", req.LabelSelector),
		slog.String("pod_name", req.PodName),
		slog.Duration("since", req.Since))

	// Record metrics
	metrics.RecordK8sQuery(ctx, req.Namespace, "pod_logs")

	var pods []corev1.Pod

	if req.PodName != "" {
		// Fetch specific pod
		var pod *corev1.Pod

		pod, err = a.clientset.CoreV1().Pods(req.Namespace).Get(ctx, req.PodName, metav1.GetOptions{})
		if err != nil {
			err = fmt.Errorf("getting pod %s: %w", req.PodName, err)
			return result, err
		}

		pods = append(pods, *pod)
	} else if req.LabelSelector != "" {
		// List pods by label selector
		var podList *corev1.PodList

		podList, err = a.clientset.CoreV1().Pods(req.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: req.LabelSelector,
		})
		if err != nil {
			err = fmt.Errorf("listing pods with selector %s: %w", req.LabelSelector, err)
			return result, err
		}

		pods = podList.Items
	} else {
		err = errors.New("either pod_name or label_selector must be specified")
		return result, err
	}

	if len(pods) == 0 {
		result = "No pods found matching criteria."
		return result, err
	}

	var logBuilder strings.Builder
	sinceSeconds := int64(req.Since.Seconds())
	tailLines := int64(req.TailLines)

	for _, pod := range pods {
		// Determine container name
		containerName := req.Container
		if containerName == "" && len(pod.Spec.Containers) > 0 {
			containerName = pod.Spec.Containers[0].Name
		}

		fmt.Fprintf(&logBuilder, "=== Pod: %s, Container: %s ===\n", pod.Name, containerName)

		// Fetch logs
		logReq := a.clientset.CoreV1().Pods(req.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container:    containerName,
			SinceSeconds: &sinceSeconds,
			TailLines:    &tailLines,
		})

		logStream, streamErr := logReq.Stream(ctx)
		if streamErr != nil {
			fmt.Fprintf(&logBuilder, "Error fetching logs: %v\n\n", streamErr)
			continue
		}

		logData, readErr := io.ReadAll(logStream)
		logStream.Close()

		if readErr != nil {
			fmt.Fprintf(&logBuilder, "Error reading logs: %v\n\n", readErr)
			continue
		}

		// Apply grep filter if specified
		filteredLogs := string(logData)
		if req.Grep != "" {
			filteredLogs = a.grepLogs(filteredLogs, req.Grep)
		}

		// Sanitize logs
		sanitizedLogs := a.sanitizer.Sanitize(filteredLogs)

		logBuilder.WriteString(sanitizedLogs)
		logBuilder.WriteString("\n\n")

		// Check size limit
		if logBuilder.Len() > MaxLogSize {
			logBuilder.WriteString("... (truncated - logs exceed 50KB limit)\n")
			break
		}
	}

	result = logBuilder.String()

	if result == "" {
		result = "No log data retrieved."
	}

	return result, err
}

// GetResource retrieves a Kubernetes resource configuration.
func (a *Agent) GetResource(ctx context.Context, resourceType string, namespace string, name string, outputFormat string) (result string, err error) {
	a.logger.InfoContext(ctx, "getting Kubernetes resource",
		slog.String("type", resourceType),
		slog.String("namespace", namespace),
		slog.String("name", name),
		slog.String("format", outputFormat))

	// Record metrics
	metrics.RecordK8sQuery(ctx, namespace, resourceType)

	var resource interface{}

	switch strings.ToLower(resourceType) {
	case "configmap":
		resource, err = a.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})

	case "deployment":
		resource, err = a.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})

	case "service":
		resource, err = a.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})

	case "pod":
		resource, err = a.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})

	case "gitrepository":
		gvr := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
		resource, err = a.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})

	case "kustomization":
		gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
		resource, err = a.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})

	case "atlasmigration":
		gvr := schema.GroupVersionResource{Group: "db.atlasgo.io", Version: "v1alpha1", Resource: "atlasmigrations"}
		resource, err = a.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})

	default:
		err = fmt.Errorf("unsupported resource type: %s", resourceType)
		return result, err
	}

	if err != nil {
		err = fmt.Errorf("fetching resource: %w", err)
		return result, err
	}

	// Format output
	formatted := fmt.Sprintf("%+v", resource)

	// Sanitize
	result = a.sanitizer.Sanitize(formatted)

	return result, err
}

// ListPods lists pods in a namespace.
func (a *Agent) ListPods(ctx context.Context, namespace string, labelSelector string) (result string, err error) {
	var podList *corev1.PodList

	a.logger.InfoContext(ctx, "listing pods",
		slog.String("namespace", namespace),
		slog.String("label_selector", labelSelector))

	podList, err = a.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		err = fmt.Errorf("listing pods: %w", err)
		return result, err
	}

	if len(podList.Items) == 0 {
		result = "No pods found."
		return result, err
	}

	var builder strings.Builder

	fmt.Fprintf(&builder, "Found %d pods:\n\n", len(podList.Items))

	for _, pod := range podList.Items {
		fmt.Fprintf(&builder, "• %s\n", pod.Name)
		fmt.Fprintf(&builder, "  Status: %s\n", pod.Status.Phase)
		fmt.Fprintf(&builder, "  Node: %s\n", pod.Spec.NodeName)
		fmt.Fprintf(&builder, "  Created: %s\n", pod.CreationTimestamp.Format(time.RFC3339))

		if len(pod.Status.ContainerStatuses) > 0 {
			builder.WriteString("  Containers:\n")

			for _, cs := range pod.Status.ContainerStatuses {
				fmt.Fprintf(&builder, "    - %s (Ready: %t, RestartCount: %d)\n",
					cs.Name, cs.Ready, cs.RestartCount)
			}
		}

		builder.WriteString("\n")
	}

	result = builder.String()
	return result, err
}

// GetEvents retrieves Kubernetes events.
func (a *Agent) GetEvents(ctx context.Context, namespace string, fieldSelector string, limit int) (result string, err error) {
	if limit == 0 {
		limit = 50
	}

	a.logger.InfoContext(ctx, "getting Kubernetes events",
		slog.String("namespace", namespace),
		slog.String("field_selector", fieldSelector),
		slog.Int("limit", limit))

	var eventList *corev1.EventList

	if namespace == "" {
		eventList, err = a.clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{
			FieldSelector: fieldSelector,
			Limit:         int64(limit),
		})
	} else {
		eventList, err = a.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fieldSelector,
			Limit:         int64(limit),
		})
	}

	if err != nil {
		err = fmt.Errorf("getting events: %w", err)
		return result, err
	}

	if len(eventList.Items) == 0 {
		result = "No events found."
		return result, err
	}

	var builder strings.Builder

	fmt.Fprintf(&builder, "Found %d events:\n\n", len(eventList.Items))

	for _, event := range eventList.Items {
		fmt.Fprintf(&builder, "[%s] %s/%s\n",
			event.LastTimestamp.Format(time.RFC3339),
			event.InvolvedObject.Kind,
			event.InvolvedObject.Name)
		fmt.Fprintf(&builder, "  Type: %s\n", event.Type)
		fmt.Fprintf(&builder, "  Reason: %s\n", event.Reason)
		fmt.Fprintf(&builder, "  Message: %s\n", event.Message)
		builder.WriteString("\n")
	}

	result = builder.String()
	return result, err
}

// grepLogs filters logs by pattern (case-insensitive).
func (a *Agent) grepLogs(logs string, pattern string) (result string) {
	pattern = strings.ToLower(pattern)
	lines := strings.Split(logs, "\n")

	var filtered []string

	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), pattern) {
			filtered = append(filtered, line)
		}
	}

	result = strings.Join(filtered, "\n")
	return result
}

// WhoisLookup performs a whois lookup on an IP address.
func (a *Agent) WhoisLookup(ctx context.Context, ipAddress string) (result string, err error) {
	a.logger.InfoContext(ctx, "performing whois lookup",
		slog.String("ip", ipAddress))

	// Use ip-api.com for geolocation lookup (free, no auth required)
	// This provides: country, ISP, ASN, organization
	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=status,message,country,countryCode,region,regionName,city,isp,org,as,query", ipAddress)

	// Note: Using exec.CommandContext to call curl since we're in a container
	// and net/http might have issues with DNS
	cmd := exec.CommandContext(ctx, "wget", "-qO-", url)

	output, execErr := cmd.Output()
	if execErr != nil {
		err = fmt.Errorf("executing whois lookup: %w", execErr)
		return result, err
	}

	// Parse the JSON response to make it readable
	var data map[string]interface{}

	err = json.Unmarshal(output, &data)
	if err != nil {
		// If JSON parsing fails, return raw output
		result = string(output)
		return result, err
	}

	// Check status
	status, _ := data["status"].(string)
	if status != "success" {
		message, _ := data["message"].(string)
		result = fmt.Sprintf("Whois lookup failed for %s: %s\n", ipAddress, message)
		return result, err
	}

	// Format the results nicely
	var builder strings.Builder

	fmt.Fprintf(&builder, "Whois lookup for %s:\n\n", ipAddress)
	fmt.Fprintf(&builder, "Country: %s (%s)\n", data["country"], data["countryCode"])
	fmt.Fprintf(&builder, "Region: %s (%s)\n", data["regionName"], data["region"])
	fmt.Fprintf(&builder, "City: %s\n", data["city"])
	fmt.Fprintf(&builder, "ISP: %s\n", data["isp"])
	fmt.Fprintf(&builder, "Organization: %s\n", data["org"])
	fmt.Fprintf(&builder, "ASN: %s\n", data["as"])

	result = builder.String()
	return result, err
}
