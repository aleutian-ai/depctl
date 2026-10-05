package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
)

func TestVectorReadinessCheckReadyNilIsAlwaysReady(t *testing.T) {
	var r *vectorReadiness
	if err := r.checkReady(); err != nil {
		t.Errorf("nil readiness checkReady() = %v, want nil", err)
	}
}

func TestVectorReadinessCheckReadyPerState(t *testing.T) {
	cases := []struct {
		state       vectorState
		wantErr     bool
		wantContain string
	}{
		{vectorStateUnknown, false, ""},
		{vectorStateReady, false, ""},
		{vectorStateChecking, true, "checking"},
		{vectorStateStarting, true, "starting"},
		{vectorStateUnreachable, true, "unreachable"},
		{vectorStateError, true, "not ready"},
	}
	for _, c := range cases {
		r := newVectorReadiness()
		r.set(c.state, "detail-text")
		err := r.checkReady()
		if c.wantErr && err == nil {
			t.Errorf("state %s: checkReady() = nil, want an error", c.state)
			continue
		}
		if !c.wantErr && err != nil {
			t.Errorf("state %s: checkReady() = %v, want nil", c.state, err)
			continue
		}
		if c.wantErr && !strings.Contains(err.Error(), c.wantContain) {
			t.Errorf("state %s: checkReady() = %q, want it to mention %q", c.state, err, c.wantContain)
		}
	}
}

func TestCheckVectorReadinessUnreachable(t *testing.T) {
	cfg := config.Default("/data")
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = "http://127.0.0.1:1" // nothing listens here
	cfg.Vector.Managed = false                 // exercise the plain report-only path, not WATCH-016's bootstrap
	readiness := newVectorReadiness()

	checkVectorReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, detail := readiness.get()
	if state != vectorStateUnreachable {
		t.Errorf("state = %s, want unreachable", state)
	}
	if detail == "" {
		t.Error("detail is empty, want it to name the endpoint")
	}
}

func TestCheckVectorReadinessBootstrapsManagedContainerWhenRuntimeFound(t *testing.T) {
	defer func(prev time.Duration) { qdrantStartupTimeout = prev }(qdrantStartupTimeout)
	qdrantStartupTimeout = 300 * time.Millisecond

	runtime, logPath := fakeRuntimeScript(t, "") // ps reports nothing: container absent
	defer func(prev func(string) (string, error)) { execLookPath = prev }(execLookPath)
	execLookPath = func(file string) (string, error) {
		if file == "podman" {
			return runtime, nil
		}
		return "", fmt.Errorf("not found")
	}

	cfg := config.Default(t.TempDir())
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = deadBackendURL(t) // never becomes healthy — proves the bootstrap path ran, not that it fully succeeded
	cfg.Vector.Managed = true

	readiness := newVectorReadiness()
	checkVectorReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, detail := readiness.get()
	if state != vectorStateError {
		t.Fatalf("state = %s, want error (bootstrap attempted, container never became healthy)", state)
	}
	if !strings.Contains(detail, "did not become healthy") {
		t.Errorf("detail = %q, want it to explain the health-probe timeout, proving ensureManagedQdrant actually ran", detail)
	}
	if !strings.Contains(readFileString(t, logPath), "run -d --name ragctl-qdrant") {
		t.Error("fake runtime was never invoked with a run command — managed bootstrap branch was not taken")
	}
}

func TestCheckVectorReadinessReportsPlainUnreachableWhenNotManaged(t *testing.T) {
	runtime, logPath := fakeRuntimeScript(t, "")
	defer func(prev func(string) (string, error)) { execLookPath = prev }(execLookPath)
	execLookPath = func(file string) (string, error) {
		if file == "podman" {
			return runtime, nil
		}
		return "", fmt.Errorf("not found")
	}

	cfg := config.Default(t.TempDir())
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = deadBackendURL(t)
	cfg.Vector.Managed = false

	readiness := newVectorReadiness()
	checkVectorReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, _ := readiness.get()
	if state != vectorStateUnreachable {
		t.Errorf("state = %s, want unreachable — vector.managed is false, so no bootstrap should be attempted", state)
	}
	if data, _ := os.ReadFile(logPath); len(data) != 0 {
		t.Errorf("fake runtime was invoked (%q) despite vector.managed being false", data)
	}
}

func TestCheckVectorReadinessReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Default("/data")
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = srv.URL
	readiness := newVectorReadiness()

	checkVectorReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, _ := readiness.get()
	if state != vectorStateReady {
		t.Errorf("state = %s, want ready", state)
	}
}
