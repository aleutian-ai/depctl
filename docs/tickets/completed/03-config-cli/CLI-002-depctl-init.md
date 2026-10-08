# CLI-002: `depctl init`

**Epic:** Configuration and CLI Skeleton
**Status:** done
**Depends on:** CLI-001, STORE-004
**Estimated size:** small

## Goal
Implement `depctl init`, which creates all local directories/files needed for depctl to operate, and is safe to run repeatedly.

## Non-goals
- No project scanning (that's `depctl scan`, PROJ-003).
- No interactive prompts/wizard — v0.1 `init` is non-interactive with sane defaults.

## Simplicity constraints
- `init` is a straight sequence of `os.MkdirAll` + "write config if absent" + "open and close each store once to create files" calls. No plugin hooks, no template system.

## Design
Command: `cmd/depctl` (Cobra), package `internal/app` or `internal/cli`.

Creates, if missing:
```
~/.config/depctl/config.yaml        (CLI-001 defaults, only if absent)
~/.local/share/depctl/control.db    (bbolt, via STORE-001 Open, then Close)
~/.local/share/depctl/badger/       (Badger dir, via STORE-003 Open, then Close)
~/.local/share/depctl/git/          (empty dir, for GIT-001 later)
~/.local/share/depctl/registry/     (empty dir, for REG-002 user registry cache)
```
Respect `$XDG_CONFIG_HOME` / `$XDG_DATA_HOME` if set; otherwise fall back to the documented default paths above.

Idempotency: if `config.yaml` already exists, do not overwrite it. If the DB files already exist, opening them via `Open`/`Close` is a harmless no-op (STORE-001/STORE-003 already handle "open existing file"). Print a summary distinguishing "created" vs "already present" per item.

## Inputs / Outputs
- Input: none (uses default/XDG paths); optionally `--config <path>` to override.
- Output: the directory/file tree above; stdout summary of what was created vs already existed.

## Failure behavior
- Permission errors on `MkdirAll` surface immediately with the offending path in the error message; `init` does not partially succeed silently — if any step fails, report which step and leave prior successful steps in place (they're idempotent, so a retry is safe).

## Tests
- Run `init` on an empty temp `$HOME`/XDG env, verify all paths created.
- Run `init` twice in a row: second run succeeds, does not destroy or reset the config file or DB contents (write a marker record before the second `init`, verify it survives).

## Acceptance criteria
- [x] `depctl init && depctl init` succeeds twice without destroying data.
- [x] All required directories/files exist after a single `init` on a clean environment.
- [x] Config file is created only if absent; never overwritten by a repeat `init`.
