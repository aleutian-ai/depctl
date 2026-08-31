# SEC-004: No downloaded code execution

**Epic:** Security hardening
**Status:** planned
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
- [ ] `executil` guard enforces registered-root-only execution.
- [ ] Invariant documented in code comments at `internal/normalize` and `internal/source/git`.
- [ ] Test covers the rejection path.
