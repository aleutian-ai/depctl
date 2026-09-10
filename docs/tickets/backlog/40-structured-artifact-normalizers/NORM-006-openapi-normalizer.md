# NORM-006: OpenAPI normalizer

**Epic:** Structured Artifact Normalizers
**Status:** planned
**Depends on:** NORM-001 (Normalizer interface, `internal/normalize`)
**Estimated size:** medium

## Goal
Add a normalizer that supports YAML/JSON files containing an OpenAPI/Swagger root (`openapi: "3.x"` or `swagger: "2.0"`), emitting one `domain.KnowledgeObject` per path+HTTP-method operation — mirroring `internal/normalize/godoc`'s per-symbol granularity (one object per exported declaration) rather than indexing the whole spec file as a single blob.

## Non-goals
- No OpenAPI spec *validation* (no `$ref` resolution correctness checking, no full JSON Schema conformance check on request/response bodies) — a spec that parses structurally is normalized; deeper validation is a linting concern out of scope here.
- No client/server code generation.
- No Swagger 2.0-to-OpenAPI-3.x conversion — both are parsed for their respective operation shapes, not unified into one internal schema beyond the fields this ticket extracts.
- No cross-file `$ref` following into separately-hosted schema files — only refs resolvable within the single input document are read (for `RequestSchemaRef`/`ResponseSchemaRef` string capture, not full dereferencing — see Design).

## Simplicity constraints
- Use `gopkg.in/yaml.v3` (already a dependency, per `internal/registry`) for YAML specs and `encoding/json` for JSON specs — decode into minimal structs covering exactly the fields this ticket needs (paths, operations, parameters summary, schema ref strings), not a full generated OpenAPI object model.
- `RequestSchemaRef`/`ResponseSchemaRef` are captured as the raw `$ref` string (or inline-schema marker) exactly as written in the spec — no dereferencing, no schema-shape extraction beyond that string. If the ticket's simplicity constraint proves inadequate for real specs, resolving refs is a follow-up, not scope creep here.

## Design
Package: `internal/normalize/openapi`

```go
// Normalizer implements normalize.Normalizer for OpenAPI/Swagger
// documents, one KnowledgeObject per path+HTTP-method operation.
type Normalizer struct{}

// New returns a ready-to-use OpenAPI normalizer.
func New() *Normalizer

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "openapi-normalizer" }

// Version identifies this normalizer's extraction logic revision.
func (n *Normalizer) Version() string { return "v1" }

// Supports reports whether src's content parses as YAML/JSON with a
// top-level "openapi" (3.x) or "swagger" (2.0) key.
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool

// Normalize parses the spec at src.LocalPath and emits one
// KnowledgeObject per path+method operation.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error)
```

Per-operation `KnowledgeObject`:
```go
domain.KnowledgeObject{
    SourceID:    src.SourceID,
    SourceURI:   src.URI,
    ContentType: "openapi_operation",
    LogicalPath: src.LogicalPath,
    Title:       fmt.Sprintf("%s %s", method, path), // e.g. "GET /pets/{id}"
    Version:     src.Version,
    Commit:      src.Commit,
    Content:     normalize.NormalizeLineEndings([]byte(renderedOperation)), // human-readable rendering: summary/description/params/request+response ref strings
    Metadata: map[string]string{
        "api_title":            spec.Info.Title,
        "api_version":          spec.Info.Version,
        "path":                 path,
        "method":               method, // upper-case: GET/POST/...
        "operation_id":         op.OperationID,
        "tags":                 strings.Join(op.Tags, ","),
        "request_schema_ref":   requestRef,
        "response_schema_ref":  responseRef,
        "deprecated":           strconv.FormatBool(op.Deprecated),
    },
}
```
`renderedOperation` is a small deterministic text rendering (summary, description, parameter names/locations, the captured schema refs) — plain text good enough for embedding and for a human/agent reading the chunk directly, not a re-serialization of the raw YAML/JSON operation object.

An optional per-path "path summary" object (mirroring godoc's package-doc object) is out of scope for v1 — each operation object already carries `path`/`tags`/`api_title` in its metadata, which is enough for an agent to reconstruct the grouping without a redundant object.

## Inputs / Outputs
- Input: `SourceSnapshot` pointing at one OpenAPI/Swagger file (`src.LocalPath`).
- Output: `[]domain.KnowledgeObject`, one per path+method operation found. A spec with zero operations (e.g. only `components` defined, no `paths`) yields `(nil, nil)`, matching godoc's precedent for "nothing documentable here" rather than an error.

## Failure behavior
- Malformed YAML/JSON → typed `NormalizeError`, matching HTTP-003's precedent (`internal/normalize/html`), that file skipped, not fatal to the rest of the generation.
- A `paths` entry with no recognized HTTP-method keys (`get`/`post`/`put`/`patch`/`delete`/`head`/`options`) → skipped, not an error (could be a legitimate `parameters`-only path-level block).
- Spec has neither `openapi` nor `swagger` root key → `Supports` returns false; never reaches `Normalize`.

## Tests
- A fixture OpenAPI 3.x spec with three operations across two paths → three `KnowledgeObject`s, correct `path`/`method`/`operation_id`/`tags`/`deprecated` metadata.
- A fixture Swagger 2.0 spec → equivalent extraction (method/path/operationId fields exist in 2.0 too, under a slightly different top-level shape).
- Malformed YAML input produces a typed `NormalizeError`, does not panic.
- A spec with `paths` but zero method-keyed operations under any path → `(nil, nil)`.
- `Supports` returns false for an arbitrary YAML file with no `openapi`/`swagger` key (so it doesn't shadow the plaintext/markdown normalizers in `normalize.Registry`'s try-order).

## Acceptance criteria
- [ ] `internal/normalize/openapi.Normalizer` implements `normalize.Normalizer`.
- [ ] Golden-snapshot test against a committed fixture spec under `testdata/`.
- [ ] One `KnowledgeObject` per path+method operation, not one per file.
- [ ] Deprecated operations are tagged (`Metadata["deprecated"] == "true"`), not silently dropped.
