# Epic: Local Corpus

`ragctl`'s only path into the registry+sync system is "a real, resolved dependency of a real scanned project" — there is no way to say "index this local git repo of documents, it isn't anyone's dependency." Working around this (a synthetic npm project with fake dependency entries, one registry manifest per repo pointing at a local path) is exactly what this project did by hand to index a 20-repo reference corpus — this epic automates that workaround into one command instead of leaving it as manual project+YAML scaffolding.

This is deliberately **not** a redesign of the domain model — `scan`/`resolve`/registry all stay exactly as they are. This epic only automates the same synthetic-project trick a human would otherwise do by hand.

## Tickets
- [CORPUS-001](CORPUS-001-ragctl-corpus-add.md) — `ragctl corpus add <path>`: generates the synthetic project + registry manifest for a local git repo in one step.

## Non-goals for this epic
- No new `domain.Ecosystem` or a "local" resolver — the synthetic node-project mechanism already works and needs no new domain concept, only automation of its bookkeeping.
- No support for non-git local content (a plain folder with no `.git`) — see epic 08 (git-acquisition)'s own scope; a corpus this points at must be a real git repo for the same reasons any other `git` source must be (versioned, `HEAD`-resolvable).
