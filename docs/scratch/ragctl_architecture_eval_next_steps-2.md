# ragctl: Current Architecture, New Retrieval Concepts, Eval Strategy, and Next Steps

## Executive summary

ragctl has crossed the line from “local RAG prototype” into a coherent software system with a real control plane, generation lifecycle, version-aware query path, and explicit cleanup semantics. The strongest architectural idea is still the same: **repository state determines which dependency knowledge is valid**. Retrieval happens only after that state has been resolved.

The next phase should therefore avoid turning ragctl into a generic “better RAG” project. The work should concentrate on four things:

1. **Preserve more structure inside a valid dependency generation**: heading paths, code fences, OpenAPI operations, protobuf services/RPCs, and symbol identity.
2. **Improve evidence returned to agents**: breadcrumbs, compact neighborhood expansion, and eventually a call-site symbol join from a repo graph into exact-version dependency knowledge.
3. **Build an independent eval repository** that can test ragctl, model-only baselines, graph systems, and combinations without importing ragctl internals.
4. **Close lifecycle correctness gaps** such as orphaned failed generations while resisting attractive but unproven optimizations such as SimHash near-dup reuse and elaborate drift dashboards.

The near-term goal is not a larger platform. It is a smaller number of high-signal changes followed by measured evidence.

---

## 1. What ragctl is now

A useful one-line definition is:

> **ragctl is a repo-state-synchronized knowledge layer that gives coding agents authoritative, exact-version dependency context.**

That wording matters. It separates ragctl from generic semantic retrieval.

The current system already implements a lifecycle closer to package management and release engineering than to a toy vector search pipeline:

- discover projects;
- resolve exact dependency versions;
- match dependencies to known source manifests;
- compute a plan;
- build a knowledge generation;
- acquire authoritative source material;
- normalize and chunk it;
- fingerprint and deduplicate it;
- embed and replicate it;
- validate the candidate generation;
- promote it atomically;
- serve only the active version-aware generation;
- retain old versions while referenced;
- garbage collect only when safe.

The current shipped behavior is already organized around four major feature flows:

- discovery and resolution;
- sync/build/replicate/validate/promote;
- query serving over MCP;
- garbage collection.

### 1.1 Current architectural thesis

The most important invariant is:

> **State first, retrieval second.**

The repository and its dependency graph decide which dependency version is true. The retriever does not guess that from semantic similarity or from whatever documentation happens to be present locally.

This implies a hierarchy of truth:

1. project state / lock state;
2. resolved dependency identity and exact version;
3. active validated knowledge generation;
4. source authority and structure inside that generation;
5. retrieval ranking;
6. model interpretation.

This ordering should remain visible in every future design decision.

---

## 2. Current architecture in detail

### 2.1 Discovery and resolution

Discovery, dependency resolution, and registry matching are deliberately separate concerns.

Conceptually:

```text
project.Scan
  -> Resolver
  -> Resolution{DependencyVersion...}
  -> Registry.Match
  -> Planner
```

The practical value of this split is that project discovery does not need to understand registry policy, and a resolver can be replaced or extended without rewriting the rest of the system.

Current resolution support:

- Go: project state resolved through Go tooling.
- Python: direct parsing for supported lock formats and exact requirements pins.
- Node: supported lockfile parsing.
- Rust and Java: detectable but not yet fully resolved/synced.

A dependency-list fingerprint allows planner logic to distinguish a real dependency-state change from a no-op rescan.

### 2.2 The GOMODCACHE caveat

`GOMODCACHE` should remain an **acquisition cache**, never an authority source.

Correct sequence:

```text
go.mod / resolved module graph
        -> exact dependency@version
        -> locate exact bytes in GOMODCACHE if present
        -> otherwise acquire through an allowed source path
```

Incorrect sequence:

```text
scan GOMODCACHE
        -> infer what the project depends on
```

Why:

- multiple versions of the same module may be cached;
- unrelated repos may have populated the same machine-wide cache;
- `replace` directives may point to local paths instead of the cache;
- module-cache path escaping is an implementation detail best left to Go tooling;
- a resolved dependency may not yet be present locally;
- the cache should be treated as read-only.

For offline benchmarks, pre-materialize the exact dependency versions used by fixtures, then prohibit network access during the run.

### 2.3 Sync lifecycle

The core write path already has the right shape:

```text
resolve/match
 -> Create generation
 -> Acquire
 -> Normalize
 -> Fingerprint
 -> Chunk
 -> Replicate
 -> Validate
 -> Promote
```

The critical property is that a candidate is **not active just because indexing finished**.

The candidate moves through lifecycle state, is validated, and only then becomes active.

Validation currently distinguishes between:

- structural integrity;
- sanity relative to a prior generation;
- version correctness.

Only sanity is eligible for a force override. Structural integrity and version correctness are hard blockers. That distinction is exactly right and should be preserved.

### 2.4 Storage planes

The current three-store shape is useful:

- **bbolt**: control state, projects, resolutions, generations, references, jobs, active pointers;
- **Badger**: knowledge objects, chunks, manifests, content hashes;
- **vector backend**: embeddings and queryable points.

This should remain conceptually explicit. Control state and content state are not the same thing.

### 2.5 Query serving

