# BOOT-003: Add CI baseline

**Epic:** Bootstrap
**Status:** planned
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
- [ ] Clean checkout passes CI.
- [ ] `go test ./...`, `go test -race ./...`, `go vet ./...`, and a `gofmt` check all run in CI.
- [ ] Workflow runs automatically on push and pull_request events.
