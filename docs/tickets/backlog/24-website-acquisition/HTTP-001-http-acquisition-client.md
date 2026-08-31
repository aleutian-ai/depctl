# HTTP-001: HTTP acquisition client

**Epic:** Documentation Website Acquisition
**Status:** planned
**Depends on:** none (ships only after Git-source ingestion is working, i.e. after GIT-001/002/003)
**Estimated size:** medium

## Goal
Build a shared, safe HTTP client for fetching documentation pages: timeouts, a distinct user-agent, conditional requests (ETag/Last-Modified), rate limiting, max body size, and a bounded redirect policy.

## Non-goals
- Sitemap discovery (HTTP-002).
- HTML content extraction (HTTP-003).
- Browser rendering / executing JavaScript sites.

## Simplicity constraints
- Use `net/http` directly with a configured `http.Client` + `http.Transport`; do not add an HTTP client framework dependency.
- Rate limiting can be a simple per-host token bucket (`golang.org/x/time/rate` or a tiny hand-rolled limiter) — no distributed rate limiter needed for a local single-process daemon.

## Design
Package: `internal/source/http`

```go
type Client struct {
    HTTPClient      *http.Client
    UserAgent       string
    MaxBodyBytes    int64
    MaxRedirects    int
    RateLimitPerHost float64 // requests/sec
}

func (c *Client) Fetch(ctx context.Context, url string, prior *ConditionalState) (*FetchResult, error)

type ConditionalState struct {
    ETag         string
    LastModified string
}

type FetchResult struct {
    StatusCode int
    Body       []byte
    ETag       string
    LastModified string
    NotModified bool // 304
}
```
Config surface (from `security-hardening` SEC-003, referenced but not owned here):
```yaml
fetch:
  max_page_bytes: <int>
  max_redirects: <int>
  timeout: <duration>
```

## Inputs / Outputs
- Input: URL + optional prior conditional-request state.
- Output: `FetchResult` (body or 304-not-modified signal).

## Failure behavior
- Body exceeds `MaxBodyBytes` → abort read, typed `FetchError{Reason: "body_too_large"}`.
- Redirect count exceeds `MaxRedirects` → typed `FetchError{Reason: "too_many_redirects"}`.
- Non-2xx/304 status → typed `FetchError` carrying status code.
- Context cancellation/timeout → propagate as context error.

## Tests
- 304 response with matching ETag returns `NotModified: true`, no body read.
- Body larger than configured max is rejected before fully buffering (use `io.LimitReader`).
- Redirect chain longer than `MaxRedirects` fails.
- Rate limiter delays a burst of requests to the same host (time-boxed test with a fake clock or small limits).

## Acceptance criteria
- [ ] Conditional GET round-trip works against a local `httptest.Server`.
- [ ] Oversized response body never fully buffered in memory.
- [ ] User-agent header always set.
