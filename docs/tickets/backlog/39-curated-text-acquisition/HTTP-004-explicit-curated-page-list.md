# HTTP-004: Explicit curated page list

**Epic:** Curated Text Acquisition
**Status:** planned
**Depends on:** HTTP-001 (epic 24, HTTP acquisition client), HTTP-003 (epic 24, HTML normalizer), REG-002 (registry loader)
**Estimated size:** small

## Goal
Let a manifest name an explicit, curator-picked list of page URLs for a dependency — a `Source` that fetches and indexes exactly the pages listed, nothing else. Reuses epic 24's `internal/source/http.Client` (HTTP-001) and `internal/normalize/html.Normalizer` (HTTP-003) unchanged; this ticket is a registry-schema and wiring change, not a new fetch/parse path.

## Non-goals
- No sitemap discovery, no crawling, no following links found in a fetched page — every URL indexed is one that appeared in the manifest, full stop. (See epic INDEX's note on HTTP-002's tension with this principle — that's a separate, unresolved question for epic 24, not something this ticket touches.)
- No new HTML extraction logic — HTTP-003's normalizer is reused as-is; a page that HTTP-003 can't usefully extract (e.g. a JS shell) is skipped the same way HTTP-003 already skips it.
- No collection-file-level precedence/override semantics — a `collection` here is purely a convenience for writing many URLs once against one dependency; it is not a new manifest-layering mechanism (that's epic 39's own INDEX non-goal, deferred).

## Simplicity constraints
- Extend the existing `registry.Source` struct with one new optional field (`URLs []string`) rather than inventing a parallel source-list type — a `type: web` source either has one `URL` (already supported) or a `URLs` list (new), never both.
- A `collection` is sugar at the manifest-parsing level: a collection file expands to a list of ordinary `Source` entries attached to one dependency match; it does not need its own runtime type distinct from `[]registry.Source`.
- No new package: this lives in `internal/registry` (schema + `Manifest`/`Source`) plus a small addition wiring `Source.URLs` into whatever currently drives HTTP-003 per `Source.URL` (the generation pipeline's per-source acquisition step).

## Design
Extend `internal/registry/manifest.go`:

```go
// Source is one place knowledge can be acquired from, ranked by Authority.
type Source struct {
    ID        string   `yaml:"id" json:"id"`
    Type      string   `yaml:"type" json:"type"` // git | godoc | website | github-releases | web
    URL       string   `yaml:"url,omitempty" json:"url,omitempty"`
    URLs      []string `yaml:"urls,omitempty" json:"urls,omitempty"` // explicit curated page list; type "web" only
    Ref       string   `yaml:"ref,omitempty" json:"ref,omitempty"`
    Module    string   `yaml:"module,omitempty" json:"module,omitempty"`
    Authority int      `yaml:"authority" json:"authority"`
}
```

`internal/registry/schema/knowledge-package.schema.json` gains:
```json
"urls": { "type": "array", "minItems": 1, "items": { "type": "string" } }
```
plus a schema-level rule that exactly one of `url`/`urls` is set for `type: web` sources (JSON Schema `oneOf` on the `sources[].items` entry, mirroring how `id`/`type`/`authority` are already `required`).

Manifest shape (matches the scratch doc's §6A example):
```yaml
sources:
  - id: k8s-controller-concepts
    type: web
    urls:
      - https://kubernetes.io/docs/concepts/architecture/controller/
      - https://kubernetes.io/docs/concepts/architecture/leases/
    authority: 40
```

A `collection` file is a second, optional manifest shape that expands to ordinary `Source` entries at load time — not a new `Source.Type`:
```yaml
apiVersion: ragctl.dev/v1alpha1
kind: KnowledgeCollection
metadata:
  name: acme-kubernetes
match:
  ecosystems: [go]
  packages: [k8s.io/client-go]
sources:
  - id: acme-k8s-deploy-guide
    type: web
    url: https://wiki.example.com/platform/kubernetes
    authority: 20
  - id: acme-k8s-upgrade-policy
    type: web
    url: https://wiki.example.com/platform/kubernetes/upgrade-policy
    authority: 20
```
`registry.ParseManifest` (or a sibling `ParseCollection`) validates a `KnowledgeCollection` document against its own embedded schema and returns an equivalent `Manifest` — same `Match`/`Sources` shape the loader already knows how to consume, so no downstream acquisition code needs to know "collection" ever existed.

Acquisition wiring: wherever the generation pipeline currently calls HTTP-001's `Client.Fetch` + HTTP-003's `Normalizer.Normalize` once per `Source.URL` for `Type: "web"`, it now loops `Source.URLs` (or the single `[]string{Source.URL}` when only `URL` is set) and fetches/normalizes each page independently — same client, same normalizer, called N times instead of 1.

## Inputs / Outputs
- Input: a manifest `Source{Type: "web", URLs: [...]}`, or a `KnowledgeCollection` document expanding to several such sources.
- Output: one or more `domain.KnowledgeObject` per listed URL (via HTTP-003, unchanged), attributed to the originating `Source.ID` and the dependency the manifest/collection matched.

## Failure behavior
- One URL in a list 404s/times out → that page's acquisition fails and is recorded per-source (matching HTTP-001's typed `FetchError`), the rest of the list still proceeds — one bad URL in a curated list doesn't fail the whole source.
- `url` and `urls` both set, or both empty, on a `type: web` source → `ManifestError` at load time (schema validation), never a runtime surprise mid-sync.
- A `KnowledgeCollection` document that fails its own schema validation → `ManifestError` naming the collection file, same shape as an invalid `KnowledgePackage`.

## Tests
- Manifest with `Source.URLs` (three URLs) parses and validates; `ParseManifest` round-trips it.
- Manifest with both `url` and `urls` set on the same source fails schema validation.
- A `KnowledgeCollection` fixture expands to the expected `[]Source` with `Match` carried through unchanged.
- Acquisition loop against a local `httptest.Server` serving two of three URLs: two objects produced, one failure recorded, sync not aborted.

## Acceptance criteria
- [ ] `registry.Source` supports `URLs` for `type: web`, validated by the embedded JSON Schema.
- [ ] `KnowledgeCollection` documents parse and expand to ordinary `Manifest`/`Source` values with no downstream code change.
- [ ] A failing URL within a curated list does not abort the rest of that source's acquisition.
- [ ] No code path in this ticket ever derives a URL that wasn't written explicitly in a manifest or collection file.
