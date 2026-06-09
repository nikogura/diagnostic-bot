package k8s

import (
	"context"
	"log/slog"
	"maps"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func testAgent(objects ...runtime.Object) (agent *Agent) {
	agent = &Agent{
		clientset: fake.NewSimpleClientset(objects...),
		logger:    slog.New(slog.DiscardHandler),
		sanitizer: NewSanitizer(),
	}
	return agent
}

// gvrListKinds maps the GVRs exercised in tests to their list kinds so the
// dynamic fake can serve List calls.
//
//nolint:gochecknoglobals // test fixture
var gvrListKinds = map[schema.GroupVersionResource]string{
	{Version: "v1", Resource: "configmaps"}:                                     "ConfigMapList",
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
	{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}:         "CertificateList",
	{Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests"}:  "CertificateRequestList",
	{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:       "ApplicationList",
}

// dynamicTestAgent builds an Agent backed by a fake dynamic client.
func dynamicTestAgent(objs ...*unstructured.Unstructured) (agent *Agent) {
	scheme := runtime.NewScheme()

	runtimeObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		runtimeObjs = append(runtimeObjs, o)
	}

	agent = &Agent{
		dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListKinds, runtimeObjs...),
		logger:        slog.New(slog.DiscardHandler),
		sanitizer:     NewSanitizer(),
	}
	return agent
}

// unstructuredObj builds a minimal unstructured resource.
func unstructuredObj(apiVersion, kind, namespace, name string, fields map[string]any) (obj *unstructured.Unstructured) {
	metadata := map[string]any{"name": name}
	if namespace != "" {
		metadata["namespace"] = namespace
	}

	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   metadata,
	}
	maps.Copy(object, fields)

	obj = &unstructured.Unstructured{Object: object}
	return obj
}

func TestGetResourceRejectsSecrets(t *testing.T) {
	t.Parallel()

	agent := testAgent()

	for _, rt := range []string{"secret", "secrets", "Secret", "SECRETS"} {
		_, err := agent.GetResource(context.Background(), rt, "default", "any", "")
		if err == nil {
			t.Errorf("resource_type %q must be rejected", rt)
			continue
		}

		if !strings.Contains(err.Error(), "not permitted") {
			t.Errorf("resource_type %q: expected a not-permitted error, got: %v", rt, err)
		}
	}
}

func TestGetResourceRejectsNonAllowlisted(t *testing.T) {
	t.Parallel()

	_, err := testAgent().GetResource(context.Background(), "widget", "", "thing", "")
	if err == nil || !strings.Contains(err.Error(), "non-allowlisted") {
		t.Errorf("expected a non-allowlisted error, got: %v", err)
	}
}

func TestGetResourceConfigMapIsSanitized(t *testing.T) {
	t.Parallel()

	cm := unstructuredObj("v1", "ConfigMap", "prod", "app-config", map[string]any{
		"data": map[string]any{"config": "api_key=AKIA1234567890ABCDEFG"},
	})

	result, err := dynamicTestAgent(cm).GetResource(context.Background(), "configmap", "prod", "app-config", "")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}

	if strings.Contains(result, "AKIA1234567890ABCDEFG") {
		t.Errorf("secret-shaped value must be scrubbed from configmap output:\n%s", result)
	}

	if !strings.Contains(result, "app-config") {
		t.Errorf("expected the configmap name in output:\n%s", result)
	}
}

func TestGetResourceHTTPRoute(t *testing.T) {
	t.Parallel()

	route := unstructuredObj("gateway.networking.k8s.io/v1", "HTTPRoute", "apps", "grafana", map[string]any{
		"spec": map[string]any{
			"hostnames":  []any{"grafana.nxtools.dev"},
			"parentRefs": []any{map[string]any{"name": "istio-ingress-nxtools"}},
		},
	})

	result, err := dynamicTestAgent(route).GetResource(context.Background(), "httproute", "apps", "grafana", "")
	if err != nil {
		t.Fatalf("GetResource(httproute): %v", err)
	}

	for _, want := range []string{"grafana.nxtools.dev", "istio-ingress-nxtools"} {
		if !strings.Contains(result, want) {
			t.Errorf("httproute output missing %q:\n%s", want, result)
		}
	}
}

func TestListResourcesAcrossNamespaces(t *testing.T) {
	t.Parallel()

	legacy := unstructuredObj("gateway.networking.k8s.io/v1", "HTTPRoute", "apps", "grafana-legacy", map[string]any{
		"spec": map[string]any{"hostnames": []any{"grafana.tools.nxteam.dev"}},
	})
	migrated := unstructuredObj("gateway.networking.k8s.io/v1", "HTTPRoute", "infra", "grafana-nxtools", map[string]any{
		"spec": map[string]any{"hostnames": []any{"grafana.nxtools.dev"}},
	})

	// Empty namespace → list across all namespaces.
	result, err := dynamicTestAgent(legacy, migrated).ListResources(context.Background(), "httproute", "", "")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}

	for _, want := range []string{"grafana-legacy", "grafana-nxtools", "grafana.tools.nxteam.dev", "grafana.nxtools.dev", "apps", "infra"} {
		if !strings.Contains(result, want) {
			t.Errorf("list output missing %q:\n%s", want, result)
		}
	}
}

func TestListResourcesRejectsSecrets(t *testing.T) {
	t.Parallel()

	_, err := dynamicTestAgent().ListResources(context.Background(), "secrets", "default", "")
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("listing secrets must be rejected, got: %v", err)
	}
}

func TestListPods(t *testing.T) {
	t.Parallel()

	pods := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-2", Namespace: "prod"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
	}

	result, err := testAgent(pods...).ListPods(context.Background(), "prod", "")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}

	for _, want := range []string{"api-1", "api-2", "Running", "Pending"} {
		if !strings.Contains(result, want) {
			t.Errorf("ListPods output missing %q:\n%s", want, result)
		}
	}
}
