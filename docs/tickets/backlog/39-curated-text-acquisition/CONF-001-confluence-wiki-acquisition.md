# CONF-001: Confluence/internal-wiki acquisition

**Epic:** Curated Text Acquisition
**Status:** planned
**Depends on:** HTTP-004 (registry `Source` shape this extends), HASH-001 (fingerprinting, epic 07), REG-002 (registry loader)
**Estimated size:** medium

## Goal
Add Confluence (or a generic internal-wiki API with the same page-by-ID shape) as another acquisition provider, following the scratch doc's §6A acquisition contract: `configured page ID → authorized fetch → preserve title/headings/links/code/tables → normalize into KnowledgeObjects → attach source + provenance → generation pipeline`. This is the same acquisition-contract shape epic 24's HTTP client already established (fetch → normalize → attribute) — not a new retrieval architecture, not a new storage plane.

## Non-goals
- No automatic whole-space ingestion — only explicitly listed page IDs are ever fetched, matching §6A's "Out of scope for now" list ("automatic whole-Confluence-space ingestion").
- No recursive following of links found inside a fetched page, even to other pages in the same space.
- Credentials are never read from or written into registry YAML — see Design's auth section. This ticket does not build a general secrets-manager integration; an env-var reference is the whole mechanism.
- No new content-identity/dedup mechanism — a changed Confluence page is just new content flowing through the existing `data/fingerprint` primitive (HASH-001); this ticket does not special-case wiki content in fingerprinting at all.

## Simplicity constraints
- New package `internal/source/confluence`, following the same shape as `internal/source/http` (epic 24, HTTP-001): a small hand-written client against Confluence's REST API (`GET /rest/api/content/{id}?expand=body.storage,version`), no generated SDK, no third-party Confluence client dependency.
- Reuse `internal/normalize/html.Normalizer` (HTTP-003) for content extraction — Confluence's `body.storage` representation is XHTML-ish; a thin pre-pass converts Confluence storage-format macros to plain HTML tags where cheap (e.g. `<ac:structured-macro ac:name="code">` → treat as a code block), falling back to stripping unknown macros rather than building a full Confluence-macro interpreter.
- One new provider, one new `Source.Type` value (`"confluence"`) — not a new normalizer, not a new chunker, not a new domain type beyond what's needed to carry page ID/revision through to `KnowledgeObject.Metadata`.

## Design
Package: `internal/source/confluence`

```go
// Client fetches Confluence pages by ID via the REST content API — no
// space browsing, no CQL search, no link-following.
type Client struct {
    BaseURL    string
    AuthEnvVar string // name of the env var holding an API token/PAT; never the token itself
    HTTPClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

func New(baseURL string, opts ...Option) *Client

// FetchPage retrieves one page's current storage-format body and
// version metadata by its Confluence page ID.
func (c *Client) FetchPage(ctx context.Context, pageID string) (*Page, error)

// Page is one fetched Confluence page: enough to normalize and to
// preserve original provenance (original URL/ID, revision).
type Page struct {
    ID          string
    Title       string
    BodyStorage string // Confluence XHTML storage format
    WebURL      string // human-navigable page URL, for SourceURI
    Version     int    // Confluence's own page version number
    UpdatedAt   time.Time
}
```

Auth: the token itself is never embedded in registry YAML or in `internal/config`'s YAML file — only the *name* of an environment variable is, following the existing `VectorConfig.APIKeyEnv` pattern in `internal/config/config.go`. New config surface:

