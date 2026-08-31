# Epic: Project Discovery

Find and register the local projects `ragctl` will manage, by detecting ecosystem manifest files across a directory tree, assigning each a stable ID, and persisting them via `ragctl scan`.

## Tickets
- [PROJ-001 — Project scanner](PROJ-001-project-scanner.md): directory walk detecting go.mod/pyproject.toml/package.json/Cargo.toml/pom.xml etc., skipping build/cache dirs.
- [PROJ-002 — Stable project IDs](PROJ-002-stable-project-ids.md): `BLAKE3(canonical root)`-derived project ID; documents move/rename policy.
- [PROJ-003 — `ragctl scan`](PROJ-003-ragctl-scan.md): CLI command wiring scanner + IDs into bbolt, idempotent.