The strongest part of query serving is that version scope is selected **before** vector retrieval.

Normal query modes resolve to:

- project-resolved version;
- latest retained version;
- compare project vs latest;
- all-retained for admin/debug use.

`all-retained` is intentionally outside the normal safety guarantee and should stay visibly marked as such.

The MCP surface is already legible:

- search dependency docs;
- get dependency version;
- list project dependencies;
- get release changes;
- knowledge status;
- sync project, disabled by default as the sole write path.

### 2.6 Garbage collection

The current reference-aware GC is good lifecycle engineering.

A dependency version is not safe to delete just because one project stopped referencing it. Retention must account for:

- references from other projects;
- explicit pins;
- latest references;
- current active generations;
- grace periods;
- backend-specific active state.

Deletion ordering is also correctly defensive:

```text
vector points
 -> Badger content
 -> bbolt bookkeeping
```

A crash should leave something either safely unqueryable or safely resumable, never half-queryable.

---

## 3. The new design principles

The recent paper discussions and architecture review add several principles without changing the core thesis.

### 3.1 Preserve structure instead of reconstructing it later

STAIR is useful because it formalizes a simple point: flattening content destroys signal.

For ragctl, useful structure already exists in richer forms than a document table of contents:

```text
dependency@version
  -> package/module
  -> file
  -> symbol
  -> section
  -> example/code block
```

The near-term task is not “build a learned structure-aware retriever.” It is:

> **Stop discarding cheap structure that is already available during normalization.**

### 3.2 Hard state constraints are not ranking features

Exact version identity should remain a hard precondition.

Do not degrade this:

```text
resolve version
 -> filter to valid generation
 -> rank inside valid state
```

into this:

```text
search everything
 -> add version as a score boost
```

The first is ragctl. The second is generic retrieval with metadata.

### 3.3 Returned evidence should be self-describing

Every chunk should be able to explain where it came from without forcing the query layer to perform an extra parent-object lookup.

Example breadcrumb:

```text
go.etcd.io/bbolt@1.3.11 > tx.go > (*Tx).Bucket
```

For prose:

```text
grpc-go@1.72.0 > Authentication > Transport Credentials > NewTLS
```

This makes location itself part of the evidence.

### 3.4 Retrieval should become layered, not merely denser

Longer-term retrieval path:

```text
query
 -> classify query shape
 -> resolve hard state
 -> exact/lexical candidates
 -> semantic candidates
 -> structural boost
 -> neighborhood expansion
 -> compact evidence bundle
 -> model
```

This remains compatible with local-first operation and does not require retraining a corpus-specific retriever after every dependency update.

### 3.5 Let evals decide which complexity earns permanence

Every attractive retrieval or optimization feature should be framed as a hypothesis.

Examples:

- Does overlap improve conceptual queries?
- Does section hierarchy improve recall after version filtering?
- Does SimHash meaningfully reduce work?
- Does graph + ragctl outperform either independently?
- Does embedding-model drift matter at the task level?

The eval repository exists to answer these questions before the implementation becomes permanent architecture.

---

## 4. Repository topology: what should be separate

### 4.1 Split by independent purpose, not by package boundary

The rule:

> **A new repo should represent an independently useful product, interface, or measurement boundary.**

Do not create separate repositories merely because a package is technically reusable.

### 4.2 Recommended GitHub-level shape now

```text
aleutianai/
  ragctl/
  context-evals/        # or ragctl-evals if scope should stay narrow
```

Potential later extraction only after a second real consumer:

```text
aleutianai/
  context-core/         # only if two+ products share a stable schema/contract
```

Do not create separate repos now for:

- normalization;
- chunking;
- fingerprinting;
- embedding adapters;
- drift tracking;
- storage abstractions.

Those are still implementation details of ragctl or the eval harness.

### 4.3 Why eval is different

The eval repo has a different scientific and operational role.

It should be able to evaluate:

- model-only;
- ragctl;
- repo-graph providers;
- ragctl + graph;
- future web/Context7-like systems;
- alternative embedding and retrieval strategies.

It should not need to import ragctl internals in order to grade ragctl.

That separation strengthens both projects:

```text
ragctl        = builds/serves state-aware dependency knowledge
context-evals = independently measures whether context systems help
```

---

## 5. Internal Go package reshuffling

### 5.1 Do not put ordinary Go code under `/go` unless ragctl becomes a true multi-language monorepo

For a Go-first repository, standard Go layout is clearer:

```text
ragctl/
  cmd/
  internal/
  docs/
  testdata/
```

Adding a top-level `go/` directory makes the import tree less idiomatic without solving a current problem.

The better reshuffle is to group existing Go packages by **responsibility/plane** so related packages are physically adjacent.

### 5.2 Proposed target layout

