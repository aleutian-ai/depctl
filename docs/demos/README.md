# Integration demos

One runnable demo per integration. Each was run exactly as written (2026-10-04) against real containers (Podman) with local models only: no cloud accounts, no paid API keys.

| Demo | Shows | Time |
|---|---|---|
| [Managed Qdrant](qdrant-managed.md) | The default: ragctl starts its own Qdrant and serves exact-version docs | ~3 min |
| [Your own Qdrant](qdrant-byo.md) | ragctl shares a Qdrant you already run, without touching your data | ~5 min |
| [Mem0](mem0.md) | ragctl's docs become searchable memories in a self-hosted Mem0 | ~10 min |
| [Cognee](cognee.md) | ragctl's docs become a Cognee dataset and knowledge graph | ~10 min |
| [Graphiti](graphiti.md) | ragctl's docs become episodes and entities in a Graphiti graph | ~10 min |

## Before any demo

- `ragctl` on your `PATH` (`go build -o build/ragctl ./cmd/ragctl`, then add `build/` to `PATH`).
- [Podman](https://podman.io/) with a running machine.
- [Ollama](https://ollama.com/) running locally. ragctl pulls its own embedding model on first use. The memory-system demos also need:

  ```bash
  ollama pull nomic-embed-text
  ollama pull ministral-3:3b    # any small chat model; quality varies
  ```

Every demo starts in a **sandbox**, so your real ragctl install is never touched:

```bash
source docs/demos/demo-env.sh
```

That gives you four helpers:

| Helper | Does |
|---|---|
| `demo_project v1.6.0` | creates a tiny Go project that depends on `github.com/google/uuid` at that version, and prints its directory |
| `demo_project_id <dir>` | prints ragctl's project ID for a scanned directory |
| `demo_search <project-id> "<question>"` | searches ragctl's index the way an agent's MCP `search_dependency_docs` call does |
| `demo_reset` | stops the sandbox daemon and deletes the sandbox |

Every demo ends with its own container cleanup, then `demo_reset`.

## Ports used

| Service | Port |
|---|---|
| ragctl-managed Qdrant | 6333 (your real install may already use it; the managed demo reuses it safely in its own collection) |
| "Your own" Qdrant | 16333 |
| Mem0 | 8888 |
| Cognee | 8000 |
| Graphiti | 8001 |
