# PY-003: requirements resolver

**Epic:** Python Resolver
**Status:** planned
**Depends on:** PY-001
**Estimated size:** small

## Goal
Resolve exact-pinned entries from `requirements.txt` (`foo==1.2.3`); explicitly mark range-only entries (`foo>=1.2`) as unresolved rather than guessing.

## Non-goals
- Active-environment inspection (e.g. `pip show`) to resolve ranges — explicitly out of scope for v0.1 per the source docs' "never silently pretend a range is an exact installed version" rule; ranges stay unresolved.

## Simplicity constraints
- A single-pass line parser for `requirements.txt` syntax subset actually needed (`name==version`, comments, blank lines, `-r other.txt` includes are out of scope — skip with a warning, don't recurse).
- No PEP 508 full-grammar parser; regex/simple tokenizing for `==` pins is sufficient.

## Design
Package: `internal/resolver/python` (extends PY-001/PY-002), used only when `uv.lock`/`poetry.lock` are absent but `requirements.txt` is present.

```go
type reqEntry struct {
    Name     string
    Operator string // "==", ">=", "<=", "~=", "" (unpinned)
    Version  string
}
```

Rule:
```text
foo==1.2.3   -> resolved DependencyVersion{Version: "1.2.3"}
foo>=1.2     -> unresolved: included in Resolution.Warnings, excluded from Dependencies
foo          -> unresolved (no version at all)
```

## Inputs / Outputs
- Input: `requirements.txt` contents.
- Output: `Resolution` with `Dependencies` (exact pins only) and `Warnings` (unresolved range/unpinned entries, named explicitly).

## Failure behavior
- Never fabricate a version for a range constraint. Warnings must name the exact package and constraint that couldn't be resolved.

## Tests
- `pydantic==2.11.7` → resolved.
- `pydantic>=2.10` → unresolved, appears in warnings, not in dependencies.
- Bare `pydantic` (no version) → unresolved.
- Comment lines and blank lines ignored without error.

## Acceptance criteria
- [ ] Exact pins resolve correctly.
- [ ] Range/unpinned entries never silently resolved — always surfaced as warnings.
