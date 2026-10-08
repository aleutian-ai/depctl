# Epic: Describe

`depctl describe` — read-only visibility into what knowledge depctl actually has: which packages, how many sources, how much content, how it's trusted, whether it's synced or stale. Not part of the original v0.1 build order (`docs/tickets/planned/README.md`'s epics 01-19) — added after real usage (epics 11-17, dogfooding a real 20-repo corpus) surfaced that there was no way to answer "what do I have" without hand-inspecting bbolt/Badger/registry files directly.

Distinct from `depctl status`/`depctl doctor` (epic 18): those report fleet *health* (project counts, job queues, backend up/down). `describe` reports corpus *contents* (which packages, which sources, how rich).

## Tickets
- [DESC-001](DESC-001-depctl-describe.md) — `depctl describe` (fleet-wide table + per-package drill-down) and `--html`.

## Non-goals for this epic (v1)
- Source liveness/health checks — that's a registry-side concern, see `docs/tickets/backlog/34-registry-coverage`.
- Interactive/queryable HTML (search, filter, sort) — v1's `--html` is a static file. See `docs/tickets/backlog/36-describe-advanced`.
- Content preview (showing actual chunk text) — advanced backlog.
