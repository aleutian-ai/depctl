# REG-005: No-manifest fallback

**Epic:** Registry Coverage
**Status:** planned
**Depends on:** REG-003 (registry matcher), GEN-002 (generation build)
**Estimated size:** small

## Goal
When a resolved dependency has no matching registry manifest, sync its own resolved repository as a best-effort, explicitly-unverified source instead of failing that dependency's sync outright.

## Non-goals
- No attempt to guess a repo URL from the package name/ecosystem beyond what the resolver already knows (e.g. a Go module's resolved path) — if the resolution itself doesn't carry a fetchable repo location, this fallback does not apply and the existing hard failure stands.
- No change to `TrustClass` semantics — the fallback source is always `TrustUnknown`, never elevated.

## Simplicity constraints
- This is one new branch in `syncVersion` (`internal/cli/sync.go`)/`reg.Match`, not a new subsystem: on no match, construct a single synthetic `registry.Source{Type: "git", ...}` from whatever repo location the resolution already carries, and proceed through the existing `generation.Build`/`Replicate` path unchanged.
- Only Go currently has a resolvable repo location without registry help (`go list -m -json`'s module path is frequently the repo itself or a well-known vanity-import pattern) — do not build ecosystem-specific URL-guessing for Python/Node in this ticket; land Go, extend later per-ecosystem only if the resolver already exposes a usable location.

## Design
`domain.Resolution`/`DependencyVersion` does not currently carry a repo URL — check what, if anything, the Go resolver's `go list -m -json` output already gives (`goModule.GoMod` points at the go.mod file's module cache path, not a browsable repo; the module path itself, e.g. `github.com/x/y`, usually *is* the repo host+path for Go, but vanity import paths are not). Scope this ticket down to the case where the module path parses as a direct `github.com/<org>/<repo>` (or similar well-known host) shape — vanity-import resolution (an HTTP `go-import` meta-tag lookup) is real, separate work, not bundled here.

```go
// in syncVersion, replacing the current hard failure:
manifest, ok := reg.Match(dep.Dependency.Ecosystem, dep.Dependency.Name)
if !ok {
    fallback, ok := fallbackManifest(dep.Dependency)
    if !ok {
        return fmt.Errorf("no registry manifest for %s", dep.Dependency.Name)
    }
    manifest = fallback
}
```

`fallbackManifest` returns `(registry.Manifest, bool)` — `ok=false` when the dependency's ecosystem/name gives no derivable repo location, preserving today's exact failure for anything it can't help with.

## Inputs / Outputs
- Input: a `domain.DependencyVersion` with no registry match.
- Output: a synthetic single-source `registry.Manifest` (git source, `version.strategy: none`, `ref: HEAD`, authority 0) when derivable, else the existing error.

## Failure behavior
Unchanged from today when no fallback is derivable — this ticket only adds a path that succeeds in a case that previously failed, never removes the existing failure mode.

## Tests
- A Go dependency whose module path is `github.com/org/repo` with no registry manifest syncs via the fallback and produces `TrustUnknown` objects.
- A dependency whose module path isn't a recognizable repo host still fails exactly as today.
- A dependency *with* a real registry manifest is unaffected — the fallback path is never consulted when `reg.Match` succeeds.

## Acceptance criteria
- [ ] Go dependencies with a `github.com`-shaped module path sync via fallback when unmapped.
- [ ] Fallback-sourced objects carry `TrustClass: unknown`.
- [ ] Everything that succeeds today continues to behave identically (fallback only fires on what previously errored).
