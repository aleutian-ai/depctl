package cli

import (
	"context"
	"fmt"
	"sync"

	"aleutian-ai/ragctl/internal/config"
)

// vectorState is where checkVectorReadiness's background probe currently
// stands. Every daemon-owned vector-backend consumer (sync, GC, search)
// checks this before touching the network itself, so an unreachable
// Qdrant never turns into a raw dial error surfacing once per dependency.
type vectorState string

const (
	vectorStateUnknown     vectorState = "unknown"
	vectorStateChecking    vectorState = "checking"
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
	case vectorStateUnreachable:
		return fmt.Errorf("vector backend unreachable: %s", detail)
	case vectorStateError:
		return fmt.Errorf("vector backend not ready: %s", detail)
	default:
		return nil
	}
}

// checkVectorReadiness probes the configured vector backend once in the
// background (called as its own goroutine from runDaemonRun) — entirely
// off any client's request path, so no MCP tool call or CLI command
// ever blocks on this. Unlike checkEmbeddingReadiness there is no pull
// step: an unreachable Qdrant just gets reported (WATCH-016 adds an
// auto-start branch on top of this).
func checkVectorReadiness(ctx context.Context, cfg config.Config, readiness *vectorReadiness, logf func(format string, args ...any)) {
	readiness.set(vectorStateChecking, "")
	if err := probeBackend(ctx, cfg); err != nil {
		detail := fmt.Sprintf("%s not reachable at %s: %v", cfg.Vector.Backend, cfg.Vector.Endpoint, err)
		readiness.set(vectorStateUnreachable, detail)
		logf("vector readiness: %s", detail)
		return
	}
	readiness.set(vectorStateReady, "")
	logf("vector readiness: %s ready", cfg.Vector.Backend)
}
