# CORE-001: Define core domain types

**Epic:** Core Domain and Storage
**Status:** planned
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

type KnowledgeSource struct {
    ID          string
    ProjectName string
    SourceType  SourceType
    URI         string
    Authority   int
    VersionMode VersionMode
}

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

type SyncJob struct { /* see CORE-002 Job type */ }
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
- [ ] `internal/domain` has no imports of bbolt, Badger, net/http, or os/exec.
- [ ] All listed types exist with the documented fields.
- [ ] Validation helpers exist where invariants are obvious (non-empty name, canonical ecosystem, etc.).
- [ ] Package identities serialize deterministically (stable field ordering / string form).
