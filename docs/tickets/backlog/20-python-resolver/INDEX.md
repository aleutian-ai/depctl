# Epic: Python Resolver

Second ecosystem resolver, started only after the Go vertical slice is stable. Detects Python projects and resolves exact dependency versions preferring lockfiles (uv, then Poetry) over unpinned manifests. Corresponds to Milestone 19 in the implementation plan.

## Tickets
- [PY-001](PY-001-python-project-detection.md) — Detect Python projects via uv.lock/pyproject.toml/requirements.txt/poetry.lock/Pipfile.lock.
- [PY-002](PY-002-uv-resolver.md) — Priority-one resolver: parse `uv.lock` for exact versions, handling direct/transitive/git/editable packages.
- [PY-003](PY-003-requirements-resolver.md) — Resolve exact `==` pins from `requirements.txt`; never guess ranges.
- [PY-004](PY-004-poetry-support.md) — Parse `poetry.lock` for exact versions (may ship after v0.1 if schedule slips).
