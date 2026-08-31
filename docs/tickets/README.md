# ragctl — Tickets

Source: `ragctl_implementation_plan.md` + `ragctl_design_spec.md`.

Tickets are split into two directories by how close they are to the v0.1 critical path:

- **[planned/](planned/README.md)** — epics 01-19. This is the build order: bootstrap through MCP server gets you the full vertical slice the implementation plan calls the first externally-demoable release (a coding agent querying exact dependency-version docs via MCP). Epics 18-19 (status/doctor, watch mode) round out v0.1 usability. Work through these in order.
- **[backlog/](backlog/README.md)** — epics 20-32. Additional ecosystems, additional vector backends, evaluation/observability/security hardening, and optional integrations. The source plan is explicit that these wait until the Go-only vertical slice is stable and someone has used the system themselves — don't start pulling these forward just because a ticket exists for them.

Every ticket, in either directory, follows the same self-contained format (Goal, Non-goals, Simplicity constraints, Design, Inputs/Outputs, Failure behavior, Tests, Acceptance criteria) and carries a task ID matching the implementation plan (`BOOT-001`, `GO-002`, etc.) so it can be cross-referenced back to its source milestone.

**Guiding rule across all tickets:** keep implementations as simple as possible. Build one reference implementation per interface before adding more (one resolver, one vector backend, one embedder). Do not build the interface abstraction until a second implementation is imminent.
