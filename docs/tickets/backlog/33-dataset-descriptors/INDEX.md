# Epic: Dataset Descriptors

Raw datasets (Census TIGER/Line shapefiles, NOAA nautical charts, and similar non-git assets pulled by `hack/fetch-geodata`) are never RAG content — there's nothing to normalize/chunk/embed in a shapefile or GeoTIFF. But their *provenance* (what was downloaded, from where, when, and whether it changed since last time) is useful context that today only exists as a directory of files with no metadata attached. This epic gives every fetched dataset a small sidecar descriptor (`dataset.json`) recording that provenance, without turning raw data into a ragctl domain concept.

This is deliberately **not** a v0.1 dependency. Generation Builder (epic 11) and everything after it proceeds without this. See [[ragctl-rag-scope-raw-data-vs-docs]] (project memory) for the underlying principle: dataset descriptors are searchable context about an asset, not the asset itself, and not part of the knowledge registry (which resolves package/version → authoritative docs, a different problem).

## Tickets

- [DATA-001](DATA-001-dataset-descriptor-convention.md) — `dataset.json` sidecar convention: what fields every fetched dataset records.
- [DATA-002](DATA-002-http-provenance-and-hashing.md) — Capture HTTP response provenance (ETag, Last-Modified, Content-Length) and BLAKE3 the raw download in `hack/fetch-geodata`.
- [DATA-003](DATA-003-spatial-metadata-extraction.md) — Optional GDAL-based inspection (CRS, layers, feature counts, field names) recorded into the descriptor when GDAL is available.
- [DATA-004](DATA-004-dataset-snapshot-comparison.md) — Compare two `dataset.json` snapshots of the same dataset ID and report what changed.

## Non-goals for this epic

- No `Dataset`/`DatasetVersion` domain types in `internal/domain` — a `data/**/dataset.json` file *is* the catalog; no registry/resolver subsystem wraps it unless something concretely needs to query it later.
- No entry into the knowledge registry (REG-001..004) — datasets are not resolved by package/version.
- No geospatial diffing (features added/removed/changed) — DATA-004 stops at descriptor-level comparison (hash/schema/feature-count deltas); true geometry diffing is a future data-analysis adapter, not ragctl.
