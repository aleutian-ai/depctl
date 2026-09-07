# Epic: Local-First Acquisition

`git.Cache.EnsureMirror` (GIT-001) already avoids re-fetching a URL it has mirrored before, but has no concept of "this exact repo already exists somewhere else on disk" (e.g. under `~/offline-knowledge`, populated by `hack/fetch-corpus` — a completely separate mechanism with its own directory layout) and no fast, clear failure when offline and nothing local is found — a real `git clone` attempt against an unreachable host just hangs until GIT-001's clone timeout expires before surfacing an error.

Extends epic 08 (git-acquisition); ticket IDs continue that epic's `GIT-` numbering since it's the same subsystem (`internal/source/git`), not a new one.

## Tickets
- [GIT-004](GIT-004-fast-offline-failure.md) — fail fast and clearly when offline and a source isn't already mirrored, instead of waiting out the full clone timeout. Minimum version of this epic; ships alone if nothing else here does.
- [GIT-005](GIT-005-local-search-path-discovery.md) — before falling back to network, check configured local directories for an already-cloned copy of the same repo and mirror from that instead.

## Non-goals
- No change to `EnsureMirror`'s existing cache-hit behavior (already mirrored by ragctl = already fast, already offline-safe).
- No automatic writing of registry manifests pointing at discovered local copies — GIT-005 only changes *acquisition* (what `EnsureMirror` mirrors from), not the registry (what URL a manifest declares). A manifest still declares the real, canonical URL; GIT-005 is what makes that URL resolve locally when possible without the manifest author needing to know a local copy exists.
