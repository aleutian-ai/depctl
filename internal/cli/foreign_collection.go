package cli

import (
	"context"

	"github.com/aleutian-ai/depctl/internal/config"
	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
)

// checkForeignCollectionData is SAFE-001's (epic 61) own guard against
// the exact incident this session found live, twice: two independent
// depctl installs sharing one Qdrant server, one of them fresh, silently
// commingling data because ambient sync (SCOPE-002) fires automatically
// on first project registration with no separate opt-in step. Runs once
// at daemon startup, off the request path (matching checkEmbeddingReadiness/
// checkVectorReadiness's own async pattern below) — a fresh control.db
// (zero active generations ever registered by this instance) whose
// configured collection already has real points in it gets one loud
// warning logged before ambient sync can ever reach it.
//
// Deliberately a warning, never a refusal — matching this project's own
// "warn, don't silently block" precedent (MCP-005): a collection
// intentionally shared or reused across installs is a real, valid
// choice, not something this check can distinguish from an accidental
// collision. It only flags the one case it can be sure about: this
// specific control.db has never registered anything, yet the collection
// isn't empty.
func checkForeignCollectionData(ctx context.Context, cfg config.Config, store *bboltstore.Store, logf func(format string, args ...any)) {
	pointers, err := store.ListActivePointers(ctx, cfg.Vector.Backend)
	if err != nil || len(pointers) > 0 {
		return // already has its own real state (or couldn't tell) — not a fresh instance, or not this check's business either way
	}
	vb, err := buildVectorBackend(cfg)
	if err != nil {
		return
	}
	n, err := vb.Count(ctx, cfg.Vector.Collection, nil)
	if err != nil || n == 0 {
		return
	}
	logf("WARNING: vector.collection %q at %s already contains %d point(s), but this instance has never registered an active generation of its own — if this collection is shared with another depctl install, the next sync (including automatic ambient sync) will commingle data into it. Set vector.collection to something unique to this install, or sync.disable_ambient: true if you didn't mean to sync yet.",
		cfg.Vector.Collection, vectorLocation(cfg), n)
}
