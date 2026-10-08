# Epic: Structural Preservation

**Status: done** — all four tickets verified done against `docs/architecture.md`.

Stop discarding cheap structure depctl's normalizers and chunkers already derive in-memory but currently throw away or leave stranded at the wrong layer. Implements `docs/scratch/depctl_architecture_eval_next_steps-2.md` §6.1 (Markdown heading paths), §6.2 (fenced code-block preservation), §8.1 (chunk self-description), and the breadcrumb half of §13.1 (query-serving evidence) — Phase 1 of that document's recommended sequence.

The core idea, straight from that doc: `dependency@version > package/module > file > symbol > section > example` is real structure depctl already walks during normalization and chunking. This epic promotes pieces of that walk onto `domain.Chunk.Metadata` and into query/MCP output, instead of re-deriving it later or forcing a second store lookup.

## Tickets
- [STRUCT-001](STRUCT-001-markdown-section-paths.md) (done) — structured ancestor heading path (`section_path`) alongside the Markdown chunker's existing flat `heading_path` breadcrumb string.
- [STRUCT-002](STRUCT-002-fenced-code-block-preservation.md) (done) — structured per-block code-fence records (index, language, content, section path) on the Markdown normalizer's output, replacing/extending today's document-wide `code_languages` set.
- [STRUCT-003](STRUCT-003-chunk-self-describing-metadata.md) (done) — promote dependency/version/source_type/package/file/symbol/section_path onto every `domain.Chunk.Metadata`, so `query.Service.search`'s existing single `GetChunk` call is enough to build a full `ResultChunk` — no second Badger lookup of the parent `KnowledgeObject`.
- [STRUCT-004](STRUCT-004-mcp-breadcrumb-output.md) (done) — surface a computed breadcrumb string (`grpc-go@1.72.0 > Authentication > Transport Credentials > NewTLS`) on `query.ResultChunk` and the `search_dependency_docs` MCP tool's output.

## Non-goals for this epic
- OpenAPI and protobuf normalizers (design doc §6.3/§6.4) — separate, larger normalizers with their own granularity questions; not scoped here.
- Source-symbol/tree-sitter chunking beyond what NORM-004/CHUNK-003 already do (design doc §6.5) — explicitly deferred pending eval evidence.
- Token-aware chunk sizing, overlap experiments, neighborhood expansion (design doc §8.2–§8.4) — separate, evidence-gated retrieval work, not structural preservation.
- Any change to how a chunk is split or how many chunks a document produces — this epic only adds/promotes metadata onto chunks that already exist; it does not change chunking boundaries.

## Dependencies
Builds on CHUNK-001/002/003 (`docs/tickets/completed/10-fingerprinting-chunking`), NORM-002 (`docs/tickets/completed/09-normalization`), and the query/MCP surfaces from `docs/tickets/completed/17-mcp-server`. Read `docs/internal/data-chunk.md`, `docs/internal/normalize.md`, `docs/internal/query.md`, and `docs/internal/mcp.md` for the as-shipped behavior these tickets extend.
