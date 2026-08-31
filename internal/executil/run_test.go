package executil

import (
	"context"
	"errors"
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

func TestRunBinaryNotFound(t *testing.T) {
	_, err := Run(context.Background(), RunOptions{Args: []string{"ragctl-nonexistent-binary-xyz"}})
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
}
