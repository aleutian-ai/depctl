# Epic: Go Monorepo/Workspace Resolution

[STRESS-001](../../planned/49-full-loop-stress-testing/STRESS-001-real-dependency-heavy-project-scan.md) (scanning real `github.com/hashicorp/terraform`) found that `depctl scan`'s "one `go.mod` = one project" discovery model can't resolve any of a Go workspace-shaped monorepo's nested submodules when the workspace's `go.work` file isn't checked into the repo (terraform's own case — it's built via workspace tooling that isn't committed). Each submodule's `go.mod` carries `replace` directives for only its *own* known siblings, so a standalone `go list -m all` run inside one submodule hits an unresolvable placeholder pseudo-version (`v0.0.0-00010101000000-000000000000`) for any sibling its own `replace` block happens to omit. Terraform's root module itself resolves perfectly (569 deps, ~5s) — this is specifically a nested-submodule gap.

This is a real, previously-undiscovered limitation, not a hypothetical: terraform ships 10 such submodules, and this pattern (a large project splitting optional/plugin-shaped functionality into sibling `go.mod`s tied together by local `replace` directives, with or without a committed `go.work`) is a common shape for actively-maintained large Go projects, not a one-off.

## Tickets
- [x] [MONO-001](MONO-001-monorepo-resolution-options.md) — Decide and scope an approach. Done — built Option B (synthesized `go.work` fallback); all 11 of terraform's discovered Go projects now resolve.

## Non-goals
- Not about multi-ecosystem monorepos (Go + Node in one repo) — that's [epic 32's STRESS-004](../../planned/49-full-loop-stress-testing/STRESS-004-multi-ecosystem-monorepo-scan.md), already covered elsewhere. This epic is specifically about a single ecosystem (Go) with multiple interdependent modules tied together by local `replace` directives and/or a `go.work`.
