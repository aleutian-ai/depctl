# Demo: Cognee

**Shows:** `depctl export cognee` adds depctl's version-correct docs to a Cognee dataset and runs Cognee's own `cognify` pipeline over them, so they're searchable through Cognee.

It runs fully local using Cognee's documented Ollama settings.

## Start Cognee

```bash
podman run -d --name demo-cognee -p 8000:8000 \
  -e LLM_API_KEY=ollama -e LLM_PROVIDER=ollama -e LLM_MODEL=ministral-3:3b \
  -e LLM_ENDPOINT=http://host.containers.internal:11434 \
  -e EMBEDDING_PROVIDER=ollama -e EMBEDDING_MODEL=nomic-embed-text:latest \
  -e EMBEDDING_ENDPOINT=http://host.containers.internal:11434/api/embed \
  -e EMBEDDING_DIMENSIONS=768 -e HUGGINGFACE_TOKENIZER=nomic-ai/nomic-embed-text-v1.5 \
  -e ENABLE_BACKEND_ACCESS_CONTROL=false \
  docker.io/cognee/cognee:main
until curl -sf http://localhost:8000/health; do sleep 2; done
```

## Run

```bash
source docs/demos/demo-env.sh
depctl init
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
depctl export cognee --project "$pid" --endpoint http://localhost:8000
curl -s -X POST http://localhost:8000/api/v1/search -H 'Content-Type: application/json' \
  -d "{\"searchType\":\"CHUNKS\",\"query\":\"How do I generate a new random UUID?\",\"datasets\":[\"$pid\"]}" |
  python3 -c 'import json,sys; [print("-", r["text"][:160].replace("\\n"," ")) for r in json.load(sys.stdin)[:3]]'
```

## What to look for

- The export reports `added` and then `cognify triggered`. `cognify` runs Cognee's full extraction pipeline before it returns, which takes a few minutes on a local model. That's expected.
- Cognee's dataset list (`curl -s http://localhost:8000/api/v1/datasets`) shows one dataset named for the depctl project.
- Cognee's search returns depctl's `uuid` v1.6.0 docs (e.g. `NewRandom returns a Random (Version 4) UUID`).
- Exporting again finishes in about a second: Cognee skips content it has already processed.

## Clean up

```bash
demo_reset
podman rm -f demo-cognee
```

Notes: Cognee sends telemetry by default (`TELEMETRY_DISABLED=1` on the Cognee container opts out) and runs without authentication unless you enable it.
