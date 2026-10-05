# Demo: Graphiti

**Shows:** `ragctl export graphiti` turns ragctl's version-correct docs into Graphiti episodes. Graphiti extracts them into entities and facts in Neo4j, searchable through Graphiti's own API.

**Graphiti's published server needs patching** to ingest anything over REST. Its queued ingestion job runs on a database connection that's already closed, and its worker swallows the error. It also ignores the embedding-model and small-model settings, which matters for local models. [`graphiti/Containerfile`](graphiti/Containerfile) builds the published image with those fixed. Use **Neo4j 5.26+**; Graphiti's own README pins 5.22, which current Graphiti can't use.

## Start Graphiti

```bash
podman build -t localhost/graphiti-ollama:local docs/demos/graphiti
podman network create demo-net
podman run -d --name demo-neo4j --network demo-net -e NEO4J_AUTH=neo4j/graphitidemo docker.io/library/neo4j:5.26
until podman logs demo-neo4j 2>&1 | grep -q Started.; do sleep 2; done
podman run -d --name demo-graphiti --network demo-net -p 8001:8000 \
  -e OPENAI_API_KEY=ollama -e OPENAI_BASE_URL=http://host.containers.internal:11434/v1 \
  -e MODEL_NAME=ministral-3:3b -e EMBEDDING_MODEL_NAME=nomic-embed-text:latest \
  -e NEO4J_URI=bolt://demo-neo4j:7687 -e NEO4J_USER=neo4j -e NEO4J_PASSWORD=graphitidemo \
  localhost/graphiti-ollama:local
until curl -sf http://localhost:8001/healthcheck; do sleep 2; done
```

## Run

```bash
source docs/demos/demo-env.sh
ragctl init
dir=$(demo_project v1.6.0); ragctl scan "$dir"; pid=$(demo_project_id "$dir")
ragctl sync --project "$pid"
ragctl export graphiti --project "$pid" --endpoint http://localhost:8001
# Graphiti extracts in the background; watch it fill in (about 1.5 min per episode on a 3B model):
podman exec demo-neo4j cypher-shell -u neo4j -p graphitidemo "MATCH (n) WHERE n.group_id='$pid' RETURN labels(n)[0] AS label, count(*) AS n;"
curl -s -X POST http://localhost:8001/search -H 'Content-Type: application/json' \
  -d "{\"group_ids\":[\"$pid\"],\"query\":\"How do I generate a random UUID?\",\"max_facts\":5}" | python3 -m json.tool
```

## What to look for

- The export reports `2 episode(s) queued`. ragctl splits each dependency into episodes of about 12 KB, so even a large dependency fits a model's context.
- Neo4j fills in to 2 `Episodic` nodes plus extracted `Entity` nodes (`NewRandom`, `RFC 4122`, `UUIDv7`, ...). How many varies run to run with a small local model; verified runs saw 10 to 46.
- Graphiti's search returns facts it extracted from ragctl's `uuid` docs, e.g. `The github.com/google/uuid package generates UUIDs based on RFC 4122` or `NewRandom returns a Random (Version 4) UUID`.

## Clean up

```bash
demo_reset
podman rm -f demo-graphiti demo-neo4j && podman network rm demo-net
```

Notes: Graphiti's REST server has no authentication by default. Re-exporting adds new episodes rather than replacing old ones.
