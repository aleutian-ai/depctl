# Demo: managed Qdrant

**Shows:** with `--vector-backend qdrant` and no Qdrant of your own, depctl starts one for you, indexes a dependency at the exact version your project uses, and answers questions from that version only.

## Run

```bash
source docs/demos/demo-env.sh
depctl init --vector-backend qdrant
dir=$(demo_project v1.6.0)
depctl scan "$dir"                     # registers the project and starts syncing
pid=$(demo_project_id "$dir")
depctl sync --project "$pid"           # waits for the sync to finish
depctl describe go github.com/google/uuid
demo_search "$pid" "How do I generate a new random UUID?"
```

## What to look for

- `depctl init --vector-backend qdrant` writes `vector.backend: qdrant` and `vector.managed: true`. If nothing is listening on 6333, the first sync starts a `depctl-qdrant` container itself.
- `describe` shows `active version v1.6.0` and a complete backend replica.
- The search returns `v1.6.0` chunks such as `New creates a new random UUID or panics`, never another version's docs.

**Version correctness, live:** add a second project on another version, and each project gets only its own:

```bash
dir2=$(demo_project v1.5.0); depctl scan "$dir2"; pid2=$(demo_project_id "$dir2"); depctl sync --project "$pid2"
depctl describe go github.com/google/uuid      # both active: the newest sync is listed first, the other under "also active"
demo_search "$pid"  "random UUID"              # v1.6.0 only
demo_search "$pid2" "random UUID"              # v1.5.0 only
```

## Clean up

```bash
demo_reset
```

The managed `depctl-qdrant` container is shared with your real install, so the demo leaves it running. The demo's data sits in its own collection; to remove it, delete that collection (its name is `vector.collection` in the sandbox's `config.yaml`) before running `demo_reset`.
