# internal/registry

`internal/registry` answers "where can I obtain trustworthy knowledge for this package/version?" — a question distinct from `internal/resolver`'s "what package/version does this project use?" The registry itself is data (YAML manifests validated against a JSON Schema), not code, so new package coverage ships without touching Go internals. `Loader` merges built-in, user, and project manifests into a `Registry` that answers exact ecosystem+package lookups; `internal/registry/discover` is a separate, human-in-the-loop helper that proposes candidate sources for packages the registry doesn't yet cover, by querying npm/PyPI metadata. `internal/registry/schema` and `internal/registry/builtin` are data-only directories (a JSON Schema and the seed manifests) embedded into the package via `go:embed` — neither is a Go subpackage.

## Key types and functions

### internal/registry

- `Manifest` — a KnowledgePackage: maps an ecosystem+package identity to its knowledge sources (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go).
- `Metadata` — manifest identity (`Name`), independent of what it matches (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go).
- `Match` — exact-membership predicate: ecosystem ∈ `Ecosystems` and name ∈ `Packages` (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go).
- `VersionStrategy` — how a resolved dependency version maps to a point in the knowledge source, e.g. a git tag template (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go).
- `Source` — one acquirable knowledge source (`git`/`godoc`/`website`/`github-releases`), ranked by `Authority` (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go).
- `ManifestError` — wraps a manifest that failed schema validation or YAML parsing, naming the failing field(s) (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go).
- `ParseManifest(data []byte) (Manifest, error)` — decodes YAML generically, round-trips through JSON, validates against the embedded `knowledge-package.schema.json`, then decodes into `Manifest` (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go).
- `Registry` — the merged, matchable set of loaded manifests plus load `Warnings` (/Users/jin/GolandProjects/ragctl/internal/registry/loader.go).
- `Loader` — loads and merges manifests from built-in, user, and project sources, ascending priority (/Users/jin/GolandProjects/ragctl/internal/registry/loader.go).
- `NewLoader(userRegistryDir, projectRegistryDir string) *Loader` — either dir may be `""` to skip that source (/Users/jin/GolandProjects/ragctl/internal/registry/loader.go).
- `(*Loader) Load(ctx) (*Registry, error)` — reads embedded `builtin/*.yaml` (fatal if invalid), then user dir, then project dir (each skips invalid files with a warning), then builds the match index (/Users/jin/GolandProjects/ragctl/internal/registry/loader.go).
- `(*Registry) Match(eco domain.Ecosystem, pkg string) (Manifest, bool)` — O(1) index lookup; no match is `(Manifest{}, false)`, not an error (/Users/jin/GolandProjects/ragctl/internal/registry/matcher.go).
- `(*Registry) ManifestNames() []string` — every loaded manifest's `metadata.name` (/Users/jin/GolandProjects/ragctl/internal/registry/matcher.go).
- `(*Registry) Manifest(name string) (Manifest, bool)` — looks up a loaded manifest by name (/Users/jin/GolandProjects/ragctl/internal/registry/matcher.go).
- `LivenessResult` — whether a `Source`'s URL was reachable when checked, with the underlying error preserved (/Users/jin/GolandProjects/ragctl/internal/registry/liveness.go).
- `CheckLiveness(ctx, source Source) LivenessResult` — probes a source without acquiring it: `git ls-remote` for `git`, HTTP `HEAD` for `website`; only runs when explicitly called, never implicitly (/Users/jin/GolandProjects/ragctl/internal/registry/liveness.go).

### internal/registry/discover

- `Discover(ctx, ecosystem domain.Ecosystem, pkg string) ([]registry.Source, error)` — queries the ecosystem's package-metadata API for structured source URLs (npm's `repository`, PyPI's `project_urls`/`home_page`); never guesses, returns an empty result for a package with no such fields (/Users/jin/GolandProjects/ragctl/internal/registry/discover/discover.go). Supports `EcosystemNode` and `EcosystemPython` only.

### internal/registry/schema and internal/registry/builtin

