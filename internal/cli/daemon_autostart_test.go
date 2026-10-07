package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon/client"
)

// pristineEnv is the environment as it was before any test called
// t.Setenv (isolateEnv points HOME and the Go caches elsewhere), so the
// ragctl binary can be built against the real module cache.
var pristineEnv = os.Environ()

var (
	ragctlBinOnce sync.Once
	ragctlBinPath string
	ragctlBinErr  error
)

// requireRagctlBinary builds cmd/ragctl once per test binary and returns
// its path. Auto-start tests need a real executable: under `go test`,
// os.Executable() is the test binary itself.
func requireRagctlBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	ragctlBinOnce.Do(func() {
		var dir string
		if dir, ragctlBinErr = os.MkdirTemp("", "ragctl-bin-"); ragctlBinErr != nil {
			return
		}
		ragctlBinPath = filepath.Join(dir, "ragctl")
		cmd := exec.Command("go", "build", "-o", ragctlBinPath, "aleutian-ai/ragctl/cmd/ragctl")
		cmd.Env = pristineEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			ragctlBinErr = errors.New(string(out))
		}
	})
	if ragctlBinErr != nil {
		t.Fatalf("build ragctl: %v", ragctlBinErr)
	}
	return ragctlBinPath
}

// useRealRagctlBinary makes auto-start spawn the built binary, and stops
// whatever daemon the test left running.
func useRealRagctlBinary(t *testing.T) {
	t.Helper()
	bin := requireRagctlBinary(t)
	previous := daemonExecutable
	daemonExecutable = bin
	t.Cleanup(func() {
		stopRunningDaemon(t)
		daemonExecutable = previous
	})
}

// stopRunningDaemon shuts down an auto-started daemon if one is up, so
// it doesn't outlive the test holding the temp data dir open.
func stopRunningDaemon(t *testing.T) {
	t.Helper()
	socket, err := socketPath()
	if err != nil {
		return
	}
	c, err := client.Dial(context.Background(), socket)
	if err != nil {
		return
	}
	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown auto-started daemon: %v", err)
		return
	}
	waitFor(t, "the auto-started daemon to exit", func() bool {
		_, err := client.Dial(context.Background(), socket)
		return errors.Is(err, client.ErrNotRunning)
	})
	// The daemon closes its socket before it closes its stores, so a test
	// that opens control.db right away can still lose the lock to it on a
	// slow machine (seen on CI-like 2-CPU runs under -race).
	path, err := controlDBPath()
	if err != nil {
		return
	}
	waitFor(t, "the auto-started daemon to release control.db", func() bool {
		store, err := bboltstore.OpenWithTimeout(path, 50*time.Millisecond)
		if err != nil {
			return false
		}
		store.Close()
		return true
	})
}

func TestEnsureDaemonAutostartsWhenAbsent(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	c, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	health, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.PID == os.Getpid() {
		t.Error("auto-start ran the daemon in this process; it must be a separate, detached one")
	}
	if !strings.Contains(runCommandOutput(t, "daemon", "status"), "running (pid") {
		t.Error("daemon status did not report the auto-started daemon")
	}
}

func TestEnsureDaemonReusesRunningDaemon(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	first, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("first ensureDaemon: %v", err)
	}
	firstHealth, err := first.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}

	second, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("second ensureDaemon: %v", err)
	}
	secondHealth, err := second.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if firstHealth.PID != secondHealth.PID {
		t.Errorf("second call started another daemon (pids %d and %d)", firstHealth.PID, secondHealth.PID)
	}
}

func TestConcurrentAutostartElectsOneDaemon(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	const clients = 3
	pids := make([]int, clients)
	errs := make([]error, clients)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := ensureDaemon(context.Background())
			if err != nil {
				errs[i] = err
				return
			}
			h, err := c.Health(context.Background())
			errs[i] = err
			pids[i] = h.PID
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}
	for i, pid := range pids {
		if pid != pids[0] {
			t.Fatalf("clients connected to different daemons: %v (client %d)", pids, i)
		}
	}

	// Every daemon that lost the race must say so plainly, not report a
	// store error.
	logPath, err := daemonLogPath()
	if err != nil {
		t.Fatalf("daemonLogPath: %v", err)
	}
	if data, err := os.ReadFile(logPath); err == nil {
		text := string(data)
		if strings.Contains(text, "no daemon answers") {
			t.Errorf("a losing daemon reported an ownership failure instead of 'already running':\n%s", text)
		}
	}
}

func TestEnsureDaemonRespectsAutostartDisabled(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	disabled := false
	writeTestConfig(t, func(c *config.Config) { c.Daemon.Autostart = &disabled })

	_, err := ensureDaemon(context.Background())
	if !errors.Is(err, client.ErrNotRunning) {
		t.Fatalf("ensureDaemon with autostart disabled = %v, want ErrNotRunning", err)
	}
	if !strings.Contains(err.Error(), "ragctl daemon run") {
		t.Errorf("error = %q, want it to name the command that starts a daemon", err)
	}
	logPath, err := daemonLogPath()
	if err != nil {
		t.Fatalf("daemonLogPath: %v", err)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("a daemon was spawned even though autostart is disabled")
	}
}

// TestEnsureDaemonAutoInitializesBeforeSpawning is the regression test
// for the MCP bootstrapping gap: an agent session that's the very first
// thing to ever touch this machine's ragctl install must not need a
// human to run `ragctl init` in a terminal first — see
// docs/scratch/mcp-bootstrapping.md.
func TestEnsureDaemonAutoInitializesBeforeSpawning(t *testing.T) {
	isolateEnv(t)
	writeTestConfig(t, func(*config.Config) {})
	useRealRagctlBinary(t)

	controlPath, err := controlDBPath()
	if err != nil {
		t.Fatalf("controlDBPath: %v", err)
	}
	if _, err := os.Stat(controlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control.db already exists before ensureDaemon ran: %v", err)
	}

	c, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("ensureDaemon with no prior `ragctl init` = %v, want it to auto-initialize and succeed", err)
	}
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	if _, err := os.Stat(controlPath); err != nil {
		t.Errorf("control.db still missing after ensureDaemon: %v", err)
	}
}

func TestEnsureDaemonReportsStartupFailure(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	// A "daemon" that fails immediately, standing in for any startup
	// error: the client must surface what it logged, not just time out.
	script := filepath.Join(t.TempDir(), "broken-daemon")
	body := "#!/bin/sh\necho 'startup failed: something is wrong' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	previous := daemonExecutable
	daemonExecutable = script
	t.Cleanup(func() { daemonExecutable = previous })

	_, err := ensureDaemon(context.Background())
	if err == nil {
		t.Fatal("ensureDaemon succeeded with a daemon that can't start")
	}
	if !strings.Contains(err.Error(), "did not start") || !strings.Contains(err.Error(), "something is wrong") {
		t.Errorf("error = %q, want the timeout plus the daemon's own log line", err)
	}
}
