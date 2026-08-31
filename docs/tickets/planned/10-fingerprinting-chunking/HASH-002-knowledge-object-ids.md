# HASH-002: Knowledge object IDs

**Epic:** Fingerprinting and Chunking
**Status:** planned
**Depends on:** HASH-001
**Estimated size:** small

## Goal
Derive a deterministic, human-inspectable ID for each `KnowledgeObject` from its content fingerprint.

## Non-goals
- Does not assign chunk IDs (CHUNK-001).

## Simplicity constraints
- ID is a pure string-encoding function over the HASH-001 digest — no separate ID-generation service, no ULID/UUID involved (those are reserved for lifecycle entities without content-derived identity, e.g. jobs, generations).

## Design
- Package: `internal/data/fingerprint`
- ```go
  func ObjectID(digest [32]byte) string // "ko_" + base32(digest), lowercase, no padding
  ```
- Use `encoding/base32` with a no-padding, lowercase-friendly alphabet (e.g. Crockford's base32 or standard base32 lowercased) prefixed with `ko_`, e.g. `ko_a1b2c3...`.
- This ID is used as the Badger key suffix for `obj/<object-id>` (STORE-003 keyspace).

## Inputs / Outputs
- Input: HASH-001 digest for a given `KnowledgeObject`.
- Output: string ID with `ko_` prefix.

## Failure behavior
- N/A — pure deterministic encoding.

## Tests
- Same digest → same ID.
- ID format matches `^ko_[a-z0-9]+$`.
- Two different digests never collide in a large-N property test (e.g. 10k random digests, no duplicate IDs).

## Acceptance criteria
- [ ] ID generation is deterministic and collision-free in practice.
- [ ] ID format documented and matches the `ko_<base32>` convention used elsewhere in storage keys.
