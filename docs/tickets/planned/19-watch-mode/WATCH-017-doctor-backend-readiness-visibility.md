# WATCH-017: `doctor` visibility for backend readiness

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-014 (embedding readiness), WATCH-015 (vector readiness), WATCH-016 (managed Qdrant) — this ticket only surfaces state those already maintain, it computes nothing new.
**Estimated size:** small

## Goal
`ragctl doctor` should make the full backend-readiness picture obvious at a glance — Ollama reachable, the embedding model actually pulled, Qdrant reachable, and (once WATCH-016 lands) whether it's a managed container and which one. Explicitly **fine with this being several separate, granular checks rather than one collapsed line** — a "waiting for these systems to come up" style report (Ollama / model / Qdrant / bbolt / Badger each as their own line) is the desired shape, not something to compress. bbolt and Badger openness are already covered by existing checks (`checkControlDB`, `checkBadger`, `internal/cli/doctor.go:283,310`) — this ticket doesn't touch those, just extends the same list with the embedding/vector pieces that don't have dedicated checks yet.

## Non-goals
- No new probing logic — this ticket is purely a presentation/wiring layer over WATCH-014/015/016's already-maintained state. If a check here needs to *compute* something new, that computation belongs in one of those tickets instead.
- No change to `checkBackendReachable`/`checkEmbeddingModel` (`internal/cli/doctor.go:432,447`) — those check different things (a live reachability probe; whether *already-active generations* match the currently-configured model) and stay exactly as they are. The new checks in this ticket are additive, not replacements.
- No redesign of `printDoctorReport`'s flat one-line-per-check format (`internal/cli/doctor.go:253`) into a sectioned/grouped report — the proposed mockup groups checks under "Embedding backend"/"Vector backend" headers, which would be a real (if small) formatting change beyond what this ticket needs; ship as additional flat-list entries first, matching every other check's existing convention, and revisit grouping only if the flat list genuinely reads poorly once these land.