```text
ragctl/
├── cmd/
│   └── ragctl/
│
├── internal/
│   ├── discovery/
│   │   ├── project/
│   │   ├── resolver/
│   │   │   ├── goresolver/
│   │   │   ├── python/
│   │   │   └── node/
│   │   └── registry/
│   │
│   ├── knowledge/
│   │   ├── normalize/
│   │   │   ├── markdown/
│   │   │   ├── godoc/
│   │   │   ├── openapi/
│   │   │   └── proto/
│   │   ├── chunk/
│   │   │   ├── markdown/
│   │   │   ├── symbol/
│   │   │   ├── openapi/
│   │   │   └── proto/
│   │   └── fingerprint/
│   │
│   ├── pipeline/
│   │   ├── generation/
│   │   ├── replicate/
│   │   └── planner/
│   │
│   ├── lifecycle/
│   │   ├── validate/
│   │   ├── promote/
│   │   ├── retention/
│   │   └── gc/
│   │
│   ├── serve/
│   │   ├── query/
│   │   └── mcp/
│   │
│   ├── storage/
│   │   ├── control/
│   │   │   └── bbolt/
│   │   ├── content/
│   │   │   └── badger/
│   │   └── vector/
│   │
│   ├── providers/
│   │   ├── source/
│   │   │   └── git/
│   │   └── embedding/
│   │       └── ollama/
│   │
│   ├── domain/
│   └── config/
│
├── docs/
│   ├── architecture.md
│   ├── features/
│   └── internal/
│
└── testdata/
```

### 5.3 Why this shape

It makes package intent visible from the path.

Examples:

```text
internal/knowledge/normalize/markdown
internal/lifecycle/validate
internal/storage/control/bbolt
internal/serve/query
```

This is easier to reason about than many unrelated first-level directories under `internal/`.

### 5.4 Do not reshuffle everything in one giant refactor

A package move has low user value and high merge/conflict risk.

Recommended sequence:

1. finish or checkpoint active functional work;
2. add/confirm package-level tests;
3. move one responsibility group at a time;
4. update imports and docs;
5. run full test suite after each group;
6. avoid changing behavior in the same commit as package moves.

Suggested move order:

```text
1. normalize/chunk/fingerprint -> knowledge/
2. query/mcp -> serve/
3. bbolt/Badger/backend -> storage/
4. source/embedding -> providers/
5. generation/replicate/planner -> pipeline/
6. project/resolver/registry -> discovery/
```

Lifecycle is already conceptually grouped and can be left largely as-is unless current paths are inconsistent.

### 5.5 Go import-cycle caution

The new grouping must not introduce “layer” packages that import upward and downward.

Keep dependencies directional:

```text
domain/config
    ^
    |
storage/providers
    ^
    |
knowledge
    ^
    |
pipeline/lifecycle
    ^
    |
serve/cli
```

Subdirectories are organizational boundaries, not permission to create broad umbrella packages.

Avoid parent packages such as `knowledge` or `storage` accumulating “god interfaces” solely to simplify imports.

---

## 6. Normalization roadmap

### 6.1 Markdown: preserve heading paths

Immediate change:

- every normalized section/chunk receives a structured heading path;
- store the path directly on the chunk;
- preserve a display breadcrumb for MCP output.

Example metadata:

```json
{
  "section_path": ["Transactions", "Read transactions", "Bucket lookup"],
  "breadcrumb": "Transactions > Read transactions > Bucket lookup"
}
```

This is the lowest-cost, highest-confidence STAIR-inspired change.

### 6.2 Markdown: preserve fenced code blocks

Current behavior should evolve from “languages observed” to structured code-block preservation.

Proposed block record:

```json
{
  "index": 2,
  "language": "go",
  "content": "tx.Bucket(...)", 
  "section_path": ["Transactions", "Bucket lookup"]
}
```

Important: preserve linkage between code and explanatory prose.

Do not automatically strip code into an isolated corpus with no parent context.

### 6.3 OpenAPI normalizer

Add a normalizer that supports YAML/JSON containing OpenAPI/Swagger root keys.

Granularity:

```text
one KnowledgeObject per path + HTTP method operation
```

Suggested identity fields:

- API title/version;
- path;
- method;
- operationId;
- tags;
- request schema refs;
- response schema refs;
- deprecated flag;
- source path;
- dependency/version identity.

This mirrors per-symbol granularity and gives agents retrievable API operations rather than monolithic specification files.

### 6.4 Protobuf normalizer

Granularity:

- service object;
- one knowledge object per RPC method;
- optionally message/enum objects when referenced strongly enough to be useful.

Metadata:

- package;
- service;
- RPC name;
- request type;
- response type;
- streaming mode;
- comments;
- source file;
- dependency/version.

### 6.5 Source-symbol normalization

Do not immediately build a universal AST platform.

Near-term:

- preserve existing package/file/symbol metadata where already available;
- use language-native tooling where cheap;
- defer universal tree-sitter integration until evals show source-symbol chunking materially helps.

The repo-graph provider may already own project-side AST/LSP structure. ragctl should not duplicate that role unnecessarily.

---


## 6A. Multi-text source mappings and curated text ingestion

ragctl should stay focused on **text-first knowledge** for now. Video/audio ingestion is deliberately out of scope. Text sources are already powerful enough to validate the core thesis without introducing media extraction, platform Terms-of-Service questions, ASR quality, or multimodal preprocessing.

The registry should support **multiple curated text sources for one dependency**. A dependency entry is not limited to its source repository or official documentation.

Example:

```text
k8s.io/client-go@v0.34.1
    |
    +-- exact source repository
    +-- version-matched official docs
    +-- selected Kubernetes documentation pages
    +-- selected technical articles
    +-- selected books / chapters
    +-- internal wiki pages
    +-- internal runbooks
    +-- organization-specific architecture docs
```

