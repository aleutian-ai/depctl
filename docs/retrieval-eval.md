# Keyword vs. vector search: retrieval eval

**Question:** does ragctl's search get worse without Ollama, when it searches by keyword (BM25) instead of embeddings?

**Answer (2026-10-07): no measurable drop-off.**
- On 323 questions over 12 real Go dependencies, keyword and vector search found the right doc equally well; the difference was within noise.
- Combining them (hybrid) was measurably better than either.
- Keyword search was also about 25× faster per query.

## Results

Every question was asked of every method, scoped (like all ragctl searches) to the exact dependency version synced. "Hit@3" means the right doc was among the top 3 results. MRR is the average of 1/rank of the first right answer (0 when it isn't in the top 10), so higher is better.

| Method | Hit@1 | Hit@3 | Hit@10 | MRR |
|---|---|---|---|---|
| Keyword (BM25), `retrieval.mode: keyword` | 40% | 60% | 79% | 0.527 |
| Vector (Ollama `nomic-embed-text-v2-moe`, embedded store) | 37% | 57% | 77% | 0.496 |
| Hybrid: both, merged by reciprocal rank fusion | **42%** | **64%** | **83%** | **0.556** |
| Vector, with nomic's `search_query:` / `search_document:` task prefixes | 37% | 58% | 78% | 0.497 |

**Paired differences in MRR**, with 95% bootstrap intervals over the 323 questions:

| Comparison | Difference | Interval | Verdict |
|---|---|---|---|
| keyword − vector | +0.030 | [−0.015, +0.075] | no measurable difference |
| hybrid − keyword | +0.029 | [+0.001, +0.058] | hybrid is better |
| hybrid − vector | +0.060 | [+0.027, +0.093] | hybrid is better |
| prefixed − plain vector | +0.001 | [−0.022, +0.023] | prefixes make no difference |

**The two methods fail on different questions.** In the top 10, keyword alone found the right doc for 31 questions, vector alone for 25, and neither for 42. That complementarity is why hybrid wins.

**By question kind:**

| Kind | n | Keyword MRR | Vector MRR | Hybrid MRR | Note |
|---|---|---|---|---|---|
| Exact identifier (`pgxpool.NewWithConfig`) | 10 | 0.833 | 0.670 | 0.750 | Keyword found every one in its top 3 |
| Plain description ("open the database with a timeout") | 20 | 0.476 | 0.487 | 0.592 | |
| Paraphrase, avoiding the docs' words | 293 | 0.519 | 0.491 | 0.547 | |

**Latency per search:**

| | Median | p95 |
|---|---|---|
| Keyword | 0.5 ms | 4.2 ms |
| Vector | 12.3 ms | 24.5 ms |

Vector time is mostly embedding the question with Ollama.

## How it was measured

- **Corpus.** 12 dependencies ragctl itself uses, at the versions in its `go.mod`, synced in `retrieval.mode: auto` with Ollama running, so both indexes hold the same 10,559 chunks: BurntSushi/toml, badger, fsnotify, google/uuid, pgx, the MCP Go SDK, ulid, the Prometheus client, cobra, bbolt, OpenTelemetry (root module) and yaml.v3.
- **Hand-written questions (48).** Realistic questions in three kinds: exact identifiers, plain descriptions, and paraphrases. Each right answer is one or more specific docs (for example `pgxpool.Acquire`), checked against the corpus. The tool refuses any question whose answer isn't in the corpus.
- **Generated questions (275).** A local model (`ornith-1.5:9b`) read a randomly chosen doc chunk and wrote a question that chunk answers, told not to use its identifiers or phrasing. 46 questions that still reused an identifier or a five-word phrase were thrown out. The right answer is that exact chunk.
- **Hybrid** is reciprocal rank fusion of each method's top 50 (score = Σ 1/(60 + rank)). ragctl doesn't implement it yet; it was computed here to see whether it would be worth building.

## Caveats

- **Generated questions may still lean towards keyword search.** A question written from one chunk can share ordinary words with it even after the filter. The hand-written set, where this matters less, shows the same order (keyword 0.498, vector 0.430, hybrid 0.526), though with only 48 questions.
- **The right-answer rule is strict.** A generated question counts only its source chunk as right, so a near-duplicate doc ranking first counts as a miss. This lowers every method's score equally; it doesn't favor one.
- **One corpus, one model.** These are Go API docs and READMEs with one embedding model, ragctl's default. Prose-heavy docs, other languages or a stronger embedding model could shift the balance towards vector search.
- **The identifier and description groups are small** (10 and 20 questions). Their per-kind numbers are indicative, not conclusive.

## What it means for ragctl

- **Running without Ollama is a sound choice.** `retrieval.mode: keyword` (or `auto` while Ollama is down) costs no measurable quality on this kind of corpus. It's faster, and it's better at exact names.
- **Hybrid search is the measured improvement worth building.** `auto` mode already keeps both indexes whenever Ollama is available, so merging their results when both exist would be a small change. It would gain about 0.03–0.06 MRR (+4 points of hit@10) over either method alone.
- **Task prefixes aren't worth adding.** They made no difference for `nomic-embed-text-v2-moe` here.

## Re-running it

The tool is `hack/retrieval-eval`. The question sets are `hack/retrieval-eval/questions-hand.json` and `questions-generated.json`. Generated questions name their right answer by chunk ID, which is content-derived, so they stay valid for the same dependency versions.

```bash
# a sandbox install in auto mode with Ollama running, holding the 12 dependencies (see "Corpus")
ragctl daemon stop                      # the tool opens the stores directly
go run ./hack/retrieval-eval prefixed -config <config.yaml>   # optional: the task-prefix comparison
go run ./hack/retrieval-eval run -config <config.yaml> \
  -questions hack/retrieval-eval/questions-hand.json,hack/retrieval-eval/questions-generated.json \
  -report report.md
go run ./hack/retrieval-eval gen -config <config.yaml> -out more.json -per-dep 25   # more questions (needs ornith-1.5:9b)
```
