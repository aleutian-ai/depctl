// Package executil runs subprocesses safely: argv-style only, no shell
// interpretation, with context cancellation/timeout support. Every
// ecosystem resolver shells out through this package rather than calling
// os/exec directly.
package executil

import (
	"bytes"
	"context"
	"fmt"
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