The central rule remains:

> Dependency state determines what software version is true; the source registry determines which knowledge sources are allowed to explain that state.

### Registry layering

Source mappings should be user-selectable and composable:

```text
Aleutian built-in registry
        ↓
organization/user registry
        ↓
project-local registry
```

A user should be able to:

- use Aleutian's curated source list unchanged;
- extend it with internal sources;
- override individual sources;
- replace it entirely with an organization-owned registry.

Eventually the manifest format should support explicit `extend` and `replace` semantics rather than relying only on whole-manifest precedence.

### Text source applicability

Different text sources have different relationships to a dependency version. Applicability should therefore be modeled independently from source type.

Useful dimensions include:

```text
version applicability:
  exact
  range
  unknown / conceptual

temporal applicability:
  published_at
  knowledge_as_of

scope:
  API_REFERENCE
  CONCEPTUAL
  MIGRATION
  OPERATIONS
  ARCHITECTURE
  EXAMPLE
  RUNBOOK

authority:
  upstream source
  official documentation
  organization documentation
  curated third-party material

curation provenance:
  Aleutian registry
  organization registry
  project-local override
```

A source can be conceptual and still be useful even if it cannot be tied to an exact version. Exact source and exact-version documentation should dominate API/signature questions, while books, architecture pages, and internal wikis may be better for conceptual or operational questions.

### Website and wiki ingestion should be explicitly curated, not broadly spidered

For v0.x, ragctl should **not crawl arbitrary websites or recursively spider an entire wiki**.

A registry entry should name the exact page or explicit page set that the curator wants indexed.

Examples:

```yaml
sources:
  - id: k8s-deployments
    type: web
    url: https://kubernetes.io/docs/concepts/workloads/controllers/deployment/

  - id: k8s-controller-concepts
    type: web
    urls:
      - https://kubernetes.io/docs/concepts/architecture/controller/
      - https://kubernetes.io/docs/concepts/architecture/leases/
```

For internal systems:

```yaml
sources:
  - id: acme-kubernetes-runbooks
    type: confluence
    pages:
      - "123456"
      - "123987"
      - "124212"
```

or a generic prepared-page representation:

```yaml
sources:
  - id: platform-wiki
    type: web
    urls:
      - https://wiki.example.com/platform/kubernetes
      - https://wiki.example.com/platform/kubernetes/upgrade-policy
```

The principle is:

> **Curators select the corpus; ragctl acquires and indexes it.**

Do not make a crawler silently decide which neighboring pages become trusted context.

### Optional explicit collections

A source mapping may contain many pages, but the mapping should still be explicit. For larger curated sets, allow a manifest or collection file:

```yaml
collection:
  id: acme-kubernetes
  dependency: k8s.io/client-go
  sources:
    - ...
    - ...
    - ...
```

This provides convenience without converting ragctl into a general web crawler.

### Internal wiki / Confluence support

Confluence or internal wiki support should be treated as another **text acquisition provider**, not as a new retrieval architecture.

The acquisition contract should be approximately:

```text
configured page ID / URL
    ↓
authorized fetch
    ↓
preserve title / headings / links / code blocks / tables
    ↓
normalize into KnowledgeObjects
    ↓
attach source + applicability + curation provenance
    ↓
generation pipeline
```

Important requirements:

- authentication is user-provided and never embedded in registry files;
- indexing is limited to explicitly configured pages/page IDs;
- linked pages are not recursively ingested unless explicitly listed;
- original page URL/ID and revision timestamp/version are retained;
- page updates should produce a new source fingerprint and therefore a new candidate generation when relevant;
- internal documentation can receive high organizational relevance without being confused with upstream software authority.

### Source precedence should be query-sensitive

The evidence hierarchy should depend on query shape.

For exact API questions:

```text
exact dependency source
> exact-version official docs
> organization docs
> third-party prose
> model memory
```

For operational questions:

```text
organization runbook
> organization architecture/wiki
> upstream operational docs
> third-party conceptual material
```

For conceptual questions:

```text
official conceptual docs
+ high-quality books/articles
+ relevant source/examples
```

This should initially be evaluated rather than encoded as a complicated learned router.

### Out of scope for now

Explicitly defer:

- YouTube/video downloading;
- ASR/transcription;
- multimodal video analysis;
- broad recursive web crawling;
- automatic whole-Confluence-space ingestion;
- automatic discovery of arbitrary third-party sources;
- a standalone media preprocessing repository.

The immediate source expansion target is **curated text**:
repository source, Markdown, HTML/web pages, internal wiki pages, books/chapters where licensing permits, OpenAPI, protobuf, and user-prepared text.


## 7. Stub and content-quality detection

Stub detection is useful, but **stub-ness is not authority**.

An official repository page that says “Coming soon” is still authoritative. It is simply low-information.

Recommended metadata split:

```text
Authority        = how trustworthy is the source?
ContentQuality   = how useful/complete is this content?
```

Possible `ContentQuality` values:

- normal;
- stub;
- generated-noise;
- sparse;
- deprecated.

Retrieval may penalize low-quality content while keeping provenance accurate.

Avoid mutating source authority merely because content is short or incomplete.

---

## 8. Chunking roadmap

