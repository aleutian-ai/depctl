# REG-008: Project registry CLI wiring

**Epic:** Registry Federation
**Status:** planned
**Depends on:** REG-002 (registry loader)
**Estimated size:** small

## Goal
Make `internal/registry.Loader.ProjectRegistryDir` — already a real field, already read by `Load` — actually reachable from the CLI, so a `<project-root>/.depctl/registry/*.yaml` manifest is loaded for real instead of being silently skipped on every command.

## Non-goals
- No new manifest semantics (extend/replace, merge-by-source-ID) — that's REG-009. This ticket only changes *which directories* get loaded, not how a collision between them is resolved (today's whole-manifest-overwrite-on-name-collision stays exactly as is).
- No change to the user-tier (`userRegistryDirPath`) loading — untouched.
- No project-config file format work (`.depctl.yaml` or similar) — the project-registry directory path is derived directly from the already-known `domain.Project.Root`, nothing new to parse.

## Simplicity constraints
- This is a wiring fix, not a new subsystem: every current call site that constructs a `registry.NewLoader(userDir, "")` gets its second argument changed to a real path derived from a project root already in hand at that call site.
- No new caching layer — a `Registry` is cheap to build (a handful of YAML files parsed once), so loading it once per project root inside an existing loop is acceptable; don't add a package-level cache keyed by root.

## Design
Confirmed in `internal/registry/loader.go`: `Loader.ProjectRegistryDir string // "" to skip"` and `Load` already calls `reg.loadDir(l.ProjectRegistryDir, "project")` unconditionally. Nothing in `internal/registry` needs to change.

The gap is entirely in `internal/cli`:

- `loadRegistryForCLI` (`internal/cli/plan.go`) currently takes only `ctx` and calls `registry.NewLoader(userDir, "").Load(ctx)` — no project root parameter at all.
- `computePlans` (`internal/cli/plan.go`) calls `loadRegistryForCLI(ctx)` **once**, before it loops over every registered `domain.Project` — but the project registry directory is scoped to one project's root, not to the CLI invocation as a whole. A fleet-wide `depctl plan`/`depctl sync` with multiple registered projects, each with its own `.depctl/registry/`, needs one `*registry.Registry` per project, not one shared across all of them.
- `runRegistryList`/`runRegistryDiscover` (`internal/cli/registry.go`) both call `registry.NewLoader(userDir, "").Load(...)` directly with a hard-coded `""` project dir, and have no project-root context at all today (they're not scoped to a registered project).

Changes:

```go
// internal/cli/plan.go
func loadRegistryForCLI(ctx context.Context, projectRoot string) (*registry.Registry, error) {
	userDir, err := userRegistryDirPath()
	if err != nil {
		return nil, fmt.Errorf("resolve user registry dir: %w", err)
	}
	projectDir := ""
	if projectRoot != "" {
		projectDir = filepath.Join(projectRoot, ".depctl", "registry")
	}
	return registry.NewLoader(userDir, projectDir).Load(ctx)
}
```

`computePlans` moves the `loadRegistryForCLI` call inside its per-project loop, calling it with `p.Root` for each `domain.Project` it processes — replacing the single call currently made before the loop. `RunSync` (`internal/cli/sync.go`) has the same shape problem: it calls `loadRegistryForCLI(ctx)` once before iterating `plans`, then reuses that one `reg` across every `projectPlan`/`syncVersion` call regardless of which project the action belongs to. It needs to either look up the right per-project registry from a `map[string]*registry.Registry` keyed by project root (built once, projects rarely number in the thousands) or accept a `map[string]*registry.Registry` returned alongside `plans` from `computePlans` — pick whichever keeps `RunSync`'s existing loop shape simplest; do not restructure `computePlans`'s return type beyond what's needed to carry per-project registries out to `RunSync`.

`runRegistryList`/`runRegistryDiscover` are not scoped to a registered project today (no `--project` flag, no project-root argument) — leave their behavior as user+built-in only (pass `""` for project dir, as before) rather than inventing a new flag; note this as an intentional non-goal, not an oversight, since REG-002's original design only requires project-tier loading where a project is actually in scope (plan/sync), not for the standalone `registry` inspection commands.

## Inputs / Outputs
- Input: a `domain.Project.Root` already resolved by `computePlans`'s existing project loop.
- Output: each project's plan/sync computed against a `*registry.Registry` that includes that project's own `.depctl/registry/*.yaml` manifests (when the directory exists), on top of built-in and user tiers — instead of every project sharing one registry built with an empty project dir.

## Failure behavior
- A missing `<project-root>/.depctl/registry/` directory is not an error — `loadDir` already treats a missing dir as "load nothing" (`filepath.Glob` on a nonexistent dir returns no matches, no error). No new failure mode is introduced.
- A malformed manifest in the project directory is skipped with a warning, exactly as a malformed user-tier manifest is today (`Registry.Warnings`).

## Tests
- Two registered projects, each with a distinct `.depctl/registry/*.yaml` manifest for the same `metadata.name`: `depctl plan` for project A resolves using A's manifest, project B's plan resolves using B's — not cross-contaminated by loading order.
- A project with no `.depctl/registry/` directory behaves identically to before this ticket (built-in + user tiers only).
- `depctl sync --project <A>` builds/replicates using A's project-tier manifest when one exists there and a built-in manifest of the same name would otherwise have matched.
- `depctl registry list`/`depctl registry discover` behavior is unchanged (still user+built-in only) — regression check that this ticket didn't accidentally scope them to a project.

## Acceptance criteria
- [ ] `loadRegistryForCLI` accepts a project root and derives `<root>/.depctl/registry` as the project registry dir.
- [ ] `computePlans` builds a registry per project root, not one shared registry for the whole invocation.
- [ ] `RunSync` uses the same per-project registry as `computePlans` did for that project's plan, not a single global one.
- [ ] `depctl registry list`/`depctl registry discover` are unaffected (still `""` project dir, documented as intentional).
