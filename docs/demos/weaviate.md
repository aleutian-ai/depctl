# Demo: your own Weaviate

**Shows:** depctl stores its index in a Weaviate you already run, in a collection of its own, and never touches anything else on the server. The API key comes from an environment variable, never from the config file.

## Run

Start "your" Weaviate, with API-key auth on, and give it a collection of its own:

```bash
podman run -d --name demo-weaviate -p 18080:8080 \
  -e AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED=false -e AUTHENTICATION_APIKEY_ENABLED=true \
  -e AUTHENTICATION_APIKEY_ALLOWED_KEYS=wvdemo -e AUTHENTICATION_APIKEY_USERS=demo \
  -e DEFAULT_VECTORIZER_MODULE=none -e CLUSTER_HOSTNAME=node1 -e PERSISTENCE_DATA_PATH=/var/lib/weaviate \
  cr.weaviate.io/semitechnologies/weaviate:1.32.4
sleep 8
curl -s -H 'Authorization: Bearer wvdemo' -H 'Content-Type: application/json' -X POST http://localhost:18080/v1/schema \
  -d '{"class":"MyApp","vectorizer":"none","properties":[{"name":"note","dataType":["text"]}]}' >/dev/null
curl -s -H 'Authorization: Bearer wvdemo' -H 'Content-Type: application/json' -X POST http://localhost:18080/v1/objects \
  -d '{"class":"MyApp","vector":[1,0,0,0],"properties":{"note":"not depctl data"}}' >/dev/null
```

Point a sandboxed depctl at it:

```bash
source docs/demos/demo-env.sh
export DEPCTL_WEAVIATE_KEY=wvdemo       # before the first depctl command: the daemon reads it
depctl init
python3 - "$DEMO_DEPCTL_DIR/config.yaml" <<'EOF'
import sys, yaml
p = sys.argv[1]; c = yaml.safe_load(open(p))
c["vector"]["backend"] = "weaviate"
c["vector"]["endpoint"] = "http://127.0.0.1:18080"
c["vector"]["api_key_env"] = "DEPCTL_WEAVIATE_KEY"
c["vector"]["managed"] = False
yaml.safe_dump(c, open(p, "w"), sort_keys=False)
EOF
depctl doctor | grep -i vector
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
demo_search "$pid" "How do I generate a new random UUID?"
wv() { curl -s -H 'Authorization: Bearer wvdemo' -H 'Content-Type: application/json' "$@"; }
wv http://localhost:18080/v1/schema | python3 -c 'import json,sys; print([c["class"] for c in json.load(sys.stdin)["classes"]])'
wv -X POST http://localhost:18080/v1/graphql -d '{"query":"{ Aggregate { MyApp { meta { count } } } }"}'; echo
```

## What to look for

- `doctor` reports the vector backend as `weaviate` and reachable. With `DEPCTL_WEAVIATE_KEY` wrong or unset, it says so instead.
- The search returns `uuid`'s `New`/`NewRandom` docs at `v1.6.0`.
- The schema lists `MyApp` plus one `Depctl_<random>` collection. depctl names its collection uniquely per install (`depctl-<random>`, made into a valid Weaviate name).
- `MyApp` still holds its 1 object.
- **Two versions side by side.** Add a second project pinned to `v1.5.0`. Each project answers from its own version, and depctl's collection holds both: 84 objects for `v1.6.0`, 81 for `v1.5.0`:

  ```bash
  dir=$(demo_project v1.5.0); depctl scan "$dir"; pid5=$(demo_project_id "$dir")
  depctl sync --project "$pid5"
  demo_search "$pid5" "random UUID"                                  # v1.5.0
  demo_search "$pid" "How do I generate a new random UUID?"          # still v1.6.0
  C=$(wv http://localhost:18080/v1/schema | python3 -c 'import json,sys; print([c["class"] for c in json.load(sys.stdin)["classes"] if c["class"].startswith("Depctl_")][0])')
  versions() { wv -X POST http://localhost:18080/v1/graphql -d "{\"query\":\"{ Aggregate { $C(groupBy: [\\\"version\\\"]) { groupedBy { value } meta { count } } } }\"}" |
    python3 -c 'import json,sys; [print(g["groupedBy"]["value"], g["meta"]["count"]) for g in list(json.load(sys.stdin)["data"]["Aggregate"].values())[0]]'; }
  versions
  ```

- **Garbage collection.** Upgrade the second project to `v1.6.0`, so nothing uses `v1.5.0` any more. `gc` deletes exactly `v1.5.0`'s objects. The grace period is shortened here only so the demo doesn't wait 14 days:

  ```bash
  cp "$(dirname "$dir")/proj-v1.6.0/go.mod" "$(dirname "$dir")/proj-v1.6.0/go.sum" "$dir"/
  depctl scan "$dir"; depctl sync --project "$pid5"
  sed -i.bak 's/grace_period: .*/grace_period: 1s/' "$DEMO_DEPCTL_DIR/config.yaml"; depctl daemon stop; sleep 2
  depctl gc                                                          # 1 deleted: v1.5.0
  versions                                                           # v1.6.0 84
  wv -X POST http://localhost:18080/v1/graphql -d '{"query":"{ Aggregate { MyApp { meta { count } } } }"}'; echo   # still 1
  ```

## Clean up

```bash
demo_reset
podman rm -f demo-weaviate
```

Notes: if pulling the image hits Docker Hub's rate limit, `mirror.gcr.io/semitechnologies/weaviate:1.32.4` is the same image. depctl uses cosine distance and stores no text in Weaviate, so it reports no keyword or hybrid search there.
