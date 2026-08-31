# SEC-003: Fetch limits

**Epic:** Security hardening
**Status:** planned
**Depends on:** HTTP-001 (HTTP acquisition client), GIT-001 (Git cache manager)
**Estimated size:** small

## Goal
Enforce configurable resource limits on all external fetches (Git and HTTP) to bound worst-case resource consumption from untrusted upstream sources.

## Non-goals
- No sandboxing/isolation beyond size/redirect/decompression limits — that is out of scope for v1.
- No rate-limit backoff policy design (already covered by HTTP-001/GIT-001) — this ticket only adds the hard caps.

## Simplicity constraints
- Limits are simple config-driven byte/count caps enforced at the point of read (`io.LimitReader`, explicit counters), not a general resource-governance framework.

## Design
Config (`internal/config`):

```yaml
fetch:
  max_file_size: 10485760      # 10 MiB
  max_website_page_size: 5242880
  max_source_total_bytes: 524288000
  max_redirects: 5
  max_decompressed_size: 52428800
```

Enforcement points:
- `internal/source/http`: wrap response bodies in `io.LimitReader(body, max_website_page_size+1)`, treat exceeding the limit as an error, and set the HTTP client's redirect policy to fail after `max_redirects` hops.
- `internal/source/git`: track cumulative bytes fetched per source across a sync run and abort with a typed error once `max_source_total_bytes` is exceeded.
- Any decompression step (e.g. sitemap.xml.gz if used later) caps output at `max_decompressed_size`.

## Inputs / Outputs
- Input: configured limits.
- Output: fetch operations that fail cleanly (typed `ErrFetchLimitExceeded`) instead of unbounded resource use.

## Failure behavior
Exceeding a limit is a permanent (non-retryable) error for that source in the current sync run, logged with the specific limit that triggered.

## Tests
- HTTP response exceeding `max_website_page_size` is truncated/rejected, not silently accepted.
- Redirect chain longer than `max_redirects` fails.
- Git source exceeding `max_source_total_bytes` aborts cleanly.

## Acceptance criteria
- [ ] All five config limits implemented and enforced at their respective points.
- [ ] Exceeding any limit produces a typed, non-retryable error.
- [ ] Tests cover each limit independently with a small threshold for fast test execution.
