# CORPUS-001: `depctl corpus add`

**Epic:** Local Corpus
**Status:** planned
**Depends on:** REG-002 (registry loader), resolver/node (existing, static package-lock.json parsing)
**Estimated size:** small

## Goal
Automate the manual workaround for indexing a local git repo that isn't a real ecosystem dependency: `depctl corpus add <local-git-path> --name <id>` should do everything a human currently does by hand (maintain a synthetic `package.json`/`package-lock.json`, write a registry manifest pointing at the local path) in one command.

## Non-goals
- No new domain concept — this generates exactly the same synthetic node-project + registry-manifest shape a human would write, nothing new for `scan`/`resolve`/`sync` to understand.
- No support for re-scoping an existing real project's dependencies — this only manages one dedicated synthetic "corpus" project depctl owns entirely.

## Simplicity constraints
- One fixed synthetic project directory (default `<data-dir>/corpus/`, override with `--project-dir`), one `package.json`/`package-lock.json` this command owns and edits — never touches a project the user scanned themselves.
- Version bumping (the existing "how you force a resync" mechanism) is exposed directly: `depctl corpus add` on an already-added path with `--resync` bumps its version; without the flag, adding an already-present name is a no-op with a clear message, not a silent double-add.

## Design
Package: `internal/cli` (new `corpus.go`, following the existing command-per-file convention).

```text
depctl corpus add <local-git-path> --name <id> [--ref <branch-or-HEAD>] [--resync]
depctl corpus list
depctl corpus remove <id>
```

`corpus add`:
1. Validate `<local-git-path>` is a real git repo (`git rev-parse --is-inside-work-tree`).
2. Write/update the synthetic project's `package.json`/`package-lock.json`: add `{"depctl-corpus-<id>": "<version>"}` (version starts at `1.0.0`, or bumped on `--resync`).
3. Write `<data-dir>/registry/depctl-corpus-<id>.yaml` — a manifest identical in shape to what was hand-authored this session (`match.packages: [depctl-corpus-<id>]`, one `git` source at the local path, `ref: HEAD` unless `--ref` given, `version.strategy: none`).
4. Run `depctl scan` against the synthetic project directory (reusing the existing scanner, not reimplementing resolution).

`corpus list`/`corpus remove` read/rewrite the same two files — no new storage.

## Inputs / Outputs
- Input: a local filesystem path to a git repo, a chosen name.
- Output: the synthetic project scanned and ready for `depctl sync`; unchanged from what a human produces manually today.

## Failure behavior
- Path is not a git repo: clear error, nothing written.
- `--name` collides with an existing entry without `--resync`: no-op with a message pointing at `--resync`, never silently overwrites.

## Tests
- `corpus add` on a fresh repo produces a scannable project with exactly one new dependency.
- `corpus add --resync` on an existing name bumps its version and nothing else's.
- `corpus remove` cleanly drops one entry from both `package.json` and the lockfile, leaving others untouched.
- `corpus add` on a non-git directory fails clearly, writes nothing.

## Acceptance criteria
- [x] `depctl corpus add/list/remove` implemented.
- [x] Adding a repo produces the same effective shape a human would hand-write (verified against this session's own manually-created `depctl-core-*` manifests).
- [x] Re-adding without `--resync` is a safe no-op.

## Post-implementation note
Verified for real against `~/offline-knowledge/go/gorm`: `corpus add` scanned and registered it, a second `add` without `--resync` correctly no-op'd, `--resync` bumped the version and re-synced, `corpus remove` cleanly dropped the dependency, lockfile entry, and registry manifest (confirmed via `corpus list` showing 0 entries and the manifest file gone). One real bug caught in the process: the git-repo validation only checked `executil.Run`'s Go-level `error` return, never `RunResult.ExitCode` — so `git rev-parse --is-inside-work-tree` failing (non-git directory) was silently treated as success, exactly the same class of mistake this codebase's own `acquireGitSources` code was written to avoid. Fixed to check both.

