# Scratch notes

Working notes from design discussions. They're kept as a record of why things are the way they are, not as current documentation: each describes the system as it was when written, and most open with a **Status** line saying whether the gap they discuss has since been closed. For how depctl works now, read [../architecture.md](../architecture.md), [../features/](../features/README.md) and [../internal/](../internal/README.md).

| Note | What it's about | Status |
|---|---|---|
| [action-controller-proposal.md](action-controller-proposal.md) | Generalizing the sync scheduler into one action controller for sync, GC and scan | Implemented |
| [daemon-mcp-topology.md](daemon-mcp-topology.md) | `depctl serve` holding its own stores, before the single-owner daemon (ADR-011) | Resolved |
| [watch-scenarios-before-after.md](watch-scenarios-before-after.md) | What watching and the daemon could and couldn't do before and after that change | Resolved |
| [mcp-bootstrapping.md](mcp-bootstrapping.md) | An MCP client starting depctl on a machine where it was never initialized | Fixed |
| [embedding-provider-lock-in.md](embedding-provider-lock-in.md) | Only Ollama is supported for embeddings | Real gap, deliberately deferred. Keyword search (`retrieval.mode: keyword`) now gives a way to run with no embedding provider at all. |
| [depctl_architecture_eval_next_steps-2.md](depctl_architecture_eval_next_steps-2.md) | A broad architecture review and evaluation plan from September 2026 that seeded several later epics | Historical; much of it has since shipped (see [../tickets/completed/](../tickets/completed/README.md)) |
