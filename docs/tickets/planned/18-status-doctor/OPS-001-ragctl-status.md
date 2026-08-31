# OPS-001: `ragctl status`

**Epic:** Status and Doctor
**Status:** planned
**Depends on:** storage (STORE-001, STORE-003), jobs, backend adapter (VEC-002)
**Estimated size:** small

## Goal
Implement `ragctl status`, giving a human (and machine, via `--json`) a snapshot of system health and activity.

## Non-goals
- Deep diagnostics/remediation — that's OPS-002 (`doctor`).

## Simplicity constraints
- Pure read-only aggregation over existing store APIs; no new persisted state, no background stats collector/cache.
- Text and JSON output share one internal struct — do not maintain two separate formatting code paths beyond the final marshal/print step.

## Design
Package: `internal/ops` + `cmd/ragctl` `status` command.

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

`ragctl status` prints the text table shown in the design spec (§74); `ragctl status --json` prints the `Status` struct as JSON (matches design spec §60 example shape).

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
- [ ] Text and `--json` output both implemented.
- [ ] Reports project count, dependency reference count, active generations, pending/failed jobs, bbolt/Badger size, backend health, last sync time.
