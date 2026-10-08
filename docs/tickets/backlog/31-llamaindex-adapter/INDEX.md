# Epic: LlamaIndex Adapter

Lets users who already run LlamaIndex ingestion pipelines plug them into `depctl` via an out-of-process protocol, keeping Python entirely outside the core Go binary.

## Tickets

- [LLAMA-001](LLAMA-001-adapter-protocol.md) — Stdio JSON protocol + `ExternalNormalizer` implementing the core `Normalizer` interface.
- [LLAMA-002](LLAMA-002-example-llamaindex-sidecar.md) — Reference Python sidecar example under `examples/llamaindex-sidecar/`.
