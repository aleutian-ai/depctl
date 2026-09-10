# REG-002: Registry loader

**Epic:** Knowledge Registry
**Status:** done
**Depends on:** REG-001
**Estimated size:** medium

## Goal
Load and merge `KnowledgePackage` manifests from the built-in registry, the user's local registry directory, and (optionally) a cloned remote registry, applying a defined priority order.

## Non-goals
- Does not implement matching logic (REG-003).
- Does not implement remote registry clone/fetch/update mechanics beyond reading an already-present local directory — cloning a remote git registry is a config-driven path that can reuse GIT-001 later; this ticket only reads whatever is on disk.

## Simplicity constraints
- Loader just walks directories for `*.yaml`/`*.yml` files and parses each with REG-001's `ParseManifest`. No lazy loading, no watch/reload — load once per process invocation (or per `ragctl registry reload`).
- Do not build a generic plugin loader; this is directory-walk + parse.

## Design
- Package: `internal/registry`
- Locations, in priority order (highest wins on conflict):
  1. project override: `<project-root>/.ragctl/registry/*.yaml` (optional, may not exist for v0.1 if project config isn't built yet — check `CLI-001` status; if absent, skip)
  2. user registry: `~/.config/ragctl/registry/*.yaml`
  3. built-in registry: embedded via `embed.FS` at `internal/registry/builtin/*.yaml`
- Function:
  ```go
  type Loader struct { ... }
  func (l *Loader) Load(ctx context.Context) (*Registry, error)
  type Registry struct {
      manifests map[string]Manifest // keyed by metadata.name
  }
  ```
- On duplicate `metadata.name` across sources, higher-priority source wins; loader records a warning (returned in a `Warnings []string` field on `Registry`, or logged via slog) rather than failing the whole load.

## Inputs / Outputs
- Input: config paths (from CLI-001 config) for user/remote registry dirs.
- Output: merged `*Registry` in memory.

## Failure behavior
- A single malformed manifest file produces a warning + is skipped, not a fatal load error — one bad community manifest shouldn't break the whole registry.
- If the built-in registry itself fails to parse, that IS fatal (it's compiled in and should never be invalid).

## Tests
- Duplicate name in user + built-in registry: user version wins, warning recorded.
- Malformed manifest in user dir: skipped with warning, load still succeeds.
- Empty user/remote dirs: only built-in manifests loaded.

## Acceptance criteria
- [x] Duplicate conflict returns a clear warning (not silently dropped, not a hard failure).
- [x] Priority order (project > user > built-in) is respected.

## Post-implementation note
`Loader.ProjectRegistryDir` and `Registry.add`'s priority handling both work correctly when a project directory is supplied — this ticket's own scope (the `Loader`/`Registry` merge mechanics) is fully done. Separately, no CLI call site (`loadRegistryForCLI` in `internal/cli/plan.go`, `internal/cli/registry.go`) actually passes a non-empty project directory yet, so project-tier overrides never take effect in practice today. That's a CLI-wiring gap, not a defect in this ticket's own scope — tracked as `docs/tickets/backlog/38-registry-federation/REG-008-project-registry-cli-wiring.md`.
