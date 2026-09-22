# NODE-001: Node project detection

**Epic:** Node / JavaScript / TypeScript Resolver
**Status:** done
**Depends on:** RES-001
**Estimated size:** small

## Goal
Detect Node.js/JavaScript/TypeScript project roots by locating `package.json` plus whichever lockfile is present.

## Non-goals
- Parsing dependency versions (NODE-002/003).
- Handling multiple lockfile types differently — this ticket only detects presence and picks one.

## Simplicity constraints
- Do not build a generic "lockfile plugin" abstraction. A single switch/priority list is enough.
- Do not attempt to detect Deno or other JS runtimes.

## Design
Package: `internal/resolver/node`

```go
type Resolver struct{}

func (r *Resolver) Name() string { return "node" }
func (r *Resolver) Detect(ctx context.Context, root string) (bool, error)
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) // stub until NODE-002/003
```

Detect returns true if `package.json` exists at `root`. Recognized lockfiles, in priority order for `Resolve` (later tickets):
```text
package-lock.json
pnpm-lock.yaml
yarn.lock
bun.lock
```
Store which lockfile was found on the Resolution (`LockPath`) so downstream tickets know which parser to use.

## Inputs / Outputs
- Input: candidate project root directory.
- Output: bool detected + (later) `domain.Resolution{Ecosystem: EcosystemNode, ManifestPath, LockPath}`.

## Failure behavior
- Missing `package.json` → `Detect` returns `false, nil` (not an error).
- Unreadable directory → return error from `Detect`.

## Tests
- Fixture with `package.json` + `package-lock.json` → detected, lockfile = package-lock.json.
- Fixture with `package.json` only, no lockfile → detected, LockPath empty (later resolvers must handle this by falling back to manifest-only or erroring per NODE-002).
- Fixture with no `package.json` → not detected.
- Fixture with `pnpm-lock.yaml` and no `package-lock.json` → lockfile = pnpm-lock.yaml.

## Acceptance criteria
- [ ] `Detect` correctly identifies Node projects across `testdata/projects/node-npm/` and `testdata/projects/node-pnpm/` fixtures.
- [ ] Lockfile priority order matches the list above.

## Post-implementation note
Built and tested (`internal/resolver/node`). Like the Python resolver (epic 20), this gives resolution only — resolved Node dependencies mostly can't *sync* yet, since the automatic fallback-manifest path is Go-only and almost no npm packages are curated in the registry. See `docs/tickets/planned/56-npm-pypi-fallback-manifest`.
