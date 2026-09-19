# STRESS-005: Full cold sync at real scale

**Epic:** Full-Loop Stress Testing
**Status:** done
**Depends on:** STRESS-001 (reuses its project — `hashicorp/terraform`, or `prometheus/prometheus` if that was the fallback used)
**Estimated size:** medium

## Goal
Run a full, cold `ragctl sync` (no `--dependency` filter — every resolved dependency) against STRESS-001's 100+-dependency project, against a real embedder (Ollama) and real vector backend (Qdrant), and measure real wall-clock and disk usage. This is the actual scenario that originally motivated GIT-005 (14+ minutes, 828MB, zero completions, before that fix) — this ticket re-measures it post-fix, at the same real scale, not a fixture-sized proxy.

## Non-goals
- No comparison against Grounded Docs or any other system in this ticket — that's `context-evals` scope per epic 45's own note.
- No fixing of anything found beyond confirming/denying the GIT-005 improvement — a new bottleneck found here gets its own follow-up ticket.

## Simplicity constraints
- Reuses the already-running `ragctl-qdrant` container and local Ollama from this session's existing live-testing setup (`vector.managed: false` pointed at the existing container) rather than provisioning new infrastructure.

## Design
1. Fresh `ragctl init` (isolated `HOME`), `vector.managed: false` pointed at the real running Qdrant, real Ollama embedder configured.
2. `ragctl scan <STRESS-001 project>`.
3. `time ragctl sync` (no filter) — full cold sync.
4. Record: total wall-clock, `du -sh` of the git cache directory and the Badger/bbolt data directory afterward, `synced`/`failed`/`skipped` counts from the sync report.
5. Compare against the original pre-GIT-005 numbers (14+ min, 828MB, zero completions) qualitatively — this ticket's job is to establish the current real number, not to hit a specific target.

