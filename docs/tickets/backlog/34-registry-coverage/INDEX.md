# Epic: Registry Coverage

The knowledge registry (epic 07) works well once a manifest exists, but coverage today is 100% manual: 6 built-in manifests ship with the binary, and every other package needs someone to hand-write YAML before depctl can sync it at all. This epic addresses the coverage gap without weakening the registry's trust-boundary property (docs/tickets/completed/07-knowledge-registry, SEC-001) — nothing here lets depctl silently trust an un-reviewed source, it only reduces how much manual typing is needed to get a real source registered or to degrade gracefully when one isn't.

Website/blog/video acquisition (a separate, already-real gap — a manifest can *declare* a `website` source but depctl has no HTTP client to actually fetch one) is tracked separately: `docs/tickets/backlog/24-website-acquisition`. Nothing here duplicates it.

## Tickets
- [REG-005](REG-005-no-manifest-fallback.md) — fall back to indexing the resolved repo itself when no registry manifest matches, instead of failing the dependency's sync outright.
- [REG-006](REG-006-candidate-source-discovery.md) — propose (not auto-apply) candidate sources from ecosystem package metadata, for a human to approve into a real manifest.
- [REG-007](REG-007-source-liveness-check.md) — on-demand check that a manifest's declared sources are still reachable, surfaced via `doctor`/`describe`.
- [REG-008](REG-008-go-vanity-import-fallback.md) — extend REG-005's no-manifest fallback with a `go-import` meta-tag lookup, so vanity-import Go module paths (not just `github.com`-shaped ones) can still sync without a hand-written manifest. Found live: two of cobra's own transitive dependencies (`go.yaml.in/yaml/v3`, `gopkg.in/check.v1`) failed sync for exactly this reason. *(done)*

## Non-goals for this epic
- No change to how a source becomes trusted enough to sync from — REG-005's fallback source is explicitly `TrustUnknown`, REG-006's discovered candidates are never auto-synced.
- No web-search/LLM-based source guessing — REG-006 is metadata-field extraction only (structured fields ecosystems already publish), not inference.
