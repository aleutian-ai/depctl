# Epic: Configuration and CLI Skeleton

Give the project a real command-line entry point and a validated configuration model before any feature logic exists. `depctl init` must be safe to run repeatedly, and every planned top-level command must exist (even as an explicit "not implemented" stub) so the CLI surface is stable from day one.

## Tickets
- [CLI-001 — Configuration model](CLI-001-configuration-model.md): `~/.config/depctl/config.yaml` schema, defaults, `depctl config validate`.
- [CLI-002 — `depctl init`](CLI-002-depctl-init.md): idempotently creates config dir, control DB, Badger dir, Git/registry cache dirs.
- [CLI-003 — CLI command skeleton](CLI-003-cli-command-skeleton.md): Cobra registration for every top-level command, with clear "not implemented" errors for stubs.
