# Epic: Python Resolver

Second ecosystem resolver. Detects Python projects and resolves exact dependency versions preferring lockfiles (uv, then Poetry, then exact-pinned requirements.txt) over unpinned manifests. Built out of backlog order, at the user's explicit request, alongside the Node resolver (epic 21) — before this, only architecture.md's epics-1-2 note mentioned this; the tickets themselves were left saying `planned` in `backlog/` long after the code shipped. See docs/architecture.md's "Epics 1-2" section for the doc-drift precedent this follows.

## Tickets
- [PY-001](PY-001-python-project-detection.md) — Detect Python projects via uv.lock/pyproject.toml/requirements.txt/poetry.lock/Pipfile.lock.
- [PY-002](PY-002-uv-resolver.md) — Priority-one resolver: parse `uv.lock` for exact versions, handling direct/transitive/git/editable packages.
- [PY-003](PY-003-requirements-resolver.md) — Resolve exact `==` pins from `requirements.txt`; never guess ranges.
- [PY-004](PY-004-poetry-support.md) — Parse `poetry.lock` for exact versions (may ship after v0.1 if schedule slips).

## Post-implementation notes
- All four tickets (PY-001..004) are built and tested: `internal/resolver/python` detects via `uv.lock`/`poetry.lock`/`requirements.txt`/`pyproject.toml`/`Pipfile.lock` (priority order), and resolves the first three lockfile-shaped ones exactly. `pyproject.toml` alone and `Pipfile.lock` are *detected* (so `depctl scan` reports the project, not silently ignores it) but not resolved — `Resolve` returns a clear, typed error naming exactly which file to add, not a guess.
- PY-004 (Poetry) shipped despite its own ticket hedging "may ship after v0.1 if schedule slips" — it didn't slip.
- **What this resolver does NOT give you: sync.** Resolution (knowing your exact dependency versions) and acquisition (fetching/indexing their docs) are separate. Only Go dependencies get an automatic no-curation fallback manifest; Python dependencies sync only if hand-curated in the registry (2 exist: fastapi, pydantic) — see `docs/tickets/planned/56-npm-pypi-fallback-manifest` (Python leg) for the gap this leaves, found live scanning a real Python project (mem0) where every resolved dependency failed sync with "no registry manifest".
