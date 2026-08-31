# WATCH-001: Manifest watch list

**Epic:** Watch Mode
**Status:** planned
**Depends on:** project scanner (PROJ-001)
**Estimated size:** small

## Goal
For each registered project, compute the specific set of dependency-manifest file paths that should be watched — never the whole project tree.

## Non-goals
- Actually watching files (WATCH-002) or reacting to changes (WATCH-003).

## Simplicity constraints
- A static, ecosystem-keyed map of filenames — not a plugin/registry mechanism. Adding a new ecosystem's watch list is a one-line map edit.
- No recursive directory watching; only the exact manifest file paths per project root (plus nested manifests already known from scanning).

## Design
Package: `internal/project` (extends PROJ-001) or new `internal/watch`.

```go
var watchFiles = map[Ecosystem][]string{
    EcosystemGo:     {"go.mod", "go.sum"},
    EcosystemPython: {"pyproject.toml", "requirements.txt", "uv.lock", "poetry.lock", "Pipfile.lock"},
    EcosystemNode:   {"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock"},
    EcosystemRust:   {"Cargo.toml", "Cargo.lock"},
    EcosystemJava:   {"pom.xml", "build.gradle", "build.gradle.kts"}, // + lock state per project
}

func WatchPaths(p Project) []string // resolves watchFiles[p.Ecosystem] to absolute paths under p.Root, filtered to files that exist
```

## Inputs / Outputs
- Input: a registered `Project` (root + ecosystem, from PROJ-001).
- Output: absolute file paths to watch for that project.

## Failure behavior
- Ecosystem not in the map → return empty list, log at debug level; never error (unsupported ecosystems are already filtered by the scanner).

## Tests
- Go project returns exactly `go.mod`, `go.sum` (only those present on disk).
- Missing lockfile (e.g. no `go.sum` yet) is excluded from the returned list, not included as a phantom path.

## Acceptance criteria
- [ ] Only dependency manifest files are registered — never full project trees.
- [ ] Function is a pure lookup + existence filter, no I/O side effects beyond `os.Stat`.
