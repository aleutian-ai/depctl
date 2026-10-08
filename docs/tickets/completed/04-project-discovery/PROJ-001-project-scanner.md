# PROJ-001: Project scanner

**Epic:** Project Discovery
**Status:** done
**Depends on:** CORE-001
**Estimated size:** medium

## Goal
Implement directory walking that detects project roots for all v0.1 ecosystems by presence of their manifest files.

## Non-goals
- No dependency resolution here — detection only (resolution is RES-*/GO-*/PY-*/etc.).
- No `.depctl.yaml` project config parsing yet.

## Simplicity constraints
- Use `filepath.WalkDir` directly with a skip-list; do not build a generic pluggable "ignore rules" engine — a fixed slice of skipped directory names is sufficient for v0.1.
- Detection is "does this marker file exist in this directory" — no content parsing of manifests at scan time (that belongs to the resolver).

## Design
Package: `internal/project`

```go
type DetectedProject struct {
    Root      string
    Ecosystem domain.Ecosystem // may be ambiguous/multiple; see below
}

func Scan(ctx context.Context, root string) ([]DetectedProject, error)
```

Marker files per ecosystem:
```
go.mod                          -> go
pyproject.toml, requirements.txt -> python
package.json                    -> node
Cargo.toml                      -> rust
pom.xml, build.gradle, build.gradle.kts -> java
```
A single directory may match multiple ecosystems (e.g. a polyglot repo) — represent that as multiple `DetectedProject` entries sharing the same `Root`, not by picking one.

Skip directories by name (case-sensitive, exact match on path segment): `.git`, `node_modules`, `vendor`, `dist`, `build`, `target`, `.venv`, `venv`, `__pycache__`.

Nested projects: continue walking into subdirectories even after a match at a parent (e.g. `workspace/service-go` and `workspace/tools/rust-tool` are both detected), but do not walk *into* a skipped directory's contents.

Deduplication: the same canonical absolute root must not be returned twice even if the walk somehow revisits it (e.g. via symlink) — resolve `filepath.Abs` + `filepath.Clean` before dedup-keying.

## Inputs / Outputs
- Input: a filesystem root path to walk.
- Output: `[]DetectedProject`, each with an absolute canonical root and detected ecosystem(s).

## Failure behavior
- Permission-denied on a subdirectory: skip that subtree, collect a warning, continue the scan (do not abort the whole scan for one unreadable directory).
- Non-existent root path: return an error immediately.

## Tests
- Fixture tree per the plan:
```
workspace/
  service-go/       (go.mod)
  web/               (package.json)
  tools/rust-tool/   (Cargo.toml)
  data/python/       (pyproject.toml)
```
  scanning `workspace/` detects all four.
- Symlink loop / `.git` / `node_modules` are never descended into.
- Polyglot directory (both `go.mod` and `package.json` present) yields two `DetectedProject` entries.

## Acceptance criteria
- [x] Scanner detects all four projects in the fixture tree.
- [x] `.git`, `node_modules`, and other build/cache dirs are skipped.
- [x] Nested projects are supported.
- [x] The same project is never registered twice from a single scan.
