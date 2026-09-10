# Epic: Core Domain and Storage

Establish authoritative local state before implementing network acquisition. Critical path: domain types → bbolt schema → Badger object store → storage integration test. This epic produces the persistent local state every later subsystem builds on.

## Tickets
- [CORE-001 — Domain types](CORE-001-domain-types.md): plain Go structs for Project, Dependency, KnowledgeObject, Generation, etc. in `internal/domain`, no external deps.
- [CORE-002 — Lifecycle enums](CORE-002-lifecycle-enums.md): `GenerationState` and `JobState` enums with tested transition rules.
- [STORE-001 — bbolt store](STORE-001-bbolt-store.md): control-plane store — projects, references, generations, jobs, active pointers.
- [STORE-002 — Schema versioning](STORE-002-schema-versioning.md): explicit schema version + minimal migration framework for bbolt.
- [STORE-003 — Badger object store](STORE-003-badger-object-store.md): data-plane store — knowledge objects, chunks, manifests, embedding metadata.
- [STORE-004 — Storage integration boundary](STORE-004-storage-integration-boundary.md): first persistence checkpoint proving bbolt+Badger survive a restart together.
