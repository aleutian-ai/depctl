# LOCAL-002: Keyword search when no embedding model is available

**Epic:** Local-only retrieval mode
**Status:** done (2026-10-06)
**Depends on:** LOCAL-001, MCP-002 (knowledge query service)
**Estimated size:** small

## Goal
When no vector backend is configured, make the MCP/query service automatically use the Bleve lexical backend (LOCAL-001) so `depctl` works immediately after install with zero external infrastructure.

## Non-goals
- No automatic migration of existing vector-backend data into Bleve, or vice versa.
- Does not change the recommendation that a real vector backend gives better semantic retrieval — this is purely a no-config default path.

## Simplicity constraints
- This is a config-resolution default, not a runtime failover/circuit-breaker between backends. If a vector backend is configured, use it; if not, use Bleve. No automatic switching mid-run.

## Design
In config loading (`internal/config`), if `vector.backend` is unset/empty, default it to `local` (the Bleve adapter from LOCAL-001) at validation time, and log at `init`/`serve` startup: `"no vector backend configured — using local Bleve lexical search; configure vector.backend for semantic search"`.

The knowledge query service (MCP-002) is backend-agnostic already (it depends on the `VectorBackend` interface), so no query-service code changes should be needed beyond ensuring the resolved backend instance is whichever one config resolution picked.

## Inputs / Outputs
- Input: config with `vector.backend` unset.
- Output: daemon runs using the local Bleve backend transparently.

## Failure behavior
N/A beyond LOCAL-001's own failure behavior — this ticket only affects backend selection.

## Tests
- Config with no `vector.backend` set resolves to the local backend.
- `depctl init` + `depctl sync` + `depctl serve` with zero external services succeeds end-to-end using fixture data (this is the "works immediately after install" proof).

## Acceptance criteria
- [x] Missing `vector.backend` config defaults to local Bleve. (Reconciled: `retrieval.mode`, see below.)
- [x] Startup log clearly states the fallback is active.
- [x] End-to-end test: init → sync → serve → query, no external backend running.

## Done (2026-10-06)

Reconciled: by the time this was built, the embedded vector store (VEC-015) had already removed the vector database. The last mandatory service was Ollama, which sync needs for embedding chunks and search needs for embedding the question. So the setting is `retrieval.mode: auto | vector | keyword`, not a vector-backend default.
- **`auto`** is the default for fresh installs. Sync always builds the keyword index, and adds vectors when Ollama and the vector store are ready. Search uses vectors when it can and keyword search otherwise. This is a runtime choice, which the user picked over deciding once at `init`, knowing that it means building both indexes.
- **`vector`** behaves as before and requires Ollama. A config without the key, written before this existed, means `vector`, so existing installs don't change.
- **`keyword`** never touches Ollama or a vector store: no probe, no model pull, no managed container.

How it fits together:
- **A combined `searchIndex` (`internal/cli/retrieval.go`)** presents the indexes a mode writes as one `VectorBackend`. Writes and deletes reach all of them, so sync, validation, promotion and GC are unchanged. Its name is the existing active-generation key, so a generation's lifecycle is the same in every mode. A query with a vector that comes back empty falls back to keyword search: a filtered vector search always returns the nearest points that exist, so empty means "built without vectors".
- **Backfill both ways.** A fresh install's model download takes minutes, so many first syncs in `auto` are keyword-only. The next sync with embeddings ready adds their vectors, with no rebuild: the chunks are already stored. Generations built before an install switched to `auto` or `keyword` get keyword entries, which needs no model. Backfill (`generation.AddToIndex`) never changes a serving generation's state, and on failure removes whatever it wrote.
- **Doctor** reports keyword mode, and `auto` running without Ollama, as normal states rather than degraded ones. The embedding model, embedder and vector store show as "not used", or as "using keyword search for now".
- **`depctl init --retrieval-mode`** selects the mode explicitly.

Verified end to end in a Linux container with no Ollama installed and nothing on port 11434:
- plain `init` wrote `auto`;
- a real Go project (uuid, pgx) synced in 31 s;
- `NewRandom`, `pgxpool.New` and `NewWithConfig` came back first or second, and natural-language questions worked;
- search still worked after a daemon restart;
- doctor reported 19 ok in `auto` and 17 ok in `keyword`, with no unhealthy checks;
- in keyword mode the daemon logged no Ollama contact at all.

On the host (Ollama made unreachable):
- two versions coexisted (84 and 81 keyword entries), and GC took v1.5.0's entries to 0 while leaving v1.6.0's;
- `auto` built keyword-only while Ollama was down, then backfilled vectors on the first sync after it returned (search switched to cosine scores), and a later sync had nothing left to backfill.

Found along the way:
- Index files are located from the configured `control.db`, not a global default. Doctor tests had been creating an empty `keyword.db` in the developer's real data directory.
- Vector readiness probes only the vector store, never the keyword file.
