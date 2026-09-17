# Epic: Registry Federation

Epic 07 (`docs/tickets/completed/07-knowledge-registry`) gives ragctl a three-tier registry loader (`internal/registry.Loader`: built-in, user, project) and a whole-manifest-name-collision precedence rule (`Registry.add`, `internal/registry/loader.go`). Epic 34 (`docs/tickets/backlog/34-registry-coverage`, REG-005/006/007) closes coverage gaps in that same shipped design — what happens when no manifest matches, how a candidate manifest gets proposed, whether a declared source is still reachable. None of that epic touches how manifests from *different tiers* combine, or what a source's tier implies about how much to trust it.

This epic closes that separate gap: **tier composition semantics**. Three things epic 07 built the scaffolding for but never wired all the way through:

1. The project tier is structurally present (`Loader.ProjectRegistryDir`, read by `Load`) but every real CLI call site (`loadRegistryForCLI` in `internal/cli/plan.go`, `runRegistryList`/`runRegistryDiscover` in `internal/cli/registry.go`) hard-codes `""` for it — a project override directory has never actually been reachable from a real command. REG-008.
2. `Registry.add` on a `metadata.name` collision always fully replaces the earlier manifest — there's no way for a project or user manifest to *add a source* to a built-in manifest without retyping every field the built-in one already got right. The design doc (`docs/scratch/ragctl_architecture_eval_next_steps-2.md` §6A "Registry layering") calls for exactly this: "extend it with internal sources... override individual sources... replace it entirely." REG-009 adds that `extends`/`replace` distinction at the source level.
3. `domain.TrustCommunity` and `domain.TrustUser` are defined (`internal/domain/domain.go`) but nothing sets them — `generation.TrustClassForSourceType` (`internal/data/generation/build.go`) classifies purely by `Source.Type`, with no notion of which tier (built-in/user/project) a source came from. Once federation is real (REG-008/009), a source added outside the built-in tier needs its own trust signal distinct from the untouched built-in default. REG-010 wires tier into `Source` and into `TrustClassForSourceType`'s caller.

REG-011 is grouped into this epic rather than 34 because it's also about a registry-manifest-level concept — `VersionScope` — that only matters once sources are routinely layered from multiple tiers/authors (a project's own curated conceptual source, alongside the built-in exact-git one), per §6A "Text source applicability" (version applicability: exact/range/conceptual).

## Tickets
- [REG-008](REG-008-project-registry-cli-wiring.md) — wire the already-implemented `ProjectRegistryDir` into real CLI call sites.
- [REG-009](REG-009-extend-replace-manifest-semantics.md) — `extends`/`registryMode` manifest metadata: merge sources by ID instead of whole-manifest overwrite.
- [REG-010](REG-010-source-provenance-and-trust-user.md) — stamp tier onto `Source`, map non-built-in tiers to `domain.TrustUser`, surface in `describe`.
- [REG-011](REG-011-source-version-scope.md) — `VersionScope` on `Source` so a conceptual/range source isn't re-acquired on every version bump.

## Non-goals for this epic
- No change to what makes a source *fetchable* (git/godoc/website/github-releases) — no new source types, no HTTP acquisition (that's `24-website-acquisition`).
- No candidate-source discovery or liveness checking — that's `34-registry-coverage` (REG-006/007); this epic is purely about how already-loaded manifests from different tiers combine and how that combination affects trust, not about finding or validating new ones.
- No query-side ranking/filter change for conceptual or range-scoped sources — REG-011 explicitly flags that as an open dependency for a future query-side ticket, not something resolved here.
