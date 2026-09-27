# Epic: Node / JavaScript / TypeScript Resolver

Detect Node.js projects and resolve exact dependency versions from whichever lockfile is present (npm, pnpm, Yarn, or Bun). TypeScript is not a separate ecosystem — it rides the Node package graph.

**Status: done** (2026-09) — all five tickets shipped. NODE-001/002/003 (detection, npm, pnpm) were already built and tested. NODE-004/005 (Yarn, Bun) closed the epic: both lockfiles now resolve to real, exact dependency versions — verified not just against hand-written fixtures but against real lockfiles generated live by real Yarn 1.22.22/4.5.0 (via corepack) and real Bun 1.4.2 installs, run through the full `Resolver.Resolve()` pipeline end to end.

- [NODE-001](NODE-001-node-project-detection.md) — detect `package.json` + lockfile presence, pick lockfile priority. *(done)*
- [NODE-002](NODE-002-npm-lock-resolver.md) — parse `package-lock.json` (lockfileVersion 3) for exact versions. *(done)*
- [NODE-003](NODE-003-pnpm-resolver.md) — parse `pnpm-lock.yaml` including workspace semantics. *(done)*
- [NODE-004](NODE-004-yarn-resolver.md) — parse `yarn.lock` (Classic + Berry formats). *(done)*
- [NODE-005](NODE-005-bun-resolver.md) — parse `bun.lock` text format. *(done)*
