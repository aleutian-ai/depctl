# Epic: Structured Artifact Normalizers

Two more content-type-specific normalizers, following the same pattern `internal/normalize/godoc` already established for Go source: extract fine-grained, retrievable objects (one per API operation / one per RPC method) rather than indexing a monolithic specification file whole. Grounded in the scratch doc's §6.3 (OpenAPI normalizer) and §6.4 (Protobuf normalizer).

Both tickets implement the real `normalize.Normalizer` interface (`internal/normalize/normalize.go`: `Name() string`, `Version() string`, `Supports(src domain.SourceSnapshot) bool`, `Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error)`) and register into the same `normalize.Registry` selection mechanism the existing four normalizers (`markdown`, `plaintext`, `godoc`, `releasenotes`) already use.

## Tickets
- [NORM-006](NORM-006-openapi-normalizer.md) — `internal/normalize/openapi`: one `KnowledgeObject` per path+HTTP-method operation.
- [NORM-007](NORM-007-protobuf-normalizer.md) — `internal/normalize/proto`: one `KnowledgeObject` per RPC method (service object optionally too).

## Non-goals for this epic
- No universal AST/schema platform — each normalizer is a narrow, format-specific extractor, matching godoc's own precedent of not building a general code-search engine.
- No OpenAPI/protobuf *validation* — malformed specs are a normalize-time failure (skip + typed error), not a linting feature.
- No source-symbol/tree-sitter normalization (scratch doc §6.5) — explicitly deferred pending eval evidence that source-symbol chunking materially helps; not ticketed here.
