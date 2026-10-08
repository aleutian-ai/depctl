# OPS-001: `depctl status`

**Epic:** Status and Doctor
**Status:** done
**Depends on:** storage (STORE-001, STORE-003), jobs, backend adapter (VEC-002)
**Estimated size:** small

## Goal
Implement `depctl status`, giving a human (and machine, via `--json`) a snapshot of system health and activity.

## Non-goals
- Deep diagnostics/remediation — that's OPS-002 (`doctor`).

## Simplicity constraints
- Pure read-only aggregation over existing store APIs; no new persisted state, no background stats collector/cache.
- Text and JSON output share one internal struct — do not maintain two separate formatting code paths beyond the final marshal/print step.

## Design
Package: `internal/ops` + `cmd/depctl` `status` command.

```go
type Status struct {
    Projects              int       `json:"projects"`
    DependencyReferences  int       `json:"dependency_references"`
    ActiveGenerations     int       `json:"active_generations"`
    Jobs                  JobStats  `json:"jobs"`
    StorageBboltBytes     int64     `json:"storage_bbolt_bytes"`
    StorageBadgerBytes    int64     `json:"storage_badger_bytes"`
    Backend               BackendStatus `json:"backend"`
    LastSync              *time.Time `json:"last_sync"`
}
type JobStats struct { Pending, Running, Failed int }
type BackendStatus struct { Name string; Healthy bool }
```

`depctl status` prints a text table of the `Status` fields above, one row per field, following the plain `fmt.Fprintf` table style already used by `internal/cli/deps.go` (not `describe.go`'s `text/tabwriter`+template approach — this command is simpler than that). `depctl status --json` marshals the same `Status` struct straight to JSON via `encoding/json`, field names exactly as tagged above.

## Inputs / Outputs
- Input: none (reads bbolt/Badger/backend state).
- Output: stdout text table or JSON.

## Failure behavior
- If the vector backend health check fails/times out, `BackendStatus.Healthy=false` is reported, not a command-level error — status must always be able to report itself, degraded or not.

## Tests
- `--json` output round-trips through `json.Unmarshal` into the `Status` struct.
- Status reflects known fixture state (N projects, M generations) accurately.
- Backend down → command still exits 0 and reports `healthy: false` for the backend.

## Acceptance criteria
- [x] Text and `--json` output both implemented.
- [x] Reports project count, dependency reference count, active generations, pending/failed jobs, bbolt/Badger size, backend health, last sync time.

## Post-implementation note

Shipped in `internal/cli/status.go`, not a new `internal/ops` package: commands live in `internal/cli` (there is no per-command code under `cmd/depctl`, which is just `main.go`), and every input `status` needs — `openControlStore`, `loadDepctlConfig`, `buildVectorBackend`, the data-dir path helpers — is already there. `describe` set the same precedent. The `Status`/`JobStats`/`BackendStatus` shapes and JSON tags match the design above exactly.

Decisions the design left open:
- **Last sync** has no persisted source, and the "no new persisted state" constraint rules out adding one. It is the newest `UpdatedAt` among the configured backend's active generations — the last time a sync changed what queries see. A no-op sync changes nothing, so there's nothing better to derive from.
- **Active generations** counts the configured backend's `active_generations` pointers (new `bboltstore.ListActivePointers`). **Jobs** come from the new `bboltstore.ListJobs`; `RETRY` counts as pending, since it's waiting to run again.
- **Storage bytes** are allocated disk blocks (`diskusage_unix.go`, with an apparent-size fallback on non-Unix builds), not file sizes: an open Badger store has a sparse 2 GB value log, which made apparent size wrong by gigabytes.
- **Backend health** uses `VectorBackend.Health` under a 3-second timeout; any failure, including an unsupported backend name, is `healthy: false` with exit 0, per the failure-behavior section.

Found while building it: `bboltstore.Open` waited forever for the file lock, so `status` (like every command) hung while `depctl serve` was running. `Open` now gives up after 2 seconds with `bboltstore.ErrLocked`. See `docs/architecture.md`'s status/doctor section.

Tests: `internal/cli/status_test.go` — JSON round-trip, fixture counts (including an active pointer on another backend that must not be counted), empty store, backend probe healthy/dead/unsupported, and a command-level run with the backend down that exits 0 and reports `healthy: false`.
