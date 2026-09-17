# CHUNK-003: Symbol chunker

**Epic:** Fingerprinting and Chunking
**Status:** done
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
- [x] One chunk per symbol object, metadata correctly populated.
- [x] Package-level doc objects handled distinctly from symbol-level objects.

## Post-implementation fix (found by real-world sweeping)

`hack/chunk-sweep`, run against ~22k real `KnowledgeObject`s from `~/offline-knowledge`, found 6,287 of 56,942 chunks (~11%) had completely empty `Content`. Root cause: NORM-004's `Content` is deliberately just the symbol's doc comment (per its own ticket's design), which is empty for any exported symbol without one — a very common real-world case (undocumented getters, simple constants, etc.), confirmed via samples like `func CreateAuthorizationToken(taskID, runID, jobID int64) (string, error)` with zero doc text. This chunker passed that empty `Content` straight through even though `Metadata["signature"]` — real, useful information about the symbol — was sitting right there unused, producing a chunk with zero signal for the embedding/retrieval stages that will eventually consume it.

Fixed in `internal/data/chunk/symbol/symbol.go`: when `obj.Content` is empty (after trimming), `Chunk` now falls back to a non-empty stand-in built from metadata — the signature if present, else `package.symbol`, else whichever single field is available. This only affects the emitted `Content`; `Metadata["signature"]`/etc. are unchanged, and `ContentHash`/`ChunkID` are computed from the same fallback content actually shipped, so identity stays consistent. Regression tests: `TestUndocumentedSymbolFallsBackToSignature`, `TestUndocumentedSymbolWithNoSignatureFallsBackToPackageDotSymbol`. Re-running the sweep after the fix: 0 empty chunks across the same ~57k chunks.
