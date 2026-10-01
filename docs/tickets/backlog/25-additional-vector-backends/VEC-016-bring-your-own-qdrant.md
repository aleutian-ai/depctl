# VEC-016: Bring-your-own Qdrant, documented and verified

**Epic:** Vector Backends (bring-your-own + embedded)
**Status:** planned
**Depends on:** none (uses the already-shipped Qdrant adapter and config)
**Estimated size:** small

## Goal
Make "you already run Qdrant, point ragctl at it" a documented, real-container-verified path, not just something the config happens to allow. The target user runs Qdrant already, standalone or as the vector store under their Mem0 (the Mem0 Python library's default vector store is Qdrant). For them, ragctl should add no new vector service, and its data must stay cleanly separate from everything else in that Qdrant.

## Non-goals
- No new adapter or code path — the shipped Qdrant adapter (`internal/backend/qdrant`) and config (`vector.endpoint`, `vector.managed`, `vector.api_key_env`, `vector.collection`) already support this. This ticket proves it and documents it.
- No reading or writing of any collection ragctl doesn't own. Sharing a Qdrant *server* is in scope; sharing a *collection* is not.
- No Mem0-side configuration. Cross-agent memory still goes through `ragctl export mem0` (`MEM0-001`), not through a shared collection.

## Simplicity constraints
- Docs + one verification pass. Only touch code if the verification finds a real bug.

## Design
Configuration for an existing Qdrant:

```yaml
vector:
  backend: qdrant
  endpoint: http://localhost:6333     # the user's existing server
  managed: false                      # never start ragctl's own container
  api_key_env: QDRANT_API_KEY         # only if their server requires a key
  # collection: left as ragctl's per-install unique default (SAFE-001)
```

Verification, against real containers under Podman:
1. Run one Qdrant server. Put a non-ragctl collection in it: Mem0 (library mode, Qdrant server backend), or failing that a hand-made collection with known points.
2. Point a fresh `ragctl init` at that server with `managed: false`. `scan` + `sync` a real dependency.
3. Confirm ragctl created only its own collection, and the other collection's point count and contents are unchanged.
4. Run `ragctl gc`, plus a generation rebuild (`sync --rebuild`), and confirm neither touches the other collection. Pay attention to `Delete`: epic 14 fixed a real Qdrant filter union-semantics bug there.
5. Confirm `ragctl doctor` / `daemon status` report the external backend correctly, and that `managed: false` never starts a container even when the endpoint is briefly unreachable.

## Inputs / Outputs
- Input: existing config keys only.
- Output: a README "Vector store" section stating this works, backed by the verification above; a short how-to in `docs/offline-quickstart.md` (or wherever running-your-own-Qdrant already lives).

## Failure behavior
- Endpoint unreachable with `managed: false` → clear readiness error (existing `vectorReadiness` behavior), never a silently started container.
- Auth required but no `api_key_env` → actionable error naming the config key.

## Tests
- The real-container verification above, recorded in this ticket's post-implementation note.
- If verification finds a bug, a regression test for that bug at the adapter level.

## Acceptance criteria
- [ ] Real-container run: ragctl syncs into a Qdrant server that also holds a non-ragctl collection, and that collection is untouched after sync, GC, and rebuild.
- [ ] `managed: false` never starts ragctl's own Qdrant container.
- [ ] README and the Qdrant how-to doc describe the setup, matching what was actually verified.
