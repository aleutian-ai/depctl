# SEC-003: Fetch limits

**Epic:** Security hardening
**Status:** done — 2026-09-29
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
- [x] Three of the original five config limits implemented and enforced (see scoping note below); the other two deferred as genuinely inapplicable today.
- [x] Exceeding any limit produces a typed, non-retryable error.
- [x] Tests cover each limit independently with a small threshold for fast test execution.

## Post-implementation note (2026-09-29)

**Scoped down from the original five-field design to the three that apply to code that actually exists.** `max_website_page_size` and `max_decompressed_size` are dropped: there is no website-acquisition path (backlog epic 24, `internal/source/http`, was never built) and no decompression step anywhere in the codebase for either to bound — adding config fields for code that doesn't exist would be exactly the kind of speculative surface this project's own conventions (and `CFG-001`) argue against. `internal/config.FetchConfig` (`max_file_size`, `max_redirects`, `max_source_total_bytes`) covers the three limits that bound real, currently-existing external fetches, with the same "0 unmarshals as unset, apply the built-in default" convention `Sync.MaxConcurrency` already established (`MaxFileSizeOrDefault`/`MaxRedirectsOrDefault`/`MaxSourceTotalBytesOrDefault`).

New shared package `internal/httplimit`: `ReadLimited(r, max)` reads at most `max` bytes and returns a typed `ErrFetchLimitExceeded` if more were available (unlike a bare `io.LimitReader`, which silently truncates); `Client(base, maxRedirects)` wraps an `*http.Client` with a bounded `CheckRedirect`.

Applied to every real external-fetch call site found in the codebase:
- **`internal/cli/sync.go`**: the three real production registry/vanity-import fetches (`npmRepository`, `pypiRepository`, `resolveVanityImport`) — these already had an ad hoc `io.LimitReader(resp.Body, 1<<20)` silently truncating on overflow; now use `httplimit.ReadLimited` with the configured (or default) size, and their HTTP clients gained a bounded redirect policy read live from `syncFetchLimits` (a package var set once from `cfg.Fetch` at `RunSync`'s entry — deliberately not threaded as a parameter through several call layers, matching this codebase's existing convention for rarely-changing, process-wide tunables like `vanityImportTimeout`; benign under epic 53's concurrent syncs since every concurrent call in one real daemon shares the same on-disk config).
- **`internal/registry/discover/discover.go`**: `fetchJSON` had a genuinely *unbounded* `io.ReadAll(resp.Body)` — the one real gap found, not just a hardening of an existing bound. Now uses `httplimit.ReadLimited` and a bounded-redirect client (this standalone CLI-diagnostic path uses the built-in defaults directly, since `ragctl registry discover` doesn't load a `Config` today).
- **`internal/registry/liveness.go`**: `checkWebsiteLiveness`'s HEAD-request client gained the same bounded redirect policy.
- **`internal/source/git/cache.go`**: `EnsureMirror` now checks the resulting mirror's real, on-disk size (`mirrorDirSize`, a plain file-size walk) against `MaxSourceTotalBytesOrDefault()` right after any of its three clone paths (network, GIT-006 external-mirror seed, GIT-007 checkout seed) completes — a repository over the limit is removed and reported as a permanent (`ErrKindPermanent`), non-retryable `CacheError` wrapping `ErrFetchLimitExceeded`. Wired from real config via a new `git.WithMaxMirrorBytes` `Option`, applied in `buildGitCache`.

Tests: `internal/httplimit/httplimit_test.go` (the two shared primitives directly — exact-at-limit succeeds, one-byte-over fails, redirect cap enforced with a real `httptest` server pair including a real infinite-redirect-loop rejection); `internal/source/git/fetch_limit_test.go` (`TestEnsureMirrorRejectsMirrorOverTheSizeLimit`/`SucceedsUnderTheSizeLimit`/`TestMirrorDirSizeSumsRealFileBytes`, against a real local git fixture); `internal/config/config_test.go` (`TestFetchConfigZeroValueUsesDefaults`/`ExplicitValueOverridesDefault`). Full `go build`/`go vet`/`go test ./...` clean; all pre-existing fallback-manifest tests (`TestNpm*`/`TestPypi*`/`TestFallbackManifest*`) pass unchanged.