## Inputs / Outputs
- Input: STRESS-001's scanned project, a real embedder and vector backend.
- Output: recorded wall-clock, disk usage, and synced/failed/skipped counts — a real, dated data point (like VALID-003's benchmark), not a one-time anecdote.

## Failure behavior
- Network/Ollama/Qdrant-dependent — skip (not fail) if any of those aren't reachable.
- A failed-dependency count greater than a small, explicable fraction (e.g. a genuinely bad manifest match) is this ticket's finding — record which dependencies failed and why.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note, mirroring VALID-003's dated-measurement format.

## Acceptance criteria
- [x] A full cold sync of a real 100+-dependency project completes, with wall-clock and disk usage recorded. (It did not run to full completion in one invocation — see Post-implementation note for why, which is itself this ticket's finding.)
- [x] The recorded numbers are compared against the pre-GIT-005 baseline in this ticket's own write-up.
- [x] Any failed dependencies are explained, not silently ignored.

## Post-implementation note

Ran via `hack/test-linux.sh`'s Podman pattern against real `hashicorp/terraform` (STRESS-001's fixture), real Ollama (forwarded via `host.containers.internal` + `socat`), and the real, already-running `ragctl-qdrant` container, `vector.managed: false`. One `ragctl sync` (no filter, no `--project`) after a fresh scan.

**Headline finding: a full cold sync of a genuinely large real project cannot complete in a single `ragctl sync` invocation, and the failure mode is misleading.** The run took exactly **2100s (35:00)** — not a coincidence: `internal/daemon/client/client.go`'s `longRunningRequestTimeout = 35 * time.Minute` cut the client-side HTTP request off at exactly that mark. Result: **1 synced, 560 failed, 0 skipped**. Breaking down the 560 failures by actual cause (not all the same problem):

1. **202 failures: `no registry manifest for <package>`.** Expected, orthogonal to this ticket — ragctl's knowledge registry only has curated manifests for a known set of packages, and terraform's real, wide dependency tree includes hundreds of real packages (`cloud.google.com/go/*`, `k8s.io/*`, `golang.org/x/*`, etc.) nobody has added a manifest for yet. This is [epic 34](../../backlog/34-registry-coverage/INDEX.md)'s already-known, already-backlogged gap ("manifest fallback, candidate source discovery"), not a new discovery — this run is just the first time it's been observed at real scale, confirming that gap matters in practice.
2. **358 failures: `context deadline exceeded`**, scattered from the *second* dependency processed through to the last, not clustered only at the very end. Root cause traced to `internal/daemon/scheduler.go`'s `execute`: `maxActionDuration = 30 * time.Minute` wraps **one call to `s.run`**, and for an untargeted `ragctl sync`, `s.run` executes the *entire* plan — every pending action across every registered project, potentially hundreds of git clones and embed calls — inside that single 30-minute budget, not per-dependency. The code comment explains the original reasoning honestly: *"real syncs in this codebase's own corpus testing finished in well under a minute even for large repos"* — true for the fixture-scale and even STRESS-001-scale testing done before now, but never tested against a batch of 500+ *never-before-synced* real dependencies in one call. Once a few genuinely slow early items (real git clones of large repos, real embed calls) eat into the shared 30-minute ceiling, every dependency processed after it fails instantly with the same generic `context deadline exceeded` — reading as ~360 independent per-dependency failures when the real cause is one shared, exhausted budget. This is a UX/diagnosability problem as much as a capacity one: nothing in the output tells you "the batch ran out of time," it looks like a wall of unrelated per-package failures.
3. **A secondary, benign observation**: partway through, the log shows `sync already running for <projectID>; queued a follow-up` — the daemon's file-watch-triggered auto-sync (epic 19) fired concurrently with this manual `ragctl sync` invocation, and the scheduler's coalescing correctly serialized them rather than running both at once. Working as designed, not a finding.

**Recorded numbers** (partial completion, per the finding above): 2100s wall-clock, 1 synced / 560 failed / 0 skipped, 119.3 MiB git mirror cache, 1.5 GiB total local data directory.

**Compared against the pre-GIT-005 baseline** (14+ min, 828 MB, zero completions): meaningfully different failure shape, not a clean improvement to report. Disk usage for equivalent partial progress is far smaller (119 MiB vs. 828 MB) — GIT-005's blobless/sparse-checkout fix is doing its job. But completion is still effectively zero for a project this size, for a different reason than before: not runaway disk/time per clone, but a hard 30-minute ceiling on the *entire* batch regardless of its size. **Filed as its own follow-up, per this ticket's own non-goal against fixing what it finds**: [epic 51](../../backlog/51-bulk-sync-batch-timeout/INDEX.md).

**Update — the actual root cause of item 2 was found and fixed the same session, not left as a black box.** Digging into *why* `cloud.google.com/go` alone was consuming the entire batch budget (not just accepting "it's slow") found: `internal/data/generation/build.go`'s `normalizeSources` had no awareness of nested Go module boundaries. `cloud.google.com/go`'s real repository has a one-file root module (`doc.go`) alongside **216 separately-versioned sibling modules** (`storage/`, `bigquery/`, `pubsub/`, etc., each its own `go.mod`) in the same git repo — the old walk normalized all of them as if they belonged to the root module being synced. Measured directly: **13,399** directories touched vs. **23** correctly in scope — a ~583x over-normalization, and a correctness bug too (sibling packages' docs mislabeled as the wrong dependency's own), not just a performance one. Fixed and live-reverified: see [epic 52 / BOUND-001](../../completed/52-nested-go-module-normalization-boundary/INDEX.md) — `cloud.google.com/go` now syncs in ~35s total instead of consuming the entire batch budget and failing.

**Update 2 — the full untargeted sync was re-run against a freshly-wiped Qdrant with the BOUND-001 fix in place, to get the real, complete number rather than stop at the single-dependency proof.** Result: **154 synced, 407 failed, 0 skipped** — up from 1 synced / 560 failed before the fix, a real, dramatic improvement, not just a faster failure on the same dependency. Of the 407 remaining failures: 80 are the already-known, separate "no registry manifest" registry-coverage gap (epic 34); 327 are the same batch-timeout cascade as before, hit at the exact same 2100s (35:00) ceiling — but this time triggered by a *different* dependency, `github.com/aliyun/alibaba-cloud-sdk-go`. Checked directly: this one has only **one** `go.mod` (not the nested-module shape BOUND-001 fixes) — it's a genuinely enormous single module in its own right, **36,132 `.go` files across 313 directories**, bundling dozens of individual Alibaba Cloud service clients under one package tree (unlike Google's convention of splitting each into its own module). BOUND-001 correctly does nothing here since there's no boundary to skip — all of that content legitimately belongs to this one dependency. This is real, concrete confirmation that epic 51/BATCH-001's batch-timeout question remains open and necessary, now correctly scoped to genuinely-massive single-module dependencies rather than the over-normalization artifact that dominated this run's original failures. Disk usage for this complete run: 162 MiB git cache, 1.2 GiB total data directory.
