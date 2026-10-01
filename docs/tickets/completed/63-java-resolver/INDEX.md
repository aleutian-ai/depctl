# Epic: Java Resolver

**Declined (2026-09-30)** — not a technical blocker, a deliberate priority call. Backlog triage confirmed the shipped `Resolver` interface (`internal/resolver`) is already fully ecosystem-agnostic and `domain.Ecosystem` already carries an unused `EcosystemJava` constant — adding Java support later is "write one more implementation of an interface that already exists," not an architecture project. See each ticket's own "Declined" note for the full reasoning. Not prioritized right now; trivial to pick back up whenever a real Java project needs ragctl.

Detect Maven and Gradle projects and resolve exact `group:artifact:version` dependency identities. Java is the most operationally variable of the five v1 ecosystems and should be built last; ship honest partial support rather than a fragile complete one.

- [JAVA-001](JAVA-001-maven-resolver.md) — resolve via `mvn dependency:tree` (or structured equivalent), never hand-parsed POM XML.
- [JAVA-002](JAVA-002-gradle-resolver.md) — resolve via lock state first, then a Gradle-generated JSON report; never parse Gradle DSL as text.
