# DATA-004: Dataset snapshot comparison

**Epic:** Dataset descriptors
**Status:** backlog
**Depends on:** DATA-002 (HTTP provenance and hashing)
**Estimated size:** small

## Goal
Given two `dataset.json` snapshots of the same dataset `id` fetched at different times, report what changed — archive hash, ETag, file/feature counts, CRS — without any geospatial diffing.

## Non-goals
- No geometry-level diffing (features added/removed/moved) — that's a future data-analysis adapter, explicitly out of scope for depctl.
- No automatic scheduling of re-fetches or alerting — this is a comparison report a user runs on demand.

## Simplicity constraints
- Pure diff over two JSON structs already on disk — no new fetch, no network call, no dependency on DATA-003's `spatial` section being present (compare what's there, note what's missing on either side).

## Design
A `hack/fetch-geodata -compare <old dataset.json> <new dataset.json>` mode (or a small standalone `hack/dataset-diff` tool, whichever keeps `fetch-geodata`'s flag surface simplest) prints a plain-text delta:

```text
dataset: census-tiger-tracts-ca
archive changed:     yes  (blake3 aaa... -> bbb...)
etag changed:        yes
provider_version:    2025 -> 2025 (unchanged)
feature_count:       9129 -> 9131 (if spatial section present on both)
crs:                 unchanged
```

Snapshots are historical — retaining old `dataset.json` files (e.g. under a `snapshots/<timestamp>/` directory instead of overwriting in place) is a prerequisite this ticket assumes exists by the time it's picked up, not something it builds.

## Inputs / Outputs
- Input: two `dataset.json` file paths.
- Output: a plain-text delta report to stdout; non-zero exit if any tracked field differs (useful for scripting "did anything change").

## Failure behavior
Two descriptors with different `id` fields is a usage error (refuse to compare unrelated datasets), not a silent diff.

## Tests
- Identical descriptors report no changes, exit 0.
- Descriptors differing in `blake3` report the change, exit non-zero.
- Descriptors with different `id` are rejected.

## Acceptance criteria
- [ ] Comparison correctly reports hash/ETag/provider_version/spatial deltas.
- [ ] Mismatched `id` is rejected rather than diffed.
