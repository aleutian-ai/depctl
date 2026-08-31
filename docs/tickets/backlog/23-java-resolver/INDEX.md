# Epic: Java Resolver

Detect Maven and Gradle projects and resolve exact `group:artifact:version` dependency identities. Java is the most operationally variable of the five v1 ecosystems and should be built last; ship honest partial support rather than a fragile complete one.

- [JAVA-001](JAVA-001-maven-resolver.md) — resolve via `mvn dependency:tree` (or structured equivalent), never hand-parsed POM XML.
- [JAVA-002](JAVA-002-gradle-resolver.md) — resolve via lock state first, then a Gradle-generated JSON report; never parse Gradle DSL as text.
