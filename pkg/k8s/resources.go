package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/nikogura/diagnostic-bot/pkg/metrics"
)

// resourceMapping is an allowlisted, read-only resource type.
type resourceMapping struct {
	gvr           schema.GroupVersionResource
	clusterScoped bool
}

// API group identifiers, named to avoid repetition across the allowlist.
const (
	groupGatewayAPI   = "gateway.networking.k8s.io"
	groupEnvoyGateway = "gateway.envoyproxy.io"
	groupCertManager  = "cert-manager.io"
	groupArgo         = "argoproj.io"

	groupIstioNetworking = "networking.istio.io"
	groupIstioSecurity   = "security.istio.io"
	groupIstioExtensions = "extensions.istio.io"
	groupIstioTelemetry  = "telemetry.istio.io"
)

// allowedK8sResources is the read-only allowlist of resource types the bot may
// get and list. It is the security boundary: only these types are reachable,
// and Secrets are deliberately absent (their key material lives in Secrets,
// which neither this allowlist nor the RBAC grants). Versions are pinned to the
// commonly-served ones; a cluster serving a different version returns a clear
// "could not find the requested resource" error rather than reading anything
// unexpected.
//
//nolint:gochecknoglobals // static allowlist
var allowedK8sResources = map[string]resourceMapping{
	// core
	"configmap": {gvr: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}},
	"service":   {gvr: schema.GroupVersionResource{Version: "v1", Resource: "services"}},
	"pod":       {gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}},
	"endpoints": {gvr: schema.GroupVersionResource{Version: "v1", Resource: "endpoints"}},
	"node":      {gvr: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, clusterScoped: true},
	"namespace": {gvr: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, clusterScoped: true},
	// apps
	"deployment":  {gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}},
	"statefulset": {gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}},
	"daemonset":   {gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}},
	"replicaset":  {gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}},
	// networking (legacy ingress)
	"ingress":      {gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}},
	"ingressclass": {gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"}, clusterScoped: true},
	// Gateway API (the standard)
	"gateway":        {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1", Resource: "gateways"}},
	"gatewayclass":   {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1", Resource: "gatewayclasses"}, clusterScoped: true},
	"httproute":      {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1", Resource: "httproutes"}},
	"grpcroute":      {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1", Resource: "grpcroutes"}},
	"tcproute":       {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1alpha2", Resource: "tcproutes"}},
	"tlsroute":       {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1alpha2", Resource: "tlsroutes"}},
	"udproute":       {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1alpha2", Resource: "udproutes"}},
	"referencegrant": {gvr: schema.GroupVersionResource{Group: groupGatewayAPI, Version: "v1beta1", Resource: "referencegrants"}},
	// Envoy Gateway extensions
	"envoyproxy":           {gvr: schema.GroupVersionResource{Group: groupEnvoyGateway, Version: "v1alpha1", Resource: "envoyproxies"}},
	"clienttrafficpolicy":  {gvr: schema.GroupVersionResource{Group: groupEnvoyGateway, Version: "v1alpha1", Resource: "clienttrafficpolicies"}},
	"backendtrafficpolicy": {gvr: schema.GroupVersionResource{Group: groupEnvoyGateway, Version: "v1alpha1", Resource: "backendtrafficpolicies"}},
	"securitypolicy":       {gvr: schema.GroupVersionResource{Group: groupEnvoyGateway, Version: "v1alpha1", Resource: "securitypolicies"}},
	"envoypatchpolicy":     {gvr: schema.GroupVersionResource{Group: groupEnvoyGateway, Version: "v1alpha1", Resource: "envoypatchpolicies"}},
	"envoyextensionpolicy": {gvr: schema.GroupVersionResource{Group: groupEnvoyGateway, Version: "v1alpha1", Resource: "envoyextensionpolicies"}},
	"backend":              {gvr: schema.GroupVersionResource{Group: groupEnvoyGateway, Version: "v1alpha1", Resource: "backends"}},
	// cert-manager (spec/status only — private keys live in Secrets, which are not readable)
	"certificate":        {gvr: schema.GroupVersionResource{Group: groupCertManager, Version: "v1", Resource: "certificates"}},
	"certificaterequest": {gvr: schema.GroupVersionResource{Group: groupCertManager, Version: "v1", Resource: "certificaterequests"}},
	"issuer":             {gvr: schema.GroupVersionResource{Group: groupCertManager, Version: "v1", Resource: "issuers"}},
	"clusterissuer":      {gvr: schema.GroupVersionResource{Group: groupCertManager, Version: "v1", Resource: "clusterissuers"}, clusterScoped: true},
	// Flux
	"gitrepository": {gvr: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}},
	"kustomization": {gvr: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}},
	"helmrelease":   {gvr: schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}},
	// Argo CD
	"application":    {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "applications"}},
	"applicationset": {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "applicationsets"}},
	"appproject":     {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "appprojects"}},
	// Argo Rollouts
	"rollout":          {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "rollouts"}},
	"analysisrun":      {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "analysisruns"}},
	"analysistemplate": {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "analysistemplates"}},
	"experiment":       {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "experiments"}},
	// Argo Workflows
	"workflow":         {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "workflows"}},
	"cronworkflow":     {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "cronworkflows"}},
	"workflowtemplate": {gvr: schema.GroupVersionResource{Group: groupArgo, Version: "v1alpha1", Resource: "workflowtemplates"}},
	// Istio networking (istio-gateway is networking.istio.io/Gateway, distinct from the Gateway API "gateway")
	"virtualservice":  {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1", Resource: "virtualservices"}},
	"destinationrule": {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1", Resource: "destinationrules"}},
	"istio-gateway":   {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1", Resource: "gateways"}},
	"serviceentry":    {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1", Resource: "serviceentries"}},
	"sidecar":         {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1", Resource: "sidecars"}},
	"workloadentry":   {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1", Resource: "workloadentries"}},
	"workloadgroup":   {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1", Resource: "workloadgroups"}},
	"envoyfilter":     {gvr: schema.GroupVersionResource{Group: groupIstioNetworking, Version: "v1alpha3", Resource: "envoyfilters"}},
	// Istio security
	"authorizationpolicy":   {gvr: schema.GroupVersionResource{Group: groupIstioSecurity, Version: "v1", Resource: "authorizationpolicies"}},
	"peerauthentication":    {gvr: schema.GroupVersionResource{Group: groupIstioSecurity, Version: "v1", Resource: "peerauthentications"}},
	"requestauthentication": {gvr: schema.GroupVersionResource{Group: groupIstioSecurity, Version: "v1", Resource: "requestauthentications"}},
	// Istio extensions / telemetry
	"wasmplugin": {gvr: schema.GroupVersionResource{Group: groupIstioExtensions, Version: "v1alpha1", Resource: "wasmplugins"}},
	"telemetry":  {gvr: schema.GroupVersionResource{Group: groupIstioTelemetry, Version: "v1", Resource: "telemetries"}},
	// Atlas
	"atlasmigration": {gvr: schema.GroupVersionResource{Group: "db.atlasgo.io", Version: "v1alpha1", Resource: "atlasmigrations"}},
}

// AllowedResourceTypes returns the sorted list of readable resource types, for
// tool descriptions and discovery.
func AllowedResourceTypes() (types []string) {
	types = make([]string, 0, len(allowedK8sResources))
	for name := range allowedK8sResources {
		types = append(types, name)
	}

	sort.Strings(types)

	return types
}

// isSecretType reports whether a requested type is a Kubernetes Secret. Secrets
// are never readable; this explicit check yields a clear error in addition to
// the allowlist simply omitting them.
func isSecretType(resourceType string) (isSecret bool) {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case "secret", "secrets":
		isSecret = true
	}

	return isSecret
}

// lookupResource resolves a requested type against the allowlist, rejecting
// Secrets explicitly and unknown types clearly.
func lookupResource(resourceType string) (res resourceMapping, err error) {
	if isSecretType(resourceType) {
		err = errors.New("reading Kubernetes Secrets is not permitted")
		return res, err
	}

	var ok bool

	res, ok = allowedK8sResources[strings.ToLower(strings.TrimSpace(resourceType))]
	if !ok {
		err = fmt.Errorf("unsupported or non-allowlisted resource type: %s", resourceType)
		return res, err
	}

	return res, err
}

// resourceInterface returns the dynamic client scoped correctly for a resource:
// cluster-wide for cluster-scoped types or when no namespace is given.
func (a *Agent) resourceInterface(res resourceMapping, namespace string) (ri dynamic.ResourceInterface) {
	if res.clusterScoped || namespace == "" {
		ri = a.dynamicClient.Resource(res.gvr)
		return ri
	}

	ri = a.dynamicClient.Resource(res.gvr).Namespace(namespace)
	return ri
}

// GetResource fetches a single allowlisted resource by name. Output is JSON,
// stripped of noisy managed fields and run through the secret/PII sanitizer.
// Secrets and non-allowlisted types are rejected.
func (a *Agent) GetResource(ctx context.Context, resourceType string, namespace string, name string, outputFormat string) (result string, err error) {
	a.logger.InfoContext(ctx, "getting Kubernetes resource",
		slog.String("type", resourceType),
		slog.String("namespace", namespace),
		slog.String("name", name),
		slog.String("format", outputFormat))

	var res resourceMapping

	res, err = lookupResource(resourceType)
	if err != nil {
		return result, err
	}

	metrics.RecordK8sQuery(ctx, namespace, resourceType)

	obj, getErr := a.resourceInterface(res, namespace).Get(ctx, name, metav1.GetOptions{})
	if getErr != nil {
		err = fmt.Errorf("fetching resource: %w", getErr)
		return result, err
	}

	formatted, formatErr := formatUnstructured(obj)
	if formatErr != nil {
		err = formatErr
		return result, err
	}

	result = a.sanitizer.Sanitize(formatted)
	return result, err
}

// ListResources lists an allowlisted resource type, returning name, namespace,
// spec, and status per item. With an empty namespace it lists across all
// namespaces — the form needed to diff, e.g., HTTPRoutes across gateways.
func (a *Agent) ListResources(ctx context.Context, resourceType string, namespace string, labelSelector string) (result string, err error) {
	a.logger.InfoContext(ctx, "listing Kubernetes resources",
		slog.String("type", resourceType),
		slog.String("namespace", namespace),
		slog.String("label_selector", labelSelector))

	var res resourceMapping

	res, err = lookupResource(resourceType)
	if err != nil {
		return result, err
	}

	metrics.RecordK8sQuery(ctx, namespace, resourceType)

	var list *unstructured.UnstructuredList

	list, listErr := a.listFor(ctx, res, namespace, labelSelector)
	if listErr != nil {
		err = fmt.Errorf("listing %s: %w", resourceType, listErr)
		return result, err
	}

	if len(list.Items) == 0 {
		result = fmt.Sprintf("No %s found.", resourceType)
		return result, err
	}

	formatted, formatErr := formatResourceList(list)
	if formatErr != nil {
		err = formatErr
		return result, err
	}

	result = a.sanitizer.Sanitize(formatted)
	return result, err
}

// listFor performs the dynamic list, scoped to a namespace or cluster-wide.
func (a *Agent) listFor(ctx context.Context, res resourceMapping, namespace string, labelSelector string) (list *unstructured.UnstructuredList, err error) {
	opts := metav1.ListOptions{LabelSelector: labelSelector}
	list, err = a.resourceInterface(res, namespace).List(ctx, opts)
	return list, err
}

// formatUnstructured renders a single object as indented JSON, minus managed fields.
func formatUnstructured(obj *unstructured.Unstructured) (out string, err error) {
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")

	data, marshalErr := json.MarshalIndent(obj.Object, "", "  ")
	if marshalErr != nil {
		err = fmt.Errorf("formatting resource: %w", marshalErr)
		return out, err
	}

	out = string(data)
	return out, err
}

// listItem is the compact per-resource view returned by ListResources.
type listItem struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Spec      any    `json:"spec,omitempty"`
	Status    any    `json:"status,omitempty"`
}

// formatResourceList renders a list as JSON of name/namespace/spec/status items.
func formatResourceList(list *unstructured.UnstructuredList) (out string, err error) {
	items := make([]listItem, 0, len(list.Items))

	for i := range list.Items {
		obj := list.Items[i].Object
		metadata, _ := obj["metadata"].(map[string]any)

		items = append(items, listItem{
			Namespace: nestedString(metadata, "namespace"),
			Name:      nestedString(metadata, "name"),
			Spec:      obj["spec"],
			Status:    obj["status"],
		})
	}

	data, marshalErr := json.MarshalIndent(items, "", "  ")
	if marshalErr != nil {
		err = fmt.Errorf("formatting resource list: %w", marshalErr)
		return out, err
	}

	out = string(data)
	return out, err
}

// nestedString safely reads a string value from an unstructured map.
func nestedString(m map[string]any, key string) (value string) {
	value, _ = m[key].(string)
	return value
}
