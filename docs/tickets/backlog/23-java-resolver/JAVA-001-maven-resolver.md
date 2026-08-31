# JAVA-001: Maven resolver

**Epic:** Java Resolver
**Status:** planned
**Depends on:** RES-001, RES-002
**Estimated size:** medium

## Goal
Detect Maven projects (`pom.xml`) and resolve exact dependency versions using a machine-readable Maven dependency tree/list command, normalizing identity to `group:artifact:version`.

## Non-goals
- Gradle (JAVA-002).
- Parsing `pom.xml` by hand for dependency resolution — always shell out to Maven for the resolved graph (parent POMs, dependency management, property interpolation make hand-parsing unreliable).

## Simplicity constraints
- Java is explicitly called out as the most operationally variable ecosystem and should be last among the five (per implementation plan §26/M22). Ship a minimal, honest implementation: if `mvn` isn't available or the project uses an unusual build setup, fail clearly rather than guessing.
- Do not implement a POM XML parser/dependency resolver in Go.

## Design
Package: `internal/resolver/java` (Maven-specific file, e.g. `maven.go`).

```go
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error)
```
Run a machine-readable dependency command via `internal/executil`, e.g.:
```bash
mvn -q dependency:tree -DoutputType=text -DoutputFile=/dev/stdout
```
or preferably a JSON/structured output plugin if reliably available; otherwise parse the standard indented tree-text format defensively (regex per line: `group:artifact:packaging:version:scope`).

Normalized identity:
```text
ecosystem = java
name = group:artifact
version = version
classifier (when present)
```
Example: `io.grpc:grpc-netty-shaded:1.75.0`.

## Inputs / Outputs
- Input: project root with `pom.xml`.
- Output: `domain.Resolution` with `group:artifact` as `name`.

## Failure behavior
- `mvn` binary missing → typed `ResolutionError`, surfaced via `ragctl doctor`.
- Unparseable tree output → typed `ResolutionError` with raw output attached for debugging.

## Tests
- `testdata/projects/java-maven/` fixture: direct + transitive dependency resolve to `group:artifact:version`.
- Multi-module Maven project (parent + child modules) resolves each module's own dependencies.

## Acceptance criteria
- [ ] Fixture resolves exact `group:artifact:version` triples.
- [ ] Missing `mvn` binary produces an actionable error, not a silent empty resolution.