- `internal/registry/schema/knowledge-package.schema.json` — the canonical JSON Schema a `Manifest` YAML file is validated against; embedded via `go:embed` in manifest.go. Lives inside the package (not at repo root) because `go:embed` can't reach outside a package's own directory tree.
- `internal/registry/builtin/*.yaml` — six hand-authored seed manifests (grpc-go, protobuf-go, badger, bbolt, pydantic, fastapi), embedded via `go:embed` in loader.go. Enough to validate the manifest format end-to-end, not real ecosystem coverage.

## Dataflow

```mermaid
flowchart TD
    subgraph Sources["Manifest sources (ascending priority)"]
        Builtin["embed.FS builtin/*.yaml\n(fatal if invalid)"]
        UserDir["<data-dir>/registry/*.yaml\n(warn + skip if invalid)"]
        ProjectDir["project override dir\n(warn + skip if invalid, not wired to any CLI yet)"]
    end

    Schema["schema/knowledge-package.schema.json\n(embedded JSON Schema)"]

    Builtin --> ParseManifest
    UserDir --> ParseManifest
    ProjectDir --> ParseManifest
    Schema --> ParseManifest["ParseManifest\n(YAML -> JSON -> schema validate -> Manifest)"]

    ParseManifest --> Add["Registry.add\n(last writer wins per metadata.name, logs Warnings)"]
    Add --> Build["Registry.build\n(index by ecosystem|package)"]
    Build --> Registry(("*Registry\n{manifests, index, Warnings}"))

    Registry --> Match["Registry.Match(eco, pkg)"]
    Match --> CLI1["cli.plan / cli.sync / cli.describe / cli.registry"]

    subgraph discover["internal/registry/discover (separate, human-in-the-loop path)"]
        DiscoverCall["Discover(ctx, ecosystem, pkg)"]
        NpmAPI["registry.npmjs.org/{pkg}"]
        PypiAPI["pypi.org/pypi/{pkg}/json"]
        DiscoverCall --> NpmAPI
        DiscoverCall --> PypiAPI
    end

    DiscoverCall --> CandidateSources["[]registry.Source proposal"]
    CandidateSources --> CLI2["cli registry add\n(human turns proposal into a manifest file)"]
    CLI2 --> UserDir

    Registry --> Liveness["CheckLiveness(ctx, source)\n(explicit only, e.g. describe --liveness)"]
    Liveness --> LivenessResult(("LivenessResult"))
    LivenessResult --> CLI1
```

## Walkthrough

Scenario: `ragctl plan` needs a knowledge source for the Go dependency `google.golang.org/grpc` and the built-in registry already ships a manifest for it, so no user/project override is involved.

1. **Startup: `Loader.Load` reads the built-in manifest.** `NewLoader("", "")` (or with real user/project dirs) builds a `Loader`; `Load` starts by reading `builtinFS.ReadDir("builtin")` — the `//go:embed builtin/*.yaml` filesystem compiled into the binary (/Users/jin/GolandProjects/ragctl/internal/registry/loader.go, 43). One of the six entries is `grpc-go.yaml`:

   ```yaml
   apiVersion: ragctl.dev/v1alpha1
   kind: KnowledgePackage
   metadata:
     name: grpc-go
   match:
     ecosystems: [go]
     packages: [google.golang.org/grpc]
   version:
     strategy: semver-tag
     repository: grpc/grpc-go
   sources:
     - id: repository
       type: git
       url: https://github.com/grpc/grpc-go
       ref: "v${version}"
       authority: 100
     - id: package-docs
       type: godoc
       module: google.golang.org/grpc
       authority: 95
   ```

   (/Users/jin/GolandProjects/ragctl/internal/registry/builtin/grpc-go.yaml)

2. **`ParseManifest` validates before decoding.** The raw bytes go through `yaml.Unmarshal` into a generic `any`, get re-marshaled to JSON, and are validated against the embedded `schema/knowledge-package.schema.json` via `compiledSchema.Validate` — only after that does the same YAML get decoded a second time, now into the typed `Manifest` struct (/Users/jin/GolandProjects/ragctl/internal/registry/manifest.go). A schema violation (say, a missing `authority` field) would come back as a `*ManifestError` naming the failing field; since this is a built-in manifest, `Loader.Load` treats that as fatal (/Users/jin/GolandProjects/ragctl/internal/registry/loader.go) rather than skipping it, because a built-in manifest shipping broken is a bug, not user input.

