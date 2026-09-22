# REG-013: PyPI fallback manifest

**Epic:** npm/PyPI fallback manifest
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
Same as REG-012, for Python dependencies with no hand-curated manifest.

## Design
1. **Repository lookup:** `GET https://pypi.org/pypi/<package>/json`. `info.project_urls` has no fixed key — verified live, real packages use `Source`, `Repository`, `Homepage`, `Code`, `GitHub` inconsistently. Search a fixed priority list of known keys (`Source`, `Repository`, `Source Code`, `Homepage`, `GitHub`), plus `info.home_page`, for the first value shaped like a git-hostable URL (`github.com`, or resolvable the same way `resolveVanityImport` already handles a Go vanity import — reuse it rather than duplicating the go-import-meta-tag logic, since it's ecosystem-agnostic HTTP+regex work).
2. **Tag candidates:** `v${version}`, `${version}` — Python releases don't have Go's `+incompatible`/pseudo-version shapes or npm's monorepo `pkg@version` convention, so this is the two simple cases, each verified the same way REG-012's are before being trusted.
3. Scoped by `dep.Ecosystem == domain.EcosystemPython`.

## Non-goals
- Same as REG-012: no tarball-based acquisition (sdist/wheel download), no private index support (only pypi.org).
- No `Subdir` support — unlike npm, there's no equivalent registry-provided monorepo-directory field to key off of; a Python package published from a monorepo subdirectory isn't detectable from the PyPI API alone.

## Tests
- A `project_urls` response with the repo under each of the known keys resolves correctly, in priority order.
- A response with only `home_page` set falls back to it.
- A response with no git-shaped URL anywhere fails cleanly (not a manifest match on garbage).
- Tag candidates verified the same way as REG-012 (no unverified fallback).
- Live: a real, uncurated PyPI package (not already covered by fastapi/pydantic) syncs successfully.

## Acceptance criteria
- [ ] A real, uncurated PyPI package syncs via the automatic fallback.
- [ ] No git-shaped repository URL anywhere in the response is a clean failure, not a false match.
- [ ] A package with no matching tag fails cleanly.
