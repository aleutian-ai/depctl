package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon/client"
)

// daemonHarness is a `ragctl daemon run` running in this process.
type daemonHarness struct {
	out    *syncBuffer
	done   <-chan error
	socket string
	exited bool
}

// startDaemon runs `ragctl daemon run` in the background and waits until
// it is listening. It is stopped at test cleanup.
func startDaemon(t *testing.T) *daemonHarness {
	t.Helper()
	socket, err := socketPath()
	if err != nil {
		t.Fatalf("socketPath: %v", err)
	}

	out := &syncBuffer{}
	root := NewRootCmd()
	root.SetOut(out)
	root.SetArgs([]string{"daemon", "run"})
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	waitFor(t, "the daemon to listen", func() bool { return strings.Contains(out.String(), "listening on") })

	h := &daemonHarness{out: out, done: done, socket: socket}
	t.Cleanup(func() { h.stop(t) })
	return h
}

// stop shuts the daemon down through its own API and waits for exit.
// It is a no-op once the daemon has already exited, so a test may stop
// it itself and still rely on cleanup.
func (h *daemonHarness) stop(t *testing.T) {
	t.Helper()
	if h.exited {
		return
	}
	if err := client.New(h.socket).Shutdown(context.Background()); err != nil && !errors.Is(err, client.ErrNotRunning) {
		t.Errorf("shutdown: %v", err)
	}
	h.wait(t)
}

// wait blocks until the daemon process returns, failing the test if it
// errored or took too long.
func (h *daemonHarness) wait(t *testing.T) {
	t.Helper()
	if h.exited {
		return
	}
	select {
	case err := <-h.done:
		h.exited = true
		if err != nil {
			t.Errorf("daemon exited with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("daemon did not exit")
	}
}

func TestDaemonOwnsStoresWhileRunning(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	h := startDaemon(t)

	health, err := client.New(h.socket).Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.PID != os.Getpid() {
		t.Errorf("health.PID = %d, want this process (%d)", health.PID, os.Getpid())
	}
	if health.Version == "" || health.ControlPath == "" {
		t.Errorf("health = %+v, want version and control path populated", health)
	}

	controlPath, err := controlDBPath()
	if err != nil {
		t.Fatalf("controlDBPath: %v", err)
	}
	if _, err := bboltstore.Open(controlPath); !errors.Is(err, bboltstore.ErrLocked) {
		t.Fatalf("opening control.db while the daemon runs = %v, want ErrLocked", err)
	}

	info, err := os.Stat(h.socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %v, want 0600", perm)
	}
}

func TestSecondDaemonFailsCleanlyAndFirstKeepsServing(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	h := startDaemon(t)

	root := NewRootCmd()
	root.SetOut(new(syncBuffer))
	root.SetArgs([]string{"daemon", "run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon run = %v, want an 'already running' error", err)
	}

	if _, err := client.New(h.socket).Health(context.Background()); err != nil {
		t.Errorf("first daemon stopped answering after the second tried to start: %v", err)
	}
}

func TestDaemonRunReportsLockHolderThatIsNotADaemon(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	// Hold the lock without serving anything, as an old `ragctl serve`
	// would.
	holder, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer holder.Close()

	root := NewRootCmd()
	root.SetOut(new(syncBuffer))
	root.SetArgs([]string{"daemon", "run"})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no daemon answers") {
		t.Fatalf("daemon run with the store held elsewhere = %v, want a 'no daemon answers' error", err)
	}
}

func TestDaemonRecoversStaleSocket(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	socket, err := socketPath()
	if err != nil {
		t.Fatalf("socketPath: %v", err)
	}
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale socket: %v", err)
	}

	h := startDaemon(t)
	if _, err := client.New(h.socket).Health(context.Background()); err != nil {
		t.Fatalf("health after stale-socket recovery: %v", err)
	}
}

func TestDaemonStartsWithUnreachableServices(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	deadEndpointsConfig(t, nil)

	h := startDaemon(t)
	if _, err := client.New(h.socket).Health(context.Background()); err != nil {
		t.Fatalf("health with dead embedding/vector endpoints: %v", err)
	}
	st, err := client.New(h.socket).Status(context.Background())
	if err != nil {
		t.Fatalf("status with dead endpoints: %v", err)
	}
	if st.Backend.Healthy {
		t.Error("status reported a healthy backend, but its endpoint is dead")
	}

	// WATCH-014: the background embedding-readiness check should reach
	// "unreachable" on its own, off the request path — no client action
	// triggers it.
	waitFor(t, "embedding readiness to report unreachable", func() bool {
		health, err := client.New(h.socket).Health(context.Background())
		return err == nil && health.EmbeddingState == "unreachable"
	})
}

func TestDaemonStatusAndStop(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	h := startDaemon(t)

	status := runCommandOutput(t, "daemon", "status")
	if !strings.Contains(status, "running (pid") {
		t.Errorf("daemon status = %q, want it to report a running daemon", status)
	}

	stop := runCommandOutput(t, "daemon", "stop")
	if !strings.Contains(stop, "stopped") {
		t.Errorf("daemon stop = %q, want 'stopped'", stop)
	}
	h.wait(t)

	if _, err := os.Stat(h.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still present after stop: %v", err)
	}
	controlPath, err := controlDBPath()
	if err != nil {
		t.Fatalf("controlDBPath: %v", err)
	}
	store, err := bboltstore.Open(controlPath)
	if err != nil {
		t.Fatalf("control.db still locked after the daemon stopped: %v", err)
	}
	store.Close()
}

func TestDaemonStatusWithoutDaemon(t *testing.T) {
	isolateEnv(t)
	writeTestConfig(t, func(*config.Config) {})

	out := &syncBuffer{}
	root := NewRootCmd()
	root.SetOut(out)
	root.SetArgs([]string{"daemon", "status"})
	err := root.Execute()

	var exit ExitCodeError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("daemon status with no daemon = %v, want ExitCodeError{1}", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Errorf("output = %q, want 'not running'", out.String())
	}
}

// runCommandOutput runs one ragctl command and returns its stdout,
// failing the test if it errors.
func runCommandOutput(t *testing.T, args ...string) string {
	t.Helper()
	out := &syncBuffer{}
	root := NewRootCmd()
	root.SetOut(out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestInitRefusesWhileDaemonRuns(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	startDaemon(t)

	root := NewRootCmd()
	root.SetOut(new(syncBuffer))
	root.SetArgs([]string{"init"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "daemon is running") {
		t.Fatalf("init while the daemon runs = %v, want a refusal naming the daemon", err)
	}
}