## Simplicity constraints
- Each new check is a `(Severity, string)` function added to `doctorChecks` (`internal/cli/doctor.go:72`), same shape as every existing entry — no new check-registration mechanism.
- The daemon-backed path (`runDoctorViaDaemon`) and the no-daemon fallback path (`runDoctorDirect`) both need an answer, but they don't have to compute it the same way: the daemon path can just read `EmbeddingReadiness`/`VectorReadiness` (WATCH-014/015's `Engine` methods) since the daemon already maintains that state continuously; the no-daemon fallback (which doesn't have a live background check running) does a direct, synchronous probe instead — same relationship `checkConfigFreshness` already has (`internal/cli/doctor.go:212` — only runs in `runDoctorViaDaemon`, since comparing against the *running* daemon's loaded config is meaningless with no daemon to compare against) or `checkBackendReachable` (works in both paths via a direct probe either way, since reachability doesn't depend on whether a daemon happens to be up). Decide per-check which pattern fits; don't force one.

## Design
Two new checks appended to `doctorChecks` (`internal/cli/doctor.go:72`):

- `"embedding backend"` → in the daemon path, reads `engine.EmbeddingReadiness(ctx)` (WATCH-014, already exposed via `Engine.EmbeddingReadiness`) and renders it through the same `embeddingStatusLabel`-style mapping `internal/cli/daemon.go`'s `runDaemonStatus` already uses, so `daemon status` and `doctor` never drift into inconsistent wording for the same state. In the no-daemon fallback path (`runDoctorDirect`, `doctorEnv` has no live daemon to ask), do a direct one-shot check: reuse `checkEmbeddingReadiness`'s probing logic (WATCH-014, `internal/cli/embedding_readiness.go`) but call it synchronously against a throwaway `*embeddingReadiness` rather than backgrounding it — `doctor` is already a diagnostic command expected to make live calls (see `checkBackendReachable`'s existing behavior), so a direct probe here is consistent, not a new pattern.
- `"vector backend"` → same relationship to WATCH-015's `VectorReadiness`/`checkVectorReadiness`, and (once WATCH-016 lands) reports managed-mode/container name/endpoint in its detail string when `cfg.Vector.Managed` is true — e.g. `"qdrant reachable at 127.0.0.1:6333 (managed: ragctl-qdrant)"` vs plain `"qdrant reachable at 127.0.0.1:6333"` when unmanaged.

`api.DoctorResponse`/`api.CheckResultWire` (`internal/daemon/api/api.go`) need no new fields — these are just two more entries in the existing `Checks []CheckResultWire` slice `engine.Doctor` (`internal/cli/daemon.go:680`) already returns; `doctorChecksInDaemon` (`internal/cli/daemon.go:674`, "every `doctorChecks` entry except the last two" — the two PATH-lookup checks that must run client-side regardless) picks them up automatically as long as they're not appended after that slice boundary.

## Inputs / Outputs
- Input: WATCH-014/015/016's already-maintained readiness state (daemon path) or a direct one-shot probe (no-daemon fallback path).
- Output: two new lines in `ragctl doctor`'s report, e.g.:
  ```
  OK         embedding backend              ollama: nomic-embed-text-v2-moe ready
  OK         vector backend                 qdrant reachable at 127.0.0.1:6333 (managed: ragctl-qdrant)
  ```

## Failure behavior
- Embedding/vector backend not ready: `SeverityUnhealthy` (matching `checkBackendReachable`'s existing severity choice for the same underlying condition — an unreachable required backend), detail text identical in wording to what `sync`/search would themselves report (WATCH-014/015's structured errors), so a user isn't left reconciling two different phrasings for the same problem.
- No-daemon fallback path probing a genuinely unreachable Ollama/Qdrant: bounded by the same timeouts `checkEmbeddingReadiness`/`checkVectorReadiness` already use — `doctor` must never hang waiting on a dead endpoint.

## Tests
- Daemon-path doctor check reflects whatever `EmbeddingReadiness`/`VectorReadiness` currently report — a fake `Engine` (or the real one, driven through `deadEndpointsConfig`) proves the check surfaces `unreachable` correctly.
- No-daemon fallback path's direct probe correctly reports both ready and unreachable cases against a real `httptest.Server` standing in for Ollama/Qdrant.
- `doctorChecksInDaemon`'s slice-boundary invariant (`internal/cli/daemon.go:674`, "every entry except the last two") still holds — the two new checks land before the PATH-lookup checks, not after, so they run in the daemon path too. A regression here is exactly the kind of AST-adjacent, easy-to-get-wrong-silently issue `TestOnlyAllowedFunctionsOpenStoresDirectly` (WATCH-011) exists to catch for a different invariant — this one just needs an explicit test, not a new AST check.

## Acceptance criteria
- [x] `ragctl doctor` reports embedding-backend and vector-backend readiness as two new, granular checks — not collapsed into existing ones.
- [x] Both the daemon-backed and no-daemon-fallback doctor paths answer correctly.
- [x] Wording matches what `sync`/search themselves report for the same underlying state (WATCH-014/015), so there's one consistent vocabulary across the whole tool, not doctor-specific phrasing.
- [x] Once WATCH-016 lands, the vector-backend check's detail line distinguishes managed from unmanaged Qdrant.

## Post-implementation note
Shipped per spec: `doctorEnv` gained `embeddingReadiness`/`vectorReadiness` fields (nil in the no-daemon fallback, wired from `engine.embeddingReadiness`/`vectorReadiness` in `engine.Doctor`). `checkEmbeddingBackend`/`checkVectorBackend` read that live state when present, or fall back to a synchronous one-shot probe via `checkEmbeddingReadiness`/`checkVectorReadiness` (WATCH-014/015's own functions, no new probing logic) when it's nil. Both were added to `doctorChecks` right before the two PATH-only checks, preserving `doctorChecksInDaemon`'s slice-boundary invariant. Non-ready states delegate to the existing `embeddingStatusLabel`/`vectorStatusLabel` formatters so wording never drifts from `daemon status`; the `ready` state is special-cased in `checkVectorBackend` to build its own richer label (`"qdrant reachable at <endpoint>"`, `" (managed: ragctl-qdrant)"` appended only when `cfg.Vector.Managed`), since `vectorStatusLabel`'s terse `"ready"` (correct for `daemon status`) doesn't carry the endpoint/managed detail this ticket's acceptance criteria specifically asked for. Verified live against a real config and daemon, both with and without `vector.managed` set. Tests: `healthyDoctorEnv` was fixed to point `Embedding.Endpoint` at a fake Ollama server (it was silently depending on a real local Ollama actually running, an environment-dependent test bug this ticket's work surfaced) plus dedicated coverage for the no-daemon probe path, the daemon-fed live-state path (including a real-daemon integration assertion in `TestDoctorUsesDaemonWhenReachable`), and the managed-vs-unmanaged wording. Full suite green.
