package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestInitProducesRegistryAndShutsDown(t *testing.T) {
	// Not parallel: Init mutates global OTel providers.
	logger := slog.New(slog.DiscardHandler)

	providers, err := Init(context.Background(), "diagnostic-bot-test", "v-test", logger)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	if providers.Registry == nil {
		t.Fatal("expected a non-nil Prometheus registry")
	}

	if providers.TracingActive() {
		t.Error("tracing should be inactive when no OTLP endpoint is configured")
	}

	shutdownErr := providers.Shutdown(context.Background())
	if shutdownErr != nil {
		t.Errorf("Shutdown returned error: %v", shutdownErr)
	}
}

func TestSlogHandlerInjectsTraceAndSpanIDs(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	handler := NewSlogHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(handler)

	traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	logger.InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, "0123456789abcdef0123456789abcdef") {
		t.Errorf("log output missing trace_id: %s", out)
	}

	if !strings.Contains(out, `"span_id"`) {
		t.Errorf("log output missing span_id field: %s", out)
	}
}

func TestSlogHandlerNoTraceWhenNoSpan(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(NewSlogHandler(slog.NewJSONHandler(&buf, nil)))

	logger.Info("no span here")

	var record map[string]any
	err := json.Unmarshal(buf.Bytes(), &record)
	if err != nil {
		t.Fatalf("log line was not valid JSON: %v", err)
	}

	if _, ok := record["trace_id"]; ok {
		t.Error("trace_id should be absent without an active span")
	}
}
