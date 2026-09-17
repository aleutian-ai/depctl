# WATCH-011: Enforce the single-owner invariant

**Epic:** Watch Mode
**Status:** partial — see Post-implementation note
**Depends on:** WATCH-007, WATCH-008, WATCH-009, WATCH-010
**Estimated size:** small

## Goal
Close out the migration. Remove every remaining direct store access outside the daemon, add a test that fails if one comes back, and bring the docs in line with the new process model.

> While the daemon is running, it is the only process allowed to own or mutate ragctl persistent stores.

## Non-goals
- New features. This is cleanup and enforcement.
- launchd/systemd installers or auto-start (a later, separate ticket, per ADR-011).

## Simplicity constraints
- Enforcement is one test, not a runtime mechanism beyond the bbolt lock that already exists.

## Design
- **Allowed store openers after this ticket:** `openControlStore()` / `openDataStore()` (`internal/cli/store.go`) may be called only by:
  - `ragctl daemon run`;
  - `ragctl init`, which first checks that no daemon answers on the socket and refuses with "stop the daemon before running init" if one does.
- **Audit:** everything else found by `grep -rn "openControlStore\|openDataStore\|bboltstore.Open\|badgerstore.Open" internal` must already be converted by WATCH-007..010. Anything missed is converted here.
- **Commands with no store access:** `corpus` and `registry` touch the stores only through `scan`/`sync`, which are now daemon calls; confirm this.
- **Invariant test** (`internal/cli/invariant_test.go`): parse `internal/cli`'s non-test Go files with `go/parser` and fail if `openControlStore` or `openDataStore` is called from any function other than the allowed ones (`runDaemon`, `runInit`, and their direct helpers).
- **Behavioral test:** start a daemon, then run every store-touching command in sequence (`scan`, `sync`, `plan`, `status`, `project list`, `deps`, `describe`, `gc --dry-run`, `doctor`) plus a `serve` session making one tool call. All succeed. Then stop the daemon and confirm `bboltstore.Open` succeeds immediately, so nothing leaked a handle.
- **Delete what's left of the old model:**
  - the lock-retry code;
  - any `ErrLocked` special-casing in commands. `ErrLocked` itself stays: the daemon uses it to detect a second instance.
- **Docs:**
  - `docs/architecture.md`: new "daemon" section with a process diagram (CLI / `serve` → socket → daemon → stores) and updated per-command flows; "Next up" updated.
  - `docs/internal/cli.md`: command list, and which commands are daemon clients.
  - `docs/internal/control.md`: the lock note now says the daemon is the only long-lived holder.
  - `docs/internal/daemon.md`: complete (lifecycle, API routes, scheduler, watch).
  - README quick-start: `ragctl daemon run` becomes a step before `scan`/`sync`/agent use.
  - Agent MCP config examples: unchanged (`ragctl serve`), plus a note that the daemon must be running.

## Inputs / Outputs
N/A (enforcement and docs).

## Failure behavior
- `init` with the daemon running refuses with a message instead of hitting `ErrLocked`.

## Tests
- The invariant AST test described above.
- The behavioral all-commands-with-daemon test described above.
- `init` with a running daemon is refused.
- Full suite natively and via `hack/test-linux.sh`. The Linux container has no launchd/systemd concerns; Unix sockets work there as-is.

## Acceptance criteria
- [x] Only `daemon run`, `init`, and `doctor`'s deliberate fallback open persistent stores, enforced by a test (`TestOnlyAllowedFunctionsOpenStoresDirectly`) — one more exception than originally scoped, since `doctor` needed a real fallback rather than a full migration (see WATCH-009's note).
- [ ] CLI and MCP never hold database locks independently, proven by the behavioral test. (Believed true — verified live for `project`/`deps`/`doctor`/`describe`/`serve` individually — but no single behavioral test exercises all of them plus `serve` together yet.)
- [ ] The old multi-process access model (per-command opens, lock retry) is gone. (Not audited for leftover `ErrLocked`-retry code specifically.)
- [x] Internal docs (`docs/internal/cli.md`, `daemon.md`, `control.md`) reflect the daemon model. Architecture.md and README not separately touched; epic 19 stays in `planned/` until the remaining items above are done, per CLAUDE.md's "every ticket done" rule for moving to `completed/`.

## Post-implementation note
WATCH-007, WATCH-008, WATCH-009, and WATCH-010 are all now done — `doctor` is the one remaining, explicitly intentional exception (dial-only, falls back to direct store access only when no daemon answers; see WATCH-009's note).

This ticket itself, partial:
- **Done:** the AST invariant test (`internal/cli/invariant_test.go`, `TestOnlyAllowedFunctionsOpenStoresDirectly`) — parses every non-test file in `internal/cli`, fails if any function outside a closed six-name allow-list (`openControlStore`, `openDataStore`, `openControlStoreForDaemonRun`, `runInit`, `runDaemonRun`, `runDoctorDirect` — one more than this ticket's original design anticipated, since `doctor` ended up with a real fallback path rather than being fully migrated) calls a direct-open function. Verified to actually catch a violation, not just pass vacuously, by temporarily introducing one during development and confirming the test failed with a specific, actionable message.
- **Not done:** the full behavioral all-commands-plus-serve test, deleting old `ErrLocked` retry-special-casing (need to audit whether any remains — most of it was already gone by the time this ticket was picked back up), and reconciling every other WATCH ticket's own status field (WATCH-004 through WATCH-010 already got this treatment individually; WATCH-002/003 predate this scheme).

Two related hardening items landed alongside this, not originally scoped to WATCH-011 but discovered while assessing whether the daemon architecture as a whole was actually defensible: every `daemon/client.Client` method now wraps its call in a timeout (`defaultRequestTimeout`/`describeRequestTimeout`/`longRunningRequestTimeout` — `c.http` itself set none before, the same gap already found and fixed in the qdrant client), and `config.Config.Fingerprint()` plus `api.Health.ConfigFingerprint` make the daemon's frozen-at-startup config drift detectable and surfaced (`ensureDaemon`'s stderr warning, `daemon status`'s `config:` line, and a new `doctor` check) rather than silent.
