// Package metrics exposes OBS-003's Prometheus counters/gauges/
// histograms and the optional /metrics HTTP endpoint that serves them —
// built directly on github.com/prometheus/client_golang's default
// registry, per this ticket's own simplicity constraint (no custom
// metrics abstraction). Every metric is a package-level var, registered
// once at package init via promauto, exactly the way client_golang's own
// docs recommend; call sites elsewhere in the codebase just call
// Inc/Observe/Set on the metric they care about.
package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/aleutian-ai/depctl/internal/config"
)

// The nine metrics this ticket names, with the exact names from the
// design spec. SyncJobsTotal is the one vector (labeled by action type
// and outcome state); everything else is a single time series.
var (
	SyncJobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "depctl_sync_jobs_total",
		Help: "Sync actions processed, by action type and outcome state.",
	}, []string{"type", "state"})

	SyncFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "depctl_sync_failures_total",
		Help: "Sync actions that failed, across all types.",
	})

	AcquireSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "depctl_acquire_seconds",
		Help: "Time spent acquiring a generation's source content.",
	})

	NormalizeSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "depctl_normalize_seconds",
		Help: "Time spent normalizing a generation's acquired content.",
	})

	EmbedSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "depctl_embed_seconds",
		Help: "Time spent embedding one batch of chunks.",
	})

	BackendUpsertSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "depctl_backend_upsert_seconds",
		Help: "Time spent upserting one batch of points into the vector backend.",
	})

	ActiveGenerations = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "depctl_active_generations",
		Help: "Number of currently-active generations for the configured backend.",
	})

	GCCandidates = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "depctl_gc_candidates",
		Help: "Number of GC candidates found by the most recent GC planning pass.",
	})

	BadgerBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "depctl_badger_bytes",
		Help: "On-disk size of the Badger data store, in bytes.",
	})
)

// StartServer binds and serves /metrics from cfg, returning a shutdown
// func the caller must invoke on process exit. When cfg.Enabled is false
// (the default), it does nothing and returns a no-op shutdown — no
// listener is ever created. Per this ticket's own failure-behavior
// requirement, a bind failure is fatal only when metrics are explicitly
// enabled (returned as an error here); when disabled, there is nothing to
// fail.
func StartServer(cfg config.MetricsConfig, logger *slog.Logger) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if !cfg.Enabled {
		return noop, nil
	}

	addr := cfg.ListenOrDefault()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return noop, fmt.Errorf("metrics: bind %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Handler: mux}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			if logger != nil {
				logger.Warn("metrics: server error", "error", err)
			}
		}
	}()

	return srv.Shutdown, nil
}
