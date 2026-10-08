# Epic: Optional containerized embedding backend

`depctl` currently requires Ollama installed natively on the host (`brew install ollama` or equivalent) — WATCH-014 makes depctl auto-pull the *model* once Ollama is reachable, but never installs the Ollama *runtime* itself, which is deliberately out of scope (see WATCH-014's design notes). For a user with no Ollama at all, or an organization that wants one known, pinned embedding backend it can mirror internally, a self-contained containerized option is worth offering as a second, opt-in installation path — not a replacement for the native one.

## The shape

```text
Host
────────────────────────────
depctl daemon
├── watches go.mod / lockfiles, sees the real repo
├── uses the user's real git/toolchain (host-sensitive by design — see the Non-goals below)
├── MCP, bbolt/Badger
└── calls an embedding endpoint
             │
             ▼
Optional container
────────────────────────────
Ollama + the embedding model
```

`depctl backend up` would do roughly what `podman run ...` already does by hand — stand up a containerized Ollama with the configured model pre-pulled (or pulled on first `up`), exposing it at the same `http://127.0.0.1:11434` endpoint config already expects. The daemon itself stays a native process throughout; only the embedding backend moves into a container.

## Tickets

(Not yet broken into individual tickets — this epic is a placeholder for the idea, to be scoped properly if/when picked up. A rough first cut: a `depctl backend up`/`down`/`status` command set, a pinned container image reference, and config wiring so `embedding.endpoint` just points at it.)

## Non-goals

- **Never containerize the daemon itself.** The daemon is unusually host-sensitive by design: it needs real project roots, watches dependency manifests across ecosystems, invokes the host's own `go`/Python/Node tooling and `git` (including real credentials), and relies on native filesystem-notification semantics. Putting that inside a container trades "install Ollama" for "here's the 14-line container invocation (bind mounts for the workspace, `.gitconfig`, `.ssh`, package caches, UID mapping, corporate certs...) required to make dependency discovery work at all" — a worse experience, not a better one. This point isn't a placeholder for future reconsideration; it's a load-bearing constraint on depctl's whole native-first execution model (ADR-010).
- **Not a replacement for the native Ollama path.** `depctl backend up` is a second option for someone who wants one, not a requirement — the plain "install Ollama, depctl auto-pulls the model" story (WATCH-014) stays the default and the one the README leads with.
- **Not a general container-orchestration feature.** One backend, one command, matching this project's existing "no distributed scheduler, no general workflow engine" discipline (see `docs/tickets/completed/19-watch-mode/INDEX.md`'s non-goals for the same kind of scope discipline applied elsewhere).
