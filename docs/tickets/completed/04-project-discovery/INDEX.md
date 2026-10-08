# Epic: Project Discovery

Find and register the local projects `depctl` will manage, by detecting ecosystem manifest files across a directory tree, assigning each a stable ID, and persisting them via `depctl scan`.

**Reopened 2026-09-29** for `PROJ-004`, found live via epic 49's STRESS-008 — the same physical directory, scanned via a literal symlinked path versus a realpath-resolving relative path, registered as two different projects. Fixed and reclosed the same day.

## Tickets
- [PROJ-001 — Project scanner](PROJ-001-project-scanner.md): directory walk detecting go.mod/pyproject.toml/package.json/Cargo.toml/pom.xml etc., skipping build/cache dirs.
- [PROJ-002 — Stable project IDs](PROJ-002-stable-project-ids.md): `BLAKE3(canonical root)`-derived project ID; documents move/rename policy.
- [PROJ-003 — `depctl scan`](PROJ-003-depctl-scan.md): CLI command wiring scanner + IDs into bbolt, idempotent.
- [PROJ-004 — Canonical root doesn't resolve symlinks](PROJ-004-canonical-root-does-not-resolve-symlinks.md) — **done.** `canonicalize()` (`filepath.Abs`+`Clean`) never resolved symlinks, so a literal symlinked path (`depctl scan /tmp/foo`) and a `"."`-relative, `os.Getwd()`-resolved path (`depctl serve`'s own startup scan) could diverge into two different canonical roots — and therefore two different project IDs — for the identical physical directory. Fixed by adding `filepath.EvalSymlinks` to `canonicalize`; surfaced and fixed three pre-existing test fixtures that had silently relied on the old, symlink-preserving behavior.
