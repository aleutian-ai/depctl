# Feature-level docs

Four end-to-end flows that each span multiple `internal/*` packages and several storage layers. `docs/architecture.md` documents these at the CLI-command boundary (one sequence diagram per command, scoped strictly to shipped behavior); these docs go one layer deeper, tracing a single unit of work across package boundaries — including failure/skip paths the per-command diagrams don't show. Package-level detail (types, functions, isolated dataflow) lives in [docs/internal/](../internal/README.md); read both together when tracing a bug or planning a change that crosses package lines.

- [discovery-and-resolution](discovery-and-resolution.md) — `ragctl scan`: how a project is found, its dependencies resolved, and later matched against the knowledge registry. The front door everything else depends on.
- [sync](sync.md) — `ragctl sync`: the full build → replicate → validate → promote pipeline for one dependency version, across bbolt, Badger, and the vector backend. The core write path.
- [garbage-collection](garbage-collection.md) — `ragctl gc`: reference counting, grace periods, and the ordered three-store deletion that reclaims a superseded or dropped version.
- [query-serving](query-serving.md) — `ragctl serve` (MCP): how an agent's question becomes a version-filtered vector search with real content and provenance attached.

## What's out of scope here

`ragctl init`/`config validate`/`project`/`deps`/`describe` are single-package-ish flows already fully covered by `docs/architecture.md`'s own sequence diagrams — a feature doc for each would mostly restate that content. Add one here only when a flow grows enough cross-package complexity that architecture.md's command-level diagram stops being enough to reason about it.
