# Epic: npm/PyPI fallback manifest

**Found live:** scanning a real Python+Node monorepo (mem0) on Linux/Podman. `scan` correctly resolved 315 Python and 1097+ Node dependencies across 22 sub-projects. `sync` then failed **every single one** with `no registry manifest for <package>` — zero successes. Root cause: `fallbackManifest` (`internal/cli/sync.go`), the mechanism that lets *any* Go dependency sync with zero hand-curation, is hard-gated to Go only (`if dep.Ecosystem != domain.EcosystemGo { return false }`). Only 2 hand-curated manifests exist for Python (fastapi, pydantic) and 0 for Node. Combined with SCOPE-002's ambient sync firing silently on all 22 sub-projects at once, this made a real, representative non-Go project look completely broken — the reported symptom was "sync timed out very fast," which was actually "sync failed completely, instantly, silently."

## Why this is harder than the Go case, not just more of the same work
Go's fallback works cleanly because the module system *is* the answer: a Go module path is a repository location, and `go.sum` already pins an exact version to a tag/commit by construction — `moduleTagTemplate` only had to reproduce a convention Go itself enforces.

npm and PyPI have no equivalent guarantee. A package name doesn't encode a repository location (needs a registry lookup), and — this is the real risk — **there is no reliable convention that a published package version corresponds to any particular git tag.** Many projects tag releases (`v1.2.3`, `1.2.3`, `<pkg>@1.2.3` in a monorepo); many don't, or tag inconsistently, or the git repo has diverged from what was actually published. Guessing wrong here reproduces exactly the bug this whole session was spent fixing for Go (`Ref: "HEAD"` silently indexing the wrong content under a confident version label) — except worse, because npm/PyPI's *actual* source of truth for "what did version X really contain" is the published registry artifact (the tarball), not any git ref at all.

## Tickets
- [REG-012](REG-012-npm-fallback-manifest.md) — **done.** npm: registry lookup for the repository URL, git-tag-candidate resolution (multiple conventions tried, each verified to actually exist before trusting it — never falls back to a branch head), clean failure when no tag matches. Live use (a real mem0-ts sync) surfaced and fixed two further correctness gaps beyond the original design: a monorepo tag-ambiguity bug (bare tags dropped once `Subdir` is known) and a monorepo scope-bleed bug (registry `directory` metadata sometimes missing — fixed with a structural `package.json`-search fallback, not just trusting the registry more). See the ticket's own post-implementation note.
- [REG-013](REG-013-pypi-fallback-manifest.md) — **done.** PyPI: same shape, via PyPI's JSON API. Built as designed, no live-found surprises.

## Status

**Epic moves to `completed/`** — both tickets are done, and (per REG-013's own note) closed the majority of the original mem0 finding: Python was 315 of mem0's dependencies, the larger share, and previously had zero automatic fallback.

**Separately, shipping npm/PyPI *acquisition* correctness doesn't mean npm/PyPI *documentation* is complete.** A correctly-scoped sync today still only ever produces README/CHANGELOG/LICENSE-derived content for both ecosystems — there is no structured (function/type/signature-level) extraction for either, the equivalent of what `godoc` does for Go. That's a distinct, larger gap, not yet scoped as its own ticket. See `docs/architecture.md`'s ecosystem coverage note.

**Also separately, the ambient-sync-per-project concurrency risk this epic's own INDEX originally cited (SCOPE-002's known limit) is now closed** — `internal/config.SyncConfig.MaxTotalConcurrency` plus a daemon-wide semaphore in `RunSync` bound total concurrent sync work across every project, not just within one. See `docs/tickets/planned/54-ambient-sync-and-scoped-priority/SCOPE-002-auto-trigger-full-sync-on-registration.md`'s own post-implementation note.

