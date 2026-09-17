# CORE-002: Define lifecycle enums

**Epic:** Core Domain and Storage
**Status:** done
**Depends on:** CORE-001
**Estimated size:** small

## Goal
Define the `Generation` state machine and `Job` state machine as typed enums with explicit, tested transition rules.

## Non-goals
- No persistence of these states yet (see STORE-001).
- No scheduling/execution logic for jobs (see later job-system tickets).

## Simplicity constraints
- Implement the state machine as a simple `map[State][]State` (or switch statement) of allowed transitions — do not build a generic FSM library/DSL.

## Design
Package: `internal/domain/` (same package as CORE-001, e.g. `lifecycle.go`).

```go
type GenerationState string
const (
    GenDiscovered  GenerationState = "DISCOVERED"
    GenPlanned     GenerationState = "PLANNED"
    GenAcquiring   GenerationState = "ACQUIRING"
    GenNormalizing GenerationState = "NORMALIZING"
    GenIndexing    GenerationState = "INDEXING"
    GenValidating  GenerationState = "VALIDATING"
    GenReady       GenerationState = "READY"
    GenActive      GenerationState = "ACTIVE"
    GenFailed      GenerationState = "FAILED"
    GenSuperseded  GenerationState = "SUPERSEDED"
    GenGCEligible  GenerationState = "GC_ELIGIBLE"
    GenDeleted     GenerationState = "DELETED"
)

func ValidGenerationTransition(from, to GenerationState) bool

type JobState string
const (
    JobPending   JobState = "PENDING"
    JobRunning   JobState = "RUNNING"
    JobRetry     JobState = "RETRY"
    JobSucceeded JobState = "SUCCEEDED"
    JobFailed    JobState = "FAILED"
    JobCancelled JobState = "CANCELLED"
)

func ValidJobTransition(from, to JobState) bool
```
Transition graph for generations follows the linear pipeline in the design spec (DISCOVERED → PLANNED → ACQUIRING → NORMALIZING → INDEXING → VALIDATING → {READY, FAILED} → ACTIVE → SUPERSEDED → GC_ELIGIBLE → DELETED), with FAILED reachable from any in-progress state.

## Inputs / Outputs
- Input: a `(from, to)` state pair.
- Output: bool (valid transition) — callers (STORE-001) reject invalid transitions with an error.

## Failure behavior
Pure functions — no I/O, no errors returned from the transition-check functions themselves; callers translate `false` into a typed error.

## Tests
- Table test enumerating every valid transition in the state diagrams and asserting `true`.
- Table test asserting a representative set of invalid transitions (e.g. `ACTIVE → DISCOVERED`, `SUCCEEDED → PENDING`) return `false`.

## Acceptance criteria
- [x] Both enums and their constants exist.
- [x] Invalid transitions are rejected by unit tests (not just by convention).
