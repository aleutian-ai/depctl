# WATCH-011: Enforce the single-owner invariant

**Epic:** Watch Mode
**Status:** planned
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
- [ ] Only `daemon run` and `init` open persistent stores, enforced by a test.
- [ ] CLI and MCP never hold database locks independently, proven by the behavioral test.
- [ ] The old multi-process access model (per-command opens, lock retry) is gone.
- [ ] Architecture, internal docs, and README reflect the daemon model; epic 19 can move to `completed/`.

## Post-implementation note
WATCH-007, WATCH-008, WATCH-009, and WATCH-010 are all now done — `doctor` is the one remaining, explicitly intentional exception (dial-only, falls back to direct store access only when no daemon answers; see WATCH-009's note). This ticket itself — the AST invariant test enforcing that `openControlStore`/`openDataStore` are called only from the allowed set (`runDaemonRun`, `runInit`, and `doctor`'s fallback path — the allowed set has grown by one since this ticket's original design), the full behavioral all-commands-plus-serve test, deleting old `ErrLocked` retry-special-casing, and reconciling every WATCH ticket's status field — has not been started. Unblocked now in every sense but "actually written."
