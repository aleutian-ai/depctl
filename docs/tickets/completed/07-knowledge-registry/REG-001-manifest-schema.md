# REG-001: Manifest schema

**Epic:** Knowledge Registry
**Status:** done
**Depends on:** CORE-001
**Estimated size:** medium

## Goal
Define the YAML schema for a "KnowledgePackage" manifest — the data format that maps an ecosystem+package identity to knowledge sources — and publish a JSON Schema for validation.

## Non-goals
- Does not implement loading/matching (REG-002/REG-003).
- Does not seed real package manifests (REG-004).

## Simplicity constraints
- Manifest is plain YAML data, not Go code — resist adding scripting/templating beyond the single `${version}` substitution token needed for ref templating.
- Do not design for hypothetical future manifest kinds; only `KnowledgePackage` is needed for v0.1.

## Design
- File: `schemas/knowledge-package.schema.json` (JSON Schema draft used to validate manifest YAML after conversion to JSON, or validate directly against YAML-as-JSON).
- Manifest shape (mirrors the design spec example):
  ```yaml
  apiVersion: ragctl.dev/v1alpha1
  kind: KnowledgePackage
  metadata:
    name: grpc-go
  match:
    ecosystems: [go]
    packages: [google.golang.org/grpc]
  version:
    strategy: semver-tag   # enum: semver-tag | none | manual
    repository: grpc/grpc-go
  sources:
    - id: repository
      type: git            # enum: git | godoc | website | github-releases
      url: https://github.com/grpc/grpc-go
      ref: "v${version}"
      authority: 100
    - id: package-docs
      type: godoc
      module: google.golang.org/grpc
      authority: 95
  ```
- Go type mirroring the schema in `internal/registry`:
  ```go
  type Manifest struct {
      APIVersion string
      Kind       string
      Metadata   struct{ Name string }
      Match      struct {
          Ecosystems []domain.Ecosystem
          Packages   []string
      }
      Version struct {
          Strategy   string
          Repository string
      }
      Sources []Source
  }
  type Source struct {
      ID        string
      Type      string
      URL       string
      Ref       string
      Module    string
      Authority int
  }
  ```

## Inputs / Outputs
- Input: none (this ticket produces the schema itself).
- Output: `schemas/knowledge-package.schema.json` + Go struct + `gopkg.in/yaml.v3`-based unmarshal function `ParseManifest([]byte) (Manifest, error)` with JSON Schema validation applied after YAML→JSON conversion.

## Failure behavior
- Invalid YAML or schema violations return a typed `ManifestError` listing the specific field(s) that failed, not a generic parse error.

## Tests
- Valid fixture manifest parses cleanly.
- Invalid fixture (missing `match.packages`, bad `type` enum value, authority out of 0–100 range) is rejected with a clear message.

## Acceptance criteria
- [x] Valid/invalid fixtures both covered by tests.
- [x] JSON Schema file exists and is used for validation (not just Go struct tags).
