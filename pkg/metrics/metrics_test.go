package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// TestInitRecordsMetricsWithExpectedNames wires the OTel meter through the
// Prometheus exporter exactly as production does, records each instrument, and
// asserts the gathered metric names match what dashboards query (no doubled
// _total or unit suffixes).
func TestInitRecordsMetricsWithExpectedNames(t *testing.T) {
	// Not parallel: Init sets package-global instruments.
	registry := prometheus.NewRegistry()

	exporter, err := promexporter.New(
		promexporter.WithRegisterer(registry),
		promexporter.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithoutSuffixes),
		promexporter.WithoutScopeInfo(),
	)
	if err != nil {
		t.Fatalf("creating prometheus exporter: %v", err)
	}

	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	initErr := Init(provider.Meter("test"))
	if initErr != nil {
		t.Fatalf("Init: %v", initErr)
	}

	ctx := context.Background()
	RecordInvestigationStarted(ctx, "modsecurity")
	RecordInvestigationResolved(ctx, "modsecurity")
	RecordToolExecution(ctx, "query_loki", "success")
	RecordInjectionDefang(ctx, "forged role label")
	RecordClaudeAPICall(ctx, "success")
	AddClaudeAPITokens(ctx, "input", 100)
	RecordK8sQuery(ctx, "ingress-nginx", "pod_logs")
	RecordLokiQuery(ctx, "success")
	ObserveInvestigationDuration(ctx, "modsecurity", "success", 4.2)
	AddInvestigationInFlight(ctx, 1)
	SetConversationsActive(3)

	families, gatherErr := registry.Gather()
	if gatherErr != nil {
		t.Fatalf("gather: %v", gatherErr)
	}

	got := make(map[string]bool, len(families))
	for _, mf := range families {
		got[mf.GetName()] = true
	}

	want := []string{
		"investigations_started_total",
		"investigations_resolved_total",
		"tool_executions_total",
		"injection_defangs_total",
		"claude_api_calls_total",
		"claude_api_tokens_total",
		"k8s_queries_total",
		"loki_queries_total",
		"investigation_duration_seconds",
		"investigations_in_flight",
		"conversations_active",
	}

	for _, name := range want {
		if !got[name] {
			t.Errorf("expected metric %q to be exported; gathered: %v", name, got)
		}
	}
}
