# GIT-004: Fast, clear offline failure

**Epic:** Local-First Acquisition
**Status:** planned
**Depends on:** GIT-001 (git cache manager)
**Estimated size:** small

## Goal
When `EnsureMirror` needs to clone a URL it hasn't mirrored before and the network is unreachable, fail within a few seconds with a clear "offline" error — not silently hang until the full multi-minute clone timeout expires.

## Non-goals
- No local-directory fallback — that's GIT-005. This ticket only changes how fast and how clearly a real network failure is reported.
- No general network-reachability service — the check is scoped to the one host `EnsureMirror` is about to contact, not a global "am I online" flag.

## Simplicity constraints
- One cheap probe before the real `git clone --mirror` attempt: a short-timeout TCP dial to the URL's host on port 443 (or 80 for an `http://` URL). No new dependency, no DNS-over-HTTPS trick, no retry/backoff policy — this is a fast pre-check, not a resilience layer.
- Only runs for a URL `EnsureMirror` would otherwise hit the network for — a local-path source (already offline-safe per `splitGitURL`'s existing local-path handling) skips this entirely.

## Design
In `internal/source/git/cache.go`, before `EnsureMirror`'s existing `git clone --mirror` call, for a genuinely remote URL (has a real host, not the `local` sentinel `splitGitURL` returns for a filesystem path):

```go
func probeReachable(ctx context.Context, host string) error {
    d := net.Dialer{Timeout: 3 * time.Second}
    conn, err := d.DialContext(ctx, "tcp", host+":443")
    if err != nil {
        return fmt.Errorf("host %s unreachable: %w", host, err)
    }
    conn.Close()
    return nil
}
```

On failure, `EnsureMirror` returns a `CacheError` whose message clearly says "offline" (or names the specific unreachable host), distinct from a real git protocol error (auth failure, repo not found) — a caller (e.g. `depctl sync`'s per-action failure reporting) should never have to guess which one it got from a generic git stderr dump.

## Inputs / Outputs
- Input: a remote git URL `EnsureMirror` is about to clone for the first time.
- Output: either proceeds to the real clone (host reachable) or returns a fast, clearly-labeled offline error.

## Failure behavior
A host that's reachable but the actual git operation fails (wrong URL, private repo, auth) is unaffected — the probe only catches "can't even open a TCP connection," everything else still surfaces git's own real error as today.

## Tests
- Probe against a real reachable host (e.g. a local `httptest`-style listener) succeeds.
- Probe against a closed/unused local port fails fast (well under the real clone timeout).
- `EnsureMirror` against a local-path source never invokes the probe at all.

## Acceptance criteria
- [ ] Offline attempt against a never-before-mirrored remote URL fails within ~3-5 seconds, not the full clone timeout.
- [ ] Error message distinguishes "unreachable/offline" from a real git protocol error.
- [ ] Local-path sources are completely unaffected (no probe, no behavior change).
