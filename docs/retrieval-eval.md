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
- **Hybrid** is reciprocal rank fusion of each method's top 50 (score = Σ 1/(60 + rank)). Since 2026-10-07 this is what ragctl itself does in `auto` mode when vectors exist. Asking all 323 questions through the real daemon's search API gives exactly the hybrid numbers above.

## Embedding models (2026-10-07)

Same corpus and questions. Every setup was chosen on the **tune half** and is reported on the **held-out test half** (162 questions), so the numbers aren't fitted to the questions they're scored on. "Hybrid" is each setup fused with keyword search, which is what `auto` mode does.

| Setup (held-out half) | Alone: MRR | Alone: hit@3 | Hybrid: MRR | Hybrid: hit@3 | Hybrid: hit@10 |
|---|---|---|---|---|---|
| Keyword (BM25), no model | 0.513 | 58% | — | — | — |
| `nomic-embed-text-v2-moe`, as shipped | 0.466 | 52% | 0.528 | 60% | 83% |
| nomic, with symbol titles on chunks | 0.496 | 57% | 0.541 | 61% | 83% |
| **`embeddinggemma-2:270m`, code-retrieval prompts** | **0.546** | **66%** | **0.587** | **70%** | **83%** |
| EmbeddingGemma 2, "search result" prompts | 0.570 | 69% | 0.574 | 67% | 81% |
| EmbeddingGemma 2, code prompts, truncated to 256 dimensions | 0.544 | 65% | 0.583 | 69% | 84% |
| EmbeddingGemma 2, no prompts | 0.455 | 54% | 0.519 | 62% | 80% |

**Prompts.** The code-retrieval setup asks with `task: code retrieval | query: {question}` and indexes `title: {qualified symbol, or file path} | text: {chunk}`.

**Paired differences on the held-out half**, with 95% bootstrap intervals:

| Comparison | MRR difference | Interval | Verdict |
|---|---|---|---|
| hybrid with EmbeddingGemma 2 − hybrid with nomic (shipped) | +0.059 | [+0.023, +0.097] | better |
| hybrid with EmbeddingGemma 2 − keyword alone | +0.074 | [+0.035, +0.116] | better |
| EmbeddingGemma 2 alone − nomic alone | +0.080 | [+0.023, +0.138] | better |
| EmbeddingGemma 2 alone − keyword alone | +0.033 | [−0.032, +0.099] | no measurable difference |
| 256 dimensions − 768, both hybrid | −0.004 | [−0.025, +0.017] | no measurable difference |
| "search result" − code prompts, both hybrid | −0.013 | [−0.029, +0.003] | no measurable difference |

**What this shows:**
- **EmbeddingGemma 2 is a real improvement over nomic**, alone and in hybrid. Hybrid with it beats keyword search alone by +0.074 MRR, where hybrid with nomic didn't separate from keyword on the held-out half.
- **Its prompts matter.** Without them it does no better than nomic. Which prompt (code retrieval or search result) makes no measurable difference.
- **Truncating to 256 dimensions costs nothing measurable**, and makes the vector store about 3× smaller.
- **The 270m, 570m and 740m variants give identical text embeddings** (checked vector by vector). They share the text encoder and differ only in image and audio parts. For ragctl, the 378 MB 270m is the one to use.

**Shipped as the default (2026-10-07).** Fresh installs now use `embeddinggemma-2:270m` with the code-retrieval prompts at 256 dimensions. Asked through the running daemon's search API (`run -daemon-only -daemon-project <id>`), the held-out half scores exactly what the eval predicts: **0.583 MRR, 69% hit@3, 84% hit@10** (0.607 MRR over all 323 questions). The vector store for the 12 dependencies is 16.8 MB, against 50.6 MB with nomic at 768 dimensions. Existing installs keep the model their config names.

**Latency.** Searching with the 270m model took 18 ms median, against 16 ms for nomic, both mostly embedding the question. Two setups show about 750 ms in the raw report. That's an artifact of the eval switching between seven models on every question, which forces Ollama to reload them; it isn't a property of those setups.

## Caveats

- **Generated questions may still lean towards keyword search.** A question written from one chunk can share ordinary words with it even after the filter. The hand-written set, where this matters less, shows the same order (keyword 0.498, vector 0.430, hybrid 0.526), though with only 48 questions.
- **The right-answer rule is strict.** A generated question counts only its source chunk as right, so a near-duplicate doc ranking first counts as a miss. This lowers every method's score equally; it doesn't favor one.
- **One corpus, one model.** These are Go API docs and READMEs with one embedding model, ragctl's default. Prose-heavy docs, other languages or a stronger embedding model could shift the balance towards vector search.
- **The identifier and description groups are small** (10 and 20 questions). Their per-kind numbers are indicative, not conclusive.

## What it means for ragctl

- **Running without Ollama is a sound choice.** `retrieval.mode: keyword` (or `auto` while Ollama is down) costs no measurable quality on this kind of corpus. It's faster, and it's better at exact names.
- **Hybrid search is now what `auto` does** when Ollama is available (built 2026-10-07). On the held-out test half it scores 0.528 MRR against keyword's 0.513 and vector's 0.466, and finds the right doc in its top 10 for 83% of questions against 78% and 75%.
- **Task prefixes aren't worth adding for nomic.** They made no difference for `nomic-embed-text-v2-moe` here. EmbeddingGemma 2 is the opposite: it needs its prompts (see above).
- **Switching an existing install's model or size needs a re-index.** Vectors from different models or sizes can't share a store: `doctor` flags the mismatch and syncs fail with a dimension mismatch. Until there's a command for it, stop the daemon, delete `vectors.db` and run `ragctl sync --rebuild` for each dependency (keyword search keeps working meanwhile).

## The benchmark

The **held-out test half (162 questions) is frozen as the benchmark.** The baseline to beat is the shipped default above: 0.583 MRR, 69% hit@3, 84% hit@10. Don't tune against it: choose settings on the tune half, then report the held-out half once.

New questions for later work (code-aware chunking, symbol signals, a reranker) get IDs starting with `dev-`. The tool always puts those in the tune half, so adding them never changes the benchmark. Everything else is split by a hash of its ID.

## Re-running it

The tool is `hack/retrieval-eval`. The question sets are `hack/retrieval-eval/questions-hand.json` and `questions-generated.json`. Generated questions name their right answer by chunk ID, which is content-derived, so they stay valid for the same dependency versions.

```bash
# a sandbox install in auto mode with Ollama running, holding the 12 dependencies (see "Corpus")
ragctl daemon stop                      # the tool opens the stores directly
go run ./hack/retrieval-eval index -config <config.yaml> -name eg2-270m-code -embed embeddinggemma-2:270m \
  -query-format 'task: code retrieval | query: {q}' -doc-format 'title: {title} | text: {text}' -dims 256   # optional: another embedding setup
go run ./hack/retrieval-eval run -config <config.yaml> \
  -questions hack/retrieval-eval/questions-hand.json,hack/retrieval-eval/questions-generated.json \
  -report report.md
# the shipped product, through the daemon's search API (start the daemon first)
go run ./hack/retrieval-eval run -config <config.yaml> -questions <same files> -daemon-only -daemon-project <project ID>
go run ./hack/retrieval-eval gen -config <config.yaml> -out more.json -per-dep 25   # more questions (needs ornith-1.5:9b)
```
