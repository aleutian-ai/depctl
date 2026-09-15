package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/executil"
)

// qdrantImage is pinned to match docs/offline-quickstart.md's own pin,
// so the managed container and the documented manual-setup path never
// drift apart.
const qdrantImage = "docker.io/qdrant/qdrant:v1.13.1"

// qdrantContainerName identifies the container ragctl owns — used both
// for `run --name` and for the idempotency check in ensureManagedQdrant.
const qdrantContainerName = "ragctl-qdrant"

// qdrantVolumeName identifies the named Podman/Docker volume ragctl's
// managed Qdrant stores its data in. Live-found reason this isn't a
// host bind-mount: a Podman machine on macOS only shares specific host
// directories into its VM (via `podman machine inspect`'s "Mounts"),
// and a real machine with an empty Mounts list made every bind-mount
// fail with "statfs: no such file or directory" — even after ragctl
// created the host directory itself, since the VM never saw it in the
// first place. A named volume lives entirely inside the runtime's own
// storage, so it works regardless of the VM's host-mount configuration
// — the trade-off (documented in the README) is that this data doesn't
// survive `podman machine rm`/`docker system prune -a --volumes`, only
// a container being removed and recreated.
const qdrantVolumeName = "ragctl-qdrant-data"

// qdrantStartupTimeout bounds how long ensureManagedQdrant waits for a
// freshly started (or restarted) container to answer healthy. A var,
// not a const, so tests can shorten it rather than waiting out the real
// value on a health probe that's deliberately never going to succeed.
var qdrantStartupTimeout = 20 * time.Second

// qdrantPullTimeout separately bounds `run --pull=missing`, which — on
// the very first bootstrap, when the image genuinely isn't local yet —
// has to pull it over the network first. Live-found gap: sharing
// qdrantStartupTimeout's 20s with the pull step failed a real first
// bootstrap with "context deadline exceeded" before the (~100-200MB)
// image finished pulling. A var for the same test-shortening reason as
// qdrantStartupTimeout.
var qdrantPullTimeout = 3 * time.Minute

// execLookPath is exec.LookPath, indirected so tests can point it at a
// fake podman/docker without touching the real PATH.
var execLookPath = exec.LookPath

// containerRuntime returns the path to podman or docker, preferring
// podman — this project's own dev environment already uses it (see
// docs/offline-quickstart.md) — or "" if neither is on PATH.
func containerRuntime() string {
	if path, err := execLookPath("podman"); err == nil {
		return path
	}
	if path, err := execLookPath("docker"); err == nil {
		return path
	}
	return ""
}

// ensureManagedQdrant starts (or reuses, if already running or stopped
// from a prior daemon lifetime) a ragctl-owned Qdrant container bound to
// loopback only, with storage in a named Podman/Docker volume (see
// qdrantVolumeName — deliberately not a host bind-mount), and polls it
// healthy via the configured backend's own Health check — never by
// parsing runtime-specific container-status output.
func ensureManagedQdrant(ctx context.Context, cfg config.Config, runtime string, logf func(format string, args ...any)) error {
	state, err := containerState(ctx, runtime, qdrantContainerName)
	if err != nil {
		return fmt.Errorf("check existing container: %w", err)
	}

	switch state {
	case containerRunning:
		logf("vector bootstrap: %s already running, skipping straight to readiness poll", qdrantContainerName)
	case containerStopped:
		logf("vector bootstrap: restarting existing stopped container %s", qdrantContainerName)
		if res, err := executil.Run(ctx, executil.RunOptions{Args: []string{runtime, "start", qdrantContainerName}, Timeout: qdrantStartupTimeout}); err != nil {
			return fmt.Errorf("%s start %s: %w", runtime, qdrantContainerName, err)
		} else if res.ExitCode != 0 {
			return fmt.Errorf("%s start %s: exit %d: %s", runtime, qdrantContainerName, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
	case containerAbsent:
		logf("vector bootstrap: starting new managed container %s (%s)", qdrantContainerName, qdrantImage)
		args := []string{
			runtime, "run", "-d",
			"--name", qdrantContainerName,
			"--pull=missing",
			"-p", "127.0.0.1:6333:6333",
			"-v", qdrantVolumeName + ":/qdrant/storage",
			qdrantImage,
		}
		if res, err := executil.Run(ctx, executil.RunOptions{Args: args, Timeout: qdrantPullTimeout}); err != nil {
			return fmt.Errorf("%s run %s: %w", runtime, qdrantContainerName, err)
		} else if res.ExitCode != 0 {
			return fmt.Errorf("%s run %s: exit %d: %s", runtime, qdrantContainerName, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
	}

	pollCtx, cancel := context.WithTimeout(ctx, qdrantStartupTimeout)
	defer cancel()
	var lastErr error
	for {
		if lastErr = probeBackend(pollCtx, cfg); lastErr == nil {
			return nil
		}
		select {
		case <-pollCtx.Done():
			return fmt.Errorf("managed container %s did not become healthy within %s: %w", qdrantContainerName, qdrantStartupTimeout, lastErr)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// containerExistence is the result of the one narrow, existence-only
// use of the runtime's own inspection commands this ticket allows —
// everything else (actual health) goes through the backend's own
// Health check.
type containerExistence int

const (
	containerAbsent containerExistence = iota
	containerRunning
	containerStopped
)

// containerState reports whether name already exists under runtime, and
// if so, whether it's running — via `<runtime> ps -a --filter
// name=^<name>$ --format '{{.Names}}\t{{.State}}'`, an identical flag
// set across both podman and docker.
func containerState(ctx context.Context, runtime, name string) (containerExistence, error) {
	res, err := executil.Run(ctx, executil.RunOptions{
		Args:    []string{runtime, "ps", "-a", "--filter", "name=^" + name + "$", "--format", "{{.Names}}\t{{.State}}"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return containerAbsent, err
	}
	if res.ExitCode != 0 {
		return containerAbsent, fmt.Errorf("%s ps: exit %d: %s", runtime, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	line := strings.TrimSpace(string(res.Stdout))
	if line == "" {
		return containerAbsent, nil
	}
	if strings.Contains(strings.ToLower(line), "running") {
		return containerRunning, nil
	}
	return containerStopped, nil
}
