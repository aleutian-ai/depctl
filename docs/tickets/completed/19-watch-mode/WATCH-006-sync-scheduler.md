# WATCH-006: Per-project sync scheduler

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-005
**Estimated size:** small

## Goal
Add a tiny in-memory scheduler inside the daemon. Every sync request goes through it, whether it comes from a watched file change (WATCH-007) or from `depctl sync` / MCP `sync_project` (WATCH-008, WATCH-010). It guarantees one sync per project at a time, collapses repeated requests, and runs exactly one follow-up when something changes mid-sync.

## Non-goals
- Durability. Pending work is lost when the daemon stops, and that's fine: the next start (or next file change) resyncs. No bbolt jobs, Redis, Kafka, or durable queue.
- Priorities, retries with backoff, cancellation of a running sync.
- Cross-project concurrency (ADR-011 §6 serializes globally for v1).

## Simplicity constraints
- One mutex-guarded `map[projectID]*projectState` plus one global run lock. No generic job/workflow abstraction.
- The scheduler takes the sync function as a parameter (`func(ctx, projectID string, opts SyncOptions) (api.SyncResult, error)`), so it's unit-testable without stores.

## Design
Package `internal/daemon`.

```go
type SyncOptions struct {
    Dependency string // "" = whole project
    Offline    bool
    Force      bool
    Resolve    bool   // re-resolve before syncing (watch-triggered requests)
}

type projectState struct {
    syncing bool
    dirty   bool
    pending SyncOptions     // merged options for the follow-up
    waiters []chan syncDone // requests satisfied by the next run
}

// Request queues a sync for projectID and returns a channel that
// receives the result of the run that covers this request.
func (s *Scheduler) Request(projectID string, opts SyncOptions) <-chan syncDone
```

State machine per project:
```text
idle    + request → syncing (start a run with opts)
syncing + request → dirty=true, merge opts into pending, append waiter
syncing + run ends → if dirty: dirty=false, start one follow-up with pending
                                   (its waiters get the follow-up's result)
                     else: idle
```

Rules:
- **Merging options into one follow-up:**
  - `Force` and `Resolve` are each set if any request asked for them.
  - `Offline` is set only if every request asked for it.
  - `Dependency` is kept only if every request named the same one; otherwise it's `""` (whole project).
- **Global serialization:** a run takes the global lock before calling the sync function. Two projects can both be "syncing" from the scheduler's point of view, but only one run executes at a time. The lock is a separate concern from per-project state, so allowing concurrency later only means removing it.
- **Isolation:** runs execute in their own goroutine with `recover`. A sync that returns an error or panics is logged with the project ID and delivered to its waiters as an error, and the project returns to idle (or runs its follow-up). The daemon keeps running.
- **Shutdown:** the scheduler stops starting new runs, and the running one finishes with a context detached from the shutdown signal (`context.WithoutCancel`, as `depctl watch` does today). Queued follow-ups and their waiters get `ErrShuttingDown`.
- `api.Status` gains a per-project `sync_state` (`idle` / `syncing` / `syncing+dirty`) from the scheduler's snapshot.

## Inputs / Outputs
- Input: `Request(projectID, opts)` calls.
- Output: calls to the injected sync function; results on waiter channels; log lines.

## Failure behavior
- Sync function error or panic: logged; waiters receive the error; scheduler state stays consistent.
- A request for an unknown project is not the scheduler's concern. The sync function returns its normal "project not found" error.

## Tests
All tests use a fake sync function gated by channels.
- Five requests while idle-then-syncing run the sync exactly twice: the first run, then one collapsed follow-up.
- A request arriving mid-sync triggers exactly one follow-up; with no mid-sync request there is no follow-up.
- Option merging for the follow-up: force/resolve OR, offline AND, dependency kept only when identical.
- Two projects requested together never run concurrently (a counter asserts max in-flight is 1).
- A failing or panicking sync for project A is reported to A's waiters, and project B's queued sync still runs.
- Shutdown during a run: the run completes, and queued waiters get `ErrShuttingDown`.
- Run under `-race`.

## Acceptance criteria
- [ ] `idle` / `syncing` states and the `dirty` flag behave exactly as the state machine above.
- [ ] Repeated requests collapse; a change during sync causes exactly one follow-up.
- [ ] One sync per project; globally serialized.
- [ ] A failed sync never stops the scheduler or the daemon.
- [ ] No durable queue, no new dependency.

## Post-implementation note
`internal/daemon/scheduler.go`'s `Scheduler` implements this, and was later generalized beyond sync alone: GC now shares the same global lock and collapsing machinery (`RequestGC`/`gcState`), since GC reading `references` state a concurrent sync could be changing is a real correctness requirement, not just v1 simplicity. Every run (sync or GC) is bounded by `maxActionDuration` so a hung one can't wedge the lock shut permanently. See `docs/scratch/action-controller-proposal.md`.
