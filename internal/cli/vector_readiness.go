package cli

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"aleutian-ai/ragctl/internal/config"
)

// vectorReprobeCooldown bounds how often a down backend is re-probed, so a
// burst of requests against a dead Qdrant doesn't probe it on each one.
const vectorReprobeCooldown = 5 * time.Second

// vectorState is where checkVectorReadiness's background probe currently
// stands. Every daemon-owned vector-backend consumer (sync, GC, search)
// checks this before touching the network itself, so an unreachable
// Qdrant never turns into a raw dial error surfacing once per dependency.
type vectorState string

const (
	vectorStateUnknown     vectorState = "unknown"
	vectorStateChecking    vectorState = "checking"
	vectorStateStarting    vectorState = "starting"
	vectorStateReady       vectorState = "ready"
	vectorStateUnreachable vectorState = "unreachable"
	vectorStateError       vectorState = "error"
)

// vectorReadiness is the daemon's one shared view of whether its
// configured vector backend is actually usable — set once at startup by
// a background goroutine (see checkVectorReadiness), read by every
// request path that would otherwise try to build a vector backend
// itself and fail with a raw connection error.
type vectorReadiness struct {
	mu     sync.Mutex
	state  vectorState
	detail string

	// reprobe, when set (by the daemon), re-checks a backend last seen
	// unreachable or in error (OPS-008). Without it, a backend that was
	// down at daemon startup stayed "unreachable" until a restart, even
	// after it came back. nil in tests and in doctor's one-shot copy.
	reprobe     func() error
	lastReprobe time.Time
}

func newVectorReadiness() *vectorReadiness {
	return &vectorReadiness{state: vectorStateUnknown}
}

func (r *vectorReadiness) set(state vectorState, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state, r.detail = state, detail
}

func (r *vectorReadiness) get() (vectorState, string) {
	if r == nil {
		return vectorStateUnknown, ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.detail
}

// checkReady returns nil when the vector backend is ready to use, or an
// actionable error naming exactly what's wrong — never a raw
// connection-refused from Qdrant itself. A nil *vectorReadiness (a
// caller that isn't the daemon) always reports ready.
func (r *vectorReadiness) checkReady() error {
	if r == nil {
		return nil
	}
	r.reprobeIfDown()
	state, detail := r.get()
	switch state {
	case vectorStateReady, vectorStateUnknown:
		// Unknown means either the background check hasn't run yet (a
		// race at the very start of daemon startup) or this isn't a
		// case this mechanism understands — buildVectorBackend itself
		// still rejects an unsupported backend, so there's no wrong
		// answer in falling through here.
		return nil
	case vectorStateChecking:
		return fmt.Errorf("ragctl is checking its vector backend — retry shortly, or run `ragctl daemon status` for progress")
	case vectorStateStarting:
		return fmt.Errorf("ragctl is starting its managed Qdrant container (%s) — retry shortly, or run `ragctl daemon status` for progress", detail)
	case vectorStateUnreachable:
		return fmt.Errorf("vector backend unreachable: %s", detail)
	case vectorStateError:
		return fmt.Errorf("vector backend not ready: %s", detail)
	default:
		return nil
	}
}

// reprobeIfDown re-probes a backend last seen unreachable or in error, at
// most once per vectorReprobeCooldown, and marks it ready if the probe now
// succeeds. The probe itself is bounded (backendHealthTimeout).
func (r *vectorReadiness) reprobeIfDown() {
	r.mu.Lock()
	down := r.state == vectorStateUnreachable || r.state == vectorStateError
	if !down || r.reprobe == nil || time.Since(r.lastReprobe) < vectorReprobeCooldown {
		r.mu.Unlock()
		return
	}
	r.lastReprobe = time.Now()
	probe := r.reprobe
	r.mu.Unlock()

	if probe() == nil {
		r.set(vectorStateReady, "")
	}
}

// checkVectorReadiness probes the configured vector backend once in the
// background (called as its own goroutine from runDaemonRun) — entirely
// off any client's request path, so no MCP tool call or CLI command
// ever blocks on this. If it's unreachable and cfg.Vector.Managed is
// true (WATCH-016), it attempts to start ragctl's own Qdrant container
// via podman/docker before giving up; otherwise it just reports the
// failure, same as before WATCH-016.
func checkVectorReadiness(ctx context.Context, cfg config.Config, readiness *vectorReadiness, logf func(format string, args ...any)) {
	readiness.set(vectorStateChecking, "")
	if err := probeBackend(ctx, cfg); err != nil {
		// Only Qdrant has a managed container; vector.managed is ignored
		// for any other backend.
		if !cfg.Vector.Managed || cfg.Vector.Backend != "qdrant" {
			detail := fmt.Sprintf("%s not reachable at %s: %v", cfg.Vector.Backend, redactDSN(cfg.Vector.Endpoint), err)
			readiness.set(vectorStateUnreachable, detail)
			logf("vector readiness: %s", detail)
			return
		}

		runtime := containerRuntime()
		if runtime == "" {
			detail := "vector.managed is true but no container runtime (podman or docker) was found on PATH — install one, or start Qdrant manually and set vector.managed: false"
			readiness.set(vectorStateUnreachable, detail)
			logf("vector readiness: %s", detail)
			return
		}

		readiness.set(vectorStateStarting, qdrantContainerName)
		logf("vector readiness: %s unreachable, starting managed container %s via %s", cfg.Vector.Backend, qdrantContainerName, runtime)
		if err := ensureManagedQdrant(ctx, cfg, runtime, logf); err != nil {
			readiness.set(vectorStateError, err.Error())
			logf("vector readiness: %v", err)
			return
		}
		readiness.set(vectorStateReady, "")
		logf("vector readiness: managed container %s ready", qdrantContainerName)
		return
	}
	readiness.set(vectorStateReady, "")
	logf("vector readiness: %s ready", cfg.Vector.Backend)
}

// redactDSN hides any password embedded in a connection URL (pgvector's
// endpoint) before it's shown in status output or logs.
func redactDSN(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.User == nil {
		return endpoint
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}
