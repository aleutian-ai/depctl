package registry

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"aleutian-ai/ragctl/internal/executil"
)

// livenessTimeout bounds a single liveness check — this is a quick
// reachability probe, not a full acquisition.
const livenessTimeout = 10 * time.Second

// LivenessResult reports whether a Source's URL was reachable when
// checked, preserving the underlying failure for a human to act on.
type LivenessResult struct {
	SourceID  string
	Reachable bool
	CheckedAt time.Time
	Error     string
}

// CheckLiveness probes source without acquiring it: `git ls-remote` for
// a git source (no clone), an HTTP HEAD for a website source. It only
// runs when explicitly called — see REG-007 — never implicitly from a
// command that would otherwise be fully offline.
func CheckLiveness(ctx context.Context, source Source) LivenessResult {
	result := LivenessResult{SourceID: source.ID, CheckedAt: time.Now()}

	var err error
	switch source.Type {
	case "git":
		err = checkGitLiveness(ctx, source.URL)
	case "website":
		err = checkWebsiteLiveness(ctx, source.URL)
	default:
		err = fmt.Errorf("liveness check not implemented for source type %q", source.Type)
	}

	result.Reachable = err == nil
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func checkGitLiveness(ctx context.Context, url string) error {
	result, err := executil.Run(ctx, executil.RunOptions{
		Args:    []string{"git", "ls-remote", "--exit-code", url, "HEAD"},
		Timeout: livenessTimeout,
	})
	if err != nil {
		return fmt.Errorf("git ls-remote %s: %w", url, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("git ls-remote %s exited %d: %s", url, result.ExitCode, result.Stderr)
	}
	return nil
}

func checkWebsiteLiveness(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: livenessTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HEAD %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HEAD %s returned status %d", url, resp.StatusCode)
	}
	return nil
}
