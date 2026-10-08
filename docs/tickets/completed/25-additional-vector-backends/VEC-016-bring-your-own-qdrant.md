# VEC-016: Bring-your-own Qdrant, documented and verified

**Epic:** Vector Backends (bring-your-own + embedded)
**Status:** done (2026-10-01)
**Depends on:** none (uses the already-shipped Qdrant adapter and config)
**Estimated size:** small

## Goal
Make "you already run Qdrant, point depctl at it" a documented, real-container-verified path, not just something the config happens to allow. The target user runs Qdrant already, standalone or as the vector store under their Mem0 (the Mem0 Python library's default vector store is Qdrant). For them, depctl should add no new vector service, and its data must stay cleanly separate from everything else in that Qdrant.

## Non-goals
- No new adapter or code path — the shipped Qdrant adapter (`internal/backend/qdrant`) and config (`vector.endpoint`, `vector.managed`, `vector.api_key_env`, `vector.collection`) already support this. This ticket proves it and documents it.
- No reading or writing of any collection depctl doesn't own. Sharing a Qdrant *server* is in scope; sharing a *collection* is not.
- No Mem0-side configuration. Cross-agent memory still goes through `depctl export mem0` (`MEM0-001`), not through a shared collection.

## Simplicity constraints
- Docs + one verification pass. Only touch code if the verification finds a real bug.

## Design
Configuration for an existing Qdrant:

```yaml
vector:
  backend: qdrant
  endpoint: http://localhost:6333     # the user's existing server
  managed: false                      # never start depctl's own container
  api_key_env: QDRANT_API_KEY         # only if their server requires a key
  # collection: left as depctl's per-install unique default (SAFE-001)
```

Verification, against real containers under Podman:
1. Run one Qdrant server. Put a non-depctl collection in it: Mem0 (library mode, Qdrant server backend), or failing that a hand-made collection with known points.
2. Point a fresh `depctl init` at that server with `managed: false`. `scan` + `sync` a real dependency.
3. Confirm depctl created only its own collection, and the other collection's point count and contents are unchanged.
4. Run `depctl gc`, plus a generation rebuild (`sync --rebuild`), and confirm neither touches the other collection. Pay attention to `Delete`: epic 14 fixed a real Qdrant filter union-semantics bug there.
5. Confirm `depctl doctor` / `daemon status` report the external backend correctly, and that `managed: false` never starts a container even when the endpoint is briefly unreachable.

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
- [x] Real-container run: depctl syncs into a Qdrant server that also holds a non-depctl collection, and that collection is untouched after sync, GC, and rebuild.
- [x] `managed: false` never starts depctl's own Qdrant container.
- [x] README and the Qdrant how-to doc describe the setup, matching what was actually verified (README "Vector store"; `docs/offline-quickstart.md` "Already running Qdrant? Use that instead").

## Post-implementation note (2026-10-01)

**Setup.** A user-owned `qdrant/qdrant:v1.13.1` under Podman on port 16333, separate from the developer's own depctl-managed Qdrant on 6333, which was never touched. A foreign `mem0` collection was seeded with 50 points whose payloads deliberately reuse depctl's own filter keys and values (`ecosystem: go`, `dependency: github.com/google/uuid`, `version: v1.5.0/v1.6.0`, `generation: gen_foreign_*`). Any depctl filter applied to the wrong collection would therefore have matched and deleted them. A script fingerprinted the whole collection (every ID, vector, and payload, sha256) after each step. A fresh, isolated depctl install was pointed at the server with `managed: false` and given per-install collection `depctl-5708e762`.

**Isolation, verified.** The foreign collection stayed `50 points, sha be780e7b28420437` through every step:
1. scan + sync of real `google/uuid` v1.6.0 (84 points into depctl's collection).
2. `sync --rebuild` to v1.5.0 (81 points).
3. Reference-based GC deleting v1.6.0's 84 points.
4. A rebuild superseding the first v1.5.0 generation, then `gc --superseded-duplicates` deleting its 81 points.
5. Stopping and restarting the user's Qdrant.

depctl only ever created its own collection.

**`managed: false`, verified.** With the user's Qdrant stopped, `sync` failed with `vector backend unreachable: qdrant not reachable at http://127.0.0.1:16333…`, and `daemon status` and `doctor` both reported UNHEALTHY. The container list was identical before and after: no container was started.

**No code change was needed for this ticket's own scope.** The verification did surface three real bugs elsewhere, all fixed in [epic 66](../../completed/66-version-correct-active-generations/INDEX.md) (2026-10-02):
1. **A dependency version change never triggers a rebuild.** `internal/cli/plan.go` builds the planner's `activeGenerations` map with `GetActiveGeneration` (keyed by dependency only) and never compares the active generation's version to the resolved one. Changing `uuid` v1.6.0 → v1.5.0 left v1.6.0 active and `plan` reporting "up to date". GC's own planner does the same lookup and *does* compare versions (`retention/gc_planner.go`), which is why this went unnoticed.
2. **A failed sync can leave a dependency permanently unsynced** (the `OPS-005` "referenced but never built" state, reproduced on demand). A `--rebuild` attempted while the backend was down cleared the active pointer, recorded the version reference, then failed the build. Every later plain `sync` NOOPs, because the planner's "reference unchanged" branch never checks whether a generation exists. Only another `--rebuild` recovers it.
3. **Vector readiness never rechecks.** The daemon probes the backend once at startup. If it was down then, every sync keeps failing as "unreachable" after it comes back, until a manual `depctl daemon stop`. Embedding readiness does recheck lazily.

Test-setup notes for whoever reruns this: `retention.grace_period: 0s` means "use the default" (14 days), so use `1s`. `keep_latest: true` keeps the highest version, which blocks GC after a downgrade. When overriding `HOME` for an isolated depctl, also set `CONTAINER_HOST` to the Podman machine socket, or Podman (and depctl's own container management) can't find its machine.
