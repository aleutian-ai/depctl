# Demo: your own Qdrant

**Shows:** depctl uses a Qdrant you already run (standalone, or the one under your Mem0) and never touches anything else stored in it.

## Run

Start "your" Qdrant and give it some data of its own:

```bash
podman run -d --name demo-qdrant -p 16333:6333 docker.io/qdrant/qdrant:v1.13.1
curl -s -X PUT http://localhost:16333/collections/my_app -H 'Content-Type: application/json' -d '{"vectors":{"size":4,"distance":"Cosine"}}'
curl -s -X PUT 'http://localhost:16333/collections/my_app/points?wait=true' -H 'Content-Type: application/json' \
  -d '{"points":[{"id":1,"vector":[1,0,0,0],"payload":{"note":"not depctl data"}}]}'
```

Point a sandboxed depctl at it:

```bash
source docs/demos/demo-env.sh
depctl init
python3 - "$DEMO_DEPCTL_DIR/config.yaml" <<'EOF'
import sys, yaml
p = sys.argv[1]; c = yaml.safe_load(open(p))
c["vector"]["backend"] = "qdrant"
c["vector"]["endpoint"] = "http://127.0.0.1:16333"
c["vector"]["managed"] = False      # never start a container of its own
yaml.safe_dump(c, open(p, "w"), sort_keys=False)
EOF
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
demo_search "$pid" "How do I generate a new random UUID?"
curl -s http://localhost:16333/collections | python3 -m json.tool
curl -s -X POST http://localhost:16333/collections/my_app/points/count -H 'Content-Type: application/json' -d '{}'
```

## What to look for

- The collection list shows `my_app` plus one `depctl-<random>` collection. depctl names its collection uniquely per install.
- `my_app` still has its 1 point.
- **Outage and recovery.** Stop the server and force a rebuild: the sync fails with a connection error, and depctl starts **no** container of its own (`managed: false`). Bring the server back, and a *plain* sync rebuilds what failed. No `--rebuild`, no daemon restart:

  ```bash
  podman stop demo-qdrant
  depctl sync --project "$pid" --rebuild --dependency github.com/google/uuid   # FAIL: connection refused
  podman ps                                                                    # no new container
  podman start demo-qdrant; sleep 8
  depctl sync --project "$pid"                                                 # 1 synced
  demo_search "$pid" "random UUID"
  ```

## Clean up

```bash
demo_reset
podman rm -f demo-qdrant
```
