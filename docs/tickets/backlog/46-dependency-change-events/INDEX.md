# Epic: Dependency Change Events

A 2026-09 competitive review (comparing against Grounded Docs and Tessl) surfaced a narrow, well-scoped primitive worth building ahead of any future "Upgrade Manager"/"Upgrade Lab" work: make a dependency-version change produce a durable, structured record — old version, new version, timestamp, project — separate from today's documentation content. `planner.Plan` already detects this transition (it's exactly how it decides a `SYNC_VERSION` action is needed) but the transition itself isn't currently persisted as its own fact; only the resulting `VersionReference` update and generation are.

The point, per the review, is to keep this cleanly separated from today's knowledge-serving path: a future upgrade-analysis feature should read change history from here, not by having speculative "what if you upgraded" content mixed into the same store that answers "what does this dependency actually do at the version I have now." This epic builds only the event/record; it does not build any consumer of it.

## Tickets
- [EVENT-001](EVENT-001-dependency-change-record.md) — Persist a `DependencyChangeEvent{ProjectID, Dependency, OldVersion, NewVersion, DetectedAt}` whenever `planner.Plan` detects a version change, queryable independently of the generation lifecycle.

## Non-goals
- No Upgrade Manager, no changelog summarization, no "should you upgrade" analysis — this epic is the event log a future feature would read, not that feature.
- No new MCP tool exposing this in this epic — once there's an actual consumer, that consumer's own ticket decides whether/how to expose it.
