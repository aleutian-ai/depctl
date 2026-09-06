# SEC-001: Source trust metadata

**Epic:** Security hardening
**Status:** planned
**Depends on:** NORM-001 (Knowledge object model)
**Estimated size:** small

## Goal
Ensure every `KnowledgeObject` carries explicit trust/provenance metadata (authority score, trust class, source URI, source type) so downstream consumers can reason about how much to trust retrieved content.

## Non-goals
- No trust-scoring algorithm/ML — authority is assigned by the registry manifest (REG-001), not computed.
- No UI for displaying trust to end users.

## Simplicity constraints
- This is mostly already covered by the `KnowledgeObject` fields (`Authority`, `SourceURI`, `SourceType`) defined in NORM-001/registry work — this ticket's job is to add the missing `TrustClass` field and enforce it is always populated, not to redesign the object model.

## Design
Extend `KnowledgeObject` (`internal/domain`) with:

```go
type TrustClass string

const (
    TrustOfficial   TrustClass = "official"
    TrustRepository TrustClass = "repository"
    TrustCommunity  TrustClass = "community"
    TrustUser       TrustClass = "user"
    TrustUnknown    TrustClass = "unknown"
)
```

Add `TrustClass TrustClass` to `KnowledgeObject`. Normalizers (NORM-002..005) set it based on the source's registry manifest `type`/`authority` (e.g. `git` source from the package's own repo → `repository`; a registry-declared official docs site → `official`; anything without a registry match → `unknown`). Add a validation helper `KnowledgeObject.Validate()` that rejects objects with an empty `SourceURI` or `TrustClass`.

## Inputs / Outputs
- Input: `SourceSnapshot` with its originating `KnowledgeSource` type/authority.
- Output: `KnowledgeObject` with `TrustClass` always populated.

## Failure behavior
Normalization fails fast (typed error) if trust class cannot be determined and is not explicitly `unknown`-eligible — i.e. the code must always assign a value, never leave the zero value.

## Tests
- Object derived from a registry `git` source gets `repository`.
- Object with no registry match gets `unknown`.
- `Validate()` rejects an object with empty `TrustClass`.

## Acceptance criteria
- [x] `TrustClass` field added and populated by every normalizer.
- [x] `Validate()` enforces non-empty trust metadata.
- [x] Unit tests cover the classification rules above.

## Post-implementation note
`TrustClass` is assigned in `generation.appendAttributed` (`internal/data/generation/build.go`), not inside each normalizer — normalizers (NORM-002..005) never see the registry `Source` at all (only a `SourceSnapshot`), so `SourceType`/`Authority` were already being attributed post-hoc at that single call site, and `TrustClass` follows the same pattern rather than duplicating classification logic across four normalizers. `trustClassForSourceType` maps `git`/`godoc` → `repository` and `website`/`github-releases` → `official`; a source type this codebase doesn't recognize (which today only means an empty string, since `syncVersion` currently requires a registry match before `Build` ever runs — see the still-open "no-registry-match fallback" design question) falls back to `unknown`. `TrustCommunity`/`TrustUser` are defined but unused — nothing yet produces a `KnowledgeObject` through a non-built-in-registry path; they're reserved for whatever mechanism eventually lets a source be added outside the reviewed/built-in tier.

`Validate()` is called in `indexObjects` immediately before `PutKnowledgeObject` (only for newly-created objects — a GEN-003-reused object was already validated when first created, so re-validating it would be redundant), returning `ErrNormalization` on failure to match this package's existing error-wrapping convention rather than a bare error.
