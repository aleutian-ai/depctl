# CLIENT-004: Automatic MCP client registration

**Epic:** Client integration examples
**Status:** planned
**Depends on:** CLIENT-003 (documents the shape this automates), CLI-002 (`ragctl init`)
**Estimated size:** small

## Goal
CLIENT-003 documents how a human hand-edits a client's MCP config to add `ragctl serve`. This ticket closes the gap a 2026-09 competitive review flagged as part of a "one-command experience" milestone: let `ragctl init` optionally write that registration itself for one or more known clients (e.g. Claude Code's config, opencode's config), so someone unfamiliar with ragctl gets a successful first query using only the README — no manual JSON editing required.

## Non-goals
- Not every MCP client — start with whichever 1-2 clients are already in real use here (per this repo's own testing history), not an exhaustive registry of vendor configs.
- Not a background daemon that watches for new client installs — this only runs when explicitly requested during `ragctl init` (or a dedicated `ragctl mcp register <client>` subcommand), never silently.
- No modification of a client config ragctl didn't create without confirmation — if an `ragctl` MCP entry already exists, treat it as idempotent-update, not blind overwrite; if a *different* server entry already occupies the target key, fail loudly rather than clobber it.

## Simplicity constraints
- Reuses the exact config shape CLIENT-003 already documents as the source of truth — this ticket doesn't invent a second description of the wire format, it writes the one CLIENT-003 describes.
- One small `internal/cli` addition (a registration writer per supported client's known config file path/shape), not a plugin system.

## Design
`ragctl init --mcp-client=<name>` (or a prompt during interactive `init`) locates the named client's config file at its documented path, merges in (or updates) a `ragctl serve` stdio entry, and reports exactly what it wrote and where — never silent.

## Inputs / Outputs
- Input: a client name from a small supported list.
- Output: that client's config file gains (or has updated) a `ragctl` MCP server entry pointing at the installed `ragctl` binary.

## Failure behavior
- Unknown client name → clear error listing supported clients, no file touched.
- Config file missing/unwritable → clear error, falls back to printing the snippet CLIENT-003 already documents for manual use.
- Existing non-ragctl entry at the same key → fails loudly rather than overwriting, with a message telling the user to edit manually.

## Tests
- Registration against a fixture config file for each supported client: idempotent on a second run, doesn't clobber unrelated entries.
- Unknown client name produces the documented error, no file write attempted.

## Acceptance criteria
- [ ] At least one real MCP client can be registered automatically via `ragctl init` or an equivalent subcommand.
- [ ] Re-running registration is idempotent.
- [ ] An existing unrelated config entry at the same key is never silently overwritten.
