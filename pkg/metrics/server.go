package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HealthChecker provides health status information for probes.
type HealthChecker interface {
	IsSocketConnected() bool
	LastEvent() time.Time
}

// Server provides HTTP endpoints for health checks and metrics.
type Server struct {
	Address       string
	Logger        *slog.Logger
	gatherer      prometheus.Gatherer
	healthChecker HealthChecker
}

// NewServer creates a new metrics server. The gatherer is the registry the OTel
// Prometheus exporter writes to; pass nil to fall back to the default registry.
func NewServer(address string, gatherer prometheus.Gatherer, logger *slog.Logger) (result *Server) {
	if gatherer == nil {
		gatherer = prometheus.DefaultGatherer
	}

	result = &Server{
		Address:  address,
		Logger:   logger,
		gatherer: gatherer,
	}

	return result
}

// SetHealthChecker sets the health checker used by liveness and readiness probes.
func (s *Server) SetHealthChecker(checker HealthChecker) {
	s.healthChecker = checker
}

// Start starts the metrics server.
func (s *Server) Start(ctx context.Context) (err error) {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.LivenessHandler)
	mux.HandleFunc("/readyz", s.ReadinessHandler)
	mux.HandleFunc("/health", s.DetailedHealthHandler)
	mux.Handle("/metrics", promhttp.HandlerFor(s.gatherer, promhttp.HandlerOpts{}))

	server := &http.Server{
		Addr:    s.Address,
		Handler: mux,
	}

	// Start server in goroutine
	go func() {
		s.Logger.InfoContext(ctx, "starting metrics server", slog.String("address", s.Address))

		startErr := server.ListenAndServe()
		if startErr != nil && startErr != http.ErrServerClosed {
			s.Logger.ErrorContext(ctx, "metrics server error", slog.String("error", startErr.Error()))
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	s.Logger.InfoContext(ctx, "shutting down metrics server")

	err = server.Shutdown(context.Background())
	if err != nil {
		err = fmt.Errorf("shutting down metrics server: %w", err)
		return err
	}

	return err
}

// LivenessHandler handles liveness probe requests.
// Returns unhealthy if the Slack socket connection has been dead for too long.
func (s *Server) LivenessHandler(w http.ResponseWriter, _ *http.Request) {
	if s.healthChecker == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	connected := s.healthChecker.IsSocketConnected()
	lastEvent := s.healthChecker.LastEvent()

	// If socket disconnected and last event was more than 5 minutes ago, fail liveness.
	// This gives the bot time for initial connection and brief reconnects.
	if !connected && !lastEvent.IsZero() {
		staleDuration := time.Since(lastEvent)
		if staleDuration > 5*time.Minute {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "unhealthy: socket disconnected for %s", staleDuration.Truncate(time.Second))
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// ReadinessHandler handles readiness probe requests.
// Returns not-ready if the Slack socket is not connected.
func (s *Server) ReadinessHandler(w http.ResponseWriter, _ *http.Request) {
	if s.healthChecker == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
		return
	}

	if !s.healthChecker.IsSocketConnected() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready: slack socket not connected"))
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// DetailedHealthHandler returns full health status as JSON.
func (s *Server) DetailedHealthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.healthChecker == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "detail": "no health checker configured"})
		return
	}

	connected := s.healthChecker.IsSocketConnected()

	overallStatus := "healthy"
	if !connected {
		overallStatus = "degraded"
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	response := map[string]interface{}{
		"status":           overallStatus,
		"socket_connected": connected,
		"last_event_time":  s.healthChecker.LastEvent(),
	}

	_ = json.NewEncoder(w).Encode(response)
}
