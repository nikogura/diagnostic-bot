// Package metrics declares the service's OpenTelemetry instruments and the
// helper functions call sites use to record them. Instruments are created by
// Init (after the global meter provider is installed) and are no-op until then,
// so tests and tooling that never call Init record nothing rather than panic.
package metrics

import (
	"context"
	"errors"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Attribute key constants, shared so metric labels stay consistent.
const (
	InvestigationType = "type"
	Status            = "status"
	Namespace         = "namespace"
	ResourceType      = "resource_type"
	TokenType         = "token_type"
	ToolName          = "tool_name"
	Category          = "category"
	Site              = "site"
)

//nolint:gochecknoglobals // OTel instruments are process-wide singletons by design.
var (
	investigationsStarted  metric.Int64Counter
	investigationsResolved metric.Int64Counter
	claudeAPICalls         metric.Int64Counter
	claudeAPITokens        metric.Int64Counter
	k8sQueries             metric.Int64Counter
	lokiQueries            metric.Int64Counter
	toolExecutions         metric.Int64Counter
	injectionDefangs       metric.Int64Counter
	investigationDuration  metric.Float64Histogram
	investigationsInFlight metric.Int64UpDownCounter
	panicsRecovered        metric.Int64Counter

	// conversationsActiveValue backs the conversations_active observable gauge.
	conversationsActiveValue atomic.Int64
)

// Init creates every instrument against the supplied meter. It must be called
// once, after the global meter provider has been installed. Returns the joined
// error of any instrument that failed to register.
func Init(meter metric.Meter) (err error) {
	var errs []error

	investigationsStarted, err = meter.Int64Counter("investigations_started_total",
		metric.WithDescription("Total number of investigations started"))
	errs = append(errs, err)

	investigationsResolved, err = meter.Int64Counter("investigations_resolved_total",
		metric.WithDescription("Total number of investigations resolved"))
	errs = append(errs, err)

	claudeAPICalls, err = meter.Int64Counter("claude_api_calls_total",
		metric.WithDescription("Total number of Claude API calls"))
	errs = append(errs, err)

	claudeAPITokens, err = meter.Int64Counter("claude_api_tokens_total",
		metric.WithDescription("Total number of tokens used by the Claude API"))
	errs = append(errs, err)

	k8sQueries, err = meter.Int64Counter("k8s_queries_total",
		metric.WithDescription("Total number of Kubernetes queries"))
	errs = append(errs, err)

	lokiQueries, err = meter.Int64Counter("loki_queries_total",
		metric.WithDescription("Total number of Loki queries"))
	errs = append(errs, err)

	toolExecutions, err = meter.Int64Counter("tool_executions_total",
		metric.WithDescription("Total number of tool executions"))
	errs = append(errs, err)

	injectionDefangs, err = meter.Int64Counter("injection_defangs_total",
		metric.WithDescription("Total forged control sequences defanged from untrusted tool output, by category"))
	errs = append(errs, err)

	investigationDuration, err = meter.Float64Histogram("investigation_duration_seconds",
		metric.WithDescription("End-to-end investigation duration in seconds"))
	errs = append(errs, err)

	investigationsInFlight, err = meter.Int64UpDownCounter("investigations_in_flight",
		metric.WithDescription("Number of investigations currently executing"))
	errs = append(errs, err)

	panicsRecovered, err = meter.Int64Counter("panics_recovered_total",
		metric.WithDescription("Total panics recovered in spawned goroutines, by site (self-heal; should stay flat at zero)"))
	errs = append(errs, err)

	err = registerConversationsActive(meter)
	errs = append(errs, err)

	err = errors.Join(errs...)
	return err
}

// registerConversationsActive registers the observable gauge that reports the
// current active-conversation count from an atomic backing value.
func registerConversationsActive(meter metric.Meter) (err error) {
	_, err = meter.Int64ObservableGauge("conversations_active",
		metric.WithDescription("Current number of active conversations"),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) (cbErr error) {
			observer.Observe(conversationsActiveValue.Load())
			return cbErr
		}),
	)

	return err
}

