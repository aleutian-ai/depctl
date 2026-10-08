# DATA-002: HTTP provenance and content hashing

**Epic:** Dataset descriptors
**Status:** backlog
**Depends on:** DATA-001 (descriptor convention)
**Estimated size:** small

## Goal
Record enough HTTP response metadata and a content hash in `dataset.json` to detect, without re-downloading, whether a dataset changed since the last fetch — needed for sources like NOAA ENC packages where the URL is stable but the contents aren't.

## Non-goals
- No automatic re-fetch-on-change policy — this ticket only records what's needed to detect change; deciding what to do about it is DATA-004 (or the user, manually).
- No hashing of extracted contents, only the raw downloaded archive/file — same BLAKE3 approach already used by `internal/data/fingerprint` (HASH-001), reused here as a plain `blake3.Sum256` call, not through that package (this is a dev tool, not depctl proper).

## Simplicity constraints
- Capture exactly what the HTTP response already hands over for free (`ETag`, `Last-Modified`, `Content-Length`) — no extra requests (no HEAD-then-GET), no retries/backoff beyond what `fetchOne` already does.

## Design
Extend the `dataset.json` written by DATA-001 with:

```json
{
  "etag": "\"abc123\"",
  "last_modified": "Wed, 20 Aug 2025 10:00:00 GMT",
  "content_length": 48213112,
  "blake3": "b3_...",
  "archive_filename": "tl_2025_06_tract.zip"
}
```

`download()` in `hack/fetch-geodata/main.go` already streams the response body to a file via `io.Copy` — change it to `io.Copy` through both the file and a `blake3.New()` hasher (or wrap the destination writer with `io.MultiWriter`), and capture `resp.Header.Get("ETag")` / `resp.Header.Get("Last-Modified")` / `resp.ContentLength` before closing the response.

## Inputs / Outputs
- Input: the `*http.Response` already obtained in `download()`.
- Output: the four fields above added to that source's `dataset.json`.

## Failure behavior
Missing `ETag`/`Last-Modified` (some servers don't send them) is not an error — write empty string / omit the field. `blake3` and `Content-Length` are always obtainable from the response itself and must be populated.

## Tests
- Downloading a fixture HTTP response with known `ETag`/`Last-Modified`/body produces a `dataset.json` with the expected hash and headers.
- A response missing `ETag` doesn't fail the fetch, just omits the field.

## Acceptance criteria
- [ ] `dataset.json` includes `etag`, `last_modified`, `content_length`, `blake3`, `archive_filename` when available.
- [ ] Hashing adds no second read pass over the downloaded bytes (streamed alongside the existing write).
