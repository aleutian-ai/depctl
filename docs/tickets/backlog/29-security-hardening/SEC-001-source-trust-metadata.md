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
- [ ] `TrustClass` field added and populated by every normalizer.
- [ ] `Validate()` enforces non-empty trust metadata.
- [ ] Unit tests cover the classification rules above.
