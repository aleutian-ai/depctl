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

**Prerequisites are handled automatically** — you don't need to pre-start anything:
- **Ollama**: ragctl auto-pulls its embedding model (`nomic-embed-text-v2-moe`) itself, in the background, the first time it's needed — as long as Ollama itself is installed and running.
- **Qdrant**: if nothing's reachable at the configured endpoint and Podman or Docker is on PATH, ragctl starts and manages its own Qdrant container automatically (`vector.managed: true`, the default). See the [README's "Data persistence" section](../README.md#data-persistence) for what that means for your indexed data.

If either genuinely isn't available (Ollama not installed, no container runtime), `ragctl daemon status` and `ragctl doctor` say so plainly — see Troubleshooting below.

## First use on a project ragctl hasn't seen before

Inside opencode, just ask naturally — e.g. "scan this project, then tell me how X's Y method works." The agent has three relevant tools available: `scan_project` (registers the project and resolves its dependencies — always available), `sync_project` (actually clones/chunks/embeds the docs — off by default, see `server.mcp.enable_sync_tool` in `config.yaml`), and `search_dependency_docs`. No special syntax needed; the agent resolves project/dependency names on its own via `list_project_dependencies`.

You usually don't need to ask for `sync_project` explicitly: `search_dependency_docs` triggers a sync scoped to just the one dependency it's missing and retries automatically. Explicit `sync_project` is mainly for warming a whole project ahead of time.

The first real sync of a dependency-heavy project can still take a while — cloning + local-model embedding is the dominant cost, especially for a project with many/large dependencies, though a clone now only fetches the doc-shaped files a dependency actually needs (blobless, sparse-checkout), not its full history/tree. `sync_project` streams progress as MCP progress notifications, and never blocks past ~90 seconds regardless of how long the real work takes — a `still_running` result means the sync is genuinely still going in the background, not that it failed; ask again or check `list_project_dependencies`.

## Checking it's wired up

```bash
opencode mcp list          # should show "ragctl ... connected"
./build/ragctl daemon status
./build/ragctl doctor
```

## Troubleshooting

- **`opencode mcp list` shows ragctl disconnected** — check the `command` path in your `opencode.json` actually points at a real, executable `ragctl` binary on this machine.
- **`search_dependency_docs` errors or returns nothing** — run `./build/ragctl doctor`; its `embedding backend`/`vector backend` lines report live readiness state in the same wording `sync`/search themselves use.
- **A managed Qdrant container fails to start** — check `./build/ragctl daemon status`'s `vector:` line for the specific error. On a Podman-on-macOS setup, the most common cause historically was a Podman machine with no host directories mounted into its VM (`podman machine inspect` showing an empty `"Mounts"`) — no longer an issue since the managed container now uses a named volume instead of a host bind-mount, but a genuinely broken/stopped Podman machine (`podman machine start`) can still block it.
- **`sync_project`/`gc` fail with "sync_project is disabled by config"** — set `server.mcp.enable_sync_tool: true` under `server.mcp` in `config.yaml` (`~/Library/Application Support/ragctl/config.yaml` on macOS). It's a real config key, not always present in a config file written before it existed — no key means it unmarshals to `false`.
- **First sync of a dependency is slow, or `search_dependency_docs`/`sync_project` reports `still_running: true`** — see "First use" above; this is real clone + embedding cost, not a hang or a failure. The sync keeps running in the background regardless of what any one tool call returned — check back shortly (`list_project_dependencies`, or call the tool again) rather than assuming it failed. `ragctl daemon status` while it runs shows live readiness state, and a plain `ragctl sync` streams `OK`/`FAIL` per dependency as it completes.
