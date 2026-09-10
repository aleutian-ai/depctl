# Security Policy

## Reporting a vulnerability

If you believe you've found a security vulnerability in `ragctl`, please report it privately rather than opening a public GitHub issue.

Email **support@aleutian.ai** with a description of the issue, steps to reproduce, and any relevant logs or proof-of-concept code. We'll acknowledge your report and work with you on a fix and disclosure timeline before any public details are shared.

## Scope

`ragctl` runs locally, executes subprocesses (`git`, ecosystem package-manager tooling), and acquires and indexes third-party content (dependency source repositories, documentation) that gets served back to AI coding agents. Security-relevant areas include:

- subprocess execution (`internal/executil`) and any command-injection risk in how arguments are constructed;
- content acquired from external sources (`internal/source/git`, future acquisition providers) being trusted or executed inappropriately;
- prompt-injection risk in content served to agents over MCP (`internal/mcp`) — see the trust/authority model in `docs/architecture.md` and the `TrustClass` fields on retrieved content;
- local storage (`internal/control/bbolt`, `internal/data/badger`) and any credential/config handling.

## Supported versions

`ragctl` is pre-v0.1 and does not yet have tagged releases with independent support windows — report against the `main` branch.
