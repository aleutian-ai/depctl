# internal/project

`internal/project` discovers local project roots by walking a directory tree for known ecosystem manifest files, and assigns each root a stable, content-derived ID. It's the entry point of the whole pipeline: `ragctl scan` calls `project.Scan` to find what to track before anything else (resolution, registry matching, generation) can happen. See `docs/tickets/planned/04-project-discovery`.

## Key types and functions

- `DetectedProject` — one `(Root, Ecosystem)` match found by `Scan`; a polyglot directory (e.g. both `go.mod` and `package.json`) produces multiple entries sharing the same `Root` — `internal/project/scanner.go`.
- `Scan(ctx, root) ([]DetectedProject, error)` — walks `root`, skipping known non-source directories, and returns every detected project; a permission error on a subdirectory skips that subtree rather than aborting — `internal/project/scanner.go`.
- `ProjectID(canonicalRoot string) string` — deterministic ID: `"proj_" + base32(BLAKE3(canonicalRoot))` — `internal/project/id.go`.
- `CanonicalRoot(root string) (string, error)` — normalizes a root path the same way `Scan` does, for callers computing a `ProjectID` outside of a scan — `internal/project/id.go`.

## Dataflow

```mermaid
flowchart LR
    User["ragctl scan [path]"] --> Scan["project.Scan(ctx, root)"]
    Scan -->|filepath.WalkDir, skip .git/node_modules/vendor/...| FS["filesystem"]
    FS -->|stat go.mod, package.json, Cargo.toml, pyproject.toml,\nrequirements.txt, pom.xml, build.gradle*| Scan
    Scan -->|[]DetectedProject{Root, Ecosystem}| CLI["cli.runScan"]

    CLI -->|project.ProjectID(root)| ID["proj_&lt;base32 BLAKE3&gt;"]
    ID --> Bbolt["internal/control/bbolt\nPutProject / GetProject"]
    CLI -->|dp.Root| Resolvers["internal/resolver/golang|python|node\nResolve(ctx, root)"]
```

## Walkthrough

Concrete scenario: `ragctl scan /Users/dev/myapp`, where `/Users/dev/myapp` is a Go module (`go.mod` at its root) with a `.git` directory and a `vendor/` directory sitting alongside it.

1. `cli.runScan` (`internal/cli/scan.go`) calls `project.Scan(context.Background(), "/Users/dev/myapp")` (`internal/project/scanner.go`).
2. `Scan` first `os.Stat`s the root — it exists and is a directory, so no immediate error.
3. `filepath.WalkDir` walks the tree depth-first. At `/Users/dev/myapp/.git` and `/Users/dev/myapp/vendor`, `d.Name()` matches `skipDirs` (`internal/project/scanner.go`), so `WalkDir` returns `filepath.SkipDir` and never descends into either — no marker-file stat happens inside them at all.
4. At the root directory itself (`path == "/Users/dev/myapp"`), `canonicalize` (`internal/project/scanner.go`) runs `filepath.Abs` then `filepath.Clean`, producing the canonical string `"/Users/dev/myapp"` (already absolute and clean here, but a relative invocation like `ragctl scan ./myapp` from `/Users/dev` would canonicalize to the same value).
5. `Scan` iterates the `markers` map (`internal/project/scanner.go`) and stats `/Users/dev/myapp/go.mod` — it exists, so `eco = domain.EcosystemGo`. The dedup key `"/Users/dev/myapp|go"` isn't in `seen`, so it's recorded and `DetectedProject{Root: "/Users/dev/myapp", Ecosystem: domain.EcosystemGo}` is appended to `results`. The other six marker filenames (`pyproject.toml`, `package.json`, `Cargo.toml`, ...) aren't present, so no other ecosystem is detected for this root.
6. `Scan` returns `[]DetectedProject{{Root: "/Users/dev/myapp", Ecosystem: "go"}}` to `runScan`.
7. Back in `runScan`, `supportedEcosystems[domain.EcosystemGo]` is true (`internal/cli/scan.go`), so the project proceeds. `project.ProjectID("/Users/dev/myapp")` (`internal/project/id.go`) computes `sum := blake3.Sum256([]byte("/Users/dev/myapp"))` and lowercase-base32-encodes it, producing (an actual run of this exact code):
   ```
   proj_qmlpl3fszfmjzqnnmfqyipckzipuefhmuahg35tz252d36on6zya
   ```
8. `runScan` calls `store.GetProject(ctx, "proj_qmlpl3fszfmjzqnnmfqyipckzipuefhmuahg35tz252d36on6zya")` against the `projects` bbolt bucket (`internal/control/bbolt/projects.go`). On a first scan this misses and `bboltstore.ErrNotFound` comes back, so `isNew = true`.
9. `runScan` builds `domain.Project{ID: "proj_qmlpl3...", Root: "/Users/dev/myapp", CreatedAt: now, UpdatedAt: now}` and writes it via `store.PutProject` — JSON-marshaled and put under key `proj_qmlpl3...` in the `projects` bucket. It prints `new          go       /Users/dev/myapp`.
10. `runScan` then calls `resolvers[domain.EcosystemGo].Resolve(ctx, "/Users/dev/myapp")` (`internal/resolver/golang/resolve.go`), which shells out to `go list -m -json all`, normalizes the module list, and returns a `domain.Resolution` with e.g. two dependencies (`google.golang.org/grpc@v1.67.0`, `golang.org/x/net@v0.30.0`) and a fingerprint computed by `resolver.Fingerprint` (`internal/resolver/fingerprint.go`) — for exactly those two dependencies, an actual run of this code produces:
    ```
    res_RIZLXZF5UXTYUXTC3DJ7OB4247EPHYSUKMFLCKX7ESACXNA6N24Q
    ```
    (uppercase — `resolver.Fingerprint` uses `base32.StdEncoding` directly, unlike `ProjectID`'s lowercased encoding).
11. `runScan` writes the resolution via `store.PutResolution(ctx, "proj_qmlpl3...", res)` into the `project_dependencies` bucket (`internal/control/bbolt/resolutions.go`), keyed by the same project ID, and prints `resolved     go       /Users/dev/myapp: 2 dependencies`.
12. A second `ragctl scan /Users/dev/myapp` recomputes the identical canonical root and `ProjectID`, so `GetProject` now hits — `isNew = false`, `p.CreatedAt` is carried over from the existing record, and the line printed is `existing     go       /Users/dev/myapp` instead of `new`.

## Notes

- Detection is marker-file presence only, no content parsing — `markers` (`internal/project/scanner.go`) maps filename to ecosystem; a `pyproject.toml` with no lockfile or a `package.json` with no lockfile still gets detected and registered, even though the matching resolver later fails to resolve it (a non-fatal `resolve error`, handled in `cli.runScan`).
- `skipDirs` (`internal/project/scanner.go`) is an exact-name match list (`.git`, `node_modules`, `vendor`, `dist`, `build`, `target`, `.venv`, `venv`, `__pycache__`) — no glob or `.gitignore` awareness.
- v0.1 policy, documented at `internal/project/id.go`: moving or renaming a project directory produces a new `ProjectID` on the next scan. There is no `ragctl project move` command — the old project's references and retention reasons are not transferred; a moved project just re-registers as new.
- `Scan`'s `seen` map dedupes by `canonical + "|" + ecosystem`, so rescanning is idempotent at the detection layer; idempotency of the resulting `Project`/`Resolution` records themselves is `cli.runScan`'s responsibility (it looks up `GetProject` before deciding new vs. existing).
