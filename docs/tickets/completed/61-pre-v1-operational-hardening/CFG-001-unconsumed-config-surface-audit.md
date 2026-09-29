# CFG-001: Audit and resolve unconsumed config surface

**Epic:** Pre-v1.0 Operational Hardening
**Status:** done — 2026-09-28
**Depends on:** none
**Estimated size:** small

## Problem, confirmed by reading the real code
`internal/config/config.go`'s `ServerConfig` has two fields:
```go
type ServerConfig struct {
	MCP  MCPServerConfig  `yaml:"mcp"`
	HTTP HTTPServerConfig `yaml:"http"`
}

type HTTPServerConfig struct {
	Listen string `yaml:"listen"`
}
```
`Default()` populates `HTTP: HTTPServerConfig{Listen: "127.0.0.1:7447"}` — so every freshly-`init`ed `config.yaml` ships a `server.http.listen` value that looks like a real, working setting. A repo-wide search confirms zero consumers: nothing in `internal/cli`, `internal/daemon`, or anywhere else reads `HTTPServerConfig` or starts an HTTP server from it. `docs/internal/cli.md` already notes this in passing ("Streamable HTTP is a documented future option on the same SDK, not built"), but the config field itself carries no such warning — an operator editing `config.yaml` has no way to know `server.http.listen` currently does nothing at all.

This is the kind of gap that erodes trust quietly: someone sets a config value expecting it to take effect, it silently doesn't, and they find out only by noticing MCP still only works over stdio — or don't notice at all and assume it's working.

## Non-goals
- Not a decision to build Streamable HTTP transport now — that remains a real future option, out of scope here.
- Not a rewrite of the config system — this is a targeted audit plus a decision (wire, remove, or clearly document) for each unconsumed field found, not a redesign.
- REG-008 (epic 38/backlog) — the already-implemented-but-CLI-unreachable project registry override directory — is a related "built but not wired" gap, already tracked there. Not duplicated here, but worth resolving under the same "don't ship silently-inert surface" principle before v1.0.

## Design direction (not finalized)
1. Resolve `HTTPServerConfig` specifically: either remove it from `Default()`'s generated output until Streamable HTTP actually ships (simplest, matches "don't build the abstraction before the second implementation" house style), or keep it but add an explicit comment in both the struct and the generated `config.yaml`'s own comments (if any) stating it is not yet functional.
2. Audit every other field in `Config` for the same failure mode: grep each exported field name across the codebase outside `internal/config` itself and confirm at least one real reader exists. `Config.Version` is a known second case (noted in `docs/internal/config.md`'s own notes: "exists but isn't checked against anything") — decide the same way (wire a real check, or document as reserved/unused).
3. Whatever the audit finds, the output should be a short, explicit list (in this ticket's own post-implementation note) of every config field that is genuinely inert today, each with a stated reason (reserved for future work vs. dead and removed).

## Inputs / Outputs
- Input: the full `Config` struct tree (`internal/config/config.go`).
- Output: for every field, either a confirmed real consumer, a removal, or an explicit "not yet functional" doc comment — no silent third option.

## Failure behavior
- N/A — this is an audit-and-decide ticket, not a runtime-behavior ticket.

## Tests
- No new runtime behavior necessarily needs a test; if a field is removed, existing config-loading tests must still pass with it absent from a `config.yaml` (backward-compatible: an old config file with the now-removed field should still load, per `Load`'s existing forward-compatible YAML decoding — verify this explicitly for whichever field gets removed).

## Acceptance criteria
- [x] `HTTPServerConfig` is resolved one way or the other (removed from `Default()`, or explicitly documented as not-yet-functional in both the struct's doc comment and user-facing config docs).
- [x] Every other field in `Config` is confirmed to have a real consumer, or is explicitly documented as reserved/inert (`Config.Version` at minimum, since it's already flagged in `docs/internal/config.md`).
- [x] `docs/internal/config.md` updated to reflect the resolution for each field found inert.

## Implementation notes (2026-09-28)
Resolved by **removal**, not documentation-only — matches the project's own "don't ship the abstraction before a second implementation" convention, and `yaml.v3`'s default unmarshal behavior ignores unknown keys, so an existing `config.yaml` with a `server.http` block still loads fine (verified: no `Validate` or `Load` behavior change needed). Removed `HTTPServerConfig` entirely (`internal/config/config.go`), the `ServerConfig.HTTP` field, and its population in `Default()`; updated `TestDefaultsAreApplied` (`internal/config/config_test.go`) to drop its now-nonexistent assertions; updated `docs/internal/config.md`'s field list, its walkthrough's example `config.yaml`, and its Notes section.

Audited every other `Config` field for a real consumer via `grep -rl <FieldName> --include="*.go"` excluding `internal/config` and test files: `APIKeyEnv`, `Managed`, `MirrorSearchPaths`, `CheckoutSearchPaths`, `OrphanAge`, `MaxTotalConcurrency`, `DisableAmbient` all have real, confirmed consumers. `Config.Version` was already documented as intentionally reserved (a future config-schema-version gate, not yet needed) — left as-is, now with an explicit note that it was reviewed under this ticket rather than simply overlooked.

`go build ./...`, `go vet ./...`, and `internal/config`/`internal/cli`/`internal/daemon`/`internal/mcp` test suites all pass.
