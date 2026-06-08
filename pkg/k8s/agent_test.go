package k8s

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

	_, err := testAgent().GetResource(context.Background(), "node", "", "node-1", "")
	if err == nil || !strings.Contains(err.Error(), "non-allowlisted") {
		t.Errorf("expected a non-allowlisted error for resource_type 'node', got: %v", err)
	}
}

func TestGetResourceConfigMapIsSanitized(t *testing.T) {
	t.Parallel()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "prod"},
		Data:       map[string]string{"config": "api_key=AKIA1234567890ABCDEFG"},
	}

	result, err := testAgent(cm).GetResource(context.Background(), "configmap", "prod", "app-config", "")
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
