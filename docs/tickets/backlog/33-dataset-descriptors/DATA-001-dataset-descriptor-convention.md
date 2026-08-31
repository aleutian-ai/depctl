# DATA-001: Dataset descriptor convention

**Epic:** Dataset descriptors
**Status:** backlog
**Depends on:** none (dev-tool only; no ragctl domain change)
**Estimated size:** small

## Goal
Define a `dataset.json` sidecar file written next to every dataset `hack/fetch-geodata` downloads, answering "what do I actually have on disk?" — logical dataset identity, where it came from, and when it was fetched.

## Non-goals
- No parsing/validation of dataset.json by the ragctl binary — this is a `hack/*` dev-tool convention, not a ragctl feature. `ragctl` itself has no dataset concept.
- No content hashing, HTTP metadata, or spatial inspection here — those are DATA-002/DATA-003, this ticket is just the file shape and where it's written.

## Simplicity constraints
- A directory of `dataset.json` files is the entire "catalog" — no index file, no database, no `DatasetRegistry` type.
- Distinguish `provider_version` (semantic vintage the publisher declares, e.g. TIGER's `"2025"`, or `null` if the publisher has no such concept) from `snapshot` (this fetch's content hash) — don't conflate them into one `version` field.

## Design
One `dataset.json` per fetched dataset, written by `hack/fetch-geodata` alongside the extracted directory (or the plain file, for a documentation-only fetch):

```json
{
  "id": "census-tiger-tracts-ca",
  "title": "Census TIGER/Line California Census Tracts",
  "publisher": "U.S. Census Bureau",
  "domain": "gis",
  "provider_version": "2025",
  "source_url": "https://www2.census.gov/geo/tiger/TIGER2025/TRACT/tl_2025_06_tract.zip",
  "retrieved_at": "2026-08-30T22:15:00-04:00",
  "local_path": "data/gis/us/tiger-2025/CA/tl_2025_06_tract"
}
```

`id` is derived deterministically from the manifest's category + URL basename (same identity fetch-geodata already uses for its extraction-directory idempotency check) so re-fetching the same manifest line always produces the same `id`.

## Inputs / Outputs
- Input: the `source` (category, URL) fetch-geodata already parses from `sources.txt`.
- Output: `dataset.json` written to the same directory as the extracted/downloaded content.

## Failure behavior
Writing `dataset.json` failing is a hard error for that source (same failure handling as a download/extract error today) — a dataset without a descriptor is treated as not-yet-fetched.

## Tests
- Fetching a `.zip` source writes `dataset.json` into the extraction directory with the expected fields.
- Fetching a plain-file source (e.g. the TIGER tech-doc PDF) writes `dataset.json` alongside it.
- Re-running against an already-fetched dataset does not rewrite `dataset.json` (idempotent, same as the extraction/download skip today).

## Acceptance criteria
- [ ] `dataset.json` written for every successfully fetched source.
- [ ] Fields above present and populated.
- [ ] Idempotent re-runs don't touch existing descriptors.
