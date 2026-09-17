# CORE-001: Define core domain types

**Epic:** Core Domain and Storage
**Status:** done
**Depends on:** BOOT-002
**Estimated size:** medium

## Goal
Define the foundational Go domain types shared by every later subsystem (resolvers, storage, planner, backends), with zero external dependencies.

## Non-goals
- No persistence logic (no bbolt/Badger imports here).
- No HTTP, Git, or network code.
- No vector-backend-specific structs (those live in the backend adapter packages later).

## Simplicity constraints
- Keep types as plain structs with simple field types (`string`, `time.Time`, `[]byte`, slices/maps of the above). No generics, no builder patterns, no interfaces beyond what's strictly needed for validation helpers.
- Resist adding fields "for later" — only include fields the plan explicitly names for v0.1.

## Design
Package: `internal/domain/`

Types (fields per the design spec's Go type examples; extend minimally where the plan lists a field not shown in the code sample):
```go
type Ecosystem string
const (
    EcosystemGo     Ecosystem = "go"
    EcosystemPython Ecosystem = "python"
    EcosystemNode   Ecosystem = "node"
    EcosystemRust   Ecosystem = "rust"
    EcosystemJava   Ecosystem = "java"
)

type Project struct {
    ID        string
    Root      string
    CreatedAt time.Time
    UpdatedAt time.Time
}

type Dependency struct {
    Ecosystem Ecosystem
    Name      string
    Direct    bool
}

type DependencyVersion struct {
    Dependency Dependency
    Version    string
    ResolvedBy string
    Checksum   string
}

// Resolution is the canonical output of a Resolver.Resolve call (see RES-001)
// and the shape persisted by bbolt's PutResolution/GetResolution (STORE-001).
// Defined here, not in internal/resolver, because storage/planner/every
// ecosystem resolver package needs it and none of them should import each other.
type Resolution struct {
    Ecosystem    Ecosystem
    ManifestPath string
    LockPath     string
    Dependencies []DependencyVersion
    Fingerprint  string // deterministic hash of the resolution inputs, for no-op detection
    Warnings     []string
}

type ProjectDependency struct {
    ProjectID  string
    Dependency DependencyVersion
    Direct     bool
}

// KnowledgeSource, as originally sketched here, was never built — see
// this ticket's Post-implementation note. registry.Source
// (internal/registry, REG-001) turned out to be the right shape: a
// source definition is registry/configuration data, not domain runtime
// state, and generation.Build (GEN-002) consumes []registry.Source
// directly with no domain-level wrapper.

type SourceSnapshot struct { /* raw metadata, raw URI/path, snapshot ID, fetched-at */ }

type KnowledgeObject struct {
    ID, Dependency (DependencyVersion), SourceID, SourceURI, SourceType,
    ContentType, LogicalPath, Title, Heading, Language, Version, Commit string/typed
    Authority   int
    Content     []byte
    ContentHash string
    Metadata    map[string]string
}

type Chunk struct { /* ID, ObjectID, ordinal, content, content hash */ }

type Generation struct { /* ID, Dependency, State, timestamps, manifest hash */ }

type BackendReplica struct {
    ID             string // ULID
    GenerationID   string
    BackendName    string
    EmbeddingModel string
    Dimensions     int
    Status         string // e.g. "pending", "replicating", "complete", "failed"
    PointCount     int
    LastError      string
    UpdatedAt      time.Time
}

type VersionReference struct {
    ProjectID, Ecosystem, Package, Version, Reason string
    FirstSeenAt, LastSeenAt time.Time
}
// Actually landed later, under RET-001 (epic 16), once retention needed
// it — not by this ticket directly. See Post-implementation note.

// SyncJob, as originally sketched here, was never built — see this
// ticket's Post-implementation note. RET-004 (epic 16) generalized
// CORE-002's JobState enum into one domain.Job type shared by every kind
// of persisted background work (sync, GC), rather than a sync-specific
// SyncJob type.
```
Ecosystem identifiers are the canonical lowercase strings above — never free-form.

Add a `Validate() error` method on types that have obvious invariants (`Project`, `Dependency`, `DependencyVersion`) rather than a generic validation framework.

## Inputs / Outputs
- Input: none — these are pure data definitions.
- Output: `internal/domain` package importable by all other packages.

## Failure behavior
`Validate()` methods return plain `error` (wrapped with `fmt.Errorf`), no custom error types needed yet.

## Tests
- Table-driven unit tests for each `Validate()` method covering valid and invalid inputs.
- Test that package identity (ecosystem + name + version) serializes deterministically (e.g. via `String()` or JSON marshal round-trip).

## Acceptance criteria
- [x] `internal/domain` has no imports of bbolt, Badger, net/http, or os/exec.
- [x] Every domain type required by a shipped subsystem exists with the fields that subsystem actually uses; sketched-but-superseded types (`KnowledgeSource`, `SyncJob`) are documented as superseded, not built as unused duplicates. (Revised from "all listed types exist with the documented fields" — see Post-implementation note; this ticket's own Simplicity constraints already said "resist adding fields for later," and the same reasoning applies to whole types once a later epic proves a different shape is the actual right one.)
- [x] Validation helpers exist where invariants are obvious (non-empty name, canonical ecosystem, etc.).
- [x] Package identities serialize deterministically (stable field ordering / string form).

## Post-implementation note
Reconciled against what actually shipped, not the original 2025-era sketch, rather than building the two remaining types just to make an obsolete checklist green:

- **`VersionReference`**: exists (`internal/domain/domain.go`), with the fields this ticket sketched. It arrived later, under RET-001 (`docs/tickets/completed/16-retention-gc`), once reference-counting actually needed it — not added by this ticket directly, but the type this ticket specified is the type that shipped.
- **`KnowledgeSource`**: never built, and shouldn't be. The working generation pipeline (`generation.Build`, GEN-002) consumes `[]registry.Source` directly — that's the real shape a registry match produces, and it's registry/configuration data, not domain runtime state. Adding a parallel `domain.KnowledgeSource` now would just be a second, unused representation of the same thing.
- **`SyncJob`**: never built, and shouldn't be. `domain.Job`/`JobState` (CORE-002, generalized further by RET-004 in epic 16) already represents persisted background work generically — sync and GC jobs both use it. A sync-specific `SyncJob` type would duplicate that for no reason.

`ProjectDependency` and `SourceSnapshot` also aren't standalone stored types the way originally sketched — `Resolution.Dependencies` (`[]DependencyVersion`) covers what `ProjectDependency` was for, and `SourceSnapshot` is consumed transiently by normalizers (`internal/normalize`) rather than persisted. Neither gap is worth closing: nothing reads or writes either shape today.

