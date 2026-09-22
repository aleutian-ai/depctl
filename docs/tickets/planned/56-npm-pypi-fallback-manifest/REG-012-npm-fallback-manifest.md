# REG-012: npm fallback manifest

**Epic:** npm/PyPI fallback manifest
**Status:** planned
**Depends on:** none
**Estimated size:** medium

## Goal
Let a Node dependency with no hand-curated manifest still sync, the same automatic way Go dependencies already do — without indexing the wrong content under a confident version label.

## Design
1. **Repository lookup:** `GET https://registry.npmjs.org/<package>` (the same public API `npm view`/`npm install` use). Read `repository.url` (normalize `git+https://...` / `git://...` / `github:org/repo` shorthand to a plain `https://` clone URL) and, when present, `repository.directory` — this maps directly onto the `Subdir` mechanism POINT-002 already built for Go monorepos (verified live: npm's own registry returns `directory` for monorepo packages, e.g. `eslint-visitor-keys` → `github.com/eslint/js`, directory `packages/eslint-visitor-keys`).
2. **Tag candidates, verified before trusting:** try, in order, `v${version}`, `${version}`, `${package}@${version}`, `${package}-v${version}` (the four conventions real npm monorepos actually use). Each candidate is checked with `git ls-remote --tags` (or the existing `ResolveRef` against a mirror) — **a candidate is only used if it actually resolves**; none matching is a clean acquisition failure naming every candidate tried, mirroring the Go fix's own "no fallback to a branch head" principle exactly. This reuses `refCandidates`' established shape (`internal/data/generation/build.go`) rather than inventing a new pattern.
3. Scoped by `dep.Ecosystem == domain.EcosystemNode` alongside the existing Go branch in `fallbackManifest`.

## Non-goals
- No tarball-based acquisition (see epic INDEX's Non-goals) — this is git-tag-guessing with verification, not the more-correct published-artifact path.
- No handling of scoped packages' registry quirks beyond what `registry.npmjs.org/<package>` already returns correctly (npm's API handles `@scope/name` URL-encoding itself).
- No private registry support (npm Enterprise, Verdaccio, etc.) — public registry only, matching the Go fallback's own public-only scope.

## Tests
- A registry response with a plain `repository.url` and no `directory` resolves at the repo root.
- A registry response with `repository.directory` scopes the source's `Subdir` correctly.
- Each of the four tag-candidate shapes is tried, in order, against a fixture repo tagged only one particular way — proving the resolver doesn't just get lucky on the first convention.
- No candidate resolving is a clean, named error — never a branch-head fallback.
- Live: `semver`, `commander` (plain `v${version}` tags) and a monorepo package with a `directory` field sync successfully with no hand-curated manifest.

## Acceptance criteria
- [ ] A real, uncurated npm package syncs via the automatic fallback.
- [ ] A monorepo npm package (registry `repository.directory` set) indexes only its own subdirectory.
- [ ] A package with no matching tag fails cleanly, listing what was tried.
