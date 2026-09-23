# REG-012: npm fallback manifest

**Epic:** npm/PyPI fallback manifest
**Status:** done
**Depends on:** none
**Estimated size:** medium

## Post-implementation note

Built and shipped (`internal/cli/sync.go`'s `npmFallbackManifest`/`npmRepository`/`npmTagCandidates`), largely as designed — one live-found addition to the design's own risk section: **the four tag-candidate templates are only ever tried in their package-scoped form (`${package}@${version}`, `${package}-v${version}`) once `Subdir` is known**, never the bare `v${version}`/`${version}` templates in that case. Live-testing against a real monorepo package (`eslint-visitor-keys`, `github.com/eslint/js`) found that the bare template *did* resolve — to a real tag — but to an entirely unrelated package's release from years before the monorepo restructure (ESLint core's own 3.4.3), not this package's content. `npmTagCandidates(name, subdir string)` now takes `subdir` and drops the ambiguous bare candidates whenever it's non-empty; regression-tested (`TestNpmTagCandidatesForAMonorepoPackageExcludesBareTags`).

A second, separate gap found afterward and fixed under the same epic, not this ticket's original design: npm's registry `repository.directory` field is sometimes simply absent for a package that's genuinely part of a monorepo (`@opentelemetry/api`, `open-telemetry/opentelemetry-js` — confirmed live: 72 objects indexed from across the whole repo before the fix, 2 after). Unlike the tag-ambiguity issue, this can't be caught by verifying against git — the wrong subdir (the whole repo) still verifies fine, it's just wrong. Fixed structurally, not by trusting registry metadata more carefully: `internal/data/generation/build.go`'s `discoverNodeSubdir` searches the repo tree at the resolved commit for the `package.json` that actually declares this package's name (via new `git.Cache.ListFiles`/`ReadFile` plumbing, no checkout needed), falling back cleanly to no scoping if none matches; a walk-time boundary check (`packageJSONNameAt`, mirroring the existing Go `hasGoMod` check) is the defense-in-depth backstop for whatever that discovery pass misses. See the epic's INDEX for why this is tracked as part of REG-012 rather than a new ticket.

**Still not covered by this ticket, tracked separately:** no structured (function/type/signature-level) extraction exists for Node at all — a correctly-scoped sync only ever produces README/CHANGELOG/LICENSE content, the same as before this ticket. See `docs/architecture.md`'s ecosystem coverage note.

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
