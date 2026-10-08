# SEC-004: No downloaded code execution

**Epic:** Security hardening
**Status:** done — 2026-09-29
**Depends on:** RES-002 (command execution helper), GIT-002 (version checkout)
**Estimated size:** small

## Goal
Guarantee, structurally, that registry/knowledge acquisition never executes code from a fetched dependency repository, and that package-manager commands only ever run against explicitly registered local project roots.

## Non-goals
- No sandboxing of the package-manager subprocess itself (e.g. no seccomp/container isolation) — this ticket only enforces the invocation boundary, not process-level confinement.

## Simplicity constraints
- This is primarily an architectural invariant enforced by code review + a couple of guard checks, not a new subsystem.

## Design
Two enforcement points:

1. **Acquisition path never executes**: audit `internal/source/git` and `internal/normalize/*` to confirm none of them ever invoke `os/exec` against files inside the fetched worktree/mirror (only `git` itself is invoked, via `internal/executil`, on the cache/worktree paths — never a script *within* the fetched content). Add a code comment at the top of `internal/normalize` stating this invariant explicitly.
2. **Resolver commands scoped to registered roots**: in `internal/executil` (RES-002), add a guard that resolver command execution (`go list`, `cargo metadata`, `npm`, etc.) only accepts a working directory that matches a project root already present in the bbolt `projects` bucket — reject execution against arbitrary/unregistered paths with a typed error.

## Inputs / Outputs
- Input: a working directory and command for `internal/executil`.
- Output: command runs only if the directory is a registered project root; otherwise a typed error (`ErrUnregisteredProjectRoot`).

## Failure behavior
Attempting to run a resolver command against an unregistered path fails fast with a clear error; this should be effectively unreachable in normal operation (a defense-in-depth check) but must not be optional/bypassable via a hidden flag.

## Tests
- `executil` rejects a command whose working directory is not a known registered project.
- Static/code-review check documented (not a runtime test): grep-based CI lint could optionally assert no `os/exec` calls exist under `internal/source/` or `internal/normalize/` outside the git wrapper — nice-to-have, not required for this ticket.

## Acceptance criteria
- [x] Registered-root-only execution enforced at the resolver call boundary.
- [x] Invariant documented in code comments at `internal/normalize` and `internal/source/git`.
- [x] Test covers the rejection path.

## Post-implementation note (2026-09-29)

**Design deviation from the original sketch, made deliberately:** the guard lives in `internal/cli` (`requireRegisteredProjectRoot`, called from `scanAndResolve` in `scan.go`, right before `Resolver.Resolve`), not inside `internal/executil` itself. `executil.Run` is a shared low-level exec helper used by both resolvers (`go list`) *and* git/registry operations (`git clone --mirror`, registry liveness checks, vector-backend bootstrap) that have nothing to do with project roots at all — a blanket "working directory must be a registered project root" check inside `executil.Run` would have broken every one of those unrelated callers. The real, meaningful boundary is at the one function that decides *which* path a resolver gets pointed at, not inside the generic subprocess runner.

Audited both real call sites of `Resolver.Resolve`: `scanAndResolve` (`internal/cli/scan.go`) already calls `store.PutProject` before `Resolve` in every code path; `resolveProject` (`internal/cli/watch.go`) only ever resolves `p.Root` from a `domain.Project` already read back from the store via `store.GetProject`. The invariant was already true by construction in both places — this ticket makes it explicit and enforced (`ErrUnregisteredProjectRoot`, checked via a fresh `store.GetProject` read-back immediately before `Resolve`) rather than merely an accident of the current call order, so a future refactor that reorders or bypasses registration fails loudly instead of silently shelling out to a resolver command against an arbitrary path.

Also audited the acquisition-never-executes half of this ticket: `internal/source/git` only ever invokes the real `git` binary via `executil.Run`, against paths git itself manages (its own bare mirrors/worktrees) — confirmed via a full grep of every `executil.Run`/`exec.` call site in the package. `internal/normalize/pydoc` and `internal/normalize/tsdoc` do shell out to real `python3`/`node` interpreters, but only to run depctl's own embedded extraction script (piped over stdin) — the fetched dependency's file path is passed as a plain string *argument* for that script to statically parse (Python's `ast` module; a hand-written CommonJS/ESM text scanner for JS/TS), never imported, required, or `eval`'d as live code. Confirmed by reading `extract.py`/`extract.js` directly (static `ast.parse`, no `importlib`/`exec`/`eval`; a plain text scanner, no `require(entry)`/dynamic `import()`/`vm.runInContext`). Documented explicitly in both packages' doc comments (`internal/source/git/cache.go`, `internal/normalize/normalize.go`).

New tests (`internal/cli/no_downloaded_code_execution_test.go`): `TestRequireRegisteredProjectRootRejectsUnregisteredID`/`AllowsRegisteredID` cover the guard directly; `TestExtractionScriptsNeverDynamicallyExecuteTargetContent` is a real, lasting safety net — it scans `extract.py`/`extract.js` for dynamic-execution patterns (`importlib`, `__import__`, bare `exec`/`eval` for Python; `require('child_process')`, `vm.runInContext`, `new Function(...)`, `eval` for JS — deliberately excluding a bare `exec(` for JS, since `RegExp.prototype.exec()` is a normal, benign method used throughout `extract.js`) so a future edit reintroducing dynamic execution fails CI, not just code review. `TestNormalizerSubprocessesOnlyExecuteInterpretersNotFetchedContent` confirms the subprocess invocations themselves name a real interpreter binary, not a path into fetched content.
