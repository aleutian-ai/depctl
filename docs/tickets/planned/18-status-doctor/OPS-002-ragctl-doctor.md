# OPS-002: `ragctl doctor`

**Epic:** Status and Doctor
**Status:** planned
**Depends on:** OPS-001
**Estimated size:** small

## Goal
Implement `ragctl doctor`, running a fixed list of health checks and reporting overall system health via output and exit code.

## Non-goals
- Auto-repair (`ragctl repair`) — mentioned in the design spec as a future command; do not build it here, only diagnose.

## Simplicity constraints
- Checks are a flat, ordered list of `func() CheckResult` — no plugin/registration framework needed for a dozen checks.
- Exit-code logic is a simple max-severity reduction over check results; nothing more elaborate.

## Design
Package: `internal/ops` (extends OPS-001) + `cmd/ragctl` `doctor` command.

```go
type CheckResult struct {
    Name     string
    Severity Severity // OK | Warning | Unhealthy
    Detail   string
}
```

Checks (fixed list):
```text
control DB (bbolt) open
schema version (report + reject unsupported future schema)
Badger open
stale job leases
active generation exists
active generation manifest exists
backend replica exists
embedding model compatibility
registry validity
Git binary available
package-manager executables available (go, uv, npm, cargo, mvn/gradle — only those actually used by registered projects)
```

Exit codes:
```text
0  all checks OK
1  at least one Warning, no Unhealthy
2  at least one Unhealthy
```

## Inputs / Outputs
- Input: none (reads store/config/PATH state).
- Output: stdout report (one line per check with severity), process exit code.

## Failure behavior
- A check that errors while running (e.g. can't open bbolt at all) is itself reported as `Unhealthy` with the error detail — `doctor` never panics or exits abnormally due to a broken subsystem it's trying to diagnose.

## Tests
- Healthy fixture store → exit code 0, all checks OK.
- Missing Git binary (mock PATH) → Warning or Unhealthy per severity mapping, exit code reflects it.
- Unsupported schema version → explicit Unhealthy error naming the mismatch.
- Stale job lease fixture → detected and reported.

## Acceptance criteria
- [ ] All listed checks implemented.
- [ ] Exit codes: 0 healthy, 1 warnings, 2 unhealthy.
- [ ] Schema version explicitly reported (ties into STORE-002's migration framework).
