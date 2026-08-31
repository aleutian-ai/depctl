# ragctl — Backlog Tickets (deferred until the v0.1 slice is stable)

Source: `ragctl_implementation_plan.md` + `ragctl_design_spec.md`. See also [../planned/README.md](../planned/README.md) for the v0.1 critical-path epics that should be built first.

These epics are specced out so the work is ready to pick up, but the implementation plan is explicit that none of them should start before the Go-only vertical slice (planned/01 through planned/19) is working end-to-end and someone has used it themselves. Several of these — extra ecosystems, extra vector backends, eval/observability tooling, optional adapters — map directly to the plan's "Do Not Build Yet" list and its closing rule: *"Do not add another integration because it is interesting. A new integration belongs in core only if it materially improves dependency correctness, knowledge freshness, lifecycle safety, provenance, interoperability, or maintenance cost. Otherwise: adapter, example, community plugin, or not yet."*

Same ticket format as `planned/`: each epic has an `INDEX.md`, each ticket is self-contained (Goal, Non-goals, Simplicity constraints, Design, Inputs/Outputs, Failure behavior, Tests, Acceptance criteria).

## Additional ecosystems (one at a time, after the Go slice is stable)

20. [20-python-resolver](20-python-resolver/INDEX.md)
21. [21-node-resolver](21-node-resolver/INDEX.md)
22. [22-rust-resolver](22-rust-resolver/INDEX.md)
23. [23-java-resolver](23-java-resolver/INDEX.md)

## Broader acquisition and backend coverage

24. [24-website-acquisition](24-website-acquisition/INDEX.md) — HTTP client, sitemap reader, HTML normalizer
25. [25-additional-vector-backends](25-additional-vector-backends/INDEX.md) — conformance suite, Weaviate, Milvus, Chroma, pgvector

## Quality, ops, and ecosystem integration

26. [26-evaluation-framework](26-evaluation-framework/INDEX.md) — eval cases, deterministic version-correctness metrics, `ragctl eval`, promotion gate
27. [27-observability](27-observability/INDEX.md) — structured logging, OpenTelemetry, Prometheus metrics
28. [28-external-eval-integrations](28-external-eval-integrations/INDEX.md) — Ragas/DeepEval/Promptfoo exports, Phoenix/Langfuse docs
29. [29-security-hardening](29-security-hardening/INDEX.md) — source trust metadata, prompt-injection labeling, fetch limits, no downloaded-code execution
30. [30-local-retrieval-mode](30-local-retrieval-mode/INDEX.md) — Bleve lexical adapter, local query fallback
31. [31-llamaindex-adapter](31-llamaindex-adapter/INDEX.md) — external normalizer protocol, example sidecar
32. [32-client-integration-examples](32-client-integration-examples/INDEX.md) — LibreChat, Goose, generic MCP client docs

## Dataset asset handling

33. [33-dataset-descriptors](33-dataset-descriptors/INDEX.md) — `dataset.json` provenance sidecars for raw non-git datasets (`hack/fetch-geodata`), HTTP hashing, optional GDAL spatial inspection, snapshot comparison. Not sourced from the original implementation plan doc — added after the corpus grew a `data/*` category of raw datasets; see [[ragctl-rag-scope-raw-data-vs-docs]] for why these stay out of the RAG pipeline and out of the knowledge registry.

## Ticket ID prefixes in this directory

`PY / NODE / RUST / JAVA / HTTP / VEC (010-014) / EVAL / OBS / EXT / SEC / LOCAL / LLAMA / CLIENT / DATA` — these match the task IDs used in the implementation plan directly, except `DATA` (see above).
