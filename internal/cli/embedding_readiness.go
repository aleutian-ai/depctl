package cli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/embedding/ollama"
)

// embeddingPullTimeout bounds a daemon-triggered model download in the
// background — generous, since a several-hundred-MB model over a slow
// connection can legitimately take a while, but still finite.
const embeddingPullTimeout = 15 * time.Minute

// embeddingState is where checkEmbeddingReadiness's background probe
// currently stands. Every daemon-owned embedder consumer (sync, search)
// checks this before touching the network itself, so a slow/missing
// Ollama never turns into a client call hanging with no explanation.
type embeddingState string

const (
	embeddingStateUnknown     embeddingState = "unknown"
	embeddingStateChecking    embeddingState = "checking"
	embeddingStatePulling     embeddingState = "pulling"
	embeddingStateReady       embeddingState = "ready"
	embeddingStateUnreachable embeddingState = "unreachable"
	embeddingStateError       embeddingState = "error"
)

// embeddingReadiness is the daemon's one shared view of whether its
// configured embedding provider is actually usable — set once at
// startup by a background goroutine (see checkEmbeddingReadiness),
// read by every request path that would otherwise try to build an
// embedder itself and block or fail confusingly.
type embeddingReadiness struct {
	mu     sync.Mutex
	state  embeddingState
	detail string
}

func newEmbeddingReadiness() *embeddingReadiness {
	return &embeddingReadiness{state: embeddingStateUnknown}
}

func (r *embeddingReadiness) set(state embeddingState, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state, r.detail = state, detail
}

func (r *embeddingReadiness) get() (embeddingState, string) {
	if r == nil {
		return embeddingStateUnknown, ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.detail
}

// checkReady returns nil when the embedder is ready to use, or an
// actionable error naming exactly what's wrong and what to do about it
// — never a raw connection-refused/404 from Ollama itself. A nil
// *embeddingReadiness (a caller that isn't the daemon, or a provider
// other than "ollama") always reports ready: this whole mechanism only
// understands ollama's reachability/pull semantics.
func (r *embeddingReadiness) checkReady() error {
	if r == nil {
		return nil
	}
	state, detail := r.get()
	switch state {
	case embeddingStateReady, embeddingStateUnknown:
		// Unknown means either the background check hasn't run yet
		// (a race at the very start of daemon startup) or this isn't
		// ollama — buildEmbedder itself still rejects an unsupported
		// provider, so there's no wrong answer in falling through here.
		return nil
	case embeddingStateChecking:
		return fmt.Errorf("ragctl is checking its embedding backend — retry shortly, or run `ragctl daemon status` for progress")
	case embeddingStatePulling:
		return fmt.Errorf("ragctl is initializing its embedding backend: %s is being downloaded by the local daemon — retry shortly, or run `ragctl daemon status` for progress", detail)
	case embeddingStateUnreachable:
		return fmt.Errorf("embedding backend unreachable: %s", detail)
	case embeddingStateError:
		return fmt.Errorf("embedding backend not ready: %s", detail)
	default:
		return nil
	}
}

// checkEmbeddingReadiness probes the configured embedding provider once
// in the background (called as its own goroutine from runDaemonRun) and,
// if it's ollama and the model isn't pulled yet, pulls it — entirely off
// any client's request path, so no MCP tool call or CLI command ever
// blocks on this. Only "ollama" is understood here, matching
// buildEmbedder's own scope (EMB-002); any other provider is marked
// ready immediately since nothing here knows its reachability semantics.
func checkEmbeddingReadiness(ctx context.Context, cfg config.Config, readiness *embeddingReadiness, logf func(format string, args ...any)) {
	if cfg.Embedding.Provider != "ollama" {
		readiness.set(embeddingStateReady, "")
		return
	}
	readiness.set(embeddingStateChecking, "")
	client := ollama.New(cfg.Embedding.Endpoint, cfg.Embedding.Model)

	if !client.Reachable(ctx) {
		detail := fmt.Sprintf("Ollama not reachable at %s — start it, then it'll be rechecked the next time it's needed", cfg.Embedding.Endpoint)
		readiness.set(embeddingStateUnreachable, detail)
		logf("embedding readiness: %s", detail)
		return
	}

	pulled, err := client.ModelPulled(ctx)
	if err != nil {
		detail := fmt.Sprintf("could not check whether %s is pulled: %v", cfg.Embedding.Model, err)
		readiness.set(embeddingStateError, detail)
		logf("embedding readiness: %s", detail)
		return
	}
	if pulled {
		readiness.set(embeddingStateReady, "")
		logf("embedding readiness: %s already pulled", cfg.Embedding.Model)
		return
	}

	readiness.set(embeddingStatePulling, cfg.Embedding.Model)
	logf("embedding readiness: pulling %s (not yet downloaded)", cfg.Embedding.Model)
	pullCtx, cancel := context.WithTimeout(ctx, embeddingPullTimeout)
	defer cancel()
	if err := client.PullModel(pullCtx, lineWriter(func(line string) { logf("embedding readiness: %s", line) })); err != nil {
		detail := fmt.Sprintf("failed to pull %s: %v — pull it manually (`ollama pull %s`)", cfg.Embedding.Model, err, cfg.Embedding.Model)
		readiness.set(embeddingStateError, detail)
		logf("embedding readiness: %s", detail)
		return
	}
	readiness.set(embeddingStateReady, "")
	logf("embedding readiness: %s ready", cfg.Embedding.Model)
}
