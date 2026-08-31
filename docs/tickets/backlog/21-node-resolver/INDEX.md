# Epic: Node / JavaScript / TypeScript Resolver

Detect Node.js projects and resolve exact dependency versions from whichever lockfile is present (npm, pnpm, Yarn, or Bun). TypeScript is not a separate ecosystem — it rides the Node package graph. Build npm and pnpm first (most common); Yarn and Bun can follow. Start only after the Go vertical slice (resolver framework + Go resolver) is stable.

- [NODE-001](NODE-001-node-project-detection.md) — detect `package.json` + lockfile presence, pick lockfile priority.
- [NODE-002](NODE-002-npm-lock-resolver.md) — parse `package-lock.json` (lockfileVersion 3) for exact versions.
- [NODE-003](NODE-003-pnpm-resolver.md) — parse `pnpm-lock.yaml` including workspace semantics.
- [NODE-004](NODE-004-yarn-resolver.md) — parse `yarn.lock` (Classic + Berry formats).
- [NODE-005](NODE-005-bun-resolver.md) — parse `bun.lock` text format.
