# ragctl — Planned Tickets (v0.1 critical path, in progress)

Source: `ragctl_implementation_plan.md` + `ragctl_design_spec.md`. See also [../completed/README.md](../completed/README.md) for fully-shipped epics and [../backlog/README.md](../backlog/README.md) for deferred epics (additional ecosystems, additional vector backends, eval/observability/security, optional integrations).

Each numbered directory is an epic. Each epic has an `INDEX.md` with the epic's goal and a linked list of its tickets. Every ticket is a self-contained spec (goal, non-goals, simplicity constraints, design, tests, acceptance criteria) — implementable without re-reading the source docs. A ticket's `**Status:**` line is `planned` until every one of its Acceptance Criteria boxes is genuinely checked, at which point it becomes `done` — see `docs/architecture.md` for the authoritative narrative of what's actually shipped.

**Guiding rule across all tickets:** keep implementations as simple as possible. Build one reference implementation per interface before adding more (one resolver, one vector backend, one embedder). Do not build the interface abstraction until a second implementation is imminent.

## Build order — what's left here

Epics 01 through 20 (bootstrap through describe) shipped, completing the original v0.1 build order, and live in [../completed/](../completed/README.md) — epic 19 (watch mode) reopened after shipping and stayed in this directory for a long stretch, but is now done and moved back to `completed/19-watch-mode` (2026-09); epic 02 (core-domain-storage) was the last of the original 20 to close, reconciled to what actually shipped rather than the original sketch (see `completed/02-core-domain-storage/INDEX.md` and `docs/architecture.md`'s "Epics 1-2" section for how). What remains in this directory:

At the end of what's now `completed/17-mcp-server`, the system met Milestone E (`v0.0.5-agent`) from the implementation plan: a coding agent can query exact dependency version docs via MCP.

Epics 21-22 (and epic 19's WATCH-004..020) were added after v0.1, outside the original implementation-plan build order — real usage and an architecture review (`docs/scratch/ragctl_architecture_eval_next_steps-2.md`) surfaced concrete gaps worth closing before moving further into backlog scope. Every epic that review or later real usage surfaced (22, 42, 43, 45, 47, 54, 55, 56, 57, 58, 59, 60, 61) has since shipped and lives in `../completed/` — see that directory's own README for each one's real status; only the two below are still genuinely open.

49. [49-full-loop-stress-testing](49-full-loop-stress-testing/INDEX.md) — STRESS-001 through STRESS-018: real-scale, real-concurrency, and adversarial (`kill -9`) testing of `scan → sync → gc → serve`, all against real infrastructure (real daemon, real Qdrant, real Ollama, real public dependencies) rather than fixtures or fakes — everything shipped through epic 48 was proven *correct*; this epic proves it *survives*. 5 of 18 tickets done (re-verified live, no regressions, 2026-09-27); all crash/chaos/GC-at-scale/concurrent-MCP-session coverage is unwritten — the least-tested real risk area in the project.
53. [53-foreground-jit-isolation](53-foreground-jit-isolation/INDEX.md) — COORD-001..003: `internal/daemon/scheduler.go`'s single `s.global sync.Mutex` serializes every sync/GC run daemon-wide, meaning a JIT request for one project can be stranded behind an unrelated project's bulk sync for up to 30 minutes — found live investigating STRESS-005. `RWMutex`-based build/GC coordination plus keyed request coalescing (COORD-001/002, both launch-critical) are done; COORD-003 (a bounded bulk worker pool) is also built, and its default-concurrency choice (2) is now re-benchmarked on corrected data (2026-09-28) rather than asserted — real per-dependency latency roughly doubles at concurrency 3/5. The one loose end keeping this epic open: a live proof against a real daemon (not just in-process tests) that the pool never contends with a concurrent JIT request.

Epic 61 ([61-pre-v1-operational-hardening](../completed/61-pre-v1-operational-hardening/INDEX.md)) moved to `completed/` (2026-09-28) once all four tickets (SAFE-001, OPS-003, OPS-004, CFG-001) were done — see that epic's own `INDEX.md`.

## Ticket ID prefixes in this directory

`STRESS` (epic 49), `COORD` (epic 53) are the prefixes still represented here. Prefixes for fully-shipped epics (`BOOT / CORE / STORE / CLI / PROJ / RES / GO / REG / GIT / NORM / HASH / CHUNK / GEN / EMB / VEC / VAL / PLAN / RET / MCP / OPS / WATCH / DESC / GRAPH / VERIFY / GC / VALID / POS / FIX / MONO / BOUND / SCOPE / STRUCT / POINT / SAFE / CFG`) have moved to [../completed/README.md](../completed/README.md).
