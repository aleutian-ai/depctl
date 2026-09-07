# Using opencode + ragctl + ornith (offline)

Quick reference for the setup built tonight: `opencode` (CLI agent) driving
`ornith-1.5:35b-64k` (local Ollama model, 64K context) with real MCP access
to a 20-repo knowledge corpus served by `ragctl`.

## Every time you start a session

```bash
# 1. Qdrant must be running (ragctl's vector backend)
podman start ragctl-qdrant 2>/dev/null || podman ps | grep ragctl-qdrant

# 2. Ollama must be running with the model available
ollama list | grep ornith-1.5:35b-64k

# 3. Launch opencode from anywhere — config is global (~/.config/opencode/opencode.json)
opencode
```

That's it — `opencode` spawns `ragctl serve` itself via the MCP config, you
don't start that manually.

## What's actually in the corpus

20 repos, synced and embedded (`~/ragctl-core-consumer/package.json` lists
them as fake npm dependencies — that's the plumbing, not something you
interact with). Ask about any of:

`bbolt`, `badger`, `qdrant` + `qdrant/go-client`, `modelcontextprotocol/go-sdk`
+ `modelcontextprotocol/servers`, `mark3labs/mcp-go`, `cobra`, `viper`,
`blake3`, `ulid`, `golang/tools`, `golang/mod`, `go-git`, `astral-sh/uv`,
`pypa/packaging`, `npm/cli`, `anomalyco/opencode`, `bleve`, `ollama`.

Inside opencode, just ask naturally — e.g. "how does bbolt's transaction
API work" — it'll call `ragctl`'s `search_dependency_docs` tool itself.
No special syntax needed.

## Checking it's wired up

```bash
opencode mcp list          # should show "ragctl ... connected"
opencode models            # should show ollama/ornith-1.5:35b-64k
```

## Adding more docs later (once you're back on wifi, or from a local git repo)

See `docs/offline-quickstart.md` for the full mechanism. Short version:
edit `~/ragctl-core-consumer/package.json` + `package-lock.json`, bump a
dependency's version string (or add a new one), write/update its manifest
in `~/Library/Application Support/ragctl/registry/`, then:

```bash
cd /Users/jin/GolandProjects/ragctl
./build/ragctl scan ~/ragctl-core-consumer
./build/ragctl sync
```

## Troubleshooting

- **`opencode mcp list` shows ragctl disconnected** — check
  `podman ps | grep ragctl-qdrant`; `ragctl serve` needs Qdrant reachable
  at `127.0.0.1:6333` (search calls will error, but the MCP connection
  itself doesn't strictly require Qdrant to be up).
- **No search results for a query** — try without `dependency` filter
  narrowed too tightly, or double check the exact name from the list
  above (e.g. `qdrant/go-client`'s ragctl name is `ragctl-core-go-client`,
  not `go-client` — but you shouldn't need to know this; the agent
  resolves it via `list_project_dependencies`).
- **`opencode` can't find the model** — `ollama list` to confirm
  `ornith-1.5:35b-64k` exists; if not, see the Modelfile setup in this
  session's history (`FROM ornith-1.5:35b` + `PARAMETER num_ctx 65536`).
- **Everything's slow / laptop under load** — normal; a 35B local model
  plus Qdrant plus embedding-on-sync all compete for the same machine.
  Sync is idle-cost-free once done — only live queries cost anything at
  flight time.