```go
// WikiConfig configures internal-wiki/Confluence acquisition. The token
// itself is read from the named env var at fetch time; Save never
// serializes it, matching VectorConfig.APIKeyEnv's existing convention.
type WikiConfig struct {
    ConfluenceBaseURL string `yaml:"confluence_base_url,omitempty"`
    AuthTokenEnv      string `yaml:"auth_token_env,omitempty"` // e.g. "RAGCTL_CONFLUENCE_TOKEN"
}
```
added as a field on `Config` (`Wiki WikiConfig `yaml:"wiki"``). The registry manifest side only ever names page IDs, never a token:
```yaml
sources:
  - id: acme-kubernetes-runbooks
    type: confluence
    module: "123456,123987,124212" # comma-separated page IDs; see Inputs/Outputs
    authority: 20
```
(Reuses `Source.Module` as the page-ID-list field rather than adding a new schema field, matching how `Source` already has no per-type field beyond `URL`/`Ref`/`Module` — an explicit `PageIDs []string` field is preferable if HTTP-004 lands first and the schema is already being touched; whichever lands first should add a proper `page_ids: []string` field rather than overloading `module`. Noted here as an open follow-up, not blocking this ticket's core acquisition logic.)

Normalizer wiring: `internal/normalize/html.Normalizer` (HTTP-003) is reused unchanged by constructing a `domain.SourceSnapshot` from `Page.BodyStorage` (after the storage-format-to-HTML pre-pass) with `Metadata["confluence_page_id"]`, `Metadata["confluence_version"]`, and `URI: Page.WebURL` set — HTTP-003's `Supports` already matches on content-type `text/html`, so no normalizer-side change is needed.

## Inputs / Outputs
- Input: a `registry.Source{Type: "confluence"}` naming one or more page IDs, plus `config.WikiConfig` naming the base URL and auth-token env var.
- Output: one or more `domain.KnowledgeObject` per page (via HTTP-003's normalizer), with `Metadata["confluence_page_id"]`, `Metadata["confluence_revision"]` set, `SourceURI` set to the page's original web URL, and `SourceType: "confluence"` (so query-time `TrustClass` derivation — SEC-001's `TrustClassForSourceType` — can classify wiki content distinctly from official/repository sources per §6A: "internal documentation can receive high organizational relevance without being confused with upstream software authority").

## Failure behavior
- `AuthTokenEnv` unset or the named env var empty → typed `ConfigError` at sync start, not a bare 401 from Confluence — the operator gets a clear "set $RAGCTL_CONFLUENCE_TOKEN" message.
- Confluence returns 401/403 → typed `FetchError{Reason: "unauthorized"}`, that source's acquisition marked failed, not fatal to the whole sync (same pattern as HTTP-002's per-source failure handling).
- Page ID not found (404) → typed `FetchError{Reason: "not_found"}` naming the page ID, sync continues for the rest of the list.
- A page whose storage-format body fails the macro pre-pass in a way that produces unparseable HTML → falls through to HTTP-003's existing "unparseable HTML" failure path (typed `NormalizeError`, page skipped, not fatal).

## Tests
- `FetchPage` against a local `httptest.Server` stub returning a fixture Confluence REST response: title/body/version/webURL parsed correctly.
- Missing/empty `AuthTokenEnv` produces the typed config error before any HTTP call is made.
- 401 response produces `FetchError{Reason: "unauthorized"}` without leaking the token value in the error message.
- Storage-format fixture with a `<ac:structured-macro ac:name="code">` block converts to a normalizer-recognizable code block; an unknown macro is stripped, not fatal.
- A page re-fetched with an unchanged `Version` produces the same `ContentHash`/fingerprint as before (via `data/fingerprint.Fingerprint`, unchanged mechanism); a page re-fetched with a bumped `Version` and changed body produces a new fingerprint and therefore a new candidate `KnowledgeObject` — no special-casing in `internal/data/fingerprint` itself, this is just the existing HASH-001 mechanism exercised against wiki content.

## Acceptance criteria
- [ ] `internal/source/confluence.Client` fetches pages by ID only, never by space/CQL search.
- [ ] Auth token is read from an env var named in `config.WikiConfig.AuthTokenEnv`; no token value ever appears in registry YAML, `internal/config`'s saved YAML, or logged error messages.
- [ ] Fetched pages normalize via the existing HTTP-003 `html.Normalizer` with no normalizer-side code change.
- [ ] A page's original URL/ID and revision are retained on the resulting `KnowledgeObject.Metadata`.
- [ ] A page update (new Confluence version, changed body) produces a new fingerprint/candidate generation through the existing HASH-001 mechanism, with no wiki-specific dedup logic added.
