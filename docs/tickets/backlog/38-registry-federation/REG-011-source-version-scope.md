# REG-011: Source version scope

**Epic:** Registry Federation
**Status:** planned
**Depends on:** REG-002 (registry loader), GEN-002 (generation build)
**Estimated size:** medium

## Goal
Let a `registry.Source` declare it is not tied to an exact dependency version (`VersionScope: "range"` or `"conceptual"`, e.g. a curated book/talk/architecture-doc source per §6A "Text source applicability"), so `generation.Build`'s acquisition step can skip re-fetching/re-normalizing it on every version bump instead of treating every source as implicitly `"exact"` the way `gitRef`'s `${version}` template substitution does today.

## Non-goals
- No query-side change to how a non-exact source's objects get matched by version at retrieval time — flagged explicitly below as a blocker for a separate future ticket, not designed here.
- No new source type and no acquisition path for anything other than `"git"` sources — `VersionScope` is orthogonal to `Source.Type`; a `"conceptual"`-scoped source is still acquired the same way (git clone, or whatever `Type` already implies) the first time, this ticket only changes whether that acquisition repeats on a later version's `generation.Build`.
- No cross-generation object-reuse mechanism beyond what dedup (GEN-003/HASH-001) already gives — see Design for why content fingerprinting already does most of the work here, and why this ticket only needs to add a *skip*, not a new reuse subsystem.

## Simplicity constraints
- One new field on `Source` (`VersionScope` — a `Kind` string, matching the ticket brief exactly: `"exact"` | `"range"` | `"conceptual"`, defaulting to `"exact"` when unset so every existing manifest's behavior is unchanged with zero YAML edits).
- The skip logic lives in `acquireGitSources` (`internal/data/generation/build.go`) as one added condition, not a new acquisition path — a non-`"exact"` git source still goes through the exact same `EnsureMirror`/`ResolveRef`/`MaterializeWorktree` calls the first time it's acquired for a given dependency; what changes is *whether a later generation re-runs that acquisition at all* for a source already known not to move with version.

## Design
Confirmed in `internal/registry/manifest.go`: `Source` has no scope/kind field beyond `Type`. Confirmed in `internal/data/generation/build.go`: `gitRef(source registry.Source, version string)` unconditionally treats every source's `Ref` as a `${version}`-templated tag — `acquireGitSources` calls `gitRef` for every `"git"`-type source with no branch for "this source doesn't move with version."

Add to `Source`:

```go
type Source struct {
	ID           string `yaml:"id" json:"id"`
	Type         string `yaml:"type" json:"type"`
	URL          string `yaml:"url,omitempty" json:"url,omitempty"`
	Ref          string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Module       string `yaml:"module,omitempty" json:"module,omitempty"`
	Authority    int    `yaml:"authority" json:"authority"`
	VersionScope string `yaml:"versionScope,omitempty" json:"versionScope,omitempty"` // "exact" (default) | "range" | "conceptual"
}
```

Update the embedded JSON Schema (`internal/registry/schema/knowledge-package.schema.json`) `sources[].properties` to add `"versionScope": {"enum": ["exact", "range", "conceptual"]}` — optional, not added to that item's `required` list (`["id", "type", "authority"]` stays as-is).

`acquireGitSources`/`Build` (`internal/data/generation/build.go`) needs a way to know whether this dependency already has a prior successful generation whose acquisition of this same `Source.ID` (by `URL`, since a `"range"`/`"conceptual"` source's `Ref` isn't meaningfully re-resolved) can be reused rather than re-walked. `Build`'s current signature is `Build(ctx, gen domain.Generation, sources []registry.Source, gitCache *git.Cache, store *bbolt.Store, badgerStore *badger.Store) error` — it does not currently look at any prior generation at all (contrast `syncVersion` in `internal/cli/sync.go`, which does fetch `store.GetActiveGeneration` for validation's diffing, *after* `Build` already ran). Two designs to choose between at implementation time:

1. **Skip acquisition, reuse prior objects directly.** `Build` (or its caller, `syncVersion`) looks up the prior active generation's `KnowledgeObject`s for this specific `Source.ID` (already stored, keyed by source, in Badger) and copies them forward into the new generation's manifest instead of re-normalizing — the strongest form of "skip," since it avoids both the git fetch and the normalize/chunk work.
2. **Skip only the re-fetch, keep normalize/chunk.** `acquireGitSources` still materializes a worktree for a non-`"exact"` source (cheap: `EnsureMirror` is itself already cache-aware, per `git.Cache`'s name) but resolves `Ref` once and pins to whatever commit it last resolved to (persisted somewhere — likely a new small field on `domain.Generation` or a lookup via the prior generation's manifest) rather than re-resolving `${version}`-templated refs that were never version-templated to begin with for these sources.

