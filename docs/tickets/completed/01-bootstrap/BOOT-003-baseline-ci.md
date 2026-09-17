# BOOT-003: Add CI baseline

**Epic:** Bootstrap
**Status:** done
**Depends on:** BOOT-001
**Estimated size:** small

## Goal
Stand up a GitHub Actions workflow that runs the core Go quality gates on every push/PR.

## Non-goals
- No golangci-lint with a large ruleset yet — keep lint minimal or skip it entirely for the first spike.
- No release/publish pipeline.
- No matrix builds across OSes — Linux runner is sufficient for v0.1.

## Simplicity constraints
- Do not block the first spike on an exhaustive lint configuration; a barebones `.golangci.yml` (or omitting the lint job) is fine initially.
- Do not add caching, artifact upload, or other CI conveniences until the basic job is green and useful.

## Design
`.github/workflows/test.yml` with a single job running on `ubuntu-latest`:
```yaml
- actions/checkout
- actions/setup-go (version pinned per BOOT-002)
- go test ./...
- go test -race ./...
- go vet ./...
- gofmt -l . (fail if output non-empty)
```
Optionally add a `golangci-lint` step behind a separate job that is allowed to be added later without blocking this ticket.

## Inputs / Outputs
- Input: pushes and PRs to the repository.
- Output: pass/fail CI status check.

## Failure behavior
Any failing step fails the workflow and blocks merge (once branch protection is configured — branch protection itself is outside this ticket's scope).

## Tests
- Manual: push a branch with a deliberately failing `go vet` issue and confirm CI fails; fix it and confirm CI passes.

## Acceptance criteria
- [x] Clean checkout passes CI.
- [x] `go test ./...`, `go test -race ./...`, `go vet ./...`, and a `gofmt` check all run in CI.
- [x] Workflow runs automatically on push and pull_request events.

## Post-implementation note
`.github/workflows/test.yml`: one job, `ubuntu-latest`, `actions/setup-go@v5` pinned to `1.25.6` (matching `go.mod`/README, closing BOOT-002's remaining gap). Steps run `gofmt -l .` (fails the job if any file is unformatted), `go vet ./...`, `go test ./...`, then `go test -race ./...`, in that order — cheapest/fastest checks first so a formatting or vet mistake fails fast without waiting for the full race-enabled suite. All four verified locally before committing (`go test ./...` and `go test -race ./...` both pass clean across every package, `gofmt -l .` and `go vet ./...` both produce no output). No lint job yet, per this ticket's own Non-goals — a `.golangci.yml` exists (BOOT-001) but nothing invokes `golangci-lint` in CI yet; that's a follow-up, not a gap in this ticket's own scope. No caching/artifact steps added either, matching the Simplicity constraints.
