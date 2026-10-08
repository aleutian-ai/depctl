# Demo: keyword search, no Ollama

**Shows:** depctl with no embedding model at all. It syncs and searches a dependency's docs by keyword (BM25), still scoped to the exact version your project uses. Then, in the default `auto` mode, it searches by keyword while Ollama is unavailable and adds semantic search on the first sync after Ollama is back, with no rebuild.

## Part 1: keyword mode

```bash
source docs/demos/demo-env.sh
depctl init --retrieval-mode keyword
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
demo_search "$pid" "NewRandom"
demo_search "$pid" "parse a UUID from a string"
depctl doctor | grep -E "config  |embedding backend|vector backend  "
ls "$DEMO_DEPCTL_DIR"
```

### What to look for

- The sync needs no model, so it's quick.
- `NewRandom` finds `New`'s and `NewRandom`'s docs. "parse a UUID from a string" finds `ParseBytes` and `MustParse` (both "like Parse") and a changelog note about `Parse`. Everything comes from `v1.6.0`.
- `doctor` says `retrieval keyword`, with the embedding backend and vector store "not used". That's a normal state, not a warning.
- The data directory has a `keyword.db` and no `vectors.db`.

## Part 2: auto mode, Ollama unavailable, then back

`auto` is the default. To simulate Ollama being unavailable without stopping yours, point depctl at a closed port:

```bash
demo_reset; source docs/demos/demo-env.sh
depctl init
sed -i.bak 's#endpoint: http://127.0.0.1:11434#endpoint: http://127.0.0.1:1#' "$DEMO_DEPCTL_DIR/config.yaml"
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
demo_search "$pid" "how do I generate a random UUID"
depctl doctor | grep -E "embedding backend|embedding model"
```

Now "Ollama is back":

```bash
mv "$DEMO_DEPCTL_DIR/config.yaml.bak" "$DEMO_DEPCTL_DIR/config.yaml"; depctl daemon stop
depctl sync --project "$pid"
depctl doctor | grep -E "embedding backend|embedding model"
demo_search "$pid" "how do I generate a random UUID"
```

### What to look for

- With Ollama unavailable, the sync still succeeds, and the search answers by keyword. `doctor` says "using keyword search for now", and that one version is searched by keyword until a sync adds its vectors. Nothing is reported unhealthy.
- After Ollama is back, the plain sync prints `VECTORS github.com/google/uuid v1.6.0 (added vectors …)`. Nothing is re-fetched or rebuilt. `doctor` shows every version on the embedding model, and the search is semantic again (`NewRandom` first).

## Clean up

```bash
demo_reset
```

Notes: keyword search matches identifiers whole and in parts (`pgxpool.NewWithConfig`, `snake_case`, module paths). It's strongest when a question uses the docs' own words; semantic search is stronger on paraphrase. `retrieval.mode: vector` keeps the old behavior: Ollama required.
