# CHUNK-003: Symbol chunker

**Epic:** Fingerprinting and Chunking
**Status:** planned
**Depends on:** CHUNK-001, NORM-004
**Estimated size:** small

## Goal
Chunk Go-doc-derived `KnowledgeObject`s (from NORM-004) one exported API symbol per chunk, including package, symbol name, signature, doc comment, source path, and version in metadata.

## Non-goals
- Does not re-extract symbols from source — NORM-004 already produced one `KnowledgeObject` per exported symbol; this chunker is close to a 1:1 passthrough with metadata shaping.

## Simplicity constraints
- Since NORM-004 already emits one object per symbol, this chunker's job is close to identity — resist the urge to further split or merge symbol objects. One symbol → one chunk, no size-based splitting logic needed here (symbol docs are naturally small).

## Design
- Package: `internal/data/chunk/symbol`
- `SymbolChunker` implements `Chunker` (CHUNK-001).
- `Chunk(ctx, obj)`: if `obj.ContentType == "symbol_doc"` or `"package_doc"`, emit exactly one `domain.Chunk` wrapping the object's full content, with metadata copied/promoted from the object:
  ```text
  Metadata["package"]    = obj.Metadata["package"] (import path)
  Metadata["symbol"]     = obj.Metadata["symbol"]
  Metadata["signature"]  = obj.Metadata["signature"]
  Metadata["source_path"]= obj.LogicalPath
  Metadata["version"]    = obj.Version
  ```
- Register in CHUNK-001's `Registry` keyed on `ContentType ∈ {"symbol_doc", "package_doc"}`.

## Inputs / Outputs
- Input: a `domain.KnowledgeObject` produced by NORM-004.
- Output: exactly one `domain.Chunk`.

## Failure behavior
- Object missing expected metadata fields (e.g. no `signature`): proceed with empty string rather than erroring — not every symbol has a meaningful signature to render (e.g. a constant).

## Tests
- Exported function symbol object → one chunk with correct package/symbol/signature/source_path/version metadata.
- Package doc object → one chunk with package-level metadata (symbol field empty/omitted).

## Acceptance criteria
- [ ] One chunk per symbol object, metadata correctly populated.
- [ ] Package-level doc objects handled distinctly from symbol-level objects.
