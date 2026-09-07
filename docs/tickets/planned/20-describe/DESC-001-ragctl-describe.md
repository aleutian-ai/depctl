# DESC-001: `ragctl describe`

**Epic:** Describe
**Status:** planned
**Depends on:** REG-002/003 (registry loader/matcher), GEN-002 (generation manifest), VEC-003 (backend replica), SEC-001 (TrustClass)
**Estimated size:** medium

## Goal
Give a human a read-only view of what knowledge ragctl actually has, fleet-wide or drilled into one package: registry sources declared, whether a generation is active, how much content it holds, and its trust-class composition — as an ASCII table by default, or a static HTML file via `--html`.

## Non-goals
- No liveness check of declared sources (is the URL still reachable) — that requires actually contacting the source, out of scope here (see `docs/tickets/backlog/34-registry-coverage/REG-007`).
- No content preview (sample chunk text) — advanced backlog.
- No interactivity in `--html` output (search/sort/filter) — v1 is a static, self-contained file.
- No new persisted state or background collector — pure read-only aggregation over existing bbolt/Badger/registry state, same principle as OPS-001 (`ragctl status`).

## Simplicity constraints
- One internal struct (`Report`) built once, rendered two ways (text table, HTML) — do not maintain separate data-gathering code paths per output format.
- Trust class per source is derived from `generation.TrustClassForSourceType(source.Type)` (declared, deterministic) — do not walk actual `KnowledgeObject`/`Chunk` records to measure it; that data doesn't exist for a source with zero synced chunks anyway, and the declared mapping is already exact today (SEC-001).
- HTML output is one dependency-free template (Go's `html/template`, no JS framework, no build step) written to a single file.

## Design
Package: `internal/cli` (same as `status`/`doctor` will be — this is CLI-facing reporting, not a reusable library other packages need).

```go
type Report struct {
    GeneratedAt time.Time
    Registry    RegistrySummary
    Packages    []PackageEntry
}

type RegistrySummary struct {
    ManifestCount int
    Warnings      []string // registry.Registry.Warnings — surfaces malformed-manifest skips
}

type PackageEntry struct {
    Ecosystem      domain.Ecosystem
    Package        string
    ManifestMatch  bool
    Sources        []SourceEntry
    ActiveVersion  string // "" if no active generation
    GenerationID   string
    ChunkCount     int
    ObjectCount    int
    ObjectsReused  int
    ReplicaStatus  string // BackendReplica.Status, "" if none
    ReplicaPoints  int
}

type SourceEntry struct {
    ID         string
    Type       string
    URL        string
    Authority  int
    TrustClass domain.TrustClass // generation.TrustClassForSourceType(Type)
}
```

Data gathering (fleet-wide, `ragctl describe`):
1. Load the registry (`loadRegistryForCLI`, same as `plan`/`sync`).
2. `store.ListAllReferences(ctx)` — fleet-wide, dedupe to distinct `(ecosystem, package)` pairs (a describe report is per-package, not per-project-reference).
3. For each pair: `reg.Match(ecosystem, package)` for declared sources; `store.GetActiveGeneration(ctx, ecosystem, package, backendName)` for the active generation (if any); `readGenerationManifest` for object/chunk counts; `store.GetBackendReplica(ctx, gen.ID, backendName)` for replica status/point count.
4. A package with a manifest match but no active generation (never synced, or failed) still gets a row — `ActiveVersion` empty, `ManifestMatch: true`, `Sources` populated. This is the "what do I have vs. what's declared" gap the report exists to surface.

Per-package drill-down (`ragctl describe <ecosystem>/<package>`): same `PackageEntry` construction for one pair, printed with full source list instead of a table row.

`--html [--out path]`: renders `Report` through an embedded `html/template` (light/dark aware inline CSS, no external assets — matches this repo's own artifact conventions even though this isn't a claude.ai artifact) to `path` (default `./ragctl-describe.html`), prints the written path, does not open a browser automatically.

## Inputs / Outputs
- Input: `ragctl describe [ecosystem/package] [--html] [--out path] [--json]`.
- Output: ASCII table (fleet-wide) or drill-down text (single package) to stdout; or a written HTML file; `--json` prints `Report` as JSON for scripting (cheap to add alongside text since both come from one struct).

## Failure behavior
- A malformed local registry manifest is already collected as a warning by `registry.Registry.Warnings` (REG-002) — surfaced in `RegistrySummary.Warnings`, not a command failure.
- A package with no active generation is reported with empty generation fields, not an error — "declared but never synced" is a normal, expected state this command exists to show.
- Backend unreachable when checking replica status: reported as `ReplicaStatus: "unknown"`, same non-fatal principle as OPS-001's backend health check.

## Tests
- Fleet-wide report against fixture bbolt/Badger state (2-3 packages, one active, one manifest-only, one with no manifest) produces the expected row set.
- Per-package drill-down matches the corresponding fleet-wide row's data.
- `--json` output round-trips through `json.Unmarshal` into `Report`.
- `--html` writes a well-formed file (parseable by `html.Parse`) containing every package name from the fixture.
- A package present in `ListAllReferences` but absent from the registry gets `ManifestMatch: false` and an empty `Sources` list, not an error.

## Acceptance criteria
- [x] `ragctl describe` prints a fleet-wide ASCII table.
- [x] `ragctl describe <ecosystem>/<package>` prints a per-package drill-down (sources, authority, trust class, generation/chunk/replica state).
- [x] `--html` writes a static, self-contained HTML file.
- [x] `--json` prints the same underlying `Report` as JSON.
- [x] Packages with no active generation and packages with no registry match are both shown, not silently dropped.

## Post-implementation note
Verified against this session's real 20-repo `ragctl-core-*` corpus (real bbolt/Badger/Qdrant state), not just fixtures — output immediately surfaced two pieces of real stale/latent state worth knowing about independent of this ticket: a leftover `VersionReference` for `offline-go-casaos` (registered during an earlier, since-cleaned-up registry experiment this session, manifest gone but reference never removed — exactly the "declared vs. actually there" gap this command exists to catch) and the machine-specific local-path source (`/Users/jin/offline-knowledge/go/bbolt`) flagged earlier in this session's registry-coverage discussion. Neither is a bug in `describe` — both are exactly the kind of drift it's supposed to make visible.

`TrustClassForSourceType` (SEC-001) was exported from `internal/data/generation` to `internal/cli` for reuse rather than duplicating the mapping — the ticket's simplicity constraint already called for reusing the declared mapping instead of measuring actual object trust classes from Badger.

