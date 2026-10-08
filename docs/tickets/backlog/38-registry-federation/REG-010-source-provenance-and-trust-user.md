# REG-010: Source provenance and TrustUser

**Epic:** Registry Federation
**Status:** planned
**Depends on:** REG-002 (registry loader), REG-008 (project registry CLI wiring)
**Estimated size:** medium

## Goal
Stamp which registry tier (builtin/user/project) a `registry.Source` was loaded from onto the `Source` itself, and use that to classify a KnowledgeObject built from a non-built-in-tier source as `domain.TrustUser` — an existing `TrustClass` value nothing currently produces — instead of only classifying by raw `Source.Type` the way `TrustClassForSourceType` does today.

## Non-goals
- No `TrustCommunity` wiring — that value still has no producer after this ticket. `TrustCommunity` implies some notion of a shared, non-project-local, non-built-in registry (an org registry, a published community package) that doesn't exist yet; inventing a rule for it here would be speculative. This ticket only closes the gap for `TrustUser`, which maps directly onto the two tiers (`user`, `project`) the loader already has real directories for.
- No per-source trust override field (a manifest author manually setting `trustClass: official` on a project source) — tier-derived classification only. A future ticket can add an explicit override if a real need shows up.
- No change to `Authority`'s numeric ranking or to REG-009's extend/replace merge order — this ticket only adds a new piece of metadata to `Source`, it doesn't change which source wins a merge or a `reg.Match`.

## Simplicity constraints
- Reuse `Registry.add`'s existing `sourceLabel` parameter (`"builtin"`/`"user"`/`"project"`) as the tier value — it's already computed and threaded through exactly where a `Source` needs to be stamped; no new tier-detection logic.
- One new field on `Source`, one new branch in `TrustClassForSourceType`'s caller (not inside `TrustClassForSourceType` itself, which stays a pure function of `Source.Type` per its current doc comment) — no new package.

## Design
Confirmed in `internal/registry/manifest.go`: `Source` has `ID, Type, URL, Ref, Module, Authority` — no tier/provenance field. Confirmed in `internal/registry/loader.go`: `Registry.add(m Manifest, sourceLabel string)` receives `"builtin"`/`"user"`/`"project"` as `sourceLabel` but only uses it in a warning string — it's never attached to the manifest's `Sources`.

Add to `Source`:

```go
// Source is one place knowledge can be acquired from, ranked by Authority.
type Source struct {
	ID        string `yaml:"id" json:"id"`
	Type      string `yaml:"type" json:"type"` // git | godoc | website | github-releases
	URL       string `yaml:"url,omitempty" json:"url,omitempty"`
	Ref       string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Module    string `yaml:"module,omitempty" json:"module,omitempty"`
	Authority int    `yaml:"authority" json:"authority"`
	Tier      string `yaml:"-" json:"-"` // "builtin" | "user" | "project" — stamped by Registry.add at load time, never set in YAML
}
```

