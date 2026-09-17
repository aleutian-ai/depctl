# NORM-001: Normalizer interface

**Epic:** Normalization Pipeline
**Status:** done
**Depends on:** CORE-001
**Estimated size:** small

## Goal
Define the shared `Normalizer` interface that all content-type-specific normalizers (Markdown, plain text, Go doc, release notes) implement, and establish that normalizer identity (name+version) is part of derived content identity.

## Non-goals
- Does not implement any concrete normalizer (NORM-002 through NORM-005).
- Does not implement chunking (that's CHUNK-001, a separate interface).

## Simplicity constraints
- Interface only — no base/abstract struct with shared helper logic unless a second normalizer immediately needs it (per project principle: build one reference implementation before abstracting).

## Design
- Package: `internal/normalize`
- ```go
  type Normalizer interface {
      Name() string
      Version() string
      Supports(src domain.SourceSnapshot) bool
      Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error)
  }
  ```
- `Name()+Version()` together form part of the content fingerprint input in HASH-001 (`normalizer name/version` is one of the four fingerprint inputs) — bumping `Version()` when parsing logic changes is how the system signals "this content needs re-normalization" without needing byte-level source changes (see design spec §112, Normalizer Versioning).
- A simple `Registry` type (`[]Normalizer`) with a `Select(src) (Normalizer, bool)` helper that returns the first normalizer whose `Supports` returns true, in registration order.

## Inputs / Outputs
- Input: `domain.SourceSnapshot` (materialized file/content + metadata from acquisition).
- Output: `[]domain.KnowledgeObject`.

## Failure behavior
- `Normalize` returns a typed error; a parse failure on one file should not be fatal to the whole batch — the caller (GEN-002) is responsible for per-file error aggregation and continuing where sensible.

## Tests
- Fake normalizer implementing the interface passes a basic contract test (given a fixture snapshot, returns non-empty objects).
- `Registry.Select` picks the first matching normalizer and returns `false` when none match.

## Acceptance criteria
- [x] Interface compiles and is used by at least one fake implementation in tests.
- [x] Normalizer version is documented as part of derived object identity (referenced by HASH-001).