### 8.1 Make chunks self-describing

Promote relevant parent metadata onto `domain.Chunk.Metadata`:

- dependency;
- version;
- source type;
- package/module;
- file;
- symbol;
- section path;
- code-block identity where applicable.

A query result should not need another lookup just to tell the agent where the chunk belongs.

### 8.2 Token-aware sizing

Move from raw bytes toward a pluggable estimator:

```go
type TokenEstimator func(string) int
```

Initial default can remain approximate.

Do not add heavyweight tokenizer dependencies until an actual model-specific limit requires them.

### 8.3 Overlap

Treat overlap as an experiment, not a doctrine.

Test at least:

- no overlap;
- fixed overlap;
- structural neighborhood expansion.

The important hypothesis is whether **semantic structure beats redundant byte overlap** for developer documentation.

### 8.4 Neighborhood expansion

Preferred direction:

```text
hit
 -> parent section
 -> adjacent relevant siblings
 -> referenced symbol/example
 -> compact evidence bundle
```

This is more meaningful than blindly duplicating 200 bytes around every boundary.

---

## 9. Fingerprinting and deduplication

### 9.1 Keep exact hashing as the core correctness mechanism

Exact content hashing is deterministic, interpretable, and safe.

It should remain the primary identity/reuse mechanism.

### 9.2 SimHash near-duplicate detection: defer until measured

SimHash is a plausible optimization for dependency-version upgrades where most content changes only slightly.

But it adds:

- threshold tuning;
- near-hit indexing;
- false-positive risk;
- more complex manifests;
- chunk-diff logic;
- cross-generation search costs.

Before implementation, measure:

```text
on N real version upgrades:
- percentage of objects reused by exact hash;
- percentage newly created;
- bytes/chunks reprocessed;
- wall-clock time spent re-chunking;
- embedding work attributable to tiny textual changes.
```

Only build near-dup reuse if the wasted work is actually material.

### 9.3 Chunk-level exact reuse may be simpler than SimHash

If optimization becomes necessary, consider first whether independently fingerprinting chunks gives enough reuse without introducing fuzzy identity.

Prefer the simplest mechanism that preserves correctness.

---

## 10. Embedding roadmap

### 10.1 Add bounded concurrency

`WithConcurrency(n)` for independent batches is a straightforward performance improvement.

Requirements:

- default `n=1`;
- deterministic result ordering;
- bounded worker pool;
- deterministic error semantics;
- metrics for batch latency and throughput.

### 10.2 Embedding comparison belongs primarily in eval tooling

A command that compares candidate and current embedding models is useful, but it should initially live in the eval repo or as a thin experimental command rather than the core sync path.

Useful outputs:

- cosine similarity distribution on identical chunks;
- retrieval top-K overlap;
- task-outcome delta;
- latency;
- memory usage;
- embedding throughput.

Embedding-space similarity alone should not become a promotion criterion without evidence that it predicts retrieval or task regressions.

---

## 11. Orphan cleanup: correctness work to do soon

The current GC handles **no-longer-referenced successful generations**.

A separate class exists:

- failed generations;
- interrupted generations;
- candidates stuck in nonterminal states;
- content written before a crash but never promoted.

These can leak storage indefinitely.

### 11.1 Proposed orphan state

Add an orphan planner that selects generations that are:

- FAILED, or;
- nonterminal beyond an age threshold;
- not active;
- not currently protected by an in-flight lease/job.

Suggested config:

```text
Retention.OrphanAge = 24h
```

### 11.2 Safety

Start with:

```text
ragctl gc --orphans --dry-run
ragctl gc --orphans
```

Do not make orphan cleanup implicit until behavior is well tested.

### 11.3 Backend deletion API

Prefer a lifecycle-specific generation deletion capability over turning the normal query `Filter` into a catch-all administrative selector.

For example:

```go
DeleteGeneration(ctx context.Context, generationID string) error
```

if backend implementations can support it cleanly.

This keeps user-query filtering semantics separate from lifecycle management semantics.

---

## 12. Drift: split operational integrity from evaluation

“Drift” currently covers several different concepts that should not be conflated.

### 12.1 ragctl-owned integrity checks

These belong in ragctl:

- unchanged content unexpectedly receives incompatible identity;
- active-generation pointer is inconsistent;
- stored point metadata disagrees with generation/version;
- same deterministic embedder configuration produces unexpectedly unstable output beyond a documented tolerance.

This is operational integrity.

### 12.2 eval-owned behavioral drift

These belong in `context-evals`:

- embedding model A vs B;
- retrieval rank changes;
- top-K overlap changes;
- version-specific answer changes;
- coding-task success changes;
- query-type-specific regressions;
- latency/token/cost changes.

This is evaluation.

### 12.3 Naming

For unchanged bytes embedded under the same configuration, prefer:

```text
EmbeddingConsistencyScore
```

over:

```text
EmbeddingDriftScore
```

because large movement is probably nondeterminism, a provider change, or a bug rather than expected semantic drift.

### 12.4 Golden queries vs canary queries

Use terminology carefully.

- **golden query**: expected behavior is human-reviewed and trusted;
- **canary query**: automatically generated/seeded probe intended to detect gross change.

Do not automatically manufacture “goldens” and then treat them as ground truth.

