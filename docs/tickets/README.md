# ragctl — Tickets

Source: `ragctl_implementation_plan.md` + `ragctl_design_spec.md`.

Tickets are split into three directories:

- **[completed/](completed/README.md)** — epics whose every ticket is fully shipped (`Status: done`, every Acceptance Criteria box checked), verified against `docs/architecture.md`. Kept for history and cross-referencing, not for picking up new work.
- **[planned/](planned/README.md)** — what's left of the v0.1 critical path (status/doctor and watch mode haven't started — the two CLI-skeleton stubs), plus post-v0.1 epics added from real usage and architecture review. A mixed epic (some tickets done, some not) stays here, not in `completed/`, until every ticket in it is done.
- **[backlog/](backlog/README.md)** — deferred scope: additional ecosystems, additional vector backends, evaluation/observability/security hardening, curated acquisition, and optional integrations. The source plan is explicit that these wait until the Go-only vertical slice is stable and someone has used the system themselves — don't start pulling these forward just because a ticket exists for them.

Every ticket, in any of the three directories, follows the same self-contained format (Goal, Non-goals, Simplicity constraints, Design, Inputs/Outputs, Failure behavior, Tests, Acceptance criteria) and carries a task ID matching the implementation plan (`BOOT-001`, `GO-002`, etc.) so it can be cross-referenced back to its source milestone. An epic only moves to `completed/` once every ticket inside it is individually verified `Status: done` — closing a ticket doesn't always mean the original sketch got built literally, though: `docs/tickets/completed/02-core-domain-storage`'s `CORE-001`/`STORE-001` closed by reconciling the ticket to a shape later epics actually proved out (two originally-sketched types formally declined as superseded, one relational split formally declined as a worse fit than what shipped), not by force-completing an obsolete checklist. See that epic's tickets and `docs/architecture.md`'s "Epics 1-2" section for the reasoning — the distinction between "genuinely incomplete" and "spec drift from real implementation experience" is worth preserving when you hit it again.

**Guiding rule across all tickets:** keep implementations as simple as possible. Build one reference implementation per interface before adding more (one resolver, one vector backend, one embedder). Do not build the interface abstraction until a second implementation is imminent.
