# STRESS-016: `explain_call_site` at scale

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** medium

## Goal
Run `explain_call_site` against many real call sites across a real, non-trivial Go codebase — not the 3-10-line fixtures GRAPH-001..004's own tests use — to check both correctness (does `gopackages.Provider`'s `go/packages` load stay correct across a real package graph, including cases the small fixtures never exercised — embedded interfaces, generics, multi-file packages, vendored/replaced dependencies) and latency (does a `go/packages` load against a real, larger package take noticeably longer, and is that acceptable for an interactive MCP tool call).

## Non-goals
- No fix for whatever's found beyond documenting it — a real correctness or latency gap here gets its own follow-up ticket.

## Simplicity constraints
- depctl's own codebase is the target: real, non-trivial, already deeply understood (so expected results are easy to verify), and needs no separate cloning step.

## Design
1. Point `explain_call_site` at 15-20 real call sites across depctl's own codebase, spanning:
   - Real external-dependency calls: `bolt.Open` (`go.etcd.io/bbolt`, used throughout `internal/control/bbolt`), `badger.Open` (`github.com/dgraph-io/badger/v4`, `internal/data/badger`), a `cobra.Command` construction (`github.com/spf13/cobra`, `internal/cli/root.go` or any `new*Cmd` function), a `blake3.Sum256` call (`github.com/zeebo/blake3`, `internal/data/fingerprint`), and a `packages.Load` call (`golang.org/x/tools/go/packages`, `internal/symbolgraph/gopackages/provider.go` — appropriately self-referential).
   - A call through an interface value: any `backend.VectorBackend` method call site in `internal/query/search.go` (dispatched through the interface, not a concrete `*qdrant.Backend`).
   - A call in one of the larger files in the repo: something in `internal/cli/sync.go` or `internal/cli/gc.go` (both grew substantially this session).
   - A handful of internal call sites (depctl calling its own code, e.g. `syncVersion` calling `generation.Build`) to confirm the internal/external boundary detection stays correct in a real, larger package graph.
2. For each, record: correct resolution (module/package/symbol match manual inspection) and latency.

## Inputs / Outputs
- Input: 15-20 real call sites across a real Go codebase (depctl's own).
- Output: pass/fail on correctness for each, plus recorded per-call latency at this scale.

## Failure behavior
- Any incorrect resolution (wrong module, wrong symbol, or a false internal/external classification) is this ticket's finding.
- Meaningfully higher latency than the fixture-scale numbers (sub-100ms in the existing unit tests) is worth recording even if not a failure — real package graphs are bigger than 2-file fixtures.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] 15-20 real call sites across a real codebase all resolve correctly.
- [x] Latency at this scale is recorded and compared against the fixture-scale numbers.

## Post-implementation note (2026-09-29)

Used depctl's own real production registration (`proj_llrg46j3ra2ehvorh5y4wv4kjajqlxtffquqgkxbfjic7khnvuha`, already `scan`ned/`sync`ed with 116 real dependencies from ordinary session usage — no new fixture needed, per the ticket's own simplicity constraint). Wrote a throwaway driver (`hack/stress016/main.go`, deleted after use) using the same real-subprocess-over-stdio pattern as `internal/cli/serve_smoke_test.go` (`sdkmcp.NewClient` + `CommandTransport` spawning the real `depctl serve` binary), and called `explain_call_site` against 15 real call sites:

| Call site | Classification | Latency |
|---|---|---|
| `bolt.Open` (`go.etcd.io/bbolt`) | external, resolved correctly | 891ms |
| `bg.Open` (`github.com/dgraph-io/badger/v4`) | external, resolved correctly | 782ms |
| `cobra.Command{}` (`github.com/spf13/cobra`) | external, resolved correctly | **3.00s** |
| `blake3.New()` (`github.com/zeebo/blake3`) | external, resolved correctly | 738ms |
| `packages.Load` (`golang.org/x/tools`, self-referential) | external, resolved correctly | 964ms |
| `s.backend.Query` (interface dispatch, `backend.VectorBackend`) | internal, correct | 383ms |
| `generation.Build` (large file, `internal/cli/sync.go`) | internal, correct | 478ms |
| `retention.PlanGC` | internal, correct | 462ms |
| `store.ClearActiveGeneration` | internal, correct | 464ms |
| `planner.Plan` | internal, correct | 470ms |
| `os.MkdirAll` (stdlib) | tool-level error, `ErrDependencyNotResolved` | 449ms |
| `s.coordinator.ExcludeForGC` | internal, correct | 409ms |
| `embedder.Embed` (interface dispatch, `embedding.Embedder`) | internal, correct | 408ms |
| `yaml.Unmarshal` (`gopkg.in/yaml.v3`) | external, resolved correctly | 685ms |
| `checkForeignCollectionData` (cross-file call within `internal/cli`) | internal, correct | 603ms |

**All 15 resolved correctly** — every external dependency named its real module/package correctly with real evidence chunks pulled from that dependency's own synced content (e.g. `bg.Open` → `github.com/dgraph-io/badger/v4`'s own doc text, not a stand-in); every internal call site (including the two interface-dispatch cases, `backend.VectorBackend.Query` and `embedding.Embedder.Embed`, and the large-file/cross-file cases) correctly returned the "inside the project" note rather than misclassifying; the stdlib call (`os.MkdirAll`) correctly produced `ErrDependencyNotResolved`, matching the existing documented behavior (depctl doesn't track the standard library) rather than a panic or a hang.

One apparent anomaly during setup, self-corrected before it became a false finding: the first badger call site chosen (`hack/verify-sync/main.go`'s `badger.Open`) was itself a call into depctl's *own* `internal/data/badger` package (a thin wrapper also named `Open`), not the real `dgraph-io/badger` library — `explain_call_site` correctly classified it as internal. The test's own expectation was wrong, not the tool; swapped in the actual external call site (`bg.Open`, aliased import, inside `internal/data/badger/store.go` itself) and it resolved correctly.

**Latency finding (not a failure, but real and worth recording):** all calls landed in the 380-965ms range except `cobra.Command{}`, which took **3.00s** — roughly 3-6x the others. `cobra.Command` is a large struct literal spanning many fields across multiple lines in `internal/cli/root.go`; type-checking that composite literal during `go/packages`' one-shot load is real, measurable extra work versus a simple call expression. This is meaningfully higher than GRAPH-001..004's own sub-100ms fixture-scale numbers across the board (400ms-3s here vs <100ms there) — expected, since a real package's `go/packages` load type-checks real, larger source files rather than 2-line fixtures, but worth flagging: an interactive MCP tool call landing at 3 seconds is at the edge of comfortable for a coding agent's request-response loop. No fix filed — the ticket's own non-goals rule that out — but this is real signal that a future performance pass on `gopackages.Provider`'s per-call load (e.g. caching a loaded package across calls within one project, rather than a fresh `packages.Load` every time) would be worth its own ticket if `explain_call_site` sees real day-to-day use.

**Coverage gap, noted honestly:** depctl's own codebase has no generics and no embedded interfaces, so this real-codebase test couldn't exercise those two cases the ticket's Design section calls out — that coverage remains limited to whatever GRAPH-001..004's own fixture-scale tests already include.