---

## 13. Query-serving enhancements

### 13.1 Breadcrumbs

Return structural identity with every hit.

Examples:

```text
grpc-go@1.72.0 > credentials > credentials.go > NewTLS
bbolt@1.3.11 > tx.go > (*Tx).Bucket
```

The agent should see:

- exact dependency;
- exact version;
- source;
- structural path;
- trust/security note;
- content.

### 13.2 Query-type awareness

Useful initial categories:

- `SYMBOL_LOOKUP`;
- `CONCEPTUAL`;
- `VERSION_LOOKUP`;
- `VERSION_TRAP`;
- `MIGRATION`;
- `AMBIGUOUS_CONTEXT`.

Do not necessarily build an LLM classifier first. The eval framework can label fixtures explicitly and reveal whether specialized retrieval policies are worthwhile.

### 13.3 Clarification

A key new evaluation concept is:

> **The correct action is sometimes to ask for missing state rather than retrieve harder.**

Examples:

- multiple `Foo` symbols match;
- target upgrade version is unspecified;
- runtime constraints are missing;
- “newer API” has multiple valid interpretations.

Potential runtime behavior later:

```text
authoritative state sufficient?
  yes -> retrieve/act
  no  -> expose ambiguity / request clarification
```

This should first be tested in evals before becoming complex production orchestration.

---

## 14. The repo-graph + ragctl symbol join

This is the most compelling cross-system feature to prototype.

The customer/repo graph and dependency knowledge share a natural key:

> **the dependency symbol used at the call site.**

Flow:

```text
project source
 -> graph/LSP resolves call site
 -> external symbol identity
 -> dependency/module identity
 -> exact project-resolved version
 -> ragctl exact-version source/docs
 -> evidence bundle
```

Example:

```text
client.go calls bbolt.(*Tx).Bucket
    -> graph resolves external symbol
    -> resolver says go.etcd.io/bbolt@1.3.11
    -> ragctl fetches exact symbol/docs/source for 1.3.11
```

This is valuable because both sides contribute deterministic information:

- graph system: **what symbol is this code referring to?**
- ragctl: **what knowledge is valid for that dependency version?**

Then the LLM interprets the evidence.

### 14.1 Keep provider interface thin

Do not build a control plane.

Potential provider output:

```go
type ExternalSymbolRef struct {
    Ecosystem    string
    Module       string
    Package      string
    QualifiedName string
    SourceFile   string
}
```

ragctl then owns mapping that identity into its resolved dependency state.

### 14.2 Candidate providers

Start with one.

Possible families:

- SCIP;
- LSP-derived symbol information;
- CodebaseMemory;
- GitNexus;
- another existing graph/index provider.

Choose the one that gives the cleanest deterministic symbol identity with the least integration work.

---

## 15. The independent eval repository

Recommended name:

```text
context-evals
```

if the goal is a reusable public benchmark across context systems.

Use:

```text
ragctl-evals
```

if you want to keep scope intentionally narrow for the first release.

My preference is `context-evals`, but keep the README honest that the first benchmark is ragctl-focused.

### 15.1 Repository layout

```text
context-evals/
├── cmd/
│   └── context-evals/
│
├── internal/
│   ├── runner/
│   ├── conditions/
│   ├── adapters/
│   │   ├── modelonly/
│   │   ├── ragctl/
│   │   ├── graph/
│   │   └── composite/
│   ├── graders/
│   │   ├── tooluse/
│   │   ├── retrieval/
│   │   ├── version/
│   │   ├── code/
│   │   ├── clarification/
│   │   └── environment/
│   ├── metrics/
│   ├── stats/
│   └── reports/
│
├── fixtures/
│   ├── go/
│   ├── python/
│   └── node/
│
├── tasks/
│   ├── version_lookup/
│   ├── version_trap/
│   ├── retrieval/
│   ├── coding/
│   ├── dynamic_update/
│   └── ambiguous_context/
│
├── results/
└── docs/
```

If you prefer Python for experimentation/statistics, the eval repo does not have to be Go. Independence is more important than language symmetry.

### 15.2 Independence rule

The evaluator may call ragctl through public interfaces:

- CLI;
- MCP;
- stable exported protocol/API later.

It should not import internal Go packages.

That prevents the benchmark from accidentally grading implementation details unavailable to a real agent.

---

## 16. Eval matrix

The most important experiment is causal decomposition.

Recommended conditions:

```text
A. model only
B. flat retrieval
C. flat retrieval + exact-version filter
D. hierarchy-aware retrieval + exact-version filter
E. repo graph only
F. repo graph + ragctl
```

Optional later:

```text
G. web
H. Context7-like provider
I. alternative embedding model
```

### 16.1 H1: version hypothesis

> Exact dependency-state filtering improves correctness over flat retrieval.

Primary comparison:

```text
B vs C
```

If this is the biggest gain, it strongly validates ragctl's core thesis.

### 16.2 H2: structure hypothesis

> Preserving hierarchy improves retrieval and downstream outcomes after version state is controlled.

Primary comparison:

```text
C vs D
```

This is the STAIR-inspired experiment.

### 16.3 H3: composition hypothesis

> Project structure and exact dependency knowledge are complementary.

Primary comparison:

```text
C/D vs E vs F
```

