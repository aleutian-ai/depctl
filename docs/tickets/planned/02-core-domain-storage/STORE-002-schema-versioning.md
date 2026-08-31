# STORE-002: Schema versioning

**Epic:** Core Domain and Storage
**Status:** planned
**Depends on:** STORE-001
**Estimated size:** small

## Goal
Introduce an explicit schema version for the bbolt control-plane database and a minimal migration framework, even though only version 1 exists today.

## Non-goals
- No actual migration logic beyond the identity/no-op migration for v1 — there is nothing to migrate yet.
- No Badger schema versioning (Badger's keyspace is versioned separately if/when needed; out of scope here).

## Simplicity constraints
- The "migration framework" can be a single ordered slice of `func(*bbolt.Tx) error` functions keyed by target version — do not build a generic pluggable migration engine before there is a second migration to run.

## Design
Package: `internal/control/schema` (or a `schema.go` file inside `internal/control/bbolt`).

```go
const CurrentSchemaVersion = 1

type Migration struct {
    Version int
    Apply   func(tx *bbolt.Tx) error
}

var Migrations = []Migration{
    {Version: 1, Apply: func(tx *bbolt.Tx) error { /* create meta bucket, no-op body */ return nil }},
}

func EnsureSchema(db *bbolt.DB) error // reads meta/schema_version, applies pending migrations in order, writes new version
```
`meta` bucket key `schema_version` stores the current integer version as its value.

If the on-disk version is higher than `CurrentSchemaVersion` (a newer binary wrote it), `EnsureSchema` must return an explicit "unsupported schema version, upgrade ragctl" error rather than attempting to proceed.

`ragctl doctor` (later ticket, OPS-002) will surface this version — this ticket only needs to make it queryable via a `Store.SchemaVersion(ctx) (int, error)` method.

## Inputs / Outputs
- Input: an open bbolt `*bbolt.DB` handle.
- Output: schema at `CurrentSchemaVersion`, or an error.

## Failure behavior
- Future/unsupported schema version → explicit typed error, `Open` fails loudly rather than silently reinterpreting data.
- A migration function returning an error aborts `EnsureSchema` inside the same transaction (no partial migration state persisted).

## Tests
- Fresh DB: `EnsureSchema` writes `schema_version = 1`.
- DB with `schema_version` set to a future number (e.g. 99): `EnsureSchema` returns an error, does not modify data.
- Migration functions are idempotent: running `EnsureSchema` twice on an already-migrated DB is a no-op and does not error.

## Acceptance criteria
- [ ] Unsupported future schema produces an explicit error.
- [ ] Migration functions are idempotent.
- [ ] `Store.SchemaVersion` returns the current version for use by `ragctl doctor` later.
