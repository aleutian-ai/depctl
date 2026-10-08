# VALID-004: Private/disconnected dependency fixture with a deliberate version break

**Epic:** Competitive Validation
**Status:** done
**Depends on:** existing `internal/source/git` (works against any git URL, including local/private paths), `internal/resolver`
**Estimated size:** small

## Goal
Build the specific fixture the comparison called for as the strongest evidence for depctl's actual differentiator: a private, non-public Go module with a deliberate, real API incompatibility between `v1` and `v2` (e.g. a function signature change or a renamed method), plus test code that only compiles/passes against the correct version. Use it to prove two things in one fixture: (1) depctl indexes a fully private/local-only dependency with zero public registry involvement (git-source acquisition needs no registry manifest match beyond a local git source), and (2) an agent given the wrong-version chunk would write code that fails against the actually-resolved version — i.e. the fixture makes "wrong-version contamination" a concrete, checkable failure mode, not an abstract one.

## Non-goals
- No agent/task-completion evaluation in this ticket — that's the later "does this improve coding-agent outcomes" question the comparison raises, out of scope for a fixture-building ticket. This ticket only proves depctl *retrieves* the version-correct chunk; whether an agent then uses it correctly is a separate, later concern (and likely `context-evals` scope).
- Not published anywhere public — the fixture lives as a local git repo under `hack/fixtures` or generated on the fly by the test itself (matching how other local-replace fixtures in this repo already work), never pushed to a real registry.

## Simplicity constraints
- Reuses the existing local-git-source acquisition path end to end — no new "private dependency" mode or config flag. A private dependency is just a `git` source whose URL happens to be a local path or an authenticated remote; nothing about acquisition should need to know the difference.

## Design
Fixture module, `example.com/breaking-lib` (or similar), two tagged commits in a local bare repo:
- `v1.0.0`: exports `func Connect(addr string) (*Client, error)`.
- `v2.0.0`: renames it to `func Connect(addr string, opts ...Option) (*Client, error)` — a real, source-incompatible signature change, with doc comments on both versions describing the (different) correct call shape.

Two fixture projects, mirroring VALID-002's shape: Project A resolves `v1.0.0`, Project B resolves `v2.0.0`.

Test: `search_dependency_docs`-equivalent call scoped to Project A for "how do I call Connect" returns only `v1.0.0`'s doc comment/signature; the same call scoped to Project B returns only `v2.0.0`'s. A negative assertion checks the *other* version's signature string never appears in either result's `Content`.

## Inputs / Outputs
- Input: a local bare git repo fixture with two tagged, source-incompatible commits.
- Output: a reusable fixture-construction helper (parallel to VALID-002's, but private/local-only and with a real API break instead of just different content) plus the retrieval-correctness test above.

## Failure behavior
- None beyond standard test failure — this is a pure fixture + assertion ticket.

## Tests
- The retrieval-correctness assertions in Design.
- One assertion that acquisition never touched any public registry/network call for this dependency (a local-path git source only) — reinforcing that "private dependency" needs no special-casing.

## Acceptance criteria
- [x] A reusable private-dependency fixture with a real, source-breaking v1→v2 change exists.
- [x] Retrieval scoped to each project returns only that project's version's correct API signature, never the other's.
- [x] The fixture requires no public registry entry or network access to build or use.

## Post-implementation note

**This ticket found a real, previously-undiscovered product bug** — not a fixture mistake, a genuine gap in `internal/normalize/godoc`. `go/doc` groups a package-level function under the *type it returns* (`doc.Type.Funcs`), not the package-level `doc.Package.Funcs` list — the extremely common Go constructor convention (`NewFoo()`, `Open()`, `Connect()` returning `*T`). `Normalize()` only ever read `t.Methods` (receiver methods), never `t.Funcs` — meaning **every constructor-shaped function in every dependency this project has ever synced was silently never indexed at all**, regardless of how central it is to that package's real API. The fixture's own `Connect(addr string) (*Client, error)` — chosen specifically to mirror a realistic API shape — was the first thing to ever exercise this code path and immediately hit it: the fixture's `syncVersion` succeeded, `ObjectCount`/`ChunkCount` looked plausible (2 objects), but neither of those 2 objects was `Connect` at all — only the package doc and the `Client` type's doc comment. Confirmed by direct inspection of the generation's Badger chunks and the vector backend's stored points before finding the root cause in `go/doc`'s own source (`Type.Funcs`'s doc comment: "sorted list of functions returning this type").

Fixed in `internal/normalize/godoc/normalize.go`: the type loop now also iterates `t.Funcs` alongside `t.Methods`, calling the same `symbolObject` helper with an empty receiver (these aren't methods). New regression test `TestConstructorFunctionReturningTypeIsExtracted` (`internal/normalize/godoc/godoc_test.go`, new fixture `testdata/sources/constructorpkg`) proves a `NewClient() *Client`-shaped function is now extracted correctly, alongside a genuine method, without disturbing the existing golden-snapshot test (a separate, pre-existing fixture with no constructor-shaped functions, so the fix is additive there, not a behavior change for existing coverage).

**A second, smaller finding, noted but not fixed here** (out of this ticket's scope — a real product decision, not a fixture concern): `query.ResultChunk` only ever surfaces the extracted doc *comment* text (`Content`), never the parsed function *signature*, even though `internal/normalize/godoc`'s `symbolObject` already computes and stores it as chunk metadata (`Metadata["signature"]`, via `go/printer`). An agent asking "what's the signature of Connect" today only gets prose, not the actual `func Connect(...)` syntax, unless the doc comment happens to restate it. Worth a future ticket to thread `signature` metadata through to `query.ResultChunk` and the MCP tool output — not filed yet.

Implemented as `privateDependencyFixture` (`internal/cli/private_dependency_fixture_test.go`), mirroring VALID-002's shape with a real Go API break instead of just different prose. `TestPrivateDependencyReturnsOnlyResolvedVersionsSignature` checks against the doc-text markers actually present in `Content` (the raw signature strings themselves aren't retrievable per the finding above); `TestPrivateDependencyFixtureUsesNoPublicRegistryOrNetworkURL` confirms the manifest's source is a plain local filesystem path. Both pass; full repo build/vet/gofmt/test clean.