// RecordInvestigationStarted increments the investigation traffic counter.
func RecordInvestigationStarted(ctx context.Context, invType string) {
	if investigationsStarted == nil {
		return
	}

	investigationsStarted.Add(ctx, 1, metric.WithAttributes(attribute.String(InvestigationType, invType)))
}

// RecordInvestigationResolved increments the resolved-investigation counter.
func RecordInvestigationResolved(ctx context.Context, invType string) {
	if investigationsResolved == nil {
		return
	}

	investigationsResolved.Add(ctx, 1, metric.WithAttributes(attribute.String(InvestigationType, invType)))
}

// SetConversationsActive updates the active-conversation gauge value.
func SetConversationsActive(count int64) {
	conversationsActiveValue.Store(count)
}

// GetConversationsActive returns the value backing the conversations_active
// gauge. Exposed so callers and tests can read back the currently reported count.
func GetConversationsActive() (count int64) {
	count = conversationsActiveValue.Load()
	return count
}

// RecordPanicRecovered increments the recovered-panic counter for a goroutine
// site. A non-zero value means a panic was contained instead of crashing the
// process — it should alert, because panics are programmer errors, not control flow.
func RecordPanicRecovered(ctx context.Context, site string) {
	if panicsRecovered == nil {
		return
	}

	panicsRecovered.Add(ctx, 1, metric.WithAttributes(attribute.String(Site, site)))
}

// AddInvestigationInFlight adjusts the in-flight saturation gauge by delta.
func AddInvestigationInFlight(ctx context.Context, delta int64) {
	if investigationsInFlight == nil {
		return
	}

	investigationsInFlight.Add(ctx, delta)
}

// ObserveInvestigationDuration records investigation latency with its outcome.
func ObserveInvestigationDuration(ctx context.Context, invType string, status string, seconds float64) {
	if investigationDuration == nil {
		return
	}

	investigationDuration.Record(ctx, seconds, metric.WithAttributes(
		attribute.String(InvestigationType, invType),
		attribute.String(Status, status),
	))
}

// RecordToolExecution increments the tool-execution counter for a tool/status.
func RecordToolExecution(ctx context.Context, toolName string, status string) {
	if toolExecutions == nil {
		return
	}

	toolExecutions.Add(ctx, 1, metric.WithAttributes(
		attribute.String(ToolName, toolName),
		attribute.String(Status, status),
	))
}

// RecordInjectionDefang increments the defang counter for a pattern category.
func RecordInjectionDefang(ctx context.Context, category string) {
	if injectionDefangs == nil {
		return
	}

	injectionDefangs.Add(ctx, 1, metric.WithAttributes(attribute.String(Category, category)))
}

// RecordClaudeAPICall increments the Claude API call counter for a status.
func RecordClaudeAPICall(ctx context.Context, status string) {
	if claudeAPICalls == nil {
		return
	}

	claudeAPICalls.Add(ctx, 1, metric.WithAttributes(attribute.String(Status, status)))
}

// AddClaudeAPITokens adds to the Claude token counter for a token type.
func AddClaudeAPITokens(ctx context.Context, tokenType string, count int64) {
	if claudeAPITokens == nil {
		return
	}

	claudeAPITokens.Add(ctx, count, metric.WithAttributes(attribute.String(TokenType, tokenType)))
}

// RecordK8sQuery increments the Kubernetes query counter.
func RecordK8sQuery(ctx context.Context, namespace string, resourceType string) {
	if k8sQueries == nil {
		return
	}

	k8sQueries.Add(ctx, 1, metric.WithAttributes(
		attribute.String(Namespace, namespace),
		attribute.String(ResourceType, resourceType),
	))
}

// RecordLokiQuery increments the Loki query counter for a status.
func RecordLokiQuery(ctx context.Context, status string) {
	if lokiQueries == nil {
		return
	}

	lokiQueries.Add(ctx, 1, metric.WithAttributes(attribute.String(Status, status)))
}
