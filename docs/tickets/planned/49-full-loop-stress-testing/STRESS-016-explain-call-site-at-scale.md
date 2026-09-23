# STRESS-016: `explain_call_site` at scale

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none
**Estimated size:** medium

## Goal
Run `explain_call_site` against many real call sites across a real, non-trivial Go codebase — not the 3-10-line fixtures GRAPH-001..004's own tests use — to check both correctness (does `gopackages.Provider`'s `go/packages` load stay correct across a real package graph, including cases the small fixtures never exercised — embedded interfaces, generics, multi-file packages, vendored/replaced dependencies) and latency (does a `go/packages` load against a real, larger package take noticeably longer, and is that acceptable for an interactive MCP tool call).

## Non-goals
- No fix for whatever's found beyond documenting it — a real correctness or latency gap here gets its own follow-up ticket.

## Simplicity constraints
- ragctl's own codebase is the target: real, non-trivial, already deeply understood (so expected results are easy to verify), and needs no separate cloning step.

## Design
1. Point `explain_call_site` at 15-20 real call sites across ragctl's own codebase, spanning:
   - Real external-dependency calls: `bolt.Open` (`go.etcd.io/bbolt`, used throughout `internal/control/bbolt`), `badger.Open` (`github.com/dgraph-io/badger/v4`, `internal/data/badger`), a `cobra.Command` construction (`github.com/spf13/cobra`, `internal/cli/root.go` or any `new*Cmd` function), a `blake3.Sum256` call (`github.com/zeebo/blake3`, `internal/data/fingerprint`), and a `packages.Load` call (`golang.org/x/tools/go/packages`, `internal/symbolgraph/gopackages/provider.go` — appropriately self-referential).
   - A call through an interface value: any `backend.VectorBackend` method call site in `internal/query/search.go` (dispatched through the interface, not a concrete `*qdrant.Backend`).
   - A call in one of the larger files in the repo: something in `internal/cli/sync.go` or `internal/cli/gc.go` (both grew substantially this session).
   - A handful of internal call sites (ragctl calling its own code, e.g. `syncVersion` calling `generation.Build`) to confirm the internal/external boundary detection stays correct in a real, larger package graph.
2. For each, record: correct resolution (module/package/symbol match manual inspection) and latency.

## Inputs / Outputs
- Input: 15-20 real call sites across a real Go codebase (ragctl's own).
- Output: pass/fail on correctness for each, plus recorded per-call latency at this scale.

## Failure behavior
- Any incorrect resolution (wrong module, wrong symbol, or a false internal/external classification) is this ticket's finding.
- Meaningfully higher latency than the fixture-scale numbers (sub-100ms in the existing unit tests) is worth recording even if not a failure — real package graphs are bigger than 2-file fixtures.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] 15-20 real call sites across a real codebase all resolve correctly.
- [ ] Latency at this scale is recorded and compared against the fixture-scale numbers.
