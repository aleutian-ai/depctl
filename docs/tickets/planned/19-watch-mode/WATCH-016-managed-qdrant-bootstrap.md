# WATCH-016: Managed local Qdrant bootstrap

**Epic:** Watch Mode
**Status:** planned
**Depends on:** WATCH-015 (vector backend readiness — this ticket extends its background check with an auto-fix branch instead of just reporting)
**Estimated size:** medium

## Goal
"Qdrant unreachable" (WATCH-015) is a correct, actionable diagnosis, but for the MCP-first bootstrapping goal this whole arc has been chasing (`docs/scratch/mcp-bootstrapping.md`), the better outcome is: if a container runtime is available, ragctl starts its own Qdrant automatically, the same way it now auto-pulls its own embedding model (WATCH-014) rather than just reporting that one's missing too. Qdrant stays a hard requirement — this makes satisfying it operationally invisible when the pieces are available, not optional.

## Non-goals
- **Never containerize the daemon itself.** Only the vector backend moves into a container; the daemon stays a native process for the reasons already on record (`docs/tickets/backlog/44-containerized-embedding-backend/INDEX.md`'s Non-goals — real project roots, host toolchains, credentials, native fsnotify).
- **No Docker/Podman installation.** Exactly like WATCH-014 never installs the Ollama runtime — if neither `podman` nor `docker` is on PATH, this reports the requirement and stops; it does not attempt to install a container runtime.
- **No general container-runtime abstraction layer.** `podman run <flags>` and `docker run <same flags>` are the same invocation with the binary name swapped for the narrow flag set this needs (`-d --name --pull -p -v <image>`) — Podman's CLI is deliberately Docker-compatible for exactly this class of command. One small function building one arg list, parameterized only by which binary was found; no interface, no plugin system.
- **No `docker compose`/`podman-compose`, no multi-container orchestration.** One container, one job.
- **The daemon manages the container, never the MCP process (`ragctl serve`).** `ragctl serve` is a thin stdio↔daemon proxy with no store access and no business logic (ADR-011 §8) — it must not gain a new responsibility. Container lifecycle lives in `runDaemonRun`'s background check, the same place WATCH-014/015's checks live.

## Simplicity constraints
- Extends WATCH-015's `checkVectorReadiness` (`internal/cli/vector_readiness.go`) with one new branch, not a parallel mechanism: unreachable + `cfg.Vector.Managed` → attempt the bootstrap; unreachable + not managed → report exactly as WATCH-015 already does, unchanged.
- Readiness polling reuses `backend.VectorBackend.Health(ctx)` (same as WATCH-015) against the *just-started* container — never parses `docker ps`/`podman inspect` output for health, only for the one narrow "does a container with this name already exist" idempotency check below.

## Design
`internal/config/config.go`'s `VectorConfig` gains one field:
```go
type VectorConfig struct {
    Backend    string `yaml:"backend"`
    Endpoint   string `yaml:"endpoint"`
    Collection string `yaml:"collection"`
    APIKeyEnv  string `yaml:"api_key_env,omitempty"`
    Managed    bool   `yaml:"managed,omitempty"`
}
```
`config.Default()` sets `Managed: true` for fresh installs — a new user gets the fully automatic story by default. An existing `config.yaml` written before this ticket has no `managed` key, which YAML-unmarshals to `false` (Go zero value) — so nobody who's already pointed `vector.endpoint` at their own real Qdrant gets a surprise second instance competing for port 6333. This asymmetry (new installs default on, existing configs default off) is deliberate, not an oversight — flag it in the PR description so it doesn't read as a bug later.

`internal/cli/vector_bootstrap.go` (new file):
```go
const qdrantImage = "docker.io/qdrant/qdrant:v1.13.1" // pinned; matches docs/offline-quickstart.md's own pin
const qdrantContainerName = "ragctl-qdrant"
const qdrantStartupTimeout = 20 * time.Second

// containerRuntime returns the path to podman or docker, preferring
// podman (this project's own dev environment already uses it — see
// docs/offline-quickstart.md), or "" if neither is on PATH.
func containerRuntime() string

// ensureManagedQdrant starts (or reuses, if already running/stopped
// from a prior daemon lifetime) a ragctl-owned Qdrant container bound
// to loopback only, with storage persisted under the ragctl data
// directory, and polls it healthy via backend.VectorBackend.Health.
func ensureManagedQdrant(ctx context.Context, cfg config.Config, runtime string, logf func(format string, args ...any)) error
```
Container invocation (identical args regardless of `runtime`):
```text
<runtime> run -d --name ragctl-qdrant --pull=missing \
  -p 127.0.0.1:6333:6333 \
  -v <dataDir>/qdrant:/qdrant/storage \
  docker.io/qdrant/qdrant:v1.13.1
```
`<dataDir>` is `config.DefaultDataDir()`, matching the `git`/`registry` subdirectories `internal/cli/init.go`'s `initStores` already creates there — `qdrant/` becomes a sibling.

Idempotency: a daemon restart (WATCH-005's lifecycle) must not fail with "container name already in use." Before `run`, check via `<runtime> ps -a --filter name=^ragctl-qdrant$ --format '{{.Names}}'` (identical flag across both runtimes — this is the one narrow, existence-only use of the runtime's own inspection commands this ticket allows, per the Non-goals above): if the container exists but is stopped, `<runtime> start ragctl-qdrant` instead of `run`; if already running, skip straight to the readiness poll.

`checkVectorReadiness` (WATCH-015, `internal/cli/vector_readiness.go`) gains the new branch:
```go
if err := probeBackend(ctx, cfg); err != nil {
    if !cfg.Vector.Managed {
        readiness.set(vectorStateUnreachable, ...)
        return
    }
    runtime := containerRuntime()
    if runtime == "" {
        readiness.set(vectorStateUnreachable, "vector.managed is true but no container runtime (podman or docker) was found on PATH — install one, or start Qdrant manually and set vector.managed: false")
        return
    }
    readiness.set(vectorStatePulling /* reuse WATCH-015's naming, or a new "starting" state — see open question below */, qdrantContainerName)
    if err := ensureManagedQdrant(ctx, cfg, runtime, logf); err != nil {
        readiness.set(vectorStateError, err.Error())
        return
    }
    readiness.set(vectorStateReady, "")
    return
}
readiness.set(vectorStateReady, "")
```
Open question for implementation: WATCH-015 didn't define a "pulling"-equivalent state for the vector backend (it has no download step on its own). This ticket needs one — either add a `vectorStateStarting` state, or reuse the existing four-state enum with the detail string carrying "starting managed container" — pick whichever keeps `embeddingStatusLabel`-style formatting (`internal/cli/daemon.go`) simplest to extend in parallel for both readiness types.

## Inputs / Outputs
- Input: `vector.managed` config (new); `podman`/`docker` on the host's PATH.
- Output: a running `ragctl-qdrant` container on a successful bootstrap; `ragctl daemon status`'s `vector:` line (WATCH-015) additionally reports managed/container name/endpoint once WATCH-017 (doctor/status formatting) lands — this ticket only needs the underlying state to exist, not necessarily fully formatted output (WATCH-017 owns presentation).

## Failure behavior
- Qdrant unreachable, `vector.managed: false`: identical to WATCH-015, unchanged — no auto-start attempted.
- Qdrant unreachable, `vector.managed: true`, no runtime found: one clean structured error naming exactly what's missing and the two ways to fix it (install a runtime, or start Qdrant manually and flip the flag) — never a raw `exec: "podman": executable file not found in $PATH`.
- Qdrant unreachable, `vector.managed: true`, runtime found, container fails to start or never becomes healthy within `qdrantStartupTimeout`: `vectorStateError` with the runtime's own stderr/the last health-probe error attached — not left stuck in a "starting" state forever.
- A previously-managed container left running from an earlier daemon lifetime: reused via the idempotency check above, not duplicated or errored on.

## Tests
- `containerRuntime()` prefers podman when both are on PATH (mock `exec.LookPath` via the same seam `internal/cli/doctor.go`'s `doctorEnv.lookPath` already uses, or a package-level var like `daemonExecutable`'s existing test-override pattern).
- `ensureManagedQdrant` against a fake `podman`/`docker` shell script (same technique `daemon_autostart_test.go`'s `requireRagctlBinary`/broken-daemon tests already use for a scriptable fake executable) proving: the exact expected args are passed, a name-conflict is handled via `start` instead of `run`, and a health-probe timeout surfaces as `vectorStateError` not an infinite hang.
- Real integration test (skipped unless podman/docker is actually available in the test environment, matching `requireGo`'s pattern of skipping rather than failing when a required tool is absent): a fresh managed Qdrant genuinely comes up and `sync` succeeds against it end to end.
- Config default test: `config.Default(dir).Vector.Managed == true`; a config loaded from YAML with no `managed` key round-trips to `false`.

## Acceptance criteria
- [ ] `vector.managed: true` (the new-install default) auto-starts a pinned, loopback-only, persistently-stored Qdrant container when none is reachable and a container runtime is available.
- [ ] `vector.managed: false` (the default for any config predating this ticket) never attempts this — zero behavior change for existing users with their own Qdrant.
- [ ] No container runtime available: one clean, actionable structured error, never a raw `exec` lookup failure.
- [ ] A daemon restart reuses an already-running or stopped managed container instead of erroring or duplicating it.
- [ ] The daemon never blocks a client request on container startup — this lives entirely in the same background-goroutine mechanism WATCH-014/015 already established.
