//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
)

func TestDaemonExitsCleanlyOnSIGTERM(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	h := startDaemon(t)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	h.wait(t)

	if !strings.Contains(h.out.String(), "stopped") {
		t.Errorf("output = %q, want a final 'stopped' line", h.out.String())
	}
	if _, err := os.Stat(h.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still present after SIGTERM: %v", err)
	}

	controlPath, err := controlDBPath()
	if err != nil {
		t.Fatalf("controlDBPath: %v", err)
	}
	store, err := bboltstore.Open(controlPath)
	if err != nil {
		t.Fatalf("control.db still locked after SIGTERM: %v", err)
	}
	store.Close()
}

// TestProcessAliveDistinguishesLiveFromReaped is OPS-006's regression
// coverage: a spawned-then-waited-on child's PID must read as not-alive
// (the exact liveness question stopOutcomeMessage below is built on),
// while this test's own process must read as alive.
func TestProcessAliveDistinguishesLiveFromReaped(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(own pid) = false, want true")
	}

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run a short-lived child: %v", err)
	}
	deadPID := cmd.Process.Pid
	if processAlive(deadPID) {
		t.Errorf("processAlive(%d) = true for an already-reaped child, want false", deadPID)
	}

	if processAlive(0) || processAlive(-1) {
		t.Error("processAlive(0 or -1) = true, want false")
	}
}

// TestStopOutcomeMessage is OPS-006's regression coverage for the real
// bug STRESS-017 found live: `ragctl daemon stop` used to declare
// "stopped" the moment the daemon's *socket* went away, even though the
// process itself (still finishing an in-flight sync/GC, bounded only by
// its own 30-minute maxActionDuration, decoupled from the daemon's own
// shutdown signal by design) could still be alive for a long time
// afterward.
func TestStopOutcomeMessage(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run a short-lived child: %v", err)
	}
	deadPID := cmd.Process.Pid

	if got := stopOutcomeMessage(0); got != "stopped" {
		t.Errorf("stopOutcomeMessage(0) = %q, want %q (no Health/PID available degrades to the old behavior)", got, "stopped")
	}
	if got := stopOutcomeMessage(deadPID); got != "stopped" {
		t.Errorf("stopOutcomeMessage(reaped pid) = %q, want %q", got, "stopped")
	}

	live := os.Getpid()
	got := stopOutcomeMessage(live)
	if got == "stopped" {
		t.Fatalf("stopOutcomeMessage(live pid %d) = %q, want the still-finishing message, not a false \"stopped\"", live, got)
	}
	if !strings.Contains(got, strconv.Itoa(live)) || !strings.Contains(got, "ps -p") {
		t.Errorf("stopOutcomeMessage(live pid) = %q, want it to name the pid and point at `ps -p`", got)
	}
}

// TestDaemonStopReportsStoppedForARealSeparateProcess is OPS-006's
// end-to-end happy-path coverage: `ragctl daemon stop` against a real,
// separate daemon OS process (not the in-process test harness
// TestDaemonStatusAndStop uses, which shares its own PID with the
// simulated "daemon" and can never observe a real process exit) — the
// PID-liveness check this ticket added must still report a plain
// "stopped" once the real process has actually exited, not just once
// its socket goes away.
//
// The reap goroutine below matters: spawnDaemon's cmd.Process.Release()
// only tells *Go* to stop tracking the child — the kernel-level
// parent/child relationship is unaffected, and this test's own `go
// test` process is that real parent. In production that's harmless
// (the short-lived CLI process that calls spawnDaemon exits within
// milliseconds, so the kernel re-parents the detached daemon to init,
// which reaps it immediately); in a long-lived test binary, with
// nothing to reap it, an exited child sits as a zombie until the whole
// test binary exits — and a zombie still answers a signal-0 liveness
// probe as "alive". Reaping it here, the way a real short-lived parent
// would, is what makes processAlive observe reality instead of that
// test-harness-only artifact — confirmed live: without this reap, the
// exact same daemon (whose own log shows it logged "stopped" and
// returned in well under a second) was still observed "alive" as a
// `ps`-confirmed zombie (STAT "ZN <defunct>") more than a minute later.
func TestDaemonStopReportsStoppedForARealSeparateProcess(t *testing.T) {
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
		t.Fatal("daemon is running in this process; the PID-liveness check below would be meaningless")
	}

	reaped := make(chan struct{})
	go func() {
		var status syscall.WaitStatus
		syscall.Wait4(health.PID, &status, 0, nil)
		close(reaped)
	}()

	stop := runCommandOutput(t, "daemon", "stop")
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon process was never reaped — can't confirm processAlive reflects reality")
	}

	if strings.TrimSpace(stop) != "stopped" {
		t.Errorf("daemon stop = %q, want exactly %q for a real process with nothing in flight", stop, "stopped")
	}
	if processAlive(health.PID) {
		t.Errorf("pid %d still alive after daemon stop reported \"stopped\"", health.PID)
	}
}
