# REG-009: Extend/replace manifest semantics

**Epic:** Registry Federation
**Status:** planned
**Depends on:** REG-002 (registry loader), REG-008 (project registry CLI wiring — so there's a real project tier to extend from)
**Estimated size:** medium

## Goal
Let a user- or project-tier manifest *add or override individual sources* on top of a lower-priority manifest of the same `metadata.name`, instead of the only option being a full silent replacement — matching the design doc's explicit call for "extend it with internal sources... override individual sources... replace it entirely" (`docs/scratch/depctl_architecture_eval_next_steps-2.md` §6A "Registry layering").

## Non-goals
- No merge of any field other than `Sources` — `Match`, `Version`, and `Metadata` itself on an `extends` manifest are taken entirely from the *incoming* (higher-priority) manifest, not merged field-by-field with the base. A manifest that wants to extend another still fully declares its own `match`/`version` block; only the source list gets merged. Partial-field merging of `Match`/`Version` is more machinery than this ticket's actual need (adding/overriding sources) justifies.
- No multi-level `extends` chain resolution beyond one hop (A extends B, B extends C) — `Extends` names the manifest to merge sources from as loaded *so far* in tier order; if that named manifest was itself built via an `extends` merge from an earlier tier, the merge naturally chains through `Registry.manifests` holding the already-merged result, but this ticket does not add cycle detection or explicit multi-hop resolution logic — `extends` naming a manifest that doesn't exist yet in `r.manifests` (wrong order, typo) is a load-time warning + explicit-replace fallback, not a hard error.
- No new `registryMode` value beyond `"extend"`/`"replace"` — no partial/select-only mode.

## Simplicity constraints
- `Registry.add` (`internal/registry/loader.go`) already has an unconditional-overwrite branch on a `metadata.name` collision — this ticket adds one conditional inside that same function, not a new merge subsystem or separate pass over `r.manifests` after loading.
- Reuse the existing `Source.ID` field as the merge key — no new source-identity field.

## Design
Confirmed in `internal/registry/manifest.go`: `Metadata` today is `type Metadata struct { Name string }` — just the name, nothing else. Confirmed in `internal/registry/loader.go`: `Registry.add(m Manifest, sourceLabel string)` on a `metadata.name` collision only appends a warning and then unconditionally does `r.manifests[m.Metadata.Name] = m` — the incoming manifest always wins in full.

Add two fields to `Metadata`:

```go
// Metadata identifies a manifest, independent of what it matches.
type Metadata struct {
	Name         string `yaml:"name" json:"name"`
	Extends      string `yaml:"extends,omitempty" json:"extends,omitempty"`           // name of a manifest to inherit Sources from
	RegistryMode string `yaml:"registryMode,omitempty" json:"registryMode,omitempty"` // "extend" (default) | "replace"
}
```

`Registry.add` becomes:

```go
func (r *Registry) add(m Manifest, sourceLabel string) {
	existing, hasExisting := r.manifests[m.Metadata.Name]
	if hasExisting {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"%s manifest %q overrides an earlier one (was from a lower-priority source; had ecosystems %v)",
			sourceLabel, m.Metadata.Name, existing.Match.Ecosystems))
	}

	mode := m.Metadata.RegistryMode
	if mode == "" {
		mode = "extend"
	}
	extendsName := m.Metadata.Extends
	if extendsName == "" {
		extendsName = m.Metadata.Name // self-extend: merge onto the same-named prior manifest
	}

	if mode == "extend" {
		if base, ok := r.manifests[extendsName]; ok {
			m.Sources = mergeSources(base.Sources, m.Sources)
		} else if m.Metadata.Extends != "" {
			// named an extends target that isn't loaded (yet, or at all) —
			// fall back to replace, matching pre-REG-009 behavior, with a
			// warning naming the problem explicitly.
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%s manifest %q declares extends: %q but no such manifest is loaded; using %q as-is (replace)",
				sourceLabel, m.Metadata.Name, m.Metadata.Extends, m.Metadata.Name))
		}
		// mode == "extend" with no prior same-name manifest and no
		// explicit Extends target: nothing to merge, m.Sources stands as-is
		// — behaviorally identical to today's first-load case.
	}
	// mode == "replace": m.Sources stands as-is, no merge — today's
	// exact behavior, now explicit and opt-in rather than the only option.

	r.manifests[m.Metadata.Name] = m
}

// mergeSources merges incoming onto base by Source.ID: an ID present in
// both keeps the incoming Source (incoming wins), an ID only in base is
// kept, and an ID only in incoming is appended. Order: base sources
// first (incoming-overridden ones keep their base position), then new
// incoming-only sources appended in their incoming order.
func mergeSources(base, incoming []Source) []Source { ... }
```

Self-extend (no explicit `extends`, `registryMode` defaults to `"extend"`) makes the *default* behavior for a same-name collision become a source-level merge rather than a whole-manifest replace — this is the important behavior shift the ticket wants: today, a project manifest with the same `metadata.name` as a built-in one silently drops every built-in source the moment it's not repeated verbatim. After this ticket, the common case (a project manifest just wants to add one extra source to the built-in list) no longer requires copy-pasting the built-in manifest's `sources:` block. `Metadata.Extends` (an explicit name, potentially different from `Metadata.Name`) exists for the less common case of building on a *different*-named manifest as a template.

Update `internal/registry/schema/knowledge-package.schema.json`: the `metadata` object is currently `"required": ["name"], "additionalProperties": false, "properties": {"name": {...}}`. Add `extends` (string) and `registryMode` (`"enum": ["extend", "replace"]`) as optional properties — do not add them to `required`.

## Inputs / Outputs
- Input: two manifests with the same `metadata.name` (or an explicit `extends` naming a different manifest), loaded in tier order (builtin → user → project).
- Output: the merged manifest in `Registry.manifests`, with `Sources` combined by ID per `mergeSources`'s rule, when `registryMode` is `"extend"` (the default); the incoming manifest verbatim when `registryMode` is `"replace"` (today's exact behavior).

