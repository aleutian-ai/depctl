# Demo: Mem0

**Shows:** `depctl export mem0` turns depctl's version-correct docs into memories in your own **self-hosted** Mem0 server, searchable through Mem0's own API. Re-exporting replaces the previous copy instead of duplicating it.

Mem0's server has no published image, so it's built from source. It runs fully local: Mem0's bundled OpenAI provider is pointed at Ollama's OpenAI-compatible endpoint.

## Start Mem0

```bash
git clone --depth 1 https://github.com/mem0ai/mem0.git /tmp/mem0-src
podman build -t localhost/mem0-server:local /tmp/mem0-src/server
mkdir -p ~/.depctl-demo && cp /tmp/mem0-src/server/init-db.sh ~/.depctl-demo/mem0-init-db.sh   # Podman's VM can't see /tmp
podman network create demo-net
podman run -d --name demo-mem0-pg --network demo-net -e POSTGRES_PASSWORD=mem0demo \
  -v ~/.depctl-demo/mem0-init-db.sh:/docker-entrypoint-initdb.d/init-db.sh:Z docker.io/pgvector/pgvector:pg17
sleep 10
podman run -d --name demo-mem0 --network demo-net -p 8888:8000 \
  -e POSTGRES_HOST=demo-mem0-pg -e POSTGRES_PASSWORD=mem0demo -e APP_DB_NAME=mem0_app \
  -e JWT_SECRET=demo-jwt-secret-0123456789abcdef -e ADMIN_API_KEY=demo-admin-key \
  -e MEM0_TELEMETRY=false -e HISTORY_DB_PATH=/tmp/history.db \
  -e OPENAI_API_KEY=ollama -e OPENAI_BASE_URL=http://host.containers.internal:11434/v1 \
  localhost/mem0-server:local sh -c "alembic upgrade head && uvicorn main:app --host 0.0.0.0 --port 8000"
sleep 10
# 768-dim local embeddings (nomic-embed-text) instead of OpenAI's 1536:
curl -s -H 'X-API-Key: demo-admin-key' -H 'Content-Type: application/json' -X POST http://localhost:8888/configure -d '{
 "vector_store": {"provider":"pgvector","config":{"host":"demo-mem0-pg","port":5432,"dbname":"postgres","user":"postgres","password":"mem0demo","collection_name":"memories_768","embedding_model_dims":768}},
 "llm": {"provider":"openai","config":{"api_key":"ollama","model":"ministral-3:3b","openai_base_url":"http://host.containers.internal:11434/v1"}},
 "embedder": {"provider":"openai","config":{"api_key":"ollama","model":"nomic-embed-text:latest","openai_base_url":"http://host.containers.internal:11434/v1","embedding_dims":768}},
 "history_db_path":"/tmp/history.db"}'
```

## Run

```bash
source docs/demos/demo-env.sh
export MEM0_ADMIN_KEY=demo-admin-key    # before the first depctl command: the daemon reads it
depctl init
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
depctl export mem0 --project "$pid" --endpoint http://localhost:8888 --api-key-env MEM0_ADMIN_KEY
curl -s -H 'X-API-Key: demo-admin-key' -H 'Content-Type: application/json' -X POST http://localhost:8888/search \
  -d "{\"query\":\"How do I generate a new random UUID?\",\"user_id\":\"$pid\",\"limit\":3}" |
  python3 -c 'import json,sys; [print(round(x["score"],2), x["metadata"]["version"], "|", x["memory"][:80].replace(chr(10)," ")) for x in json.load(sys.stdin)["results"][:3]]'
```

## What to look for

- The export reports `pushed 84, failed 0` in a few seconds.
- Mem0's own search returns `uuid`'s `New`/`NewRandom` docs, with metadata naming the dependency, `version: v1.6.0`, the depctl generation, and the trust class.
- Run the export again: still 84 memories, not 168. Each dependency's previous export is replaced (scoped to this project and dependency only).

## Clean up

```bash
demo_reset
podman rm -f demo-mem0 demo-mem0-pg && podman network rm demo-net
```

Notes: the hosted Mem0 Platform isn't supported (its API differs). Self-hosted Mem0 sends telemetry by default (`MEM0_TELEMETRY=false` above turns it off) and requires an admin key for the replace step.