3. **Decoded shape.** `ParseManifest` returns:

   ```go
   Manifest{
       Metadata: Metadata{Name: "grpc-go"},
       Match:    Match{Ecosystems: []domain.Ecosystem{"go"}, Packages: []string{"google.golang.org/grpc"}},
       Version:  VersionStrategy{Strategy: "semver-tag", Repository: "grpc/grpc-go"},
       Sources: []Source{
           {ID: "repository", Type: "git", URL: "https://github.com/grpc/grpc-go", Ref: "v${version}", Authority: 100},
           {ID: "package-docs", Type: "godoc", Module: "google.golang.org/grpc", Authority: 95},
       },
   }
   ```

4. **`Registry.add` and `Registry.build`.** `reg.add(m, "builtin")` stores it keyed by `metadata.name` — `"grpc-go"` — in `r.manifests` (no prior entry, so no override warning) (/Users/jin/GolandProjects/ragctl/internal/registry/loader.go). After all built-in/user/project sources are merged, `build()` walks every manifest's `Match.Ecosystems × Match.Packages` cross product and populates the lookup index; for this manifest that's a single pair, so `r.index["go|google.golang.org/grpc"] = grpc-go manifest` (/Users/jin/GolandProjects/ragctl/internal/registry/matcher.go, using `matchKey`).

5. **`plan` calls `Registry.Match`.** Given the resolved dependency `domain.DependencyVersion{Dependency: {Ecosystem: "go", Name: "google.golang.org/grpc"}, Version: "v1.68.0"}` (from `internal/resolver/golang`, see resolver.md's walkthrough), `internal/cli/plan.go` calls `registry.Match(domain.EcosystemGo, "google.golang.org/grpc")`. That's `r.index[matchKey("go", "google.golang.org/grpc")]` → `r.index["go|google.golang.org/grpc"]`, an O(1) hit returning the `grpc-go` manifest and `true` (/Users/jin/GolandProjects/ragctl/internal/registry/matcher.go).

6. **What the caller does with it.** `plan` now has two candidate `Source`s ranked by `Authority`: the git source (100) wins over the godoc source (95), so the higher-authority source — `https://github.com/grpc/grpc-go` at ref `v1.68.0` (the `${version}` template substituted from the resolved `v1.68.0`) — is what gets handed to `internal/source/git.Cache.EnsureMirror`/`ResolveRef` downstream (see source-git.md's walkthrough for what happens next).

   If instead `plan` were asked about `google.golang.org/grpc/stats` (a real grpc-go subpackage) with no exact manifest entry for that import path, `Match` would return `(Manifest{}, false)` — not an error, just "no known knowledge source," which `plan` reports as a gap rather than failing the run (per `Match`'s doc comment, /Users/jin/GolandProjects/ragctl/internal/registry/matcher.go).

## Notes

- `Registry.Match` is used directly by `internal/cli/plan.go`, `sync.go`, `describe.go`, and `registry.go` (see loadRegistryForCLI, buildReport/buildPackageEntry) — there is no separate consumer package; the CLI is the only caller today.
- `internal/resolver` has an analogous `Registry` type for a different purpose (resolver priority, not manifest matching) — don't conflate the two; `internal/registry.Registry` is manifest-matching only.
- `discover.Discover` results are never applied to a loaded `Registry` automatically (REG-006) — they're only a proposal a human turns into a real manifest file dropped in `<data-dir>/registry/`.
- `CheckLiveness` only supports `git` and `website` source types; `godoc` and `github-releases` sources return an "not implemented" error result rather than panicking.
- The project-override registry directory is fully supported by `Loader` but no CLI command currently passes a project root's `.ragctl/registry/` dir in (per architecture.md's `ragctl registry list` notes) — it's dead code path from the CLI's perspective today.
- Malformed built-in manifests are treated as fatal (`Load` returns an error) since they're compiled in and should never be invalid; malformed user/project manifests are skipped with a warning instead, since those are external, mutable files.
- `discover`'s `sourceType` classifies any github.com/gitlab.com URL as `git` and everything else as `website` — a coarse heuristic, not URL validation.
