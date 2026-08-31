# HASH-001: Content fingerprint service

**Epic:** Fingerprinting and Chunking
**Status:** planned
**Depends on:** NORM-001
**Estimated size:** small

## Goal
Provide a single deterministic BLAKE3-based fingerprint function over (source identity + logical path + normalizer name/version + normalized content), used as the authoritative content-identity signal throughout the system.

## Non-goals
- Does not assign object IDs (HASH-002 builds on this).
- Does not decide content-reuse policy (GEN-003 uses this fingerprint for reuse decisions).

## Simplicity constraints
- One function, no configurable hash algorithm switch — BLAKE3 only, per the locked technology choice (`github.com/zeebo/blake3`). Do not add an abstraction layer "in case we swap hash algorithms later."

## Design
- Package: `internal/data/fingerprint`
- ```go
  func Fingerprint(sourceIdentity, logicalPath, normalizerName, normalizerVersion string, content []byte) [32]byte
  ```
- Implementation: BLAKE3 hasher fed, in fixed order, with each input length-prefixed (e.g. `varint(len) + bytes`) to avoid ambiguous concatenation (e.g. `"ab"+"c"` vs `"a"+"bc"` collisions). Output the full 32-byte BLAKE3 digest.
- Whitespace normalization rule (must be documented in code comments and this ticket): normalized `content` is expected to already be whitespace-normalized by the normalizer (e.g. consistent line endings `\n`, trailing whitespace trimmed) *before* reaching this function — HASH-001 itself does not re-normalize; it trusts normalizer output. This keeps the fingerprint function pure and the whitespace policy owned by normalizers.

## Inputs / Outputs
- Input: source identity string (e.g. snapshot ID or commit+path), logical path, normalizer name+version, normalized content bytes.
- Output: 32-byte BLAKE3 digest.

## Failure behavior
- Pure function, no errors possible other than programmer error (e.g. nil content treated as empty, not a panic).

## Tests
- Same input four-tuple → same hash (determinism).
- Different normalizer version → different hash, all else equal.
- Different logical path, same content → different hash (no accidental collision from concatenation).
- Documented whitespace cases: content with `\r\n` vs `\n` — since normalizer is expected to have already normalized, this test verifies the *normalizer's* output feeding HASH-001 produces identical hashes for equivalent source line-ending variants (integration-style test spanning NORM-002 + HASH-001).

## Acceptance criteria
- [x] Same input → same hash, verified by unit test.
- [x] Whitespace normalization rules documented in a code comment on `Fingerprint`.
- [x] Normalizer version change → distinct ID, verified by unit test.
