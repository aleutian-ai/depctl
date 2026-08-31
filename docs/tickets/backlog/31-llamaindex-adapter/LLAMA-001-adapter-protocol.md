# LLAMA-001: External normalizer/ingestor protocol

**Epic:** LlamaIndex adapter
**Status:** planned
**Depends on:** NORM-001 (Normalizer interface)
**Estimated size:** medium

## Goal
Define an out-of-process protocol that lets an external ingestor (e.g. a Python LlamaIndex sidecar) normalize a `SourceSnapshot` into `KnowledgeObject`s, without importing Python into the Go binary.

## Non-goals
- No embedded Python runtime, no CGo/Python bridge.
- No built-in LlamaIndex sidecar implementation (see LLAMA-002 for the example).

## Simplicity constraints
- One transport only for v1: JSON over stdio (spawn subprocess, write request JSON to stdin, read response JSON from stdout). Do not also build an HTTP transport unless a concrete need arises — the design doc allows "HTTP or stdin/stdout JSON" but stdio is simpler to ship first and requires no port/network management.
- The protocol is intentionally the same shape as the in-process `Normalizer` interface's inputs/outputs — do not invent a divergent schema.

## Design
Package: `internal/normalize/external`

```go
type ExternalNormalizer struct {
    Command string   // path to external ingestor binary
    Args    []string
}

func (e *ExternalNormalizer) Normalize(ctx context.Context, src SourceSnapshot) ([]KnowledgeObject, error)
```

Wire protocol (JSON over stdin/stdout, one request/response per invocation):

Request:
```json
{
  "source_snapshot": { "id": "...", "uri": "...", "materialized_path": "...", "metadata": {} }
}
```

Response:
```json
{
  "objects": [ { "logical_path": "...", "title": "...", "heading": "...", "content_type": "...", "language": "...", "content": "..." } ],
  "errors": []
}
```

`ExternalNormalizer` implements the existing `Normalizer` interface (`Name`, `Version`, `Supports`, `Normalize`) so it plugs into the normal pipeline (GEN-002) unmodified. `Supports` can be config-driven (e.g. match by `content_type` or file extension list passed in config).

## Inputs / Outputs
- Input: a `SourceSnapshot` with content already materialized to a local path/URI (acquisition already happened — this ingestor only normalizes).
- Output: `[]KnowledgeObject`, or a typed error if the subprocess fails/times out.

## Failure behavior
Subprocess non-zero exit, malformed JSON, or timeout (context-bound) all produce a typed `ErrExternalNormalizer` wrapping the underlying cause; the pipeline treats this the same as any other normalizer error (does not crash the daemon).

## Tests
- A fake external command (a small test script) round-trips a snapshot and produces expected objects.
- Timeout via context cancellation kills the subprocess and returns promptly.
- Malformed JSON response produces a clear error.

## Acceptance criteria
- [ ] `ExternalNormalizer` implements `Normalizer` and communicates via stdio JSON per the schema above.
- [ ] Context cancellation reliably terminates the subprocess.
- [ ] Tests cover success, timeout, and malformed-response paths.
