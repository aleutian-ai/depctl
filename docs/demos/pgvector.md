# Demo: your own Postgres + pgvector

**Shows:** depctl stores its index in a Postgres you already run (for example the one under your Mem0), in a table of its own, and never touches anything else in the database. The password comes from an environment variable, never from the config file.

## Run

Start "your" Postgres and give it a table of its own:

```bash
podman run -d --name demo-pg -p 15432:5432 -e POSTGRES_PASSWORD=pgdemo docker.io/pgvector/pgvector:pg17
sleep 8
podman exec demo-pg psql -U postgres -c "CREATE EXTENSION IF NOT EXISTS vector;
  CREATE TABLE my_app (id int PRIMARY KEY, embedding vector(4), note text);
  INSERT INTO my_app VALUES (1, '[1,0,0,0]', 'not depctl data');"
```

Point a sandboxed depctl at it:

```bash
source docs/demos/demo-env.sh
export DEPCTL_PG_PASSWORD=pgdemo        # before the first depctl command: the daemon reads it
depctl init
python3 - "$DEMO_DEPCTL_DIR/config.yaml" <<'EOF'
import sys, yaml
p = sys.argv[1]; c = yaml.safe_load(open(p))
c["vector"]["backend"] = "pgvector"
c["vector"]["endpoint"] = "postgres://postgres@127.0.0.1:15432/postgres?sslmode=disable"
c["vector"]["api_key_env"] = "DEPCTL_PG_PASSWORD"
c["vector"]["managed"] = False
yaml.safe_dump(c, open(p, "w"), sort_keys=False)
EOF
depctl doctor | grep -i vector
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
demo_search "$pid" "How do I generate a new random UUID?"
podman exec demo-pg psql -U postgres -c '\dt'
podman exec demo-pg psql -U postgres -c 'SELECT * FROM my_app;'
```

## What to look for

- `doctor` reports the vector backend as `pgvector` and reachable. The password lives only in `DEPCTL_PG_PASSWORD`; a wrong or missing one shows up here as a clear error.
- The search returns `uuid`'s `New`/`NewRandom` docs at `v1.6.0`.
- `\dt` lists `my_app` plus one `depctl-<random>` table. depctl names its table uniquely per install.
- `my_app` still holds its 1 row.
- **Two versions side by side.** Add a second project pinned to `v1.5.0`. Each project answers from its own version, and the table holds both: 84 rows for `v1.6.0`, 81 for `v1.5.0`. Chunks the two versions share are stored once per version, so neither overwrites the other:

  ```bash
  dir=$(demo_project v1.5.0); depctl scan "$dir"; pid5=$(demo_project_id "$dir")
  depctl sync --project "$pid5"
  demo_search "$pid5" "random UUID"                                  # v1.5.0
  demo_search "$pid" "How do I generate a new random UUID?"          # still v1.6.0
  T=$(podman exec demo-pg psql -U postgres -Atc "SELECT tablename FROM pg_tables WHERE tablename LIKE 'depctl-%'")
  podman exec demo-pg psql -U postgres -c "SELECT version, count(*) FROM \"$T\" GROUP BY version;"
  ```

- **Garbage collection.** Upgrade the second project to `v1.6.0`, so nothing uses `v1.5.0` any more. `gc` deletes exactly `v1.5.0`'s rows. The grace period is shortened here only so the demo doesn't wait 14 days:

  ```bash
  cp "$(dirname "$dir")/proj-v1.6.0/go.mod" "$(dirname "$dir")/proj-v1.6.0/go.sum" "$dir"/
  depctl scan "$dir"; depctl sync --project "$pid5"
  sed -i.bak 's/grace_period: .*/grace_period: 1s/' "$DEMO_DEPCTL_DIR/config.yaml"; depctl daemon stop; sleep 2
  depctl gc                                                          # 1 deleted: v1.5.0
  podman exec demo-pg psql -U postgres -c "SELECT version, count(*) FROM \"$T\" GROUP BY version;"   # v1.6.0 | 84
  podman exec demo-pg psql -U postgres -c 'SELECT * FROM my_app;'                                    # still 1 row
  ```

## Clean up

```bash
demo_reset
podman rm -f demo-pg
```

Notes: if the database role can't run `CREATE EXTENSION vector`, ask an admin to run it once; depctl only tries when the extension is missing. depctl uses cosine distance with an HNSW index.
