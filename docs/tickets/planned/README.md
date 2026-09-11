# ragctl — Planned Tickets (v0.1 critical path, in progress)

Source: `ragctl_implementation_plan.md` + `ragctl_design_spec.md`. See also [../completed/README.md](../completed/README.md) for fully-shipped epics and [../backlog/README.md](../backlog/README.md) for deferred epics (additional ecosystems, additional vector backends, eval/observability/security, optional integrations).

Each numbered directory is an epic. Each epic has an `INDEX.md` with the epic's goal and a linked list of its tickets. Every ticket is a self-contained spec (goal, non-goals, simplicity constraints, design, tests, acceptance criteria) — implementable without re-reading the source docs. A ticket's `**Status:**` line is `planned` until every one of its Acceptance Criteria boxes is genuinely checked, at which point it becomes `done` — see `docs/architecture.md` for the authoritative narrative of what's actually shipped.

**Guiding rule across all tickets:** keep implementations as simple as possible. Build one reference implementation per interface before adding more (one resolver, one vector backend, one embedder). Do not build the interface abstraction until a second implementation is imminent.

## Build order — what's left here

Epics 01 through 20 (bootstrap through describe) shipped, completing the original v0.1 build order, and live in [../completed/](../completed/README.md), except epic 19, which reopened (below) — epic 02 (core-domain-storage) was the last of these to close, reconciled to what actually shipped rather than the original sketch (see `completed/02-core-domain-storage/INDEX.md` and `docs/architecture.md`'s "Epics 1-2" section for how). What remains in this directory:

At the end of what's now `completed/17-mcp-server`, the system met Milestone E (`v0.0.5-agent`) from the implementation plan: a coding agent can query exact dependency version docs via MCP.

Epics 21-22 (and epic 19's WATCH-004..011) were added after v0.1, outside the original implementation-plan build order — real usage and an architecture review (`docs/scratch/ragctl_architecture_eval_next_steps-2.md`) surfaced concrete gaps worth closing before moving further into backlog scope.

19. [19-watch-mode](19-watch-mode/INDEX.md): WATCH-001..003 (`ragctl watch`) shipped. WATCH-004..011 replace direct multi-process store access with a single-owner daemon (`ragctl daemon run`, local HTTP/JSON over a Unix socket). Watch moves into the daemon, `serve` becomes a stdio MCP proxy, and store-touching CLI commands become daemon clients. Motivated by `serve` holding the bbolt lock for every agent session, which blocks watch and the CLI. See ADR-011 (WATCH-004).
21. [21-structural-preservation](21-structural-preservation/INDEX.md) — Markdown heading paths/breadcrumbs on chunks, structured fenced-code-block preservation, self-describing chunk metadata, breadcrumb output over MCP. Sourced from `docs/scratch/ragctl_architecture_eval_next_steps-2.md` §6.1/6.2/8.1 — the lowest-cost, highest-confidence structural-preservation work.
22. [22-orphan-lifecycle-gc](22-orphan-lifecycle-gc/INDEX.md) — detect and reclaim failed/interrupted generations that the existing reference-based `ragctl gc` (`completed/16-retention-gc`) can't see, since they never held a real reference. A real correctness gap, not an optimization — sourced from `docs/scratch/ragctl_architecture_eval_next_steps-2.md` §11.

## Ticket ID prefixes in this directory

`WATCH` (epic 19, reopened), `STRUCT` (epic 21) and `GC` (epic 22, distinct from the existing `RET`-prefixed reference-based retention/GC in `completed/16-retention-gc`) are new prefixes. Prefixes for fully-shipped epics (`BOOT / CORE / STORE / CLI / PROJ / RES / GO / REG / GIT / NORM / HASH / CHUNK / GEN / EMB / VEC / VAL / PLAN / RET / MCP / OPS / DESC`) have moved to [../completed/README.md](../completed/README.md).
