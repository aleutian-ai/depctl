# EXT-004: Phoenix/Langfuse guide

**Epic:** External evaluation integrations
**Status:** planned
**Depends on:** OBS-002 (OpenTelemetry tracing)
**Estimated size:** small

## Goal
Document how to point `depctl`'s OpenTelemetry export (OBS-002) at Arize Phoenix or Langfuse, without adding either as a core dependency.

## Non-goals
- No proprietary SDK dependency added to core (`go.mod` stays free of Phoenix/Langfuse-specific packages).
- No bundled deployment of either tool.

## Simplicity constraints
- This is a documentation-only ticket. Configuration is just the existing `observability.otel.endpoint` config field from OBS-002 pointed at each tool's OTLP-compatible ingest endpoint.

## Design
Add `docs/observability.md` (or a section of an existing ops doc) covering:

- enabling `observability.otel.enabled: true` and setting `endpoint` to a local Phoenix or Langfuse OTLP collector URL,
- example `docker run` commands for running each tool locally,
- which span names (from OBS-002) show up and how to interpret them (resolve/plan/acquire/... /gc).

## Inputs / Outputs
- Input: none (documentation).
- Output: `docs/observability.md`.

## Failure behavior
N/A.

## Tests
N/A — documentation ticket. Verify manually that the example config produces visible traces in a locally run Phoenix/Langfuse instance before merging.

## Acceptance criteria
- [ ] Doc covers both Phoenix and Langfuse setup using only the existing OTLP config surface.
- [ ] No new Go dependency introduced.
