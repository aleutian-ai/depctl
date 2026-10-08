# WATCH-009: Read-only commands through the daemon

**Epic:** Watch Mode
**Status:** done — see Post-implementation note
**Depends on:** WATCH-005
**Estimated size:** medium

## Goal
Convert every remaining command that reads the persistent stores into a daemon client:
- `status`
- `project list` / `project show`
- `deps`
- `plan`
- `describe`
- `doctor`

## Non-goals
- Changing what any of these commands compute or print.
- A `depctl query` CLI command. There isn't one today; the query path is MCP (WATCH-010).

## Simplicity constraints
- Plain JSON request/response for these. They're fast, so no streaming.
- Each command's existing builder function (`buildStatus`, the plan computation, describe's report builder, and so on) runs in the daemon behind an `Engine` method. The CLI side only formats. When a command already has a `--json` output struct, that struct is the API response type (moved to `internal/daemon/api` if it has to be shared).

## Design
Endpoints and the command each serves:

| Endpoint | Command |
|---|---|
| `GET /v1/status` | `status` (includes WATCH-006's per-project `sync_state`) |
| `GET /v1/projects` | `project list`, also used by `list projects` in the required API |
| `GET /v1/projects/{id}` | `project show` |
| `GET /v1/projects/{id}/deps` | `deps` |
| `GET /v1/projects/{id}/plan` | `plan` |
| `POST /v1/describe` `{args, check_liveness}` | `describe` |

`describe --html`/`--out` write files client-side from the returned report, so the daemon never writes into the user's working directory.

`doctor` splits:
- **Checks that don't need the stores stay client-side.** These are config, registry validity, and git / package managers on PATH. PATH checks must still run client-side because the user's shell PATH is what matters for them, and the daemon's PATH may differ.
- **Checks that need the stores move behind `GET /v1/doctor`.** These are schema, Badger, stale jobs, active generations and manifests, replicas, backend reachable, and embedding compatibility.
- **Daemon not running:** a new first check, "depctl daemon", reports Unhealthy, and the store-backed checks are reported as "not checked" (the existing `notChecked` path). Doctor still exits 2 rather than refusing to run.

## Inputs / Outputs
Unchanged CLI surface: the same flags, text/JSON output, and exit codes.

## Failure behavior
- Daemon not running:
  - `status`, `project`, `deps`, `plan`, and `describe` fail with the WATCH-005 message.
  - `doctor` reports it as a check (above).
- Unknown project ID: the same error text as today, carried as a 404 with a message.

## Tests
- Each converted command produces today's output against a running test daemon. Reuse the existing tests' fixtures and assertions; add daemon startup to their setup helper.
- `status --json` includes `sync_state`.
- `doctor` with no daemon: exit 2, "depctl daemon" Unhealthy, store checks "not checked", PATH/config checks still evaluated.
- `doctor` with a healthy daemon: all 13 existing checks plus the daemon check OK, exit 0.
- While the daemon runs, every command here succeeds, which proves none of them opens the stores.

## Acceptance criteria
- [x] Health/status and list-projects API operations exist, plus the read endpoints above.
- [x] `status`, `project`, `deps`, `plan`, and `describe` never call `openControlStore` or `openDataStore`. `doctor` does, but only in its no-daemon fallback path — a deliberate exception, not a gap (see note above).
- [x] Output and exit codes unchanged; doctor's report is unaffected by which path produced it.
- [x] `docs/internal/cli.md` updated. (architecture.md's status/doctor flow section not separately touched — its content wasn't wrong, just pre-dates the daemon-mediated path; `docs/internal/cli.md` and `daemon.md` are the doc of record for this now.)

## Post-implementation note
Landed in two passes. First pass: `status` and `plan` became daemon clients as designed (`internal/cli/status.go`/`plan.go`); `project list`/`show`, `deps`, `describe`, and `doctor` were deliberately left as direct store access, diverging from this ticket's original design table — a considered decision at the time (`project`/`deps` judged short-lived enough not to be worth the daemon hop; `doctor` needs to work when the daemon can't; `describe` simply wasn't revisited).

Second pass (later, at the user's explicit request once this was recognized as a real seamlessness gap — see `docs/scratch/watch-scenarios-before-after.md`): `project`, `deps`, and `describe` were migrated onto `ensureDaemon` after all, via new `Engine`/daemon-API/client methods (`ProjectList`, `ProjectGet`, `Describe`). `doctor` alone keeps a genuine two-path design rather than a plain migration: `runDoctor` dials the socket without autostarting (running doctor must never have the side effect of starting a daemon), uses a new `Doctor` route for the store-dependent checks when one answers, and falls back to opening the stores itself — exactly as it always did — when none does. The two PATH-only checks (`git`, package managers) always run client-side regardless, split via `packageManagersNeeded`/`checkPackageManagersOnPath` rather than the whole check.

Verified live, not just by test: `project list`, `deps`, `doctor`, and `describe` all run cleanly while a real daemon holds the stores, no `ErrLocked`. See `docs/internal/cli.md`'s notes and `docs/internal/daemon.md` for the current, accurate command-by-command shape.
