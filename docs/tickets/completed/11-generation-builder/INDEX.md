# Epic: Generation Builder

Builds the first end-to-end pipeline that turns a resolved dependency version into a staged, content-deduplicated knowledge generation in Badger, tracked by a bbolt lifecycle record. This is the point where acquisition (Git), normalization, fingerprinting, and chunking (from earlier milestones) come together into one artifact — the `Generation` — that later milestones will embed, replicate to a vector backend, validate, and atomically promote.

## Tickets
- [GEN-001](GEN-001-generation-creator.md) — Create a `PLANNED` generation record in bbolt plus an empty manifest in Badger.
- [GEN-002](GEN-002-acquire-normalize-generation.md) — Sequential acquire → normalize → chunk pipeline that fills a generation's Badger staging data, updating lifecycle state as it progresses.
- [GEN-003](GEN-003-content-reuse.md) — Content-hash-based object reuse so unchanged content isn't duplicated across generations.
