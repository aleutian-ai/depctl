# Epic: Repo-Graph Symbol Join

The scratch doc's §14 calls this "the most compelling cross-system feature to prototype": a project's repo/code graph and ragctl's dependency knowledge share a natural join key — the dependency symbol used at a call site. Neither system needs to duplicate the other's job:

> - graph system: **what symbol is this code referring to?**
> - ragctl: **what knowledge is valid for that dependency version?**

Flow (scratch doc §14):
```text
project source
 -> graph/LSP resolves call site
 -> external symbol identity
 -> dependency/module identity
 -> exact project-resolved version
 -> ragctl exact-version source/docs
 -> evidence bundle
```
Example: `client.go` calls `bbolt.(*Tx).Bucket` → graph resolves the external symbol → ragctl's resolver already knows the project resolved `go.etcd.io/bbolt@1.3.11` → ragctl fetches exact symbol/docs/source for that version.

## Tickets
- [GRAPH-001](GRAPH-001-external-symbol-provider-interface.md) — a thin, narrow `SymbolProvider` interface resolving a call site to an `ExternalSymbolRef`. Explicitly not a control plane.
- [GRAPH-002](GRAPH-002-composite-symbol-to-evidence-bundle.md) — the actual join: `ExternalSymbolRef` → known `domain.Dependency`/version (via `internal/control/bbolt.Store`) → `internal/query.Service` evidence for that exact dependency+version. Depends on GRAPH-001.

## Non-goals for this epic
- No specific graph-provider implementation is chosen or built here (SCIP, LSP-derived symbol info, CodebaseMemory, GitNexus, or otherwise) — scratch doc §14.2: "Start with one... Choose the one that gives the cleanest deterministic symbol identity with the least integration work." That choice is explicitly deferred to whoever picks up a concrete provider integration; GRAPH-001 only specs the interface such a provider would implement.
- No context-control-plane, no agent-memory subsystem (scratch doc §21's "do not build yet" list applies here directly — a symbol join is a narrow lookup, not a new subsystem that owns state).
- No new query-ranking/ordering logic — the join hands its result to `internal/query.Service` unchanged; how that evidence is presented to an agent is existing query-serving scope, not repeated here.
