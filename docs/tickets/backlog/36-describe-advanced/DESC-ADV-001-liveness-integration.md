# DESC-ADV-001: Liveness integration

**Epic:** Describe — Advanced
**Status:** planned
**Depends on:** DESC-001, REG-007
**Estimated size:** small

## Goal
`depctl describe --check-liveness` annotates each `SourceEntry` with REG-007's reachability result, so "declared but dead" sources are visible without a separate `doctor` run.

## Design
Add `Liveness *registry.LivenessResult` (nil unless `--check-liveness`) to `SourceEntry`; text/HTML renderers show a reachable/unreachable marker per source when present. Opt-in only — same network-call discipline as REG-007 itself.

## Acceptance criteria
- [x] `--check-liveness` flag runs REG-007 per declared source and annotates output.
- [x] Omitted by default; zero network calls without the flag.

## Post-implementation note
Verified against the real `badger` manifest: the `git` source correctly reports reachable; the `godoc` source (no URL of its own, `CheckLiveness` has no check implemented for that type) reports unreachable with an explanatory error rather than silently skipping — accurate, if a little alarmist-looking in text output for a type that was never expected to be checkable.

