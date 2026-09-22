# Epic: npm/PyPI fallback manifest

**Found live:** scanning a real Python+Node monorepo (mem0) on Linux/Podman. `scan` correctly resolved 315 Python and 1097+ Node dependencies across 22 sub-projects. `sync` then failed **every single one** with `no registry manifest for <package>` — zero successes. Root cause: `fallbackManifest` (`internal/cli/sync.go`), the mechanism that lets *any* Go dependency sync with zero hand-curation, is hard-gated to Go only (`if dep.Ecosystem != domain.EcosystemGo { return false }`). Only 2 hand-curated manifests exist for Python (fastapi, pydantic) and 0 for Node. Combined with SCOPE-002's ambient sync firing silently on all 22 sub-projects at once, this made a real, representative non-Go project look completely broken — the reported symptom was "sync timed out very fast," which was actually "sync failed completely, instantly, silently."

## Why this is harder than the Go case, not just more of the same work
Go's fallback works cleanly because the module system *is* the answer: a Go module path is a repository location, and `go.sum` already pins an exact version to a tag/commit by construction — `moduleTagTemplate` only had to reproduce a convention Go itself enforces.

npm and PyPI have no equivalent guarantee. A package name doesn't encode a repository location (needs a registry lookup), and — this is the real risk — **there is no reliable convention that a published package version corresponds to any particular git tag.** Many projects tag releases (`v1.2.3`, `1.2.3`, `<pkg>@1.2.3` in a monorepo); many don't, or tag inconsistently, or the git repo has diverged from what was actually published. Guessing wrong here reproduces exactly the bug this whole session was spent fixing for Go (`Ref: "HEAD"` silently indexing the wrong content under a confident version label) — except worse, because npm/PyPI's *actual* source of truth for "what did version X really contain" is the published registry artifact (the tarball), not any git ref at all.

## Tickets
- [REG-012](REG-012-npm-fallback-manifest.md) — npm: registry lookup for the repository URL, git-tag-candidate resolution (multiple conventions tried, each verified to actually exist before trusting it — never falls back to a branch head), clean failure when no tag matches.
- [REG-013](REG-013-pypi-fallback-manifest.md) — PyPI: same shape, via PyPI's JSON API.

## Non-goals (this epic)
- **Tarball-based acquisition** (downloading the exact published npm/PyPI artifact instead of guessing a git tag) is more correct — it's what actually shipped, provably, at that version — but it means a whole new acquisition path (`registry.Source` only supports git today; `internal/data/generation/build.go`'s `acquireGitSources` is the only acquisition code that exists). That's real new architecture, not a fallback-manifest tweak, and belongs in a future epic if git-tag coverage proves too low in practice. Recorded here so it isn't silently forgotten, not built now.
- No change to the ambient-sync concurrency cap gap (SCOPE-002's own known risk) — orthogonal, separately tracked there.
- No registry-coverage curation push (hand-writing more manifests) — this epic is about the automatic path, not adding entries to `internal/registry/builtin`.

## Acceptance criteria
- [ ] A real npm package with a real GitHub repo and a real, standard tag (e.g. `semver`, `commander`) syncs successfully via the automatic fallback, with no hand-curated manifest.
- [ ] A real PyPI package in the same shape syncs successfully.
- [ ] A package whose repo has no matching tag for the resolved version fails acquisition cleanly, naming what it tried — never falls back to a branch head or any other unverified ref.
- [ ] Re-run against mem0 (or an equivalent real Node/Python project) and record the before/after sync success rate.
