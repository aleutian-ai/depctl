# JAVA-002: Gradle resolver

**Epic:** Java Resolver
**Status:** declined (2026-09-30)
**Depends on:** RES-001, RES-002
**Estimated size:** medium

## Goal
Detect Gradle projects (`build.gradle`/`build.gradle.kts`) and resolve exact dependency versions by preferring existing dependency lock state, falling back to a resolved dependency report produced by Gradle itself.

## Non-goals
- Parsing arbitrary Gradle DSL (Groovy or Kotlin) as text — explicitly disallowed by the implementation plan.
- Supporting every Gradle plugin/build variant; ship partial support honestly per the plan's Risk 2 mitigation ("ship partial support honestly").

## Simplicity constraints
- Prefer Gradle's own lock file (`gradle.lockfile` / `dependency-locks/`) when present — this needs no Gradle invocation at all, just a line parser (`group:artifact:version` per line).
- Only when no lock state exists, invoke a dedicated Gradle task/init script that emits JSON (a small init script, not custom DSL parsing) — do not attempt free-form output scraping of `gradle dependencies`.

## Design
Package: `internal/resolver/java` (Gradle-specific file, e.g. `gradle.go`).

Resolution order:
1. If `gradle.lockfile` or `dependency-locks/*.lockfile` present, parse directly (format: `group:artifact:version=configurationNames`).
2. Else, run `./gradlew` (or `gradle`) with a bundled init script that writes a resolved dependency report to a temp JSON file, then read that file.

```go
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error)
```
Identity format matches JAVA-001: `group:artifact:version[:classifier]`.

## Inputs / Outputs
- Input: project root with `build.gradle`/`build.gradle.kts` and optional lock files.
- Output: `domain.Resolution`.

## Failure behavior
- No lock state and Gradle invocation fails/unavailable → typed `ResolutionError` stating partial support and the missing lock file as the recommended fix.
- Init-script JSON malformed → typed `ResolutionError`.

## Tests
- `testdata/projects/java-gradle/` fixture with `gradle.lockfile` present — parsed without invoking Gradle.
- Fixture without lock state — falls back to init-script report (mark as integration test if it requires a real Gradle install; keep a fake/fixture JSON output for pure unit tests).

## Acceptance criteria
- [ ] Lock-file path resolves without shelling out.
- [ ] Fallback path never parses raw Gradle DSL text.
- [ ] Missing Gradle wrapper/binary produces a clear, actionable error.


## Declined (2026-09-30)

Not being built — a deliberate priority call, not a technical blocker. Reviewed against the shipped `Resolver` interface (`internal/resolver`) during a backlog-triage discussion: it is already fully ecosystem-agnostic (`Name() string`, `Detect(ctx, root) (bool, error)`, `Resolve(ctx, root) (domain.Resolution, error)`), `domain.Ecosystem` already carries unused `EcosystemRust`/`EcosystemJava` constants, and `domain.Resolution`/`DependencyVersion`/`Dependency` have zero ecosystem-specific fields — exactly the same shape Go/Python/Node's real resolvers already prove out (shell out to the ecosystem's own tool, parse its structured JSON output, map to `domain.Resolution`). This ticket's own design already matches that pattern. Nothing here needs re-scoping or new interface work if picked up later — it was never the hard part.
