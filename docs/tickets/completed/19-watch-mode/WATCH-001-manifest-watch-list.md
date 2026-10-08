# WATCH-001: Manifest watch list

**Epic:** Watch Mode
**Status:** done
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
- [x] Only dependency manifest files are registered — never full project trees. (Each root directory is watched non-recursively and events are filtered to manifest names; see WATCH-002's note.)
- [x] Function is a pure lookup + existence filter, no I/O side effects beyond `os.Stat`.

## Post-implementation note

Lives in `internal/watch/paths.go`. `domain.Project` has no ecosystem field (just ID, root, and timestamps); a project's ecosystem is on its stored `Resolution`. So `WatchPaths` takes a `watch.Project{ID, Root, Ecosystem}`, which `depctl watch` builds from each registered project plus its resolution. A project that was never resolved isn't watched, since there's no ecosystem to look up.

The map lists only Go, Python, and Node, the ecosystems with a resolver. Rust and Java entries would never be used, because `scan` doesn't register projects it can't resolve; add a line when their resolvers land. There is no logger in the codebase, so an unknown ecosystem just returns nil rather than logging at debug level.

WATCH-002 ended up watching each project's root directory rather than individual files, so the watcher filters events by manifest *name* (`isManifest`) instead of using `WatchPaths`' list. `WatchPaths` is still what `depctl watch` prints at startup to show exactly which files it's watching.

Tests: `internal/watch/watcher_test.go` — only existing manifests are returned (no phantom `go.sum`, README ignored), a `go.sum` created later is picked up, and an ecosystem without a resolver returns nil.
