# GO-004: Persist Go resolution

**Epic:** Go Resolver
**Status:** planned
**Depends on:** GO-003, STORE-001
**Estimated size:** small

## Goal
Compute a resolution fingerprint for a Go project's dependency set and persist the project → dependency-version references plus resolution metadata via the bbolt store (STORE-001).

## Non-goals
- Does not decide what to do about *changes* between resolutions — that's the planner (PLAN-001).
- Does not acquire any knowledge content.

## Simplicity constraints
- Fingerprint is a single deterministic hash over the sorted normalized dependency list — do not build a diffing engine here, just idempotency detection.

## Design
- Package: `internal/resolver/golang` (fingerprint helper) + `internal/control/bbolt` (`PutResolution`, from STORE-001).
- Fingerprint: BLAKE3 (or interim `crypto/sha256` if BLAKE3 wiring isn't ready — prefer BLAKE3 per HASH-001 once available) over a canonical serialization of `[]domain.DependencyVersion` sorted by `(ecosystem, name)`.
- Populate `domain.Resolution` (defined in CORE-001, not redefined here) with `LockPath` empty for Go (no separate lockfile beyond `go.sum`) and `Fingerprint` set to the computed hash. Persist via bbolt `PutResolution`.
- Store under `dependency_versions` / `project_dependencies` buckets as defined in STORE-001.

## Inputs / Outputs
- Input: project ID, normalized `[]domain.DependencyVersion` from GO-003.
- Output: persisted `domain.Resolution` in bbolt; fingerprint string.

## Failure behavior
- bbolt write failures propagate as typed storage errors; caller (CLI/scan command) decides whether to retry.

## Tests
- Identical dependency set resolved twice produces the identical fingerprint (order-independent — sort before hashing).
- Different dependency set produces different fingerprint.
- Round-trip: write then read back yields equal `domain.Resolution`.

## Acceptance criteria
- [ ] Second identical resolution produces no desired-state changes (same fingerprint as previously stored).
- [ ] Resolution persists and survives process restart (via STORE-001's reopen guarantee).
