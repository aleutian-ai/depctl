# PY-002: uv resolver

**Epic:** Python Resolver
**Status:** done
**Depends on:** PY-001
**Estimated size:** medium

## Goal
Resolve exact Python package versions from `uv.lock` — the priority-one Python resolution strategy for v0.1.

## Non-goals
- Poetry (PY-004) or bare `requirements.txt` (PY-003) — separate tickets, only invoked when `uv.lock` is absent.

## Simplicity constraints
- Prefer direct parsing of `uv.lock` (it's TOML, has a stable schema) over shelling out to `uv tree --json` unless direct parsing proves insufficient — fewer runtime dependencies (no `uv` binary requirement for read-only resolution).
- One resolver, one lockfile format. Do not build a generic "TOML lockfile" abstraction shared speculatively with other ecosystems.

## Design
Package: `internal/resolver/python` (extends PY-001).

```go
func (r *Resolver) Resolve(ctx context.Context, root string) (Resolution, error)
```

When `uv.lock` present: parse via a TOML library (add `github.com/BurntSushi/toml` or similar minimal dependency — confirm none already in go.mod before adding), extract `[[package]]` entries: `name`, `version`, `source` (registry / git / editable).

Normalize each to `DependencyVersion{Ecosystem: "python", Name: name, Version: version, ResolvedBy: "uv.lock"}`.

Classification rules:
- Git-sourced packages: mark with `source = git` and preserve the commit/rev in metadata (do not collapse to a bare version).
- Editable/local packages (`source = { editable = "..." }` or `path = "..."`): mark `local: true`, excluded from external knowledge sync (same rule as Go's main module in GO-003).

## Inputs / Outputs
- Input: project root containing `uv.lock`.
- Output: `Resolution{Ecosystem: python, Dependencies: [...], Fingerprint: hash(uv.lock)}`.

## Failure behavior
- Malformed/unparseable `uv.lock` → typed `ResolutionError` naming the file and parse issue; never silently skip packages.

## Tests
- Direct package entry resolves to exact name+version.
- Transitive package entry resolves same as direct (uv.lock doesn't distinguish, but flag direct via `pyproject.toml` dependency list if easy; otherwise mark unknown).
- Git package entry preserves the commit/rev, not collapsed to "latest".
- Editable/local package marked `local: true` and excluded from sync.

## Acceptance criteria
- [ ] Direct package, transitive package, Git package, and editable/local package cases all pass fixture tests.
- [ ] Same `uv.lock` content produces a stable resolution fingerprint across runs.
