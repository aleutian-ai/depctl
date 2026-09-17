# STORE-001: Build bbolt control-plane store

**Epic:** Core Domain and Storage
**Status:** done
**Depends on:** CORE-001, CORE-002
**Estimated size:** large

## Goal
Implement the bbolt-backed control-plane store: small, strongly structured state — projects, dependency references, generations, active-generation pointers, jobs, reference counts, retention metadata.

## Non-goals
- No large content storage (that's Badger, see STORE-003).
- No schema migrations beyond declaring version 1 (see STORE-002).
- No remote/network calls from within this package — pure local storage.

## Simplicity constraints
- One bbolt file, one `Store` struct wrapping `*bbolt.DB`. Do not build a generic repository/ORM abstraction over bbolt — write direct, explicit methods per entity as listed below.
- Do not introduce a service-layer interface here; STORE-004 decides if one is needed later. Keep this a concrete type.

## Design
Package: `internal/control/bbolt`

```go
type Store struct { db *bbolt.DB }

func Open(path string) (*Store, error)
func (s *Store) Close() error

func (s *Store) PutProject(ctx context.Context, p domain.Project) error
func (s *Store) GetProject(ctx context.Context, id string) (domain.Project, error)
func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error)

func (s *Store) PutResolution(ctx context.Context, projectID string, r domain.Resolution) error
func (s *Store) GetResolution(ctx context.Context, projectID string) (domain.Resolution, error)

func (s *Store) PutGeneration(ctx context.Context, g domain.Generation) error
func (s *Store) GetGeneration(ctx context.Context, id string) (domain.Generation, error)
func (s *Store) SetActiveGeneration(ctx context.Context, dependencyID, backendID, generationID string) error
func (s *Store) GetActiveGeneration(ctx context.Context, dependencyID, backendID string) (string, error)

func (s *Store) AddReference(ctx context.Context, ref domain.VersionReference) error
func (s *Store) RemoveReference(ctx context.Context, dependencyID, version, projectID string) error
func (s *Store) CountReferences(ctx context.Context, dependencyID, version string) (int, error)

func (s *Store) PutJob(ctx context.Context, j domain.SyncJob) error
func (s *Store) ClaimJob(ctx context.Context, jobType string, leaseDuration time.Duration) (domain.SyncJob, bool, error)
func (s *Store) UpdateJob(ctx context.Context, j domain.SyncJob) error

func (s *Store) PutBackendReplica(ctx context.Context, r domain.BackendReplica) error
func (s *Store) GetBackendReplica(ctx context.Context, generationID, backendID string) (domain.BackendReplica, error)
```

Buckets (created on `Open` if missing):
```
meta
projects
project_dependencies
dependency_versions
knowledge_sources
generations
active_generations
backend_replicas
jobs
references
retention
migrations
```

Key patterns (byte-sortable, canonicalized package names — escape `/` etc.):
```
projects/<project-id>
project_dependencies/<project-id>/<ecosystem>/<name>
dependency_versions/<ecosystem>/<name>/<version>
active_generations/<dependency-id>/<backend-id>
references/<dependency-id>/<version>/<project-id>
jobs/<job-id>
```
Values are small JSON records (debuggability over compactness for v1, per the design spec).

`SetActiveGeneration` must run inside a single `db.Update` (write) transaction that also flips the prior active generation's state to `SUPERSEDED` — this is the atomic promotion primitive other tickets (VAL-004) build on.

Never perform network/HTTP/Git/embedding calls inside a bbolt transaction — all such work happens before a `Store` method is called.

## Inputs / Outputs
- Input: domain objects from `internal/domain`.
- Output: persisted state on disk at the configured bbolt path; readback via getters.

## Failure behavior
- `Open` on a corrupt/locked file returns a wrapped error, not a panic.
- Not-found lookups return a typed `ErrNotFound` sentinel (`errors.Is` compatible) rather than a generic string error, so callers can distinguish "missing" from "storage failure".
- Invalid state transitions (per CORE-002) return a typed `ErrInvalidTransition`.

## Tests
- Use `t.TempDir()` for a temp DB per test.
- Transaction rollback: force an error mid-write and assert no partial state persisted.
- `SetActiveGeneration` swap occurs in one write transaction (verify prior ACTIVE becomes SUPERSEDED and new becomes ACTIVE atomically).
- Reopen test: write data, `Close()`, `Open()` the same path again, verify data present.
- Reference counting: two references for the same dependency version, remove one, `CountReferences` returns 1 (not 0).

## Acceptance criteria
- [x] All buckets created on first `Open`.
- [x] Unit tests use a temp DB.
- [x] Transaction rollback verified by test.
- [x] Active-generation pointer swap occurs in one write transaction.
- [x] Database can reopen after process restart with data intact.
- [x] Behavioral criteria the shipped design actually satisfies (revised from "matches the original bucket/key sketch line-for-line" — see Post-implementation note): project resolution round-trips; active-generation promotion is atomic; references work; jobs persist; backend replicas persist; restart survives (STORE-004 proves this across both stores together).

## Post-implementation note
Reconciled against what actually shipped, not the original bucket/method sketch, per the same reasoning as CORE-001's amendment.

**What shipped, matching this ticket's actual goal** (a control-plane store for projects, dependency references, generations, active pointers, jobs, reference counts, retention metadata — all of it): `PutProject`/`GetProject`/`ListProjects`; `PutResolution`/`GetResolution`; `PutGeneration`/`GetGeneration`; `PromoteGeneration`/`GetActiveGeneration` (an atomic single-transaction swap, added under VEC-003 in epic 13 once validation/promotion needed one — the design's sketched `SetActiveGeneration`/`GetActiveGeneration(dependencyID, backendID)` signature shape changed, but the atomicity property this ticket required is exactly what shipped); `AddReference`/`RemoveReference`/`CountReferences`/`ListReferences`/`ListAllReferences`/`DeleteAllReferences` (added under RET-001 in epic 16, keyed `<ecosystem>|<package>|<version>|<project-id>|<reason>` rather than this ticket's sketched `references/<dependency-id>/<version>/<project-id>`, since a version needed to carry multiple simultaneous references — from different projects and from non-project reasons like `"latest"`/`"manual_pin"` — which the original single-reference-per-key sketch didn't anticipate); `PutJob`/`GetJob` (added under RET-004 in epic 16, against the generalized `domain.Job` type rather than this ticket's sketched `SyncJob` with `ClaimJob`/lease semantics — job claiming ended up keyed by a deterministic ID (`gc.JobID`, BLAKE3 over type+dependency+version) for idempotent resume instead of a lease/claim model); `PutBackendReplica`/`GetBackendReplica` (epic 13).

**The one thing that was never built, and is being formally declined rather than left as a silent gap**: the fuller relational split across `project_dependencies`/`dependency_versions` buckets this ticket originally sketched. `Resolution` is stored as one JSON record per project in `project_dependencies`, not decomposed into normalized per-dependency rows. This is not being refactored to match the original drawing. bbolt is not a relational database, and the dominant real access pattern is "give me this project's complete resolution" (`GetResolution`, called by `computePlans`/`runSync`/`describe`) — one serialized `Resolution` is arguably the better physical representation for that access pattern, not a worse one. If a future need arises for a query the reference index can't already answer efficiently (e.g. "enumerate every project depending on Kubernetes in version range X–Y" without reading through `references`), that's when a normalized index earns itself — not before.

