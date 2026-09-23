# PY-001: Python project detection

**Epic:** Python Resolver
**Status:** done
**Depends on:** RES-001
**Estimated size:** small

## Goal
Implement `Resolver.Detect` for Python projects by locating the standard lock/manifest files.

## Non-goals
- Actual resolution (PY-002/003/004) — detection only decides "is this a Python project," not which versions it uses.

## Simplicity constraints
- Detection is a file-existence check in the project root — no parsing, no tool invocation.

## Design
Package: `internal/resolver/python`.

```go
type Resolver struct{}

func (r *Resolver) Name() string { return "python" }
func (r *Resolver) Detect(ctx context.Context, root string) (bool, error)
```

Detect returns true if any of these exist at `root`:
```text
uv.lock
pyproject.toml
requirements.txt
poetry.lock
Pipfile.lock
```

Store which specific file(s) were found on the `Resolution` (populated later by PY-002/003/004) so the resolution step knows which strategy to use without re-scanning.

## Inputs / Outputs
- Input: project root path.
- Output: bool detected + (for downstream use) which manifest/lock files are present.

## Failure behavior
- Filesystem read errors (e.g. permission denied) return a wrapped error, not a false negative.

## Tests
- Root with only `pyproject.toml` → detected.
- Root with `uv.lock` and `pyproject.toml` → detected, `uv.lock` flagged as present (PY-002 will prefer it).
- Root with none of the files → not detected, no error.

## Acceptance criteria
- [ ] Detects all five recognized file types.
- [ ] Registered in the resolver registry alongside the Go resolver (RES-001) with deterministic priority ordering.
