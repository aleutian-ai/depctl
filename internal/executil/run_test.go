package executil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesStdout(t *testing.T) {
	result, err := Run(context.Background(), RunOptions{Args: []string{"echo", "hello"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(string(result.Stdout)); got != "hello" {
		t.Errorf("stdout = %q, want %q", got, "hello")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestRunNonZeroExitIsNotError(t *testing.T) {
	result, err := Run(context.Background(), RunOptions{Args: []string{"sh", "-c", "exit 3"}})
	if err != nil {
		t.Fatalf("Run returned error for non-zero exit: %v", err)
	}
	if result.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", result.ExitCode)
	}
}

func TestRunTimeout(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), RunOptions{
		Args:    []string{"sleep", "5"},
		Timeout: 100 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want wrapping context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v, expected prompt return after ~100ms timeout", elapsed)
	}
}

func TestRunContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := Run(ctx, RunOptions{Args: []string{"sleep", "5"}})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want wrapping context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v, expected prompt return after cancellation", elapsed)
	}
}

func TestRunRespectsDir(t *testing.T) {
	dir := t.TempDir()
	result, err := Run(context.Background(), RunOptions{Dir: dir, Args: []string{"pwd"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(string(result.Stdout))
	// pwd can report a symlink-resolved path (relevant on macOS, where
	// t.TempDir() lives under /var which symlinks to /private/var).
	want := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		want = resolved
	}
	if got != dir && got != want {
		t.Errorf("pwd = %q, want %q", got, dir)
	}
}

// TestRunDefaultsDirWhenUnset covers the real bug this default exists
// for: an unset Dir must never silently make the subprocess inherit
// whatever the calling process's own cwd happens to be — it defaults to
// os.TempDir() instead, which is always valid for the process's whole
// lifetime.
func TestRunDefaultsDirWhenUnset(t *testing.T) {
	result, err := Run(context.Background(), RunOptions{Args: []string{"pwd"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(string(result.Stdout))
	want := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(want); err == nil {
		want = resolved
	}
	gotResolved := got
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		gotResolved = resolved
	}
	if gotResolved != want {
		t.Errorf("pwd = %q, want os.TempDir() (%q)", got, want)
	}
}

// TestRunSurvivesADeletedCallerCWD is the exact real-world reproduction:
// a long-lived process (like the daemon) whose own working directory
// gets removed out from under it must still be able to spawn
// subprocesses — without this fix, every one of them failed identically
// (observed live: every `git clone --mirror` call in a real user's
// daemon, after their daemon's own launch directory was deleted, with
// git reporting "fatal: Unable to read current working directory").
func TestRunSurvivesADeletedCallerCWD(t *testing.T) {
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir, err := os.MkdirTemp("", "executil-deleted-cwd-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Errorf("restore cwd to %s: %v", prev, err)
		}
	})

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove %s out from under the process: %v", dir, err)
	}

	// pwd (unlike e.g. echo) actually calls getcwd() — the same class of
	// call real git subcommands make internally, which is what failed
	// live ("fatal: Unable to read current working directory").
	result, err := Run(context.Background(), RunOptions{Args: []string{"pwd"}})
	if err != nil {
		t.Fatalf("Run with a deleted caller cwd and no explicit Dir: %v (this is exactly the real bug)", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("pwd exit code = %d, stderr = %q; want 0 (the deleted caller cwd should never have been inherited)", result.ExitCode, result.Stderr)
	}
}

func TestRunBinaryNotFound(t *testing.T) {
	_, err := Run(context.Background(), RunOptions{Args: []string{"depctl-nonexistent-binary-xyz"}})
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
}
