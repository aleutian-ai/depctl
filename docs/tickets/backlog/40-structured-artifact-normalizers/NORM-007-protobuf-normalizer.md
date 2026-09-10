# NORM-007: Protobuf normalizer

**Epic:** Structured Artifact Normalizers
**Status:** planned
**Depends on:** NORM-001 (Normalizer interface, `internal/normalize`)
**Estimated size:** medium

## Goal
Add a normalizer that supports `.proto` files, emitting one `domain.KnowledgeObject` per RPC method (and optionally one per `service` as a grouping summary) — mirroring `internal/normalize/godoc`'s per-symbol granularity, per the scratch doc's §6.4.

## Non-goals
- No message/enum-field-level objects for v1 — the scratch doc's §6.4 lists these as "optionally... when referenced strongly enough to be useful"; deferred until an RPC-method-only granularity proves insufficient in practice. Message/enum type names are still captured as metadata on the RPC objects that reference them (request/response type), just not as their own objects.
- No `protoc`/protobuf-compiler dependency, no `.proto` import resolution across a full `protoc` include path — parse the single file's own syntax only (see Simplicity constraints on cross-file imports).
- No gRPC-Gateway/`google.api.http` annotation extraction — plain `.proto` service/RPC structure only; HTTP-transcoding annotations are a possible follow-up, not this ticket.

## Simplicity constraints
- Hand-write a minimal `.proto` (proto3, with proto2 tolerated best-effort) parser sufficient to find `package`, `service`, `rpc`, and each RPC's request/response type names, streaming keywords (`stream`), and leading/attached comments — not a full protobuf grammar implementation. A small line/token scanner is enough; no parser-generator dependency (no `goyacc`, no ANTLR).
- `import` statements are recorded on `Metadata["imports"]` as a comma-joined list of raw import paths, not resolved to their target files — a request/response type from an imported file is still captured by its written name (e.g. `google.protobuf.Empty`), just without deref into the imported file's own definition.
- One new package, `internal/normalize/proto`; no changes to existing normalizers.

## Design
Package: `internal/normalize/proto`

```go
// Normalizer implements normalize.Normalizer for .proto files, one
// KnowledgeObject per RPC method plus one per service (grouping
// summary).
type Normalizer struct{}

// New returns a ready-to-use protobuf normalizer.
func New() *Normalizer

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "proto-normalizer" }

// Version identifies this normalizer's extraction logic revision.
func (n *Normalizer) Version() string { return "v1" }

// Supports reports whether src.LogicalPath has a .proto suffix.
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool

// Normalize parses the .proto file at src.LocalPath and emits one
// KnowledgeObject per service and one per RPC method.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error)
```

Per-service `KnowledgeObject` (summary; emitted once per `service` block):
```go
domain.KnowledgeObject{
    ContentType: "proto_service",
    LogicalPath: src.LogicalPath,
    Title:       serviceName,
    Content:     normalize.NormalizeLineEndings([]byte(serviceComment + rpcListRendering)),
    Metadata: map[string]string{
        "package": pkg,
        "service": serviceName,
        "source_file": src.LogicalPath,
    },
}
```

Per-RPC `KnowledgeObject`:
```go
domain.KnowledgeObject{
    ContentType: "proto_rpc",
    LogicalPath: src.LogicalPath,
    Title:       fmt.Sprintf("%s.%s", serviceName, rpcName),
    Content:     normalize.NormalizeLineEndings([]byte(rpcComment + rendered signature)), // e.g. "rpc GetPet(GetPetRequest) returns (Pet)"
    Metadata: map[string]string{
        "package":          pkg,
        "service":          serviceName,
        "rpc_name":         rpcName,
        "request_type":     requestType,
        "response_type":    responseType,
        "streaming":        streamingMode, // "none" | "client" | "server" | "bidi"
        "source_file":      src.LogicalPath,
    },
}
```
Comments: proto's `//` line comments (and `/* */` block comments) immediately preceding a `service`/`rpc` declaration are treated as that declaration's doc comment, following the same "attached comment = doc" convention `go/doc` uses for godoc — captured verbatim into `Content`, not re-flowed.

## Inputs / Outputs
- Input: `SourceSnapshot` pointing at one `.proto` file.
- Output: `[]domain.KnowledgeObject` — one per `service` plus one per `rpc` within it. A file with no `service`/`rpc` declarations (e.g. a pure message/enum definitions file) yields `(nil, nil)`.

## Failure behavior
- Unparseable `.proto` syntax (the hand-written scanner can't find balanced braces for a `service`/`rpc` block) → typed `NormalizeError` naming the file and best-guess line, that file skipped, not fatal to the rest of the generation.
- A `service` block with zero `rpc` methods → the service summary object is still emitted (it documents an intentionally empty/marker service), just no RPC objects for it.
- A `.proto` file with only message/enum definitions, no service → `(nil, nil)`, not an error (a normal, common layout for shared-type files).

## Tests
- A fixture `.proto` with one service, three RPCs (unary, server-streaming, bidi) → one service object + three RPC objects, correct `streaming` metadata for each.
- Leading comment on a `service`/`rpc` is captured into that object's `Content`; a declaration with no comment still normalizes (empty leading text, not an error).
- A `.proto` file with only `message`/`enum` blocks, no `service` → `(nil, nil)`.
- Malformed `.proto` (unbalanced braces inside a `service` block) produces a typed `NormalizeError`, does not panic.
- `Supports` returns false for non-`.proto` files.

## Acceptance criteria
- [ ] `internal/normalize/proto.Normalizer` implements `normalize.Normalizer`.
- [ ] Golden-snapshot test against a committed fixture `.proto` under `testdata/`.
- [ ] One `KnowledgeObject` per RPC method, plus one per service.
- [ ] Streaming mode (`none`/`client`/`server`/`bidi`) is correctly derived from `stream` keyword placement on request/response.