Prefer (1) for `"conceptual"` sources (truly version-independent; content fingerprinting/dedup, GEN-003/HASH-001, would make normalizing it again produce identical `ContentHash`-deduped objects anyway, so skipping is a pure efficiency win, not a correctness-risk shortcut) and something closer to (2) for `"range"` sources (which *can* change content over time within their range — e.g. a doc site that gets updated — so unconditionally reusing stale objects forever would be wrong; a `"range"` source should still be re-acquired on some cadence, just not tied to every dependency version bump specifically). Pin down the exact reuse mechanism (which of the two, and any TTL/staleness rule for `"range"`) during implementation — this ticket's Design intentionally leaves that as the one open implementation choice, since it depends on how `domain.Generation`/the Badger object store already key stored objects by source, which needs to be re-confirmed against the real schema at implementation time rather than assumed here.

## Inputs / Outputs
- Input: a `registry.Source` with `VersionScope` set to `"range"` or `"conceptual"`, and a dependency with a prior successful generation that already acquired that same source.
- Output: a new generation's `Build` step does not re-run git acquisition (and, per whichever of the two reuse designs above is chosen, possibly not re-normalization either) for that source — while an `"exact"`-scoped (or unset — default) source on the same manifest is acquired exactly as it is today, unaffected.

## Failure behavior
- A `VersionScope` of `"range"`/`"conceptual"` on a source whose `Type` isn't `"git"` (e.g. `"website"`, not yet acquired by anything per `Build`'s doc comment on `Type != "git"` sources being skipped entirely) is inert until that source type gains real acquisition — no new failure, just no-op, matching today's existing skip-non-git behavior.
- A dependency's *first* generation always fully acquires every source regardless of `VersionScope` — there's no prior generation to reuse from. The skip only ever applies from the second generation of a given dependency onward.
- If the prior generation's stored objects/worktree for that source are missing or corrupt (e.g. retention pruned them, RET-*), fall back to full re-acquisition rather than failing the build — a `"range"`/`"conceptual"` source's reuse is an optimization, never a hard dependency the build can't proceed without.

## Tests
- A `"conceptual"`-scoped source: dependency's second generation skips that source's git acquisition entirely (asserted via a spy/counter on `git.Cache.EnsureMirror` call count), while its `"exact"`-scoped sibling source on the same manifest is fully re-acquired.
- A `VersionScope`-unset source behaves identically to pre-REG-011 (always `"exact"`, always re-acquired) — default-value regression check.
- The reused objects still carry the correct `Dependency.Version` for the *new* generation (not stale from the prior one) even when their content is unchanged — whatever reuse design is chosen must not leave `KnowledgeObject.Dependency`/`Version` pointing at the wrong generation.
- First-ever generation for a dependency: every source is fully acquired regardless of `VersionScope` (no prior generation to skip against).

## Acceptance criteria
- [ ] `Source.VersionScope` is a real field, schema-validated, defaulting to `"exact"` when unset.
- [ ] A `"conceptual"`-scoped source is not re-acquired by `generation.Build` on every version bump for a dependency that already has a prior generation.
- [ ] An `"exact"`-scoped (or default) source's behavior is completely unchanged from before this ticket.
- [ ] The chosen reuse mechanism is documented in this ticket's post-implementation note (per this repo's convention, e.g. REG-005's own note) once the (1)-vs-(2) design choice above is actually made.

## Open design question (not resolved by this ticket)
`query.Service`'s knowledge search (`internal/query/query.go`) filters via `backend.Filter{Ecosystem, Dependency, Version, Generation}` (`internal/backend/backend.go`) — `Version` is exact-match today. A `"conceptual"`-scoped source's vector points, if tagged with the same exact `Version` as every `"exact"`-scoped source in the same generation, would be excluded from any query that doesn't happen to ask about that specific version, even though the conceptual content is (by definition) not version-specific and should probably match broadly or carry no version tag at all. This ticket does not change `Filter` or `SearchKnowledge`'s behavior — it only flags that REG-011 shipping without a corresponding query-side change leaves conceptual sources effectively unreachable through the existing version-exact filter path, and that a future ticket (query-side) is needed to actually make a `"range"`/`"conceptual"` source's content retrievable the way its acquisition-side laxness implies it should be.
