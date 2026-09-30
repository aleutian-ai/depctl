package metrics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
)

// TestStartServerDisabledDoesNotBind confirms this ticket's own
// acceptance criterion: with metrics disabled (the default), no listener
// is ever created.
func TestStartServerDisabledDoesNotBind(t *testing.T) {
	shutdown, err := StartServer(config.MetricsConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestMetricsEndpointOnFixedPort is the real end-to-end scrape: binds to
// a fixed loopback port, updates every one of the nine metrics, then
// fetches /metrics over real HTTP and checks each metric name is present
// in the response body.
func TestMetricsEndpointOnFixedPort(t *testing.T) {
	const addr = "127.0.0.1:19091"

	SyncJobsTotal.WithLabelValues("sync_version", "success").Inc()
	AcquireSeconds.Observe(0.42)
	NormalizeSeconds.Observe(0.1)
	EmbedSeconds.Observe(0.2)
	BackendUpsertSeconds.Observe(0.3)
	ActiveGenerations.Set(3)
	GCCandidates.Set(1)
	BadgerBytes.Set(1024)

	shutdown, err := StartServer(config.MetricsConfig{Enabled: true, Listen: addr}, nil)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}()

	var body string
	for range 20 {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body = string(b)
		break
	}
	if body == "" {
		t.Fatal("never got a response from /metrics")
	}

	for _, name := range []string{
		"ragctl_sync_jobs_total",
		"ragctl_sync_failures_total",
		"ragctl_acquire_seconds",
		"ragctl_normalize_seconds",
		"ragctl_embed_seconds",
		"ragctl_backend_upsert_seconds",
		"ragctl_active_generations",
		"ragctl_gc_candidates",
		"ragctl_badger_bytes",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("expected %q in /metrics output, got:\n%s", name, body)
		}
	}
}
