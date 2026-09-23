# GRAPH-003: `go/packages`-based Go symbol provider

**Epic:** Repo-Graph Symbol Join
**Status:** done
**Depends on:** GRAPH-001 (`SymbolProvider` interface)
**Estimated size:** medium

## Goal
Ship the one reference `SymbolProvider` implementation this epic needs to be end-to-end usable, resolving a Go call site to the external symbol it refers to via `golang.org/x/tools/go/packages` + `go/types` type information — deterministic, one-shot, no long-running server process. This is the "cleanest deterministic symbol identity with least integration work" choice GRAPH-001 deferred: it reuses the same type-checking machinery `gopls`/`goimports`/every serious Go tool already relies on, as a plain library call, without ragctl having to drive an LSP session or reimplement type resolution itself.

## Non-goals
- Go only, matching this project's "one reference implementation before adding more" convention (`docs/tickets/README.md`) — a Python/Node/other-ecosystem `SymbolProvider` is explicitly out of scope here and would be its own future ticket once a Go-only version proves the join out.
- No LSP protocol, no `gopls` subprocess — `go/packages`' default driver already shells out to `go list`/`go build` under the hood (the same pattern `internal/resolver/golang` already uses via `go list -m -json all`), but this ticket talks to it as a Go library, not a JSON-RPC session.
- No incremental/watch-mode re-resolution — one call site in, one answer out, exactly matching `SymbolProvider.Resolve`'s existing one-shot contract. Caching, if a caller needs it, is the caller's concern (per GRAPH-001's own non-goals).
- No whole-repository eager loading — load only the package containing the call site's file (plus its direct imports, which `go/packages`' `LoadTypes`/`LoadSyntax` mode already pulls in as needed for type info), not every package in the module.

## Simplicity constraints
- New package `internal/symbolgraph/gopackages`, implementing GRAPH-001's `symbolgraph.SymbolProvider` — no changes to the `symbolgraph` interface package itself.
- One exported constructor, `New(moduleRoot string) *Provider` — no configuration surface beyond the project root ragctl already knows from its own `domain.Project`.

## Design
Package: `internal/symbolgraph/gopackages`

```go
// Provider resolves Go call sites to external symbols using
// golang.org/x/tools/go/packages' type-checked load — a one-shot,
// deterministic lookup, not a persistent index or LSP session.
type Provider struct {
    moduleRoot string
}

func New(moduleRoot string) *Provider

func (p *Provider) Resolve(ctx context.Context, site symbolgraph.CallSite) (symbolgraph.ExternalSymbolRef, bool, error)
```

`Resolve`'s implementation:
1. `packages.Load` with `Mode: packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedDeps | packages.NeedImports`, scoped to the package containing `site.File` (a `./...`-relative pattern derived from the file's directory, not the whole module).
2. Locate the `*ast.File` matching `site.File`, then the `ast.Node` at `site.Line`/`site.Column` via `astutil.PathEnclosingInterval` (from `golang.org/x/tools/go/ast/astutil`) — walk up to the nearest `*ast.Ident` or selector expression.
3. Look up that identifier in the loaded package's `types.Info.Uses` (or `.Selections` for a method/field selector like `tx.Bucket(...)`) to get its `types.Object`.
4. If `obj.Pkg()` is nil, or `obj.Pkg().Path()` has the same module path prefix as `moduleRoot`'s own module (an internal symbol, not external) → return `(ExternalSymbolRef{}, false, nil)`.
5. Otherwise, build `ExternalSymbolRef{Ecosystem: "go", Module: <module path from obj.Pkg().Path(), trimmed to the owning module root via go.sum/build list — reuse internal/resolver/golang's existing module-path knowledge rather than re-deriving it>, Package: obj.Pkg().Name(), QualifiedName: <receiver-qualified name for a method, e.g. "(*Tx).Bucket">, SourceFile: <position.Filename from obj.Pos(), if available>}`.

## Inputs / Outputs
- Input: a `symbolgraph.CallSite` (file + line/column) within a Go module ragctl has already resolved.
- Output: `(ExternalSymbolRef, true, nil)` for a call site resolving outside the current module; `(ExternalSymbolRef{}, false, nil)` for an internal call site; a wrapped error if `packages.Load` itself fails (e.g. the file doesn't type-check).

## Failure behavior
- `packages.Load` reports package errors (`pkg.Errors`) for the loaded package → return a typed error naming the file and the underlying compile error, rather than silently returning `ok=false` — a genuinely broken build is a different failure mode from "this call site has no external symbol," and callers (GRAPH-002) already distinguish these per GRAPH-001's contract.
- `site.Line`/`site.Column` don't land on any identifiable expression (e.g. whitespace, a comment) → `(ExternalSymbolRef{}, false, nil)`, not an error — a caller handing in an imprecise position is not a provider failure.

## Tests
- A small fixture Go module with a call into a real external dependency (e.g. the same `example.com/foo` local-replace fixture pattern other tests already use) — `Resolve` at that call site's exact position returns the correct `ExternalSymbolRef`.
- A call site referring to a symbol defined in the same module → `ok == false`.
- A call site on a method via an interface value (not a concrete type) → still resolves to the interface method's defining package (proving this handles selector expressions, not just plain identifiers).
- A file with a genuine compile error → typed error, not `ok=false`.

## Acceptance criteria
- [x] `internal/symbolgraph/gopackages.Provider` implements `symbolgraph.SymbolProvider`.
- [x] Resolves both plain-identifier and selector-expression (method/field) call sites correctly.
- [x] Internal-to-module call sites return `ok == false`, never a spurious `ExternalSymbolRef`.
- [x] A genuine load/compile failure surfaces as a typed error, distinct from "not an external symbol."

### Bug found in code review, before live testing: `Module` was a package path, not a module path
A post-implementation code review (not live testing — caught by inspection first) found `refFromObject` set `ExternalSymbolRef.Module` to `pkg.Path()`, the *package's* import path — correct only for a single-package module. For any multi-package dependency (the overwhelming majority of real Go libraries: `github.com/stretchr/testify/assert` belongs to module `github.com/stretchr/testify`, `google.golang.org/grpc/credentials` to `google.golang.org/grpc`, etc.), this set `Module` to the subpackage path, which then silently failed `symbolgraph.matchDependency`'s comparison against `resolution.Dependencies[i].Dependency.Name` (the real module name) — every call into a multi-package dependency would have produced a false `ErrDependencyNotResolved`, even for a dependency the project genuinely has.

The fixture module used to write GRAPH-003's own tests was single-package, so this passed every test at ticket-close time — the same class of gap as WATCH-019's daemon round-trip bug (a real behavior only surfaces against a shape the test fixtures didn't happen to cover).

Fixed by building a package-path → module-path map via `packages.Visit` over the full loaded package graph (roots + every transitive dependency, since `NeedModule` populates `.Module` on every loaded `*packages.Package`, not just the root), and looking up each resolved symbol's package in that map — falling back to the package path itself only when no module info exists (the standard library). New regression test `TestResolveModuleIsRootNotSubpackagePath` (a fixture module with a root package and a `sub` subpackage) fails against the old code and passes against the fix.

Live-verified afterward against a real, published, multi-package dependency (`github.com/stretchr/testify`, calling `assert.Equal`) through a real daemon + real `ragctl serve` + a real MCP client: `explain_call_site` correctly returned `module: "github.com/stretchr/testify"` (not `.../assert`), `package: "assert"`, `qualified_name: "Equal"`, `version: "v1.9.0"`, and 10 real, version-correct, relevant chunks — see GRAPH-004's own post-implementation note for the full live-test session.
