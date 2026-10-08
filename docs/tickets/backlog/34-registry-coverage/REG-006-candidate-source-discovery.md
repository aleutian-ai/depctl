# REG-006: Candidate source discovery

**Epic:** Registry Coverage
**Status:** planned
**Depends on:** REG-002 (registry loader), REG-005 (no-manifest fallback)
**Estimated size:** medium

## Goal
Reduce the manual-typing cost of registering a new package by proposing candidate sources from structured ecosystem metadata (npm's `repository` field, PyPI's `project_urls`) for a human to review and turn into a real manifest — never auto-applied, never synced from directly.

## Non-goals
- No auto-sync from a discovered candidate — a candidate is a suggestion written to a review location, not a registry entry. This is the line that keeps the registry a trust boundary (see epic INDEX, SEC-001).
- No web search or LLM-based guessing — only fields the package's own ecosystem metadata already publishes (npm's `package.json`/registry API `repository`, PyPI's `project_urls`/`home_page`).
- No Go vanity-import HTTP resolution in this ticket — that's a distinct, smaller mechanism REG-005 already scoped out; fold in later only if genuinely needed.

## Simplicity constraints
- One new subcommand (`depctl registry discover <ecosystem>/<package>`), not a background/automatic process — discovery runs on request, for one package at a time, because it may need network access this repo's other commands don't otherwise require.
- Output is a YAML manifest *draft* printed to stdout (or written with `--out`), in the exact REG-001 schema shape, ready for a human to review, edit, and drop into their user/project registry directory — not a new storage format.

## Design
Package: `internal/registry/discover` (new, isolated — network-touching, optional dependency of the CLI, never imported by `generation`/`sync`).

```go
// Discover queries the given ecosystem's public package metadata API
// for pkg and returns candidate sources found in structured fields.
// It never returns more than the metadata literally states — no
// inference, no fallback guessing.
func Discover(ctx context.Context, ecosystem domain.Ecosystem, pkg string) ([]registry.Source, error)
```

- Node: `https://registry.npmjs.org/<pkg>` → `.repository.url` (git source, authority left at 0 for the human to set).
- Python: `https://pypi.org/pypi/<pkg>/json` → `info.project_urls`/`info.home_page` (git or website source depending on host).
- `depctl registry discover` prints a draft manifest (or `no candidate sources found in <ecosystem> metadata for <pkg>` — never fabricates a source).

## Inputs / Outputs
- Input: ecosystem + package name.
- Output: a draft `KnowledgePackage` YAML to stdout/file; never writes to the actual registry directory itself.

## Failure behavior
- Metadata API unreachable or package not found: clear error, no partial/guessed manifest.
- A `repository`/`project_urls` field pointing at a non-git, non-http(s) value is skipped, not error'd — malformed upstream metadata is common and shouldn't block finding the fields that are fine.

## Tests
- Node package with a `repository.url` in `git+https://...` shape normalizes to a plain `https://` git source.
- Python package with `project_urls.Source` and `project_urls.Documentation` produces both a `git` and a `website` candidate.
- Package with no usable metadata fields returns an empty candidate list, not an error.
- Output YAML round-trips through `registry.ParseManifest`.

## Acceptance criteria
- [x] `depctl registry discover <ecosystem> <package>` implemented for Node and Python.
- [x] Output is a draft manifest in REG-001's exact schema, never auto-written to a loaded registry directory.
- [x] No network call happens except when this command is explicitly invoked.

## Post-implementation note
CLI shape is two positional arguments (`<ecosystem> <package>`), not a single `<ecosystem>/<package>` string — same reasoning as `depctl describe`'s own fix earlier this session (a package identifier can contain slashes). Verified against the real npm and PyPI registries (`express`, `requests`), and the draft manifest round-trips through `registry.ParseManifest` (its own real schema validator), not just checked for well-formed YAML.

