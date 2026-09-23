# Epic: Node / JavaScript / TypeScript Resolver

Detect Node.js projects and resolve exact dependency versions from whichever lockfile is present (npm, pnpm, Yarn, or Bun). TypeScript is not a separate ecosystem — it rides the Node package graph.

**Status: partially done.** NODE-001/002/003 (detection, npm, pnpm) are built and tested — `internal/resolver/node`, wired into `ragctl scan` alongside Go and Python. NODE-004/005 (Yarn, Bun) are not: both lockfiles are *detected* (so a Yarn/Bun project is reported, not silently skipped) but `Resolve` returns a clear, typed error naming which lockfile to add instead. The epic stays in `backlog/`, not `completed/`, until all five ship — same precedent as epic 19 in `planned/`, which stayed open with some tickets shipped and others not, per this repo's "an epic moves to completed/ only once every ticket is done" rule.

- [NODE-001](NODE-001-node-project-detection.md) — detect `package.json` + lockfile presence, pick lockfile priority.
- [NODE-002](NODE-002-npm-lock-resolver.md) — parse `package-lock.json` (lockfileVersion 3) for exact versions.
- [NODE-003](NODE-003-pnpm-resolver.md) — parse `pnpm-lock.yaml` including workspace semantics.
- [NODE-004](NODE-004-yarn-resolver.md) — parse `yarn.lock` (Classic + Berry formats).
- [NODE-005](NODE-005-bun-resolver.md) — parse `bun.lock` text format.
