# PLAN-005: Retry referenced versions that were never built

**Epic:** Version-correct active generations
**Status:** done (2026-10-02)
**Depends on:** PLAN-004
**Estimated size:** small

## Goal
A version a project references but that has no active generation gets rebuilt on the next plain `sync`. No `--rebuild` needed. Live-found in `VEC-016`, and the likely cause of the real-user "referenced but never built" incidents (`OPS-005`, `MCP-010`): when a sync records a version's reference and then fails to build it, every later plan sees "reference unchanged" and emits nothing, forever.

The one exception: a version ragctl has *determined* has no usable docs source (no registry manifest and no fallback source) is not retried every sync.

## Design
- **No-source is its own record, not a failed generation.** A failed build and "we looked, no source exists" have different retry semantics, so they're structurally distinct: a separate `no_source_versions` bbolt bucket keyed `ecosystem|dependency|version`, written by `syncVersion` when neither the registry nor the fallback finds a source. It's never inferred from error text, so nothing that retries FAILED generations can pick it up by accident.
- The planner gets the set of known no-source versions as input (it stays pure and network-free). For a referenced version with no active generation:
  - registry has a manifest now → retry (the registry may have gained one since the record was made);
  - known no-source → skip;
  - otherwise → retry, with reason "referenced but not built — retrying".
- A successful build of a version deletes its no-source record, if any.

## Tests
- Planner: reference unchanged + no active generation → `SYNC_VERSION`; known no-source and unmapped → `NOOP`; known no-source but now mapped → `SYNC_VERSION`.
- `syncVersion` with no manifest and no fallback writes a no-source record.

## Post-implementation note (2026-10-02)
No-source is its own bbolt bucket (`no_source_versions`, `internal/control/bbolt/no_source.go`), written by `syncVersion` only when neither the registry nor fallback discovery finds a source, and cleared when that version later builds. The planner (`internal/planner/planner.go`) plans a build whenever a version has no active generation, in every branch including "reference unchanged", unless it's recorded as no-source and still unmapped in the registry. `doctor` now reports no-source versions as a WARN, separate from "stuck", and its stale "no plain sync will ever retry them" advice is gone. Two existing no-op test fixtures turned out to have relied on the bug: they seeded only a reference, which is exactly "referenced but never built", and now promote a real generation to describe a genuine no-op.

**Verified live, in `VEC-016`'s exact failure scenario:** a `--rebuild` attempted while Qdrant was down failed; once Qdrant was back, a plain `sync` built the version with no `--rebuild`, and search returned it.

- [x] After a sync fails to build a version, a plain `sync` retries it.
- [x] A version with no docs source is not retried on every sync.
- [x] No-source state is never derived from a generation's error text.
