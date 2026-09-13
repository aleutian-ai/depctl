# Embedding provider lock-in — validated, deliberately deferred

**Status:** real gap, not urgent. Backlogged by explicit decision, not an oversight.

## The gap

`internal/embedding.Embedder` (EMB-001) is already provider-agnostic — `Name()`/`ModelID()`/`Dimensions(ctx)`/`Embed(ctx, texts)`, nothing Ollama-specific in the interface. But `buildEmbedder` (`internal/cli/pipeline.go`) hard-rejects anything except `"ollama"`:

```go
if cfg.Embedding.Provider != "ollama" {
    return nil, fmt.Errorf("unsupported embedding provider %q (only \"ollama\" is implemented)", cfg.Embedding.Provider)
}
```

A documented v0.1 scope limit (EMB-002), not an accidental one — but real: today ragctl cannot use OpenAI, Azure OpenAI, or any self-hosted OpenAI-API-compatible embedding server (LM Studio, text-embeddings-inference, vLLM's embedding endpoint — Ollama itself even added an OpenAI-compatible `/v1/embeddings` route alongside its native one). One correction worth recording: Claude/Anthropic has no embeddings API at all — not a candidate here regardless, they point people at third-party providers (Voyage AI) for it.

## Why it's deferred, not fixed now

The default is already `nomic-embed-text` via Ollama (`internal/config/config.go`) — a small (~274MB), well-regarded local model. Requiring it isn't a new cost category for ragctl: the tool already requires a local Qdrant instance and a local daemon to work at all, and `docs/offline-quickstart.md`'s whole point is zero-network-access operation. "Also run a small local embedding model" is the same kind of ask as those, not a qualitatively different one — and it's what makes the offline story actually true rather than aspirational. Nobody is blocked from real value today without an OpenAI-compatible option.

## What the fix would look like, whenever it's picked up

Additive, not a rewrite, since the interface is already right:
- New `internal/embedding/openai` (or `openaicompat`) client mirroring `internal/embedding/ollama`'s existing shape — same `Embedder` interface, a `New(endpoint, apiKeyEnv, model string) *Client` constructor, same retry/timeout conventions already established there and in `internal/backend/qdrant`.
- Open up `buildEmbedder`'s provider switch (`internal/cli/pipeline.go`) and `internal/config`'s validation to accept it.
- Config already has an `api_key_env` convention (`config.Config` never serializes resolved secrets, only the env var name) — reuse it rather than inventing a new secret-handling path.
- Since the API shape is shared, this one client should work unmodified against real OpenAI, Azure OpenAI (different base URL), and most self-hosted alternatives — worth confirming against at least one self-hosted server in testing, not just OpenAI itself, to prove the "shared standard" claim holds in practice and not just in theory.
