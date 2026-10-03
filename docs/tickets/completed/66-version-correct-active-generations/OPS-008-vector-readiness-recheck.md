# OPS-008: Re-probe the vector backend instead of trusting the startup check

**Epic:** Version-correct active generations
**Status:** done (2026-10-02)
**Depends on:** none
**Estimated size:** small

## Goal
If the vector backend was down when the daemon started, ragctl recovers on its own once the backend comes back. Live-found in `VEC-016`: the daemon probed once at startup, cached "unreachable", and failed every sync after the user's Qdrant came back, until a manual `ragctl daemon stop`. That's a realistic state after a reboot, when ragctl's daemon autostarts before the user's own Qdrant. (Embedding readiness turned out to have the same bug, despite its message promising a recheck; see the note below.)

## Design
When a caller needs the vector backend and the cached state is unreachable or error, `checkReady` re-probes (bounded, a few seconds) before answering. A success updates the cached state to ready and proceeds; a failure returns the same typed "vector backend unreachable" error as today. A short cooldown stops a burst of calls from probing a down backend on every request.

## Tests
- Unreachable at startup, reachable later: the next `checkReady` succeeds without a daemon restart.
- Still unreachable: returns the typed error, and calls within the cooldown don't re-probe.

## Post-implementation note (2026-10-02)
`checkReady` re-probes when the cached state is unreachable or error, rate-limited to once per 5s and bounded by the existing 3s health timeout (`internal/cli/vector_readiness.go`). **Embedding readiness had the same bug**, and its own error message promised a recheck that never happened. It now re-probes the same way (`embedding_readiness.go`); if Ollama is back but the model isn't pulled, it hands off to the existing background pull instead of blocking the request. Both implementations are duplicated rather than shared, per project preference. Tests cover recovery, cooldown, and no re-probe while ready, and pass under `-race`.

**Verified live:** the daemon started while Qdrant was down (`vector: unreachable`). Qdrant was brought back with no daemon restart, and the next sync turned readiness to `ready` by itself and succeeded, on the same daemon process.

Not changed, noted for later: readiness only notices a backend coming *back*. A backend that goes down *after* startup still reads `ready` until a call fails, so that failure surfaces as the underlying dial error from deep in replication rather than the friendlier "unreachable" message. Pre-existing behavior, not a regression.

- [x] Backend down at daemon start, up later → the next sync succeeds with no restart.
- [x] `docs/offline-quickstart.md`'s "Known issue" note is removed.