If `F` does not beat the independent systems, do not build a context controller.

---

## 17. Query/task taxonomy

### 17.1 VERSION_LOOKUP

Examples:

- exact installed version;
- whether an API exists in the pinned version;
- exact behavior of a version-specific option.

### 17.2 VERSION_TRAP

The signature ragctl category.

Construct tasks where:

- latest docs differ from pinned docs;
- an API exists upstream but not in the repo version;
- an argument changed;
- an API was renamed;
- a behavior changed between two versions.

### 17.3 SYMBOL_LOOKUP

Example:

> What does `Tx.Bucket` return in the dependency pinned here?

Likely winner: exact lexical/symbol identity plus hard version state.

### 17.4 CONCEPTUAL

Example:

> How does bbolt's transaction model work?

Likely winner: semantic retrieval plus hierarchy/neighborhood context.

### 17.5 CODING_TASK

Examples:

- implement against exact installed API;
- fix compile failure;
- migrate deprecated API;
- update dependency and repair code;
- diagnose version-induced runtime issue.

Ground truth should be tests/builds wherever possible.

### 17.6 DYNAMIC_UPDATE

Core lifecycle test:

```text
fixture at dependency version A
 -> run task
 -> update dependency to B
 -> ragctl scan/sync
 -> rerun equivalent task
 -> expected knowledge/output changes
```

This tests whether knowledge tracks actual repository state rather than static corpus memory.

### 17.7 NO_TOOL / ABSTENTION

The right answer may be:

- no external lookup needed;
- evidence unavailable;
- cannot support the requested claim.

### 17.8 AMBIGUOUS_CONTEXT / CLARIFICATION

Measure whether the agent detects missing critical facts.

Metrics:

- missing-critical-field recall;
- unnecessary-question rate;
- silent-assumption rate;
- correct stopping;
- number of questions to resolution;
- task success after clarification.

---

## 18. Evaluation layers

### 18.1 Tool use

Measure:

- correct tool selection;
- correct dependency;
- correct project/version scope;
- correct arguments;
- unnecessary calls;
- failure to call when needed.

### 18.2 Retrieval

Measure:

- Recall@K;
- Precision@K;
- MRR;
- NDCG if useful;
- exact-version accuracy;
- cross-version contamination;
- authority/source correctness.

Signature metrics:

```text
VersionAccuracy
CrossVersionContaminationRate
```

### 18.3 Evidence use

Measure whether the model:

- cites/uses the correct version;
- contradicts retrieved evidence;
- makes unsupported claims;
- uses deprecated/latest-only APIs incorrectly.

### 18.4 Task outcome

Prefer deterministic grounding:

- compiler;
- tests;
- hidden tests;
- lint/static checks;
- runtime behavior;
- exact API assertions.

### 18.5 Efficiency

Measure:

- tokens;
- wall-clock time;
- tool calls;
- embedding calls;
- network use;
- number of iterations.

### 18.6 Clarification/readiness-to-act

Question:

> Given what the agent actually knew, did it make the right decision about whether it was ready to act?

This is a stronger production eval than grading final prose alone.

---

## 19. Grounding hierarchy for evals

Prefer stronger evidence when possible:

```text
weak
  self-assessment
  LLM-as-judge
  deterministic transcript checks
  environment-grounded checks
  real workflow outcome
strong
```

LLM judges remain useful for qualities that lack deterministic ground truth, but they should not be the default when compilers, tests, API results, or fixture assertions are available.

---

## 20. Future learned-state / memory evaluations

This is not a current ragctl feature, but the eval framework should leave conceptual room for it.

Rule:

> **Reflection proposes; evaluation disposes.**

If an agent proposes a persistent heuristic, memory update, or procedural rule:

```text
candidate memory update
 -> held-out grounded evaluation
 -> promote only if outcome improves
```

Do not allow generated self-critique to become authoritative state simply because it sounds plausible.

This aligns with ragctl's broader philosophy: authoritative state should be externally grounded.

---

## 21. What not to build now

Explicit non-goals protect the project.

Do not build yet:

- a generative/DSI retriever;
- corpus-specific retriever training;
- YouTube/video acquisition or ASR;
- broad recursive website/wiki crawling;
- universal tree-sitter infrastructure;
- a context-control plane;
- a large agent-memory subsystem;
- SimHash near-dup reuse before profiling;
- embedding-drift dashboards before metrics are validated;
- multi-provider orchestration;
- a SaaS/enterprise wrapper;
- a giant visualization UI;
- automatic “golden” query generation presented as ground truth.

These can be revisited after evidence exists.

---

## 22. Recommended implementation sequence

### Phase 0 - repository hygiene

- create `context-evals`;
- decide whether package regrouping is worth doing immediately;
- if yes, perform package moves as behavior-free commits;
- ensure full Go test suite is green;
- update architecture docs/import references.

### Phase 1 - cheap structural preservation

- add structured Markdown heading paths;
- put breadcrumbs directly on chunks;
- preserve fenced code blocks;
- return breadcrumbs in MCP results;
- add tests for chunk self-description.

### Phase 2 - correctness gap

- implement orphan discovery;
- add `OrphanAge`;
- add dry-run orphan GC;
- implement generation-scoped deletion;
- test interrupted sync and failed-generation cleanup.

