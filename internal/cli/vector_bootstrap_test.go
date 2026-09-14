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

	"aleutian-ai/ragctl/internal/config"
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
	cfg.Vector.Endpoint = backend.URL

	if err := ensureManagedQdrant(context.Background(), cfg, runtime, func(string, ...any) {}); err != nil {
		t.Fatalf("ensureManagedQdrant: %v", err)
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	log := string(calls)
	if !strings.Contains(log, "ps -a --filter name=^ragctl-qdrant$") {
		t.Errorf("call log = %q, want an existence check before run", log)
	}
	if !strings.Contains(log, "run -d --name ragctl-qdrant") {
		t.Errorf("call log = %q, want a run invocation for an absent container", log)
	}
	if !strings.Contains(log, "-p 127.0.0.1:6333:6333") {
		t.Errorf("call log = %q, want the port bound to loopback only", log)
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

	runtime, logPath := fakeRuntimeScript(t, "ragctl-qdrant\tExited")
	cfg := config.Default(t.TempDir())
	cfg.Vector.Endpoint = backend.URL

	if err := ensureManagedQdrant(context.Background(), cfg, runtime, func(string, ...any) {}); err != nil {
		t.Fatalf("ensureManagedQdrant: %v", err)
	}

	log := readFileString(t, logPath)
	if !strings.Contains(log, "start ragctl-qdrant") {
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

	runtime, logPath := fakeRuntimeScript(t, "ragctl-qdrant\tRunning")
	cfg := config.Default(t.TempDir())
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
	cfg.Vector.Endpoint = dead

	err := ensureManagedQdrant(context.Background(), cfg, runtime, func(string, ...any) {})
	if err == nil {
		t.Fatal("ensureManagedQdrant succeeded despite the backend never becoming healthy")
	}
	if !strings.Contains(err.Error(), "did not become healthy") {
		t.Errorf("error = %v, want it to explain the health-probe timeout, not hang forever", err)
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
