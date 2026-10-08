# depctl — Backlog Tickets (deferred until the v0.1 slice is stable)

Source: `depctl_implementation_plan.md` + `depctl_design_spec.md`. See also [../planned/README.md](../planned/README.md) for the v0.1 critical-path epics that should be built first.

These epics are specced out so the work is ready to pick up, but the implementation plan is explicit that none of them should start before the Go-only vertical slice (planned/01 through planned/19) is working end-to-end and someone has used it themselves. Several of these — extra ecosystems, extra vector backends, eval/observability tooling, optional adapters — map directly to the plan's "Do Not Build Yet" list and its closing rule: *"Do not add another integration because it is interesting. A new integration belongs in core only if it materially improves dependency correctness, knowledge freshness, lifecycle safety, provenance, interoperability, or maintenance cost. Otherwise: adapter, example, community plugin, or not yet."*

Same ticket format as `planned/`: each epic has an `INDEX.md`, each ticket is self-contained (Goal, Non-goals, Simplicity constraints, Design, Inputs/Outputs, Failure behavior, Tests, Acceptance criteria).

## Additional ecosystems (one at a time, after the Go slice is stable)

20. Python resolver — done, moved to [../completed/20-python-resolver](../completed/20-python-resolver/INDEX.md).
21. Node resolver — done (2026-09), moved to [../completed/21-node-resolver](../completed/21-node-resolver/INDEX.md).
22. Rust resolver — declined (2026-09-30), moved to [../completed/62-rust-resolver](../completed/62-rust-resolver/INDEX.md) (renumbered from 22 to avoid colliding with `completed/22-orphan-lifecycle-gc`'s existing number).
23. Java resolver — declined (2026-09-30), moved to [../completed/63-java-resolver](../completed/63-java-resolver/INDEX.md) (renumbered from 23 for the same reason).

## Broader acquisition and backend coverage

24. [24-website-acquisition](24-website-acquisition/INDEX.md) — HTTP client, sitemap reader, HTML normalizer
25. Vector backends — moved to [../completed/25-additional-vector-backends](../completed/25-additional-vector-backends/INDEX.md) (2026-10-05): every ticket done. Supported stores are Qdrant (managed or your own), pgvector, Weaviate, and an embedded file with no service.

## Quality, ops, and ecosystem integration

26. [26-evaluation-framework](26-evaluation-framework/INDEX.md) — eval cases, deterministic version-correctness metrics, `depctl eval`, promotion gate
27. Observability — done, moved to [../completed/27-observability](../completed/27-observability/INDEX.md).
28. [28-external-eval-integrations](28-external-eval-integrations/INDEX.md) — Ragas/DeepEval/Promptfoo exports, Phoenix/Langfuse docs
29. Security hardening — done, moved to [../completed/29-security-hardening](../completed/29-security-hardening/INDEX.md).
30. Local retrieval mode — moved to [../completed/30-local-retrieval-mode](../completed/30-local-retrieval-mode/INDEX.md) (2026-10-06): `retrieval.mode` auto/vector/keyword; keyword search (plain-Go BM25) whenever Ollama isn't available.
31. [31-llamaindex-adapter](31-llamaindex-adapter/INDEX.md) — external normalizer protocol, example sidecar
32. [32-client-integration-examples](32-client-integration-examples/INDEX.md) — LibreChat, Goose, generic MCP client docs
65. Cross-agent memory connectors — moved to [../planned/65-cross-agent-memory-connectors](../planned/65-cross-agent-memory-connectors/INDEX.md) (2026-09-30), picked up for active implementation.

## Dataset asset handling

33. [33-dataset-descriptors](33-dataset-descriptors/INDEX.md) — `dataset.json` provenance sidecars for raw non-git datasets (`hack/fetch-geodata`), HTTP hashing, optional GDAL spatial inspection, snapshot comparison. Not sourced from the original implementation plan doc — added after the corpus grew a `data/*` category of raw datasets; see [[depctl-rag-scope-raw-data-vs-docs]] for why these stay out of the RAG pipeline and out of the knowledge registry.

## Registry coverage

34. [34-registry-coverage](34-registry-coverage/INDEX.md) — manifest fallback, candidate source discovery, source liveness check.

## Registry federation

38. [38-registry-federation](38-registry-federation/INDEX.md) — wiring the already-built-but-unused project registry override directory, extend/replace manifest semantics, source-tier provenance (`TrustUser`), and per-source version scope. Also sourced from `docs/scratch/depctl_architecture_eval_next_steps-2.md` (§6A) — the "bring your own source registry" requirement.

## Curated acquisition, structured normalizers, retrieval experiments, and the repo-graph join

Not sourced from the original implementation plan doc — added from `docs/scratch/depctl_architecture_eval_next_steps-2.md` after the v0.1 slice matured enough to surface these as concrete, scoped gaps.

39. [39-curated-text-acquisition](39-curated-text-acquisition/INDEX.md) — explicit curator-selected web pages and Confluence/internal-wiki acquisition, extending (not duplicating) epic 24's HTTP client and HTML normalizer.
40. [40-structured-artifact-normalizers](40-structured-artifact-normalizers/INDEX.md) — OpenAPI and protobuf normalizers, one `KnowledgeObject` per operation/RPC method.
41. [41-retrieval-and-embedding-experiments](41-retrieval-and-embedding-experiments/INDEX.md) — evidence-gated: bounded embedding concurrency, token-aware chunk sizing, chunk-overlap experiment run via the separate `context-evals` repository.
44. [44-containerized-embedding-backend](44-containerized-embedding-backend/INDEX.md) — an opt-in `depctl backend up` for a self-contained, containerized Ollama + embedding model, as a second installation path alongside (never a replacement for) native Ollama — the daemon itself stays native regardless, per ADR-010.
46. [46-dependency-change-events](46-dependency-change-events/INDEX.md) — a standalone `DependencyChangeEvent` record persisted whenever `planner.Plan` detects a real version transition, infrastructure for a future Upgrade Manager, not that feature itself. Sourced from a 2026-09 competitive review.

Epic 45 ([45-competitive-validation](../completed/45-competitive-validation/INDEX.md)) moved to `planned/`, then `completed/` — all five tickets (VALID-001..004, POS-001) shipped in one pass.

Epic 50 ([50-go-monorepo-workspace-resolution](../completed/50-go-monorepo-workspace-resolution/INDEX.md)) was filed directly into `completed/` — a same-session fix (MONO-001) for a gap STRESS-001 found live, never sat in `backlog/` waiting to be picked up.

Epic 51 ([51-bulk-sync-batch-timeout](../completed/51-bulk-sync-batch-timeout/INDEX.md)) moved to `completed/` (2026-09) — BATCH-001's Options C+D shipped and were confirmed at real scale against `hashicorp/terraform` (555/569 synced, zero aggregate-timeout occurrences across the full run).

Epic 43 ([43-local-cache-reuse](../completed/43-local-cache-reuse/INDEX.md)) moved to `planned/`, then `completed/` (2026-09) — GIT-005 shipped first after real usage showed the cost this epic was waiting for; GIT-004/006/007 shipped in the same later pass, all four tickets now done.

Epic 42 ([42-repo-graph-symbol-join](../completed/42-repo-graph-symbol-join/INDEX.md)) moved to `planned/` and then `completed/` — a 2026-09 competitive review reaffirmed this join's shape independently and prompted scoping it for real implementation (GRAPH-003/004 added, settling the provider choice GRAPH-001 originally deferred), and all four tickets shipped in one pass.

## Ticket ID prefixes in this directory

`PY / HTTP / VEC (010, 011, 014, 015, 016) / EVAL / OBS / EXT / SEC / LOCAL / LLAMA / CLIENT / DATA` — these match the task IDs used in the implementation plan directly, except `DATA` (see above). `MEM0`/`GRAPHITI`/`LETTA`/`COGNEE` (epic 65) have moved to `planned/README.md` along with the epic (2026-09-30, picked up for active implementation). `NODE` (epic 21) has moved to `completed/README.md` along with the epic (2026-09). `RUST`/`JAVA` (epics 22/23) have moved to `completed/README.md` (2026-09-30), declined rather than shipped — see that README for why. `VEC-012`/`VEC-013` have likewise moved to `completed/README.md` (2026-09-30), declined. `VEC-011`/`VEC-014` were declined the same day, then revived back here 2026-10-01 alongside new ticket `VEC-016`. `REG` (epic 38, continuing from REG-008) continues epics 07/34's prefix. `NORM` (epic 40) continues epic 09's prefix into backlog use; `EMB`/`CHUNK` (epic 41) continue epics 12/10's prefixes. `CONF` (epic 39) is a new prefix, since Confluence/wiki acquisition doesn't exist yet elsewhere. `GRAPH` (epic 42) has moved to `completed/README.md` along with the epic. `MONO` (epic 50) has likewise moved to `completed/README.md`. `BATCH` (epic 51) has likewise moved to `completed/README.md` (2026-09). `GIT` (epic 43, continuing from GIT-003) has likewise moved to `completed/README.md` (2026-09) — no epic here continues that prefix into backlog use anymore.

## A note on `26-evaluation-framework`

Its `INDEX.md` now carries an unresolved status note: `docs/scratch/depctl_architecture_eval_next_steps-2.md` proposes an independent `context-evals` repo that grades depctl only through its public CLI/MCP surface, never importing internal packages — in tension with this epic's in-repo `internal/eval` design. Read the note before picking up any EVAL-* ticket.
