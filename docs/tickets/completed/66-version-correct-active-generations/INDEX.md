# Epic: Version-correct active generations

**Status: done (2026-10-02).** All four tickets shipped and passed the whole-epic acceptance run below against real containers, plus a real migration from an old-binary database. The run found and fixed one more gap, GC being unable to retire an unreferenced version (see `STORE-005`).

Added 2026-10-02 from `VEC-016`'s real-container run, which found that ragctl's control store could keep only one active version of a dependency at a time, and that the planner didn't notice when a project's version changed. See [ADR-012](../../adr/ADR-012-active-generation-per-dependency-version.md) for the invariant this epic establishes: **an active generation is unique per dependency version, not per dependency.**

- [STORE-005](STORE-005-per-version-active-pointers.md) — key active-generation pointers by version, with a one-time migration and no version-less API.
- [PLAN-004](PLAN-004-version-correct-planning.md) — a dependency version change triggers a build of the new version (bug #1).
- [PLAN-005](PLAN-005-retry-unbuilt-versions.md) — a referenced version with no active generation is retried on the next sync, except versions recorded as having no docs source (bug #2).
- [OPS-008](OPS-008-vector-readiness-recheck.md) — vector-backend (and embedding) readiness re-probes instead of trusting the daemon's startup check (bug #3).

Build order: `STORE-005` → `PLAN-004` → `PLAN-005` → `OPS-008`. `PLAN-004` must not ship without `STORE-005`: on the old one-pointer-per-dependency model, fixing the planner alone makes two projects on different versions supersede each other on every sync.

## Acceptance (whole epic), against real containers
- Project A on `uuid` v1.5.0 and project B on v1.6.0, both synced: A's searches return only v1.5.0, B's only v1.6.0; re-syncing either rebuilds nothing; GC removes neither.
- Drop A's reference and let its grace period expire: GC removes v1.5.0 and leaves v1.6.0 untouched.
- Project C on v1.4.0 synced while the vector store is down fails cleanly; once the store is back, a **plain** `sync` (no `--rebuild`) builds v1.4.0.
