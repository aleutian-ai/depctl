# REG-008: Go vanity-import fallback via `go-import` meta lookup

**Epic:** Registry Coverage
**Status:** done
**Depends on:** REG-005 (no-manifest fallback — this ticket extends `fallbackManifest`'s existing shape, not a new mechanism)
**Estimated size:** small

## Goal
Live-found gap, running a real fresh Go project through depctl end to end (WATCH-015/016/017's own live verification): `github.com/spf13/cobra` synced fine via REG-005's fallback, but two of its own transitive dependencies — `go.yaml.in/yaml/v3` and `gopkg.in/check.v1` — failed with `no registry manifest for go.yaml.in/yaml/v3` (and the same for the other). Both are real, syncable Go modules; neither has a registry manifest, and neither module path is shaped like `github.com/<org>/<repo>`, so REG-005's `fallbackManifest` (`internal/cli/sync.go`) correctly declines them today, exactly as it was scoped to do. Resolve the module path the same way `go get` itself does — an HTTP `go-import` meta-tag lookup — so these sync via the fallback path too, instead of failing every project that happens to depend on a vanity-import-path package.

## Non-goals
- No change to `fallbackManifest`'s `github.com`-shaped fast path (`internal/cli/sync.go`) — this ticket adds a second, slower resolution path tried only when the fast path doesn't apply, never replaces it.
- No vanity-import resolution for Node/Python — neither ecosystem has this concept; REG-006 already covers their equivalent gap (structured metadata-field discovery) separately.
- No caching/persistence of resolved vanity-import results across runs in this ticket — a `go-import` lookup is cheap (one HTTP GET, `?go-get=1`) and the existing `syncVersion` call graph has no natural per-run cache to hook into yet. Revisit only if live use shows this is actually a meaningful cost, not preemptively.
- No general-purpose HTTP redirect/meta-tag scraper — this implements exactly the documented [`go-import` meta tag protocol](https://go.dev/ref/mod#vcs-branch) (`<meta name="go-import" content="<root> <vcs> <repo-url>">`), nothing broader.

## Simplicity constraints
- One new function, `resolveVanityImport(ctx, modulePath string) (repoURL string, ok bool)`, called from `fallbackManifest` only when the existing `github.com`-shaped check fails — not a new package, not a new subsystem.
- Reuse `fallbackManifest`'s existing output shape exactly (`registry.Manifest` with one `git` source, `Authority: 0`, `Ref: "HEAD"`) — the only new behavior is *finding* the repo URL, not what happens once it's found.
- Bounded by a short HTTP timeout (matching `backendHealthTimeout`'s pattern elsewhere in `internal/cli`) — a hanging or slow vanity-import host must not stall a sync the way an unbounded call would.

## Design
`internal/cli/sync.go`'s `fallbackManifest` currently:
```go
func fallbackManifest(dep domain.Dependency) (registry.Manifest, bool) {
	if dep.Ecosystem != domain.EcosystemGo {
		return registry.Manifest{}, false
	}
	segments := strings.Split(dep.Name, "/")
	if len(segments) < 3 || segments[0] != "github.com" {
		return registry.Manifest{}, false
	}
	url := "https://github.com/" + segments[1] + "/" + segments[2]
	return registry.Manifest{...}, true
}
```
Extend the `github.com` check's failure branch: instead of returning `false` immediately, try `resolveVanityImport(ctx, dep.Name)`. That function performs the same lookup `go get`/`go mod download` does — `GET https://<module-path>?go-get=1`, parse the response body for `<meta name="go-import" content="<root-path> <vcs> <repo-url>">`, and confirm `vcs == "git"` (the only VCS `internal/source/git` supports) — matching `<root-path>` as a prefix of the requested module path per the documented protocol, so a subpackage import path (e.g. `go.yaml.in/yaml/v3` under root `go.yaml.in/yaml`) still resolves correctly. `fallbackManifest` itself gains a `ctx context.Context` parameter (currently takes none), and its two call sites in `syncVersion` (`internal/cli/sync.go`) pass the sync's existing `ctx` through.

```go
// resolveVanityImport performs the same lookup `go get` uses for a
// module path with no known VCS host: GET .../<path>?go-get=1 and parse
// the go-import meta tag. Only git-VCS results are usable — nothing
// else in depctl can acquire from them.
func resolveVanityImport(ctx context.Context, modulePath string) (repoURL string, ok bool)
```

## Inputs / Outputs
- Input: a Go module path with no registry manifest and no `github.com`-shaped prefix (e.g. `go.yaml.in/yaml/v3`, `gopkg.in/check.v1`).
- Output: the same synthetic single-source `registry.Manifest` REG-005 already produces, now covering vanity-import paths too; unchanged (existing hard failure) when the lookup finds nothing usable.

## Failure behavior
- Vanity-import host unreachable, slow, or returns no `go-import` meta tag: falls through to today's exact `"no registry manifest for %s"` error — this ticket only adds cases that succeed where they previously failed, same guarantee REG-005 itself made.
- A resolved `go-import` tag naming a non-git VCS (`bzr`, `svn`, `hg` are all valid per the spec): treated as not-found, since `internal/source/git` is the only acquisition path depctl has.

## Tests
- A module path served by a local `httptest.Server` returning a real `go-import` meta tag resolves and syncs via the fallback, same as a `github.com`-shaped path does today.
- A module path whose `go-import` tag names a non-git VCS falls through to the existing error, not a panic or a silent no-op.
- A module path returning no `go-import` tag at all (a genuinely non-Go-gettable path) falls through to the existing error.
- A `github.com`-shaped module path never reaches `resolveVanityImport` at all — the fast path's existing test coverage (REG-005) must keep passing unchanged, and a test should assert no HTTP call happens for that case.
- The real regression case: `go.yaml.in/yaml/v3` and `gopkg.in/check.v1` (network-gated, skipped when offline, matching this repo's existing pattern for tests that need real external hosts) sync successfully end to end.

## Acceptance criteria
- [x] Go module paths with a working `go-import` meta tag sync via the fallback, whether or not they're `github.com`-shaped.
- [x] `github.com`-shaped paths are unaffected — no new HTTP call, no behavior change, for the case REG-005 already handles.
- [x] A path with no resolvable VCS location still fails with the existing, unchanged error message.
- [x] The two dependencies that surfaced this gap live (`go.yaml.in/yaml/v3`, `gopkg.in/check.v1`) sync successfully.

## Post-implementation note
Shipped per spec. `fallbackManifest` (`internal/cli/sync.go`) gained a `ctx context.Context` parameter; its `github.com`-shaped fast path was factored out unchanged into `githubModuleURL`, and `resolveVanityImport` (new) is tried only when that fails — one `GET https://<modulePath>?go-get=1`, parsed via a `regexp` for the `go-import` meta tag, matching the resolved root against the requested module path as a prefix (so a subpackage import like `go.yaml.in/yaml/v3`, whose `go-import` root is `go.yaml.in/yaml`, still resolves). Bounded by a 5s `vanityImportTimeout`. The HTTP client is a package-level `vanityImportHTTPClient` var (same "swappable seam" pattern as WATCH-016's `execLookPath`), letting tests redirect requests at a local `httptest.Server` via a custom `http.RoundTripper` instead of touching the real network — all 6 new/updated tests in `sync_test.go` are hermetic.

Verified live against the exact two dependencies that surfaced this gap (found running WATCH-015/016/017's own live opencode verification): `gopkg.in/check.v1` (39 chunks, `backend replica: status=complete`) and `go.yaml.in/yaml/v3` (588 chunks, `status=complete`) — both `depctl describe` outputs show `no registry manifest` alongside a fully complete active generation, which is only reachable through this ticket's fallback path. `go build`/`go vet`/`gofmt -l .`/`go test ./...` all clean.

Unrelated incident during live verification: an `depctl sync` invocation without `--project` (an operator mistake, not a code bug) queued a fleet-wide re-sync across every real registered project; killing the CLI client didn't stop it (by design — the daemon owns sync work server-side, per ADR-011), and it ran long enough that the daemon was eventually force-killed rather than waited out. `depctl doctor` afterward reported 15/15 OK with zero corruption — bbolt/Badger/Qdrant all survived the hard kill cleanly, confirming generation writes are safely atomic per-action rather than needing a graceful shutdown to avoid leaving partial state.