`Tier` is deliberately not a YAML/schema field — a manifest author never declares it, it's derived purely from which directory the manifest was loaded from, matching `sourceLabel`'s existing role. `yaml:"-" json:"-"` keeps it out of `ParseManifest`'s schema validation and out of `depctl registry discover`'s draft-manifest YAML rendering (`internal/cli/registry.go`'s `yaml.Marshal(draft)`), so a discovered/drafted manifest never round-trips a stale tier.

`Registry.add` stamps it:

```go
func (r *Registry) add(m Manifest, sourceLabel string) {
	for i := range m.Sources {
		m.Sources[i].Tier = sourceLabel
	}
	// ... existing collision-warning and (post-REG-009) merge logic
	r.manifests[m.Metadata.Name] = m
}
```

If REG-009 lands first, `mergeSources` naturally carries each `Source`'s own `Tier` through unchanged (it merges whole `Source` values by ID, not field-by-field), so a merged manifest correctly has some sources stamped `"builtin"` and others `"project"` depending on which manifest actually contributed each one — this is the reason `Tier` belongs on `Source`, not `Metadata`: a REG-009-extended manifest is tier-mixed at the source level.

`TrustClassForSourceType` (`internal/data/generation/build.go`) stays a pure function of `Source.Type` per its existing doc comment ("TrustCommunity and TrustUser are assigned elsewhere, by whatever future mechanism lets a manifest be added outside the built-in/reviewed tier"). Its caller, `appendAttributed` (same file), changes:

```go
// current:
obj.TrustClass = TrustClassForSourceType(source.Type)

// after REG-010:
obj.TrustClass = trustClassFor(source)
```

```go
// trustClassFor layers Source.Tier over TrustClassForSourceType: a
// source loaded from the user or project registry tier is never fully
// trusted as "official"/"repository" no matter its declared Type — a
// human outside the built-in/reviewed set typed that YAML, so its
// content gets domain.TrustUser. A builtin-tier source (or one with no
// Tier stamped, e.g. REG-005's synthetic fallbackManifest, which never
// goes through Registry.add) keeps today's Type-based classification.
func trustClassFor(source registry.Source) domain.TrustClass {
	if source.Tier == "user" || source.Tier == "project" {
		return domain.TrustUser
	}
	return TrustClassForSourceType(source.Type)
}
```

Note `fallbackManifest` (`internal/cli/sync.go`, REG-005) constructs a `registry.Source` directly, never through `Registry.add` — its `Tier` is `""`, so `trustClassFor` falls through to `TrustClassForSourceType` unchanged, preserving REG-005's documented `TrustRepository` result exactly.

Surface in `depctl describe` (`internal/cli/describe.go`): `SourceEntry` gains a `Tier` field next to the existing `TrustClass`:

```go
type SourceEntry struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	URL        string            `json:"url,omitempty"`
	Module     string            `json:"module,omitempty"`
	Authority  int               `json:"authority"`
	Tier       string            `json:"tier,omitempty"` // "builtin" | "user" | "project"; empty for a REG-005 fallback source
	TrustClass domain.TrustClass `json:"trust_class"`
	Liveness   *registry.LivenessResult `json:"liveness,omitempty"`
}
```

populated at the same call site that already builds `SourceEntry{...}` from a `registry.Source` and calls `generation.TrustClassForSourceType(s.Type)` — that call becomes the new `trustClassFor`-equivalent logic (either exported from `generation` or duplicated as a small unexported helper in `internal/cli`, matching whichever `internal/cli` already does for similar generation-package reuse — check `describe.go`'s existing import of `"github.com/aleutian-ai/depctl/internal/data/generation"` before deciding; likely just export `trustClassFor` from `generation` alongside `TrustClassForSourceType`).

## Inputs / Outputs
- Input: a `Source` as loaded by `Registry.add` (tier known from `sourceLabel`) or constructed synthetically (REG-005 fallback, no tier).
- Output: `Source.Tier` populated for every registry-loaded source; `KnowledgeObject.TrustClass` is `domain.TrustUser` for any object built from a `user`/`project`-tier source, unchanged (`TrustClassForSourceType`-derived) for `builtin`-tier and untiered (fallback) sources; `depctl describe`'s `SourceEntry.Tier` visible in text/JSON/HTML output.

## Failure behavior
- No new failure mode — `Tier` is metadata, not a validated/required field. An empty `Tier` (fallback sources, or any `Source` constructed outside `Registry.add`, e.g. `runRegistryDiscover`'s draft) is a valid state and classifies via the existing `TrustClassForSourceType` path.

## Tests
- A source loaded from a project-tier manifest produces `KnowledgeObject.TrustClass == domain.TrustUser`, regardless of its declared `Type` (even `Type: "git"`, which would otherwise map to `TrustRepository`).
- A source loaded from a built-in manifest is unaffected — same `TrustClass` result as before this ticket.
- REG-005's `fallbackManifest` path still produces `TrustClass: TrustRepository` exactly as documented in REG-005's post-implementation note — regression check that untiered sources aren't accidentally swept into `TrustUser`.
- After REG-009's merge, a manifest with one built-in-tier source and one project-tier source (merged by ID) produces objects with the correct, per-source `TrustClass` — not a single manifest-wide trust level.
- `depctl describe` output (text and `--json`) shows `Tier` for a registry-loaded source and omits/empties it for a fallback source.

## Acceptance criteria
- [ ] `Source.Tier` is stamped by `Registry.add` from its existing `sourceLabel` parameter, never set via YAML/schema.
- [ ] A user- or project-tier source's objects carry `domain.TrustUser`, not a `Type`-derived class.
- [ ] Built-in-tier and untiered (REG-005 fallback) sources are classified exactly as before this ticket.
- [ ] `depctl describe`'s `SourceEntry` surfaces `Tier` alongside `TrustClass`.
