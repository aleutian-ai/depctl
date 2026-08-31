# NODE-002: npm lock resolver

**Epic:** Node / JavaScript / TypeScript Resolver
**Status:** planned
**Depends on:** NODE-001
**Estimated size:** medium

## Goal
Parse `package-lock.json` (current lockfile format, `lockfileVersion` 3) to resolve exact installed package versions, distinguishing direct vs transitive and workspace/local packages.

## Non-goals
- pnpm/yarn/bun formats (NODE-003/004/005).
- Resolving from `package.json` ranges alone (last resort, out of scope here).

## Simplicity constraints
- Support lockfileVersion 3 (npm 7+) only; do not attempt to support legacy v1 format unless a real fixture requires it.
- No custom JSON schema validation library — use `encoding/json` with a minimal struct covering the `packages` map.

## Design
Package: `internal/resolver/node`

Parse the `packages` object of `package-lock.json`:
```go
type npmLockPackages struct {
    Packages map[string]struct {
        Version  string `json:"version"`
        Resolved string `json:"resolved"`
        Dev      bool   `json:"dev"`
        Link     bool   `json:"link"` // workspace/local symlink
    } `json:"packages"`
}
```
Key `""` is the root project (skip). Keys of form `node_modules/<name>` (nested paths collapse to package name). A package is `Direct` if it appears in the root `package.json`'s `dependencies`/`devDependencies`. `Link: true` entries are marked `local` and excluded from external knowledge sync (per registry package identity — use registry name, e.g. `react`, `@types/node`, `@grpc/grpc-js`).

Normalized identity:
```json
{"ecosystem": "node", "name": "react", "version": "19.1.3", "direct": true, "resolved_by": "package-lock.json"}
```

## Inputs / Outputs
- Input: project root with `package.json` + `package-lock.json`.
- Output: `domain.Resolution` with `Dependencies []DependencyVersion`.

## Failure behavior
- Malformed JSON → typed `ResolutionError` with file path.
- lockfileVersion < 3 → warning + best-effort parse, or explicit "unsupported lockfile version" error (implementer's choice, document in code comment).

## Tests
- Simple direct dependency.
- Transitive dependency (nested under another package).
- Scoped package name (`@types/node`).
- npm workspace member marked local via `link: true`.

## Acceptance criteria
- [ ] Same lockfile parsed twice yields identical, stable `Fingerprint`.
- [ ] Workspace/local packages excluded from external sync but still recorded.
- [ ] Scoped package names preserved exactly (registry identity match).
