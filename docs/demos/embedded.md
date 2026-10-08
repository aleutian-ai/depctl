# Demo: embedded (the default, no vector service)

**Shows:** depctl with no vector database and no container at all. Its vector index is one file, `vectors.db`, next to depctl's other data. Search, multiple versions and garbage collection work exactly as with a server. Only Ollama runs, for embeddings. (The default `retrieval.mode: auto` also keeps a keyword index, `keyword.db`, beside it; see the [keyword demo](keyword.md).)

## Run

```bash
source docs/demos/demo-env.sh
podman ps --format '{{.Names}}' | sort > "$DEMO_ROOT/containers-before"
depctl init                            # embedded is the default
depctl doctor | grep -i vector
dir=$(demo_project v1.6.0); depctl scan "$dir"; pid=$(demo_project_id "$dir")
depctl sync --project "$pid"
demo_search "$pid" "How do I generate a new random UUID?"
ls -lh "$DEMO_DEPCTL_DIR/vectors.db"
podman ps --format '{{.Names}}' | sort | diff "$DEMO_ROOT/containers-before" - && echo "no new containers"
```

## What to look for

- `init` writes `vector.backend: embedded` with no endpoint, and `managed` off. (`--vector-backend embedded` says the same thing explicitly.)
- `doctor` reports the vector backend as `embedded` at `<data-dir>/vectors.db`.
- The search returns `uuid`'s `New`/`NewRandom` docs at `v1.6.0`.
- `vectors.db` is a few MB. depctl started no container.
- **Two versions side by side.** Add a second project pinned to `v1.5.0`. Each project answers from its own version:

  ```bash
  dir5=$(demo_project v1.5.0); depctl scan "$dir5"; pid5=$(demo_project_id "$dir5")
  depctl sync --project "$pid5"
  demo_search "$pid5" "random UUID"                                  # v1.5.0
  demo_search "$pid" "How do I generate a new random UUID?"          # still v1.6.0
  depctl status | grep -E 'active generations|backend'
  ```

- **Garbage collection.** Upgrade the second project to `v1.6.0`, so nothing uses `v1.5.0` any more. `gc` deletes exactly `v1.5.0`. The grace period is shortened here only so the demo doesn't wait 14 days:

  ```bash
  cp "$dir/go.mod" "$dir/go.sum" "$dir5"/
  depctl scan "$dir5"; depctl sync --project "$pid5"
  sed -i.bak 's/grace_period: .*/grace_period: 1s/' "$DEMO_DEPCTL_DIR/config.yaml"; depctl daemon stop; sleep 2
  depctl gc                                                          # 1 deleted: v1.5.0
  demo_search "$pid" "How do I generate a new random UUID?"          # v1.6.0, unaffected
  depctl doctor | tail -1                                            # nothing unhealthy
  ```

## Clean up

```bash
demo_reset
```

Notes: the file belongs to depctl's daemon, like `control.db`. Only one process can hold it at a time, and depctl routes every command through the daemon, so that's never a problem in practice. Search is exact (every candidate is compared) but always scoped to one dependency version, so it stays fast. This backend is for one machine; use a vector server to share an index.
