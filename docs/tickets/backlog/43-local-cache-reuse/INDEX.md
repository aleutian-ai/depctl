# Epic: Cutting duplicate/oversized dependency acquisition cost

`internal/source/git`'s `Cache` always does its own full `git clone --mirror` into ragctl's own data dir (`<data-dir>/git`), completely independent of whatever the user's own package manager already downloaded (`$GOPATH/pkg/mod`, `site-packages`, `node_modules`), and regardless of the fact that ragctl only ever reads a small, documentation-shaped subset of any repo's files. Every project ragctl scans pays for a second, full-history, full-tree clone of every dependency, most of which is never read.

This is a real cost, not an oversight — raised directly by the project's own user during the WATCH epic's MCP-bootstrapping follow-up (see `docs/scratch/mcp-bootstrapping.md` for the related gap it was raised alongside). Two complementary strategies apply to the same underlying problem, from different angles — skip fetching when it's already available locally (GIT-004), vs. never fetch more than what's actually read in the first place (GIT-005, generally the stronger win since it applies to every clone, not just ones with a local-cache hit). Deferred rather than fixed now — not worth the complexity before the v0.1 critical path is stable and real usage shows how much this actually costs in practice (disk, first-sync time) — see `docs/scratch/embedding-provider-lock-in.md` for the same "profile before optimizing" reasoning applied to a different deferred gap this same session.

## Tickets

- [GIT-004](GIT-004-local-cache-fast-path.md) — detect an existing local package-manager cache for a resolved dependency and seed/skip the mirror clone from it when found, falling back to the existing clone path otherwise.
- [GIT-005](GIT-005-partial-clone-sparse-checkout.md) — blobless partial clone (`--filter=blob:none`) plus sparse-checkout scoped to the file patterns ragctl's own normalizers already read, so a clone only ever transfers the doc-shaped content ragctl actually uses, without losing the full history cross-version diffing needs.

## Non-goals

- No changes to `internal/source/git`'s cache being the source of truth for anything version-history-dependent (release notes, cross-version diffing) — a local package cache only ever accelerates the "get this one version's current source" case.
- No new config surface for pointing at custom cache locations in this epic — start with the well-known default locations (`GOPATH`/`GOMODCACHE`, `pip`'s cache dir, `node_modules` relative to the scanned project) each ecosystem's tooling already publishes an env var or command for (`go env GOMODCACHE`, etc.), not a user-maintained list.
- No behavior change for ecosystems without a well-defined shared cache convention (arbitrary `pip install` targets, `node_modules` per-project rather than global) beyond the one obvious win (a scanned project's own `node_modules`/`site-packages`, already right there).