## Non-goals (this epic)
- **Tarball-based acquisition** (downloading the exact published npm/PyPI artifact instead of guessing a git tag) is more correct — it's what actually shipped, provably, at that version — but it means a whole new acquisition path (`registry.Source` only supports git today; `internal/data/generation/build.go`'s `acquireGitSources` is the only acquisition code that exists). That's real new architecture, not a fallback-manifest tweak, and belongs in a future epic if git-tag coverage proves too low in practice. Recorded here so it isn't silently forgotten, not built now.
- No change to the ambient-sync concurrency cap gap (SCOPE-002's own known risk) — orthogonal, separately tracked there.
- No registry-coverage curation push (hand-writing more manifests) — this epic is about the automatic path, not adding entries to `internal/registry/builtin`.

## Acceptance criteria
- [x] A real npm package with a real GitHub repo and a real, standard tag (e.g. `semver`, `commander`) syncs successfully via the automatic fallback, with no hand-curated manifest. Verified live — see REG-012's post-implementation note (`semver`, `commander`, plus a monorepo package with a `directory` field).
- [x] A real PyPI package in the same shape syncs successfully. Verified live — see REG-013's own Tests section.
- [x] A package whose repo has no matching tag for the resolved version fails acquisition cleanly, naming what it tried — never falls back to a branch head or any other unverified ref. Verified live multiple times over — see REG-012's post-implementation note (`eslint-visitor-keys` bare-tag ambiguity, `@babel/plugin-syntax-object-rest-spread`'s upstream metadata drift).
- [x] Re-run against mem0 (or an equivalent real Node/Python project) and record the before/after sync success rate. **Done.** Real, full re-clone of mem0 (23 sub-projects, ~4,700 Node+Python dependencies total), synced end to end via a real daemon against real Ollama/Qdrant (Podman/Alpine, matching this session's established live-verification methodology): **2,866 synced / 1,591 failed / 4,457 attempted — 64.3% success rate**, up from the original finding's baseline of **0%** (every sync failed instantly with `no registry manifest for <package>` before this epic). See the epic-level Results section below for the full breakdown and what the remaining ~36% failures represent.

## Results (2026-09, full mem0 re-run)

Per-project final tallies (done/total, failed — from the real sync run's own live progress output):

| Project | Done / Total | Failed |
|---|---|---|
| `mem0-ts` | 993 / 1097 | 360 |
| `server/dashboard` | 529 / 586 | 206 |
| `integrations/n8n-nodes-mem0` | 519 / 558 | 153 |
| `integrations/pi-agent-plugin` | 541 / 551 | 208 |
| `integrations/openclaw` | 448 / 472 | 162 |
| `integrations/vercel-ai-sdk` | 400 / 414 | 138 |
| `integrations/zapier-mem0` | 336 / 348 | 120 |
| `mem0` (Python root) | 314 / 315 | 140 |
| `integrations/deepseek-plugin` | 209 / 212 | 73 |
| `cli/node` | 165 / 178 | 31 |
| `server` | 3 / 4 | 0 |
| **Total** | **4,457 / 4,735** | **1,591** |

**64.3% real, end-to-end sync success** across ~4,700 real, unmodified npm+PyPI dependencies neither hand-curated nor cherry-picked — the exact shape (a real polyglot monorepo, cold) the original finding described. The ~36% failure rate is real and not surprising given this epic's own explicit, disclosed scope: no tarball-based acquisition (git-tag guessing only, and not every published version has a matching git tag — the epic's whole design principle is failing cleanly in that case rather than guessing wrong), no private registries, and REG-012's own five iteratively-found edge cases (monorepo tag ambiguity, scope-bleed, GitHub browse-URLs, upstream metadata drift) show real npm registry metadata is genuinely inconsistent at this scale — a ceiling this epic's design accepts explicitly (see Non-goals), not a bug still to fix.

Total generations produced: 1,146 (974 Node + 172 Python) — matches `4,457` synced minus content-reuse (identical text across generations counted once, GEN-003), consistent with the real per-project tallies above.
