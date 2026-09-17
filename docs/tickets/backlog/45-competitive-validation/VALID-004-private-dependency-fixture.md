# VALID-004: Private/disconnected dependency fixture with a deliberate version break

**Epic:** Competitive Validation
**Status:** planned
**Depends on:** existing `internal/source/git` (works against any git URL, including local/private paths), `internal/resolver`
**Estimated size:** small

## Goal
Build the specific fixture the comparison called for as the strongest evidence for ragctl's actual differentiator: a private, non-public Go module with a deliberate, real API incompatibility between `v1` and `v2` (e.g. a function signature change or a renamed method), plus test code that only compiles/passes against the correct version. Use it to prove two things in one fixture: (1) ragctl indexes a fully private/local-only dependency with zero public registry involvement (git-source acquisition needs no registry manifest match beyond a local git source), and (2) an agent given the wrong-version chunk would write code that fails against the actually-resolved version — i.e. the fixture makes "wrong-version contamination" a concrete, checkable failure mode, not an abstract one.

## Non-goals
- No agent/task-completion evaluation in this ticket — that's the later "does this improve coding-agent outcomes" question the comparison raises, out of scope for a fixture-building ticket. This ticket only proves ragctl *retrieves* the version-correct chunk; whether an agent then uses it correctly is a separate, later concern (and likely `context-evals` scope).
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
- [ ] A reusable private-dependency fixture with a real, source-breaking v1→v2 change exists.
- [ ] Retrieval scoped to each project returns only that project's version's correct API signature, never the other's.
- [ ] The fixture requires no public registry entry or network access to build or use.
