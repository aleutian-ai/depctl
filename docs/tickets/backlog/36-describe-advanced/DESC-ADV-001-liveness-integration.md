# DESC-ADV-001: Liveness integration

**Epic:** Describe — Advanced
**Status:** planned
**Depends on:** DESC-001, REG-007
**Estimated size:** small

## Goal
`ragctl describe --check-liveness` annotates each `SourceEntry` with REG-007's reachability result, so "declared but dead" sources are visible without a separate `doctor` run.

## Design
Add `Liveness *registry.LivenessResult` (nil unless `--check-liveness`) to `SourceEntry`; text/HTML renderers show a reachable/unreachable marker per source when present. Opt-in only — same network-call discipline as REG-007 itself.

## Acceptance criteria
- [ ] `--check-liveness` flag runs REG-007 per declared source and annotates output.
- [ ] Omitted by default; zero network calls without the flag.
