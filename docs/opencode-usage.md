# Using ragctl with opencode

How to register `ragctl` as an MCP server in [opencode](https://opencode.ai), so it can search version-correct dependency documentation while you work.

## 1. Build ragctl

```bash
go build -o build/ragctl ./cmd/ragctl
```

## 2. Register it as an MCP server

opencode's config lives at `~/.config/opencode/opencode.json` (global) or `opencode.json` in a project directory (project-local). Add a `mcp` entry — note opencode's shape is `command` as a single array (binary + args combined), not the `command`/`args` split some other MCP clients use:

```json
{
  "mcp": {
    "ragctl": {
      "type": "local",
      "command": ["/absolute/path/to/ragctl/build/ragctl", "serve"],
      "enabled": true
    }
  }
}
```

Use the real absolute path to the binary you built in step 1 — this must point at *this machine's* build, not a path copied from another machine.

## 3. Launch opencode

```bash
opencode
```

That's it — opencode spawns `ragctl serve` itself via the config above; you never run that manually. `ragctl serve` is a thin stdio proxy (ADR-011) — it holds no state itself and auto-starts the actual `ragctl` daemon in the background on first use if one isn't already running (no separate "start the daemon" step, no terminal needed for it).

**What you need running** — less than you might expect:
- **No vector database or container.** A fresh `ragctl init` uses the embedded vector store (`vector.backend: embedded`): one file, `vectors.db`, in ragctl's data directory. Qdrant, pgvector and Weaviate are opt-in (see the [README's "Vector stores" section](../README.md#vector-stores)); if you choose ragctl-managed Qdrant (`ragctl init --vector-backend qdrant`), ragctl starts its container itself with Podman or Docker.
- **Ollama is optional.** It powers semantic (vector) search. A fresh install uses `retrieval.mode: auto`: if Ollama is running, ragctl auto-pulls its embedding model (`nomic-embed-text-v2-moe`) in the background and searches by meaning; if not, it falls back to keyword search (BM25 over a local index) and adds vectors on a later sync once Ollama is up. `retrieval.mode: keyword` (`ragctl init --retrieval-mode keyword`) never needs a model at all. See the [README's "Search modes" section](../README.md#search-modes).

`ragctl daemon status` and `ragctl doctor` report what's in use. "not used" or "using keyword search for now" next to the embedding backend is a normal state, not an error.

## Choosing a chat model for opencode

The agent's own chat model is separate from ragctl's embedding model, and a small local one works fine: `ornith-1.5:9b` was tested end to end — opencode called ragctl's tools and answered correctly in about a minute, using about 6.7 GB of memory. A 35B model needs roughly 22 GB.

## Passing environment variables

An MCP server entry can set an `environment` map, which opencode passes to the `ragctl serve` process — for example the Qdrant/Weaviate API key or Postgres password that `vector.api_key_env` names:

```json
"ragctl": {
  "type": "local",
  "command": ["/absolute/path/to/ragctl/build/ragctl", "serve"],
  "environment": { "QDRANT_API_KEY": "..." },
  "enabled": true
}
```

The variable is read by ragctl's daemon, not by `ragctl serve`, and the daemon inherits the environment of whichever command auto-started it. So it only takes effect if this `ragctl serve` starts the daemon; if one is already running, run `ragctl daemon stop` first (or set the variable in the shell that starts it).

## Running opencode from a script

`opencode run` waits for stdin to close when stdin isn't a terminal, so in a script or CI job it can appear to hang. Redirect stdin:

```bash
opencode run "How does X's Y method work in the version this project uses?" < /dev/null
```

## First use on a project ragctl hasn't seen before

Inside opencode, just ask naturally — e.g. "scan this project, then tell me how X's Y method works." The agent has three relevant tools available: `scan_project` (registers the project and resolves its dependencies — always available), `sync_project` (actually clones, chunks and indexes the docs — on by default; set `server.mcp.enable_sync_tool: false` in `config.yaml` for a read-only session), and `search_dependency_docs`. No special syntax needed; the agent resolves project/dependency names on its own via `list_project_dependencies`.

You usually don't need to ask for `sync_project` explicitly: `search_dependency_docs` triggers a sync scoped to just the one dependency it's missing and retries automatically. Explicit `sync_project` is mainly for warming a whole project ahead of time.

The first real sync of a dependency-heavy project can still take a while — cloning plus local-model embedding (when Ollama is in use) is the dominant cost, especially for a project with many/large dependencies, though a clone now only fetches the doc-shaped files a dependency actually needs (blobless, sparse-checkout), not its full history/tree. `sync_project` streams progress as MCP progress notifications, and never blocks past ~90 seconds regardless of how long the real work takes — a `still_running` result means the sync is genuinely still going in the background, not that it failed; call `sync_progress`, ask again, or check `list_project_dependencies`.

## Checking it's wired up

```bash
opencode mcp list          # should show "ragctl ... connected"
./build/ragctl daemon status
./build/ragctl doctor
```

## Troubleshooting

- **`opencode mcp list` shows ragctl disconnected** — check the `command` path in your `opencode.json` actually points at a real, executable `ragctl` binary on this machine.
- **`search_dependency_docs` errors or returns nothing** — run `./build/ragctl doctor`; its `embedding backend`/`vector backend` lines report live readiness state in the same wording `sync`/search themselves use.
- **A managed Qdrant container fails to start** (only if you opted into `vector.backend: qdrant` with `vector.managed: true`) — check `./build/ragctl daemon status`'s `vector:` line for the specific error. On a Podman-on-macOS setup, the most common cause historically was a Podman machine with no host directories mounted into its VM (`podman machine inspect` showing an empty `"Mounts"`) — no longer an issue since the managed container now uses a named volume instead of a host bind-mount, but a genuinely broken/stopped Podman machine (`podman machine start`) can still block it.
- **`sync_project` fails with "sync_project is disabled by config"** — set `server.mcp.enable_sync_tool: true` under `server.mcp` in `config.yaml` (`~/Library/Application Support/ragctl/config.yaml` on macOS). Fresh installs set it to `true`, but a config file written before the key existed may lack it, and no key means `false`.
- **First sync of a dependency is slow, or `search_dependency_docs`/`sync_project` reports `still_running: true`** — see "First use" above; this is real clone and indexing cost, not a hang or a failure. The sync keeps running in the background regardless of what any one tool call returned — check back shortly (`list_project_dependencies`, or call the tool again) rather than assuming it failed. `ragctl daemon status` while it runs shows live readiness state, and a plain `ragctl sync` streams `OK`/`FAIL` per dependency as it completes.
