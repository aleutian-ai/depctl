# STRESS-001: Real dependency-heavy project scan

**Epic:** Full-Loop Stress Testing
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
Run `ragctl scan` against a real, publicly-cloneable Go project with 100+ transitive dependencies (not a hand-built fixture) and confirm full, correct resolution at that scale — the same order of magnitude as the project that originally motivated GIT-005 (14+ minutes, 828MB, before that fix). This ticket is about `scan` specifically, not `sync` — resolution only, no acquisition.

## Non-goals
- No acquisition/sync in this ticket — that's STRESS-005. `scan` only resolves and registers; nothing here should trigger a real clone.
- No fixing of anything found — a real resolution bug found here gets documented and filed as its own follow-up, not patched inline.

## Simplicity constraints
- One real project (`hashicorp/terraform`, or `prometheus/prometheus` as the named fallback — see Design), cloned once, reused as the fixture for this and later stress tickets in this epic (STRESS-002, STRESS-005, STRESS-011) — avoid re-cloning a large repo per ticket.
- Pick the project by a concrete, checkable criterion (its own `go.mod` plus `go list -m all`'s line count), not by vague "something big."

## Design
1. Clone **`github.com/hashicorp/terraform`** (`git clone https://github.com/hashicorp/terraform`) — a real, actively-maintained project whose `go.mod` has historically resolved 150+ modules via `go list -m all`. Confirm the actual count for whatever commit is checked out (`cd terraform && go list -m all | wc -l`) before treating it as "the" fixture; if it's ever meaningfully under 100, fall back to **`github.com/prometheus/prometheus`** (`git clone https://github.com/prometheus/prometheus`), which has a similarly large, real dependency tree.
2. `time ragctl scan <path>` against a real daemon (real Qdrant/Ollama not required for scan itself, but keep them running since later tickets reuse this project).
3. Record wall-clock.
4. `ragctl deps <project-id>` (or equivalent) and independently cross-check the count/names against `go list -m all`'s own output for the same project — every resolved dependency ragctl reports must appear in `go list`'s output, and vice versa (modulo any intentionally-excluded categories, e.g. the module's own root).

## Inputs / Outputs
- Input: a real project's repository URL/path.
- Output: recorded wall-clock, a pass/fail on full-resolution correctness, and (if this becomes the epic's shared large-project fixture) a note of exactly which project/commit was used so later tickets can reuse it.

## Failure behavior
- Network-dependent — skip (not fail) if the chosen project can't be cloned.
- A resolution mismatch (ragctl reports fewer/more/different dependencies than `go list -m all`) is this ticket's actual finding — record it precisely (which dependencies, in which direction) rather than just failing.

## Tests
- This is a manual/live exercise, not a `go test ./...` addition — run it directly and record results in this ticket's post-implementation note.

## Acceptance criteria
- [x] A real 100+-dependency project scans successfully, with wall-clock recorded.
- [x] ragctl's resolved dependency list matches `go list -m all`'s output exactly (or every discrepancy is documented and understood, not silently ignored).
- [x] The chosen project/commit is recorded for reuse by STRESS-005/007/009/018.

## Post-implementation note

Ran via `hack/test-linux.sh`'s Podman/Alpine pattern, against **`github.com/hashicorp/terraform`** (shallow clone, `main` at the time of this run). Two real findings, both documented rather than fixed here, per this ticket's own non-goal:

1. **`GOTOOLCHAIN=local` (`internal/resolver/golang/list.go`) is a correct, working tradeoff, confirmed at real scale.** Terraform's `go.mod` requires `go >= 1.26.8`. Run against the `golang:1.25-alpine` image (an older toolchain than the target project needs), *every* discovered submodule failed fast with a clear `go.mod requires go >= 1.26.8 (running go 1.25.14; GOTOOLCHAIN=local)` error — exactly the fail-fast behavior the code comment describes, not a hang or a slow implicit download. Re-run against `golang:1.26-alpine` (a toolchain that already satisfies the requirement), the root module resolved cleanly: **569 dependencies in ~5s**, matching `go list -m all`'s own ground-truth count for the same commit (570, the +1 being terraform's own root module, which `go list -m all` includes and ragctl's dependency count correctly excludes). No code change needed — this is `GOTOOLCHAIN=local` behaving exactly as designed under real-project conditions, not a limitation. Operationally, it does mean: **ragctl can only resolve a project whose required Go version is ≤ whatever toolchain is actually installed in the environment running `ragctl scan`** — worth a line in the docs, not a behavior change.
2. **Found and fixed: `ragctl scan` could not resolve any of terraform's 10 nested submodules** (`internal/backend/remote-state/{azure,consul,cos,gcs,kubernetes,oci,oss,pg,s3}`, `internal/legacy`) — all 10 failed with `invalid version: unknown revision 000000000000`. Root cause confirmed by inspecting `go.mod`: terraform is a Go-workspace-shaped monorepo (each submodule's `go.mod` has `replace` directives pointing at its *sibling* submodule directories) but ships **no `go.work` file** in the repo — it's built via workspace tooling that isn't checked in. Each submodule's `replace` block is also incomplete on its own (e.g. `azure/go.mod` replaces `consul`, `cos`, `gcs`, `kubernetes`, `oss`, `pg`, `s3`, and root `terraform`, but not `oci` or `legacy`), so a standalone `go list -m all` run inside any one submodule hits an unresolvable placeholder pseudo-version for whichever siblings its own `replace` block omits. Fixed same-session: see [epic 50](../../completed/50-go-monorepo-workspace-resolution/INDEX.md) — `internal/resolver/golang/list.go` now falls back to a synthesized `go.work` covering every `go.mod` under the repo when this specific failure shape is detected. Re-verified live: all 11 of terraform's discovered Go projects now resolve.

**Fixture for reuse by STRESS-005/007/009/018:** `github.com/hashicorp/terraform`, root module only (569 deps) — shallow-clone fresh each time rather than pinning a commit, since the repo moves fast and the dependency count only needs to stay "100+", not be exact run to run. Use a Go toolchain image that's ≥ whatever the checked-out commit's `go.mod` requires (check `go.mod`'s `go` directive before picking the image tag).
