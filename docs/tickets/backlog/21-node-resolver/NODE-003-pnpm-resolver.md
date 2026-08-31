# NODE-003: pnpm resolver

**Epic:** Node / JavaScript / TypeScript Resolver
**Status:** planned
**Depends on:** NODE-001
**Estimated size:** medium

## Goal
Parse `pnpm-lock.yaml` to resolve exact package versions, including pnpm workspace semantics.

## Non-goals
- Yarn/Bun (NODE-004/005).
- Full pnpm workspace dependency graph visualization — only exact version resolution is required.

## Simplicity constraints
- Use `gopkg.in/yaml.v3` (already a project dependency) — no dedicated pnpm-lock parsing library.
- Support the current lockfile version format only; do not special-case every historical pnpm-lock schema revision.

## Design
Package: `internal/resolver/node` (same package as NODE-002, different file e.g. `pnpm.go`).

Parse top-level `packages:` map where keys look like `/react@19.1.3` or `/@types/node@20.1.0`. Split the key into name + version (name may be scoped, i.e. contain a `/`). Cross-reference `importers:` section for workspace roots to determine direct vs transitive per workspace member, and to detect `link:` (local/workspace) specifiers.

```go
type pnpmLock struct {
    Importers map[string]struct {
        Dependencies    map[string]struct{ Version string `yaml:"version"` } `yaml:"dependencies"`
        DevDependencies map[string]struct{ Version string `yaml:"version"` } `yaml:"devDependencies"`
    } `yaml:"importers"`
    Packages map[string]any `yaml:"packages"`
}
```

## Inputs / Outputs
- Input: project root with `pnpm-lock.yaml` (+ workspace `pnpm-workspace.yaml` if present, read-only reference).
- Output: `domain.Resolution` per NODE-002 shape.

## Failure behavior
- Unparseable YAML → typed `ResolutionError`.
- Workspace member root without an `importers` entry → treated as no direct deps for that member, not an error.

## Tests
- Use real small pnpm fixtures under `testdata/projects/node-pnpm/` (single package and workspace variants).
- Scoped package resolution.
- Workspace-local dependency marked local.

## Acceptance criteria
- [ ] Exact versions match `pnpm-lock.yaml` contents for fixture projects.
- [ ] Workspace members correctly attributed direct dependencies.
