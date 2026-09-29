# Epic: Full-Loop Stress Testing

Everything shipped through epic 48 has been proven correct at fixture scale — real bbolt/Badger stores, real syncVersion, but small, hand-built repos and in-process fakes for the embedder/vector backend in most cases, and no sustained concurrent or adversarial load anywhere. This epic pushes the `scan → sync → gc → serve` loop that epic 45/48 proved *correct* to also prove it *survives* — real scale, real concurrency, real network, and real process kills, not just a single well-behaved run.

Sourced from a 2026-09 design review's own stress-test plan, in the order proposed there. Tickets are grouped by loop stage; STRESS-018 (the combined chaos loop) is deliberately last, since it exercises most of the others together and is the highest-value single test in the epic.

## Tickets

### Stage 1 — `scan`
- [x] [STRESS-001](STRESS-001-real-dependency-heavy-project-scan.md) — Scan a real, 100+-dependency public project; measure wall-clock, confirm full resolution. Done — `hashicorp/terraform`, 569 deps in ~5s; found and fixed the monorepo/`go.work` gap, see [epic 50](../../completed/50-go-monorepo-workspace-resolution/INDEX.md).
- [x] [STRESS-002](STRESS-002-rescan-idempotency-at-scale.md) — Re-scan the same large project three times; confirm zero state drift. Done — `deps` byte-identical and `plan` action count stable (12358) across all three scans of `hashicorp/terraform`.
- [x] [STRESS-003](STRESS-003-concurrent-scans-same-project.md) — Two concurrent `ragctl scan` invocations against the same project directory; confirm `LockProject` actually serializes under real concurrency. Done — 12/12 race iterations clean, no duplicate registrations.
- [x] [STRESS-004](STRESS-004-multi-ecosystem-monorepo-scan.md) — A monorepo spanning two ecosystems (Go + Node); confirm correct multi-resolver dispatch in one scan. Done — both discovered in one pass, zero cross-contamination (7 Go deps, 69 Node deps, correctly separated).

### Stage 2 — `sync`
- [x] [STRESS-005](STRESS-005-full-cold-sync-real-scale.md) — Cold sync of the STRESS-001 project; measure real wall-clock/disk usage post-GIT-005. Done — found and fixed the dominant real cause (nested-Go-module over-normalization, [epic 52](../../completed/52-nested-go-module-normalization-boundary/INDEX.md)); the whole-batch 30-minute ceiling itself was its own follow-up ([epic 51](../../completed/51-bulk-sync-batch-timeout/INDEX.md), now shipped and closed).
- [x] [STRESS-006](STRESS-006-kill-daemon-mid-sync.md) — `kill -9` the daemon mid-sync; confirm no corrupted state and a clean resume/orphan path. Done — store integrity never compromised across 5 real kills; found and fixed a real gap (a kill landing between a reference commit and its build reaching `ACTIVE` was permanently un-retriable and invisible to every check) — filed and fixed same-day as [epic 61's OPS-005](../../completed/61-pre-v1-operational-hardening/OPS-005-referenced-but-never-built-dependency-invisible-and-unrecoverable.md).
- [STRESS-007](STRESS-007-concurrent-cross-project-sync-real-daemon.md) — VALID-002's scenario against a real daemon and real backend, not fakes.
- [STRESS-008](STRESS-008-jit-sync-storm.md) — Many simultaneous JIT-sync triggers for different dependencies on one project.
- [STRESS-009](STRESS-009-large-single-dependency-blobless-clone.md) — A real large-history public repo, stress-testing GIT-005's blobless/sparse path against a real remote.
- [x] [STRESS-010](STRESS-010-repeated-real-failure-injection.md) — Repeated real sync failures (bad ref, embedder failure) against a real backend; confirm FIX-001's FAILED-state transition and orphan-GC pickup. Done — all three real failure modes (bad ref, embedder down, vector backend down) produced clean `FAILED` transitions, correct orphan-GC detection/cleanup, and confirmed the same OPS-004/OPS-005 recovery pattern STRESS-006 found generalizes here too (`--rebuild`, not a plain re-sync).

### Stage 3 — `gc`
- [x] [STRESS-011](STRESS-011-reference-gc-at-scale.md) — Reference-based GC with many real candidates. Done — 37 real, grace-expired candidates deleted cleanly (`37 deleted, 0 failed`); confirmed `PlanGC`'s active-generation-skip design holds at real scale (a dropped reference alone is never GC-eligible while its version is still `active` — a real version supersede or explicit demote is required), and that `uuid`/`protobuf` (still referenced) were completely untouched.
- [x] [STRESS-012](STRESS-012-orphan-gc-after-real-failures.md) — Orphan GC cleaning up STRESS-006/010's real casualties. Done — covered live during STRESS-010's own session; a real `gc --orphans` run correctly deleted all three real `FAILED` casualties (`3 deleted, 0 failed`), leaving each dependency's own healthy, active generation (from its `--rebuild` recovery) completely untouched — generation-ID-scoped deletion confirmed at real, not just fixture, scale.
- [x] [STRESS-013](STRESS-013-gc-while-sync-queued.md) — GC and a queued sync under real daemon load. Done — reframed for epic 53's `BuildCoordinator` (`gate sync.RWMutex`) replacing the pre-epic-53 single global mutex this ticket originally described; two genuinely separate real client processes confirmed full GC/sync exclusion in both orderings (sync-first: GC blocked ~4m03s behind a real `x/tools` embed, then ran in a fraction of a second; GC-first: sync blocked behind GC's 0.343s real deletion run, then performed its real rebuild) — no interleaving corruption in either direction.
- [x] [STRESS-014](STRESS-014-repeated-gc-cycle-idempotency.md) — Repeated `gc`/`gc --orphans` cycles with nothing new eligible; confirm fast, clean no-ops. Done — run against real production (already clean, no fixture needed): both commands' first runs found and cleanly deleted real, previously-uncollected garbage (9 grace-expired + 9 orphaned, respectively), then 2 further repeats each correctly short-circuited to "nothing eligible" in ~10ms (vs. ~1-3s for the real work), `doctor` clean throughout.

### Stage 4 — `serve`
- [STRESS-015](STRESS-015-concurrent-real-mcp-sessions.md) — Multiple concurrent real MCP tool calls against one daemon.
- [STRESS-016](STRESS-016-explain-call-site-at-scale.md) — `explain_call_site` against a real, large Go codebase.
- [STRESS-017](STRESS-017-daemon-restart-during-mcp-session.md) — Daemon restart while an MCP client session is connected.

### Combined chaos
- [STRESS-018](STRESS-018-full-loop-chaos-loop.md) — Repeated full-loop runs with random `kill -9`s injected at random points; `doctor` must report clean after every restart.

## Non-goals
- No load-testing infrastructure beyond what's needed to run these specific scenarios — no new benchmarking framework, no CI integration for any of these (all are manual/live, like VALID-003's own benchmark, not part of `go test ./...`).
- No fixing of whatever these tickets find beyond the ticket that found it — a real bug found here gets documented and either fixed in the same ticket (if small) or filed as its own follow-up (if not), matching this session's own established discipline. Scope creep into "also stress-test X while we're here" is explicitly out of bounds per-ticket.
- No synthetic/mocked concurrency where real concurrency is available — these tickets exist specifically because fixture-scale, single-threaded, in-process tests already passed; a test that quietly falls back to fakes defeats the point.