## Failure behavior
- `extends` naming a manifest not yet loaded: warning, falls back to replace (the manifest is used as-is) — never a hard load failure, consistent with REG-002's existing "a malformed manifest is skipped with a warning, never fatal" posture for non-built-in tiers.
- `registryMode` with any value other than `"extend"`/`"replace"` is rejected at schema-validation time (`ParseManifest`'s existing JSON Schema enum check) with the existing `ManifestError` — no new error type.
- A manifest with no `metadata.name` collision at all (the common case — most manifests are unique) is entirely unaffected; `mergeSources` is only reached on a name collision.

## Tests
- A project manifest with the same `metadata.name` as a built-in one, one new `Source.ID` not in the built-in's list, no `extends`/`registryMode` set: the resulting manifest has every built-in source plus the new one.
- A project manifest re-declaring a `Source.ID` that also exists in the built-in manifest, with a different `URL`/`Authority`: the resulting source is the project's version (incoming wins on ID collision), not the built-in's.
- A project manifest with `registryMode: replace`: behaves exactly as pre-REG-009 (built-in sources entirely dropped, only the project's own `Sources` remain).
- A manifest with `extends: some-other-manifest-name` pointed at a real, already-loaded manifest of a different name: sources merge from that named manifest, not from any same-named prior manifest.
- A manifest with `extends` naming a manifest that was never loaded: warning present in `Registry.Warnings`, manifest used as-is (replace fallback), load does not fail.
- Existing REG-002/REG-005 tests (whole-manifest built-in load, `fallbackManifest`'s synthetic single-manifest case) are unaffected — no `metadata.name` collision in those paths, so `mergeSources` never triggers.

## Acceptance criteria
- [ ] `Metadata.Extends` and `Metadata.RegistryMode` are real fields, validated by the embedded JSON Schema.
- [ ] Default behavior on a same-name collision (no `registryMode` set) is now an ID-keyed source merge, not a whole-manifest overwrite.
- [ ] `registryMode: replace` preserves the exact pre-REG-009 overwrite behavior, opt-in and explicit.
- [ ] An `extends` target that isn't loaded degrades to a warning + replace, never a hard failure.
