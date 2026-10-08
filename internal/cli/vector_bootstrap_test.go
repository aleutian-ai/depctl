package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aleutian-ai/depctl/internal/config"
)

func TestContainerRuntimePrefersPodman(t *testing.T) {
	defer func(prev func(string) (string, error)) { execLookPath = prev }(execLookPath)
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	if got := containerRuntime(); got != "/usr/bin/podman" {
		t.Errorf("containerRuntime() = %q, want podman preferred over docker", got)
	}
}

func TestContainerRuntimeFallsBackToDocker(t *testing.T) {
	defer func(prev func(string) (string, error)) { execLookPath = prev }(execLookPath)
	execLookPath = func(file string) (string, error) {
		if file == "podman" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	if got := containerRuntime(); got != "/usr/bin/docker" {
		t.Errorf("containerRuntime() = %q, want docker when podman is absent", got)
	}
}

func TestContainerRuntimeEmptyWhenNeitherFound(t *testing.T) {
	defer func(prev func(string) (string, error)) { execLookPath = prev }(execLookPath)
	execLookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	if got := containerRuntime(); got != "" {
		t.Errorf("containerRuntime() = %q, want empty when neither runtime is on PATH", got)
	}
}

// fakeRuntimeScript writes an executable shell script standing in for
// podman/docker: it appends every invocation's arguments to a log file
// (one line per call), and answers `ps -a --filter ...` with psOutput —
// letting a test control whether ensureManagedQdrant sees the container
// as absent, stopped, or running. `run`/`start` calls just exit 0.
func fakeRuntimeScript(t *testing.T, psOutput string) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	path = filepath.Join(dir, "fake-runtime")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"case \"$1\" in\n" +
		"  ps) echo '" + psOutput + "' ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake runtime script: %v", err)
	}
	return path, logPath
}

func TestEnsureManagedQdrantRunsExpectedArgsWhenAbsent(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	runtime, logPath := fakeRuntimeScript(t, "") // ps reports nothing: container absent
	cfg := config.Default(t.TempDir())
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = backend.URL

	if err := ensureManagedQdrant(context.Background(), cfg, runtime, func(string, ...any) {}); err != nil {
		t.Fatalf("ensureManagedQdrant: %v", err)
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	log := string(calls)
	if !strings.Contains(log, "ps -a --filter name=^depctl-qdrant$") {
		t.Errorf("call log = %q, want an existence check before run", log)
	}
	if !strings.Contains(log, "run -d --name depctl-qdrant") {
		t.Errorf("call log = %q, want a run invocation for an absent container", log)
	}
	if !strings.Contains(log, "-p 127.0.0.1:6333:6333") {
		t.Errorf("call log = %q, want the port bound to loopback only", log)
	}
	if !strings.Contains(log, "-v depctl-qdrant-data:/qdrant/storage") {
		t.Errorf("call log = %q, want storage in a named volume, not a host bind-mount (live-found: a Podman machine with no configured host mounts fails every bind-mount)", log)
	}
	if strings.Contains(log, "\nstart ") {
		t.Errorf("call log = %q, should not call start for an absent container", log)
	}
}

func TestEnsureManagedQdrantRestartsStoppedContainerInsteadOfRun(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	runtime, logPath := fakeRuntimeScript(t, "depctl-qdrant\tExited")
	cfg := config.Default(t.TempDir())
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = backend.URL

	if err := ensureManagedQdrant(context.Background(), cfg, runtime, func(string, ...any) {}); err != nil {
		t.Fatalf("ensureManagedQdrant: %v", err)
	}

	log := readFileString(t, logPath)
	if !strings.Contains(log, "start depctl-qdrant") {
		t.Errorf("call log = %q, want a start invocation for a stopped container", log)
	}
	if strings.Contains(log, "\nrun ") {
		t.Errorf("call log = %q, should not call run for an already-existing container", log)
	}
}

func TestEnsureManagedQdrantSkipsBothWhenAlreadyRunning(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	runtime, logPath := fakeRuntimeScript(t, "depctl-qdrant\tRunning")
	cfg := config.Default(t.TempDir())
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = backend.URL

	if err := ensureManagedQdrant(context.Background(), cfg, runtime, func(string, ...any) {}); err != nil {
		t.Fatalf("ensureManagedQdrant: %v", err)
	}

	log := readFileString(t, logPath)
	if strings.Contains(log, "\nrun ") || strings.Contains(log, "\nstart ") {
		t.Errorf("call log = %q, should call neither run nor start for an already-running container", log)
	}
}

func TestEnsureManagedQdrantHealthTimeoutSurfacesError(t *testing.T) {
	defer func(prev time.Duration) { qdrantStartupTimeout = prev }(qdrantStartupTimeout)
	qdrantStartupTimeout = 500 * time.Millisecond

	dead := deadBackendURL(t)
	runtime, _ := fakeRuntimeScript(t, "")
	cfg := config.Default(t.TempDir())
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = dead

	err := ensureManagedQdrant(context.Background(), cfg, runtime, func(string, ...any) {})
	if err == nil {
		t.Fatal("ensureManagedQdrant succeeded despite the backend never becoming healthy")
	}
	if !strings.Contains(err.Error(), "did not become healthy") {
		t.Errorf("error = %v, want it to explain the health-probe timeout, not hang forever", err)
	}
}

// TestEnsureManagedQdrantPullUsesLongerTimeout is the other live-found
// regression: a genuinely first-time `run --pull=missing` (image not
// yet local) needs its own, longer timeout than qdrantStartupTimeout —
// sharing one 20s budget between "pull a ~100-200MB image" and "wait
// for an already-local container to report healthy" failed a real
// first bootstrap with "context deadline exceeded" mid-pull. Simulated
// here via a fake runtime whose `run` takes longer than
// qdrantStartupTimeout but less than qdrantPullTimeout.
func TestEnsureManagedQdrantPullUsesLongerTimeout(t *testing.T) {
	defer func(prev time.Duration) { qdrantStartupTimeout = prev }(qdrantStartupTimeout)
	qdrantStartupTimeout = 50 * time.Millisecond

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "fake-runtime")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  ps) exit 0 ;;\n" +
		// Longer than qdrantStartupTimeout, shorter than qdrantPullTimeout —
		// proves `run` isn't bounded by the short one.
		"  run) sleep 0.3 ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake runtime script: %v", err)
	}

	cfg := config.Default(t.TempDir())
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = backend.URL

	if err := ensureManagedQdrant(context.Background(), cfg, path, func(string, ...any) {}); err != nil {
		t.Fatalf("ensureManagedQdrant: %v, want run's slow pull to survive the short qdrantStartupTimeout", err)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
