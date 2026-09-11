# OPS-002: `ragctl doctor`

**Epic:** Status and Doctor
**Status:** done
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
- [x] All listed checks implemented (two reconciled to what the codebase has — see note).
- [x] Exit codes: 0 healthy, 1 warnings, 2 unhealthy.
- [x] Schema version explicitly reported (ties into STORE-002's migration framework).

## Post-implementation note

Shipped in `internal/cli/doctor.go`, alongside `status` (same reasoning as OPS-001's note). `doctorChecks` is the flat ordered list the simplicity constraint asks for; each check reads a shared `doctorEnv` (config, bbolt, Badger, registry) opened once up front. Open failures are kept rather than returned: the owning check reports the error, and checks that depend on it report `not checked: <dependency> unavailable`, so a broken subsystem never aborts the run. `Severity`'s values are the exit codes, and `cli.ExitCodeError` carries the code to `main`.

Checks reconciled to the code as it exists, rather than built literally:
- **Stale job leases → stale jobs.** ragctl has no leases; jobs run synchronously inside one CLI invocation (`internal/lifecycle/gc`). A job still `RUNNING` after an hour means its process died, so that is what's flagged (Warning — re-running `ragctl gc` resumes it).
- **Package-manager executables** only maps ecosystems whose resolver shells out: today just `go`. The node and python resolvers parse lockfiles, so `npm`/`uv` are never required; `cargo`/`mvn`/`gradle` have no resolver yet.
- **Active generation exists** checks pointer integrity (every configured-backend pointer resolves to a generation record in state `ACTIVE`). Zero active generations is a Warning. "Is everything referenced synced?" is `ragctl plan`'s job, and doing it here would warn forever about packages with no registry manifest.
- **Schema version**: an on-disk version newer than the binary is rejected by `bboltstore.Open` itself (STORE-002), so the check surfaces that open error, which names both versions.
- **Embedding model compatibility** compares each active replica's `EmbeddingModel` with the configured `embedding.model` (Unhealthy on mismatch: queries embed with the configured model).

Added beyond the list: `config` (every other check depends on it) and `vector backend reachable` (the most common reason queries fail; `VectorBackend.Health`'s doc comment already named doctor as its consumer).

Severity choices: a missing `git` or required package manager is Unhealthy (sync or scan can't run at all); registry load warnings and stale jobs are Warnings; broken manifests, replicas, pointers, or model mismatches are Unhealthy.

Tests: `internal/cli/doctor_test.go` — healthy fixture (all 13 OK, exit 0), missing git and missing `go` (Unhealthy), unsupported schema version 99 (Unhealthy naming it, dependents marked not checked), stale vs. fresh RUNNING job (Warning, exit 1), active generation missing manifest and replica, dangling active pointer, embedding model mismatch, empty store (Warning), and a command-level run returning `ExitCodeError{Code: 2}` with the backend down. Also verified by hand against a real 21-generation corpus, including with `control.db`'s lock held by another process.