### Phase 3 - eval scaffolding

Define stable schemas for:

- task;
- condition;
- run;
- tool trace;
- result;
- grade.

Implement:

- model-only adapter;
- ragctl adapter;
- deterministic runner;
- JSONL or SQLite result storage.

### Phase 4 - first benchmark

Start with Go only.

Build:

- VERSION_LOOKUP;
- VERSION_TRAP;
- SYMBOL_LOOKUP;
- small CODING_TASK set;
- DYNAMIC_UPDATE.

Conditions:

```text
model only
flat retrieval
flat + exact version
```

This answers the most important question first.

### Phase 5 - structure experiment

Add:

- heading-aware retrieval;
- neighborhood expansion;
- code-block retrieval.

Compare:

```text
flat + version
vs
structure + version
```

Only keep complexity that produces measurable wins.

### Phase 6 - structured artifact normalizers

Add:

- OpenAPI;
- protobuf.

Create task fixtures that specifically require them.

### Phase 7 - graph symbol join

Choose one graph provider.

Implement the smallest path:

```text
call site
 -> external symbol
 -> exact dependency
 -> exact version
 -> ragctl evidence
```

Then run the composite condition in evals.

### Phase 8 - performance optimization

Only after profiling:

- embedding concurrency;
- chunk token estimator;
- overlap experiment;
- exact chunk reuse;
- SimHash if justified.

### Phase 9 - drift research

Move experimental behavioral drift into `context-evals`.

Add:

- embedding-model comparisons;
- retrieval overlap;
- generation-history task regression;
- canary queries;
- human-reviewed golden queries.

---

## 23. Concrete issue backlog

### P0

- [ ] Create independent eval repo.
- [ ] Define eval task/run/result schemas.
- [ ] Add Markdown `section_path`.
- [ ] Add chunk breadcrumbs.
- [ ] Add structured code-fence preservation.
- [ ] Add `VERSION_LOOKUP` fixtures.
- [ ] Add `VERSION_TRAP` fixtures.
- [ ] Add model-only and ragctl eval adapters.
- [ ] Add exact-version-only benchmark condition.
- [ ] Implement orphan-GC dry run.

### P1

- [ ] Environment-grounded coding grader.
- [ ] Dynamic-update fixtures.
- [ ] Neighborhood expansion experiment.
- [ ] OpenAPI normalizer.
- [ ] Proto normalizer.
- [ ] Orphan deletion execution path.
- [ ] Embedding bounded concurrency.
- [ ] Query breadcrumb formatting over MCP.
- [ ] First public benchmark report.

### P2

- [ ] Graph provider adapter.
- [ ] Call-site external-symbol join.
- [ ] Ambiguous-context/clarification tasks.
- [ ] Canary query support.
- [ ] Token-aware chunk estimator.
- [ ] Overlap A/B experiment.

### P3 / evidence-gated

- [ ] SimHash near-duplicate index.
- [ ] Near-dup chunk reuse.
- [ ] Embedding consistency history.
- [ ] Human-reviewed golden query registry.
- [ ] Multi-ecosystem benchmark expansion.
- [ ] Alternative retrieval providers.
- [ ] Context-controller experiments.

---

## 24. Acceptance criteria for the next public milestone

The next release should be able to demonstrate all of the following:

- A real repository is scanned and its exact dependency state resolved.
- An exact dependency/version is acquired and turned into a validated active generation.
- Query results visibly identify dependency, version, source, and structural breadcrumb.
- A version-trap task fails or performs worse without exact state and succeeds more often with ragctl.
- Updating the fixture dependency causes the active knowledge state to update and the benchmark result to follow it.
- The benchmark is reproducible without hidden network dependence.
- Raw tasks, outputs, and graders are public.
- At least one deterministic task-level metric is reported, not just retrieval similarity.
- Failed/abandoned generations can be detected and safely reclaimed.
- The README explains both where ragctl helps and where it does not.

---

## 25. The public technical story

The strongest launch story is not:

> “I built a local RAG server for dependency docs.”

It is:

> **Coding agents routinely answer against the wrong version of a dependency. ragctl binds knowledge to the repository's real dependency state, validates that knowledge before promotion, and serves only the version that is actually true for the project. Here is the benchmark showing when that matters.**

Then the structure work becomes an additional finding:

> Once version state is correct, preserving section/symbol structure may improve conceptual retrieval and task outcomes.

And the graph integration becomes:

> Project-side code intelligence identifies what symbol the code actually uses; ragctl supplies authoritative knowledge for that symbol at the exact pinned dependency version.

That is a coherent progression rather than a collection of RAG features.

---

## 26. Final architectural boundary

The clean boundary to preserve is:

```text
repo / build state
       |
       v
dependency resolution
       |
       v
authoritative generation state
       |
       v
structured knowledge
       |
       v
retrieval
       |
       v
evidence bundle
       |
       v
agent
```

The model may eventually become much better at asking for information, reranking it, and packing context.

ragctl's durable responsibility is different:

> **Know which dependency knowledge is true now, preserve where that knowledge came from, and make state changes observable and testable.**

The eval repository answers the complementary question:

> **Does giving the agent that state-aware evidence actually improve real outcomes?**

That is the next phase.
