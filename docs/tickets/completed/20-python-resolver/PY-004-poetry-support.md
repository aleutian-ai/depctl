# PY-004: Poetry support

**Epic:** Python Resolver
**Status:** done
**Depends on:** PY-001
**Estimated size:** medium

## Goal
Resolve exact versions from `poetry.lock`, mirroring PY-002's approach for uv.

## Non-goals
- This ticket may slip past v0.1 if schedule requires — it is explicitly lower priority than PY-002/PY-003 per the implementation plan.

## Simplicity constraints
- Parse `poetry.lock` directly (also TOML, stable `[[package]]` schema) rather than invoking a `poetry` CLI subprocess — avoids requiring Poetry to be installed just to read lock data.
- Reuse the same `DependencyVersion` normalization and local/git classification logic from PY-002 rather than duplicating it — extract a shared helper if the two implementations converge, but do not do this speculatively before both exist.

## Design
Package: `internal/resolver/python` (extends PY-001/PY-002).

Parse `poetry.lock` `[[package]]` entries: `name`, `version`, `source.type` (`legacy`/`git`/`directory`/`file`/`url`).

```text
source.type == "git"       -> mark git-sourced, preserve rev
source.type == "directory" -> mark local: true, excluded from sync
default (pypi)              -> normal resolved DependencyVersion
```

Only activate this resolver when `uv.lock` is absent but `poetry.lock` is present (priority: uv > poetry > requirements, per PY-001 detection order).

## Inputs / Outputs
- Input: project root containing `poetry.lock`.
- Output: `Resolution{Ecosystem: python, Dependencies: [...], ResolvedBy: "poetry.lock"}`.

## Failure behavior
- Malformed lockfile → typed `ResolutionError`, same convention as PY-002.

## Tests
- PyPI-sourced package resolves to exact version.
- Git-sourced package preserves rev.
- Directory/path-sourced package marked local and excluded from sync.

## Acceptance criteria
- [ ] `poetry.lock` parsed without requiring the `poetry` binary.
- [ ] Priority order (uv > poetry > requirements) respected when multiple lock files coexist.
