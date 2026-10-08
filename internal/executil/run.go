// Package executil runs subprocesses safely: argv-style only, no shell
// interpretation, with context cancellation/timeout support. The Go
// resolver, the git source layer and depctl's other short tool calls go
// through this package rather than calling os/exec directly.
package executil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// RunOptions configures a single subprocess invocation.
type RunOptions struct {
	Dir     string
	Args    []string // Args[0] is the binary name; no shell involved
	Timeout time.Duration
	Env     []string // additional env vars, appended to os.Environ()
}

// RunResult captures a completed (or non-zero-exiting) subprocess.
type RunResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Run executes opts.Args[0] with opts.Args[1:] as arguments, never through a
// shell. A non-zero exit code is not itself an error — callers decide
// whether a given exit code is meaningful. Run returns an error only when
// the process could not be started or completed (binary missing, context
// cancelled or timed out).
func Run(ctx context.Context, opts RunOptions) (RunResult, error) {
	if len(opts.Args) == 0 {
		return RunResult{}, fmt.Errorf("executil: Args must not be empty")
	}

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, opts.Args[0], opts.Args[1:]...)
	cmd.Dir = opts.Dir
	if cmd.Dir == "" {
		// An empty Dir makes exec.Cmd inherit the calling process's own
		// cwd — fine for a short-lived CLI command, but the daemon is a
		// long-lived background process whose ambient cwd can silently
		// become invalid (the directory it happened to be launched
		// from, or a project directory, removed or moved while it kept
		// running). A subprocess started with an already-deleted cwd
		// fails immediately, identically, for every command, for a
		// reason that has nothing to do with the command itself — found
		// live: every `git clone --mirror` call in a real daemon failed
		// with "fatal: Unable to read current working directory" once
		// its own working directory was removed out from under it,
		// long after startup, misleadingly looking like a per-dependency
		// git/network failure. Defaulting to os.TempDir() — always
		// present, valid for the whole process lifetime — means a
		// caller only needs to set Dir when the command's own semantics
		// actually require a specific directory (most call sites in
		// this codebase already do).
		cmd.Dir = os.TempDir()
	}
	if len(opts.Env) > 0 {
		cmd.Env = append(cmd.Environ(), opts.Env...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := RunResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("executil: run %v: %w", opts.Args, ctxErr)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("executil: run %v: %w", opts.Args, err)
	}

	return result, nil
}
