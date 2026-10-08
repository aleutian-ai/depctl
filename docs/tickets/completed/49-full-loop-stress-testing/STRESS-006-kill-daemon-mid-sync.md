# STRESS-006: Kill the daemon mid-sync

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-28, real finding filed as its own follow-up (OPS-005, epic 61 reopened)
**Depends on:** none (a small fixture project is enough; STRESS-001's project works too if a longer sync window is wanted)
**Estimated size:** medium

## Goal
`kill -9` the daemon process while a real sync is actively running (not a graceful `depctl daemon stop`), then restart it, and confirm: no corrupted bbolt/Badger state (both stores still open and readable), the interrupted generation is left in a sane, inspectable state (feeding correctly into FIX-001's `FAILED` transition where applicable, or a genuinely orphaned non-terminal state that GC-001's orphan planner can see), and a subsequent `depctl sync` re-run completes normally with no lingering corruption.

## Non-goals
- No graceful-shutdown testing — that path (`depctl daemon stop`) is already covered by existing scheduler tests. This ticket is specifically about the ungraceful case, `kill -9`, which bypasses `Scheduler.Shutdown` entirely.
- No fix for whatever's found beyond confirming the system's actual behavior — a real corruption bug found here is a release-blocking finding, filed as its own ticket immediately, not patched inline under time pressure.

## Simplicity constraints
- Use **`github.com/grpc/grpc-go`** (module `google.golang.org/grpc`) as the kill target — already this codebase's own running example throughout `docs/`, real, multi-file, with a non-trivial clone+embed duration (a few seconds at least) so there's a real window to land the kill inside `ACQUIRING`/`NORMALIZING`/`INDEXING`/`VALIDATING`, not just before or after the whole action.

## Design
1. Start the daemon, begin `depctl sync --dependency google.golang.org/grpc` against a project resolving `github.com/grpc/grpc-go`.
2. From a second terminal, `kill -9 <daemon pid>` at a moment estimated to land mid-`ACQUIRING`/`REPLICATE` (timing by trial — run once first to see how long the target dependency takes, then kill partway through on the next attempt).
3. Confirm the `sync` client process itself reports a connection-lost error, not a hang.
4. Restart the daemon (`depctl daemon status`/autostart via any client command).
5. `depctl doctor` — must report a clean, non-corrupted state (bbolt/Badger both open successfully, schema version intact).
6. Inspect the interrupted generation's state directly (`depctl describe` or a direct store read) — record what state it's actually in.
7. `depctl gc --orphans --dry-run` — confirm the interrupted generation (if left in a FAILED/stuck-non-terminal state) is correctly detected as a candidate.
8. Re-run `depctl sync` for the same dependency — confirm it completes normally, without needing any manual cleanup.
9. Repeat steps 1-8 at least 3 times with the kill landing at different points in the pipeline (during acquisition, during embedding, during the final promotion write) to cover more of the failure-window space than one lucky/unlucky timing.

## Inputs / Outputs
- Input: a project with a real, multi-second sync target; a `kill -9` landed mid-sync.
- Output: pass/fail on store integrity, generation state sanity, orphan-GC detection, and clean re-sync — across at least 3 differently-timed kills.

## Failure behavior
- Any store corruption (bbolt/Badger failing to open, `doctor` reporting a real problem, a re-sync that fails or produces wrong results) is this ticket's real finding — a release-blocking bug, filed immediately as its own ticket.

## Tests
- Manual/live exercise; ideally scripted so the same kill-timing scenarios can be re-run after any fix.

## Acceptance criteria
- [x] A `kill -9` mid-sync, at three different pipeline points, never corrupts bbolt or Badger. Confirmed across 5 real kills (see notes) — `doctor` always reported both stores open, schema intact.
- [x] `depctl doctor` reports clean after every restart. 18 ok / 0 warning / 0 unhealthy, every time.
- [x] The interrupted generation is not silently invisible — confirmed via a direct store read (not `depctl describe`, which turned out not to show non-active generations at all — see notes): real records persist correctly in their true non-terminal state (`ACQUIRING`, `INDEXING`).
- [x] Falsified, not satisfied, by real testing: **a plain re-sync after a kill does *not* complete normally** — this is this ticket's actual finding, filed immediately as [OPS-005](../../completed/61-pre-v1-operational-hardening/OPS-005-referenced-but-never-built-dependency-invisible-and-unrecoverable.md) per this ticket's own non-goal ("a real bug found here is a release-blocking finding, filed as its own ticket immediately, not patched inline").

## Post-implementation note (2026-09-28)

Live-verified against a real daemon, real Ollama, an isolated Podman Qdrant container, and a real fixture (`google.golang.org/grpc` and several of its real transitive dependencies — `golang.org/x/tools`, `golang.org/x/net`, `golang.org/x/sys`, `google.golang.org/genproto/googleapis/rpc`). Five real `kill -9`s at different real timings, each verified by exact daemon PID/path before killing (never a broad pattern match).

**A real methodology surprise, not a bug**: warm git-mirror and embedding caches make a *rebuild* of an already-synced dependency dramatically faster than its first cold sync (grpc-go: 27.5s cold, ~2s warm) — a kill timed off the cold-sync duration lands too late once caches are warm. Fixed by using different, never-before-synced real dependencies for each kill window instead of repeatedly re-killing the same one.

**Store integrity: never a problem, across all 5 kills.** `doctor` always reported both stores open, schema version intact, no warnings.

**The real finding.** A kill landing after a new dependency's `ActionAddReference` commits but before its matching `ActionSyncVersion` reaches `ACTIVE` (i.e., interrupted anywhere from early `ACQUIRING` through `INDEXING`) leaves a permanently un-retriable dependency:
- The real `Generation` record *does* persist correctly, in its true non-terminal state (`ACQUIRING`/`INDEXING`, confirmed via a direct `ListAllGenerations` read) — not corrupted, not lost. `depctl describe`, however, only reports the currently-*active* generation per dependency, so it showed *nothing at all* for these — a display-scope gap worth a note but not itself the bug.
- `depctl gc --orphans --dry-run` correctly reported nothing eligible — not a bug either, `RetentionConfig.OrphanAge` (24h default) hadn't elapsed for generations only minutes old. Working as designed: don't reap something that might still be a legitimately slow, resumable build.
- **The actual gap**: `internal/planner/planner.go`'s `Plan` function's `default:` branch (dependency version unchanged since last scan) emits `ActionNoop` *unconditionally* — it never checks whether an active generation actually exists. Since the reference was already added before the kill, every subsequent plain `depctl sync` forever reports "up to date" / `0 synced, 0 failed, 0 skipped` for that dependency, with no error, no warning, nothing in `doctor` — confirmed live, reproduced twice (`golang.org/x/tools`, `golang.org/x/net`). This is the exact sibling of OPS-004's already-fixed "active but empty" case, except *worse*: there's no equivalent `doctor` check today that would ever notice a referenced-but-never-actually-built dependency, unlike OPS-004's case which `checkEmptyActiveGenerations` does catch. Confirmed recoverable only via `depctl sync --rebuild --dependency <name>` (OPS-004's new flag) — never by a plain, repeated `depctl sync`, however many times.

Recovered the fixture cleanly via `--rebuild` for all affected dependencies; final `doctor`: 18 ok / 0 warning / 0 unhealthy. Real production daemon and Qdrant collection confirmed untouched throughout (isolated Podman container + isolated `$HOME`, never the shared production instance).
