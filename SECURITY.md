# Security Policy

## Reporting a vulnerability

If you believe you've found a security vulnerability in `depctl`, please report it privately rather than opening a public GitHub issue.

Email **support@aleutian.ai** with a description of the issue, steps to reproduce, and any relevant logs or proof-of-concept code. We'll acknowledge your report and work with you on a fix and disclosure timeline before any public details are shared.

## Scope

`depctl` runs locally, executes subprocesses (`git`, `go list`, and `python3`/`node` running depctl's own API-doc extraction scripts, never a dependency's code), and acquires and indexes third-party content (dependency source repositories, documentation) that gets served back to AI coding agents. Security-relevant areas include:

- subprocess execution (`internal/executil`) and any command-injection risk in how arguments are constructed;
- content acquired from external sources (`internal/source/git`, future acquisition providers) being trusted or executed inappropriately;
- prompt-injection risk in content served to agents over MCP (`internal/mcp`) — see the trust/authority model in `docs/architecture.md` and the `TrustClass` fields on retrieved content;
- local storage (`internal/control/bbolt`, `internal/data/badger`, and the `vectors.db`/`keyword.db` index files in `internal/backend/embedded` and `internal/backend/keyword`) and the daemon's Unix socket;
- credentials for external services: API keys and database passwords are read from environment variables named in the config (`vector.api_key_env`, `depctl export --api-key-env`), never stored in it, and `depctl doctor` redacts passwords in connection strings.

## Supported versions

`depctl` is pre-1.0. Only the latest release and `main` get security fixes; there are no separate support windows yet. Report against the latest release or `main`.
