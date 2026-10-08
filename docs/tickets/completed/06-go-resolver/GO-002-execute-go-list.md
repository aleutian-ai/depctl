# GO-002: Execute `go list -m -json all`

**Epic:** Go Resolver
**Status:** done
**Depends on:** GO-001, RES-002
**Estimated size:** medium

## Goal
Resolve the full Go module dependency graph for a detected project by shelling out to `go list -m -json all` and parsing the streamed JSON module objects.

## Non-goals
- No custom MVS (minimal version selection) reimplementation — always delegate to the `go` toolchain.
- No caching of `go list` output in this ticket (that's implicit in higher-level generation reuse, not here).

## Simplicity constraints
- Do not build a general-purpose Go module graph library. Parse only the fields listed below.
- Do not attempt to support GOFLAGS/GOPROXY configuration beyond what the user's environment already provides — just invoke the command in the project root with inherited environment.

## Design
- Package: `internal/resolver/golang`
- Use the command runner from `internal/executil` (RES-002) to run:
  ```
  go list -m -json all
  ```
  with working directory set to the project root, and a configurable timeout (default e.g. 60s).
- `go list -m -json all` emits a stream of concatenated JSON objects (not a JSON array) — decode with `json.NewDecoder(stdout)` and loop `Decode` until `io.EOF`.
- Struct to decode into:
  ```go
  type goModule struct {
      Path     string
      Version  string
      Main     bool
      Indirect bool
      Replace  *goModule
      GoMod    string
  }
  ```
- Collect all decoded modules into a slice for GO-003 to normalize.

## Inputs / Outputs
- Input: project root (must have passed GO-001 Detect).
- Output: `[]goModule` (raw parsed modules) or a typed `ResolutionError`.

## Failure behavior
- Non-zero exit from `go list`: wrap stderr into a typed `ResolutionError` with the command's stderr output. Do not partially return modules on failure.
- Context cancellation/timeout: propagate as `context.DeadlineExceeded`/`context.Canceled` wrapped in `ResolutionError`.
- Malformed JSON mid-stream: fail fast with a decode error including which module index failed.

## Tests
- Fixture: simple direct dependency (`testdata/projects/go-simple/`).
- Fixture: indirect dependency present in output.
- Fixture: local `replace` directive (`replace foo => ../foo`) — `Replace.Path` present, no version.
- Fixture: version `replace` directive (`replace foo => bar v1.2.3`).
- Fixture: Go workspace, if `go.work` present, document behavior (list still runs per-module; workspace fixture optional per plan, can be skipped if `go list` behavior is unchanged).
- Cancelled context returns promptly without hanging.

## Acceptance criteria
- [x] All modules from `go list -m -json all` are captured with Path, Version, Main, Indirect, Replace, GoMod fields.
- [x] Local and version replace directives are both captured correctly.
- [x] Command failure produces an actionable typed error, not a generic string.

## Post-implementation amendment (found by real-world scanning)

This ticket's simplicity constraint said not to override GOFLAGS/GOPROXY beyond the user's environment. That held until `depctl scan` was run against ~1100 real repos (`~/offline-knowledge/{go,python}/*` — kubernetes, moby, cli, and 36+ others), which surfaced three environment-dependent failure modes that inherited-environment execution can't avoid. `listModules` now sets three env vars on every invocation (`internal/resolver/golang/list.go`):

- **`GOFLAGS=-mod=mod`** — a vendored project (`vendor/` present, `go.mod` `go >= 1.14`) makes `go list -m -json all` fail outright under Go's default vendor-mode auto-detection ("can't compute 'all' using the vendor directory" / "inconsistent vendoring"). Forcing `-mod=mod` bypasses vendor mode for module-graph resolution. Hit by kubernetes, cli, and 34 others in the real-world scan.
- **`GOWORK=off`** — `-mod=mod` is itself invalid when an ancestor directory has a `go.work` file ("-mod may only be set to readonly or vendor when in workspace mode"). We detect and resolve per `go.mod`, not per workspace, so workspace mode is disabled outright rather than special-cased. Hit by kubernetes (which has both a vendor dir and a `go.work`).
- **`GOTOOLCHAIN=local`** — without this, a `go.mod` requesting a newer Go than what's installed makes the toolchain try to download that release over the network, which can eat most of `defaultListTimeout` (60s) per affected repo before failing — and was the actual cause of a 2-minute scan hang before this fix. `GOTOOLCHAIN=local` fails in ~10ms with a clear "go.mod requires go >= X" message instead. Hit by moby.

All three are covered by regression tests reproducing the exact real-world failure: `TestListModulesVendorDirectory`, `TestListModulesWorkspaceMode`, `TestListModulesDoesNotDownloadToolchain` (`internal/resolver/golang/list_test.go`).

This doesn't change the ticket's substance — it's still one `go list -m -json all` invocation via `executil`, no MVS reimplementation, no generic module-graph library — it's a narrower, evidence-based amendment to "inherited environment only" specifically because three real large-repo failure modes are entirely environment-driven and each has an unambiguous fix.

**Honest tradeoff on `GOTOOLCHAIN=local`:** re-running the same ~1100-repo scan after this fix shows the vendor and workspace errors fully eliminated (0 occurrences, was 36+), but the count of *successfully resolved* Go projects actually dropped (279 → 223). That's not a regression — before this fix, `GOTOOLCHAIN` was unset (Go's default `auto`), so a repo requesting a newer Go than installed would silently download that toolchain over the network mid-scan and often succeed. With `GOTOOLCHAIN=local`, those 118 repos now fail fast (~10ms) with a clear "go.mod requires go >= X" message instead of resolving via a surprise network fetch. Given depctl's local-first design, a routine `depctl scan` silently downloading gigabytes of Go toolchains without asking is the wrong default — deterministic, fast failure beats implicit success via a hidden network side effect. Users on an affected repo can upgrade their local Go toolchain themselves; `depctl` won't do it for them silently.
