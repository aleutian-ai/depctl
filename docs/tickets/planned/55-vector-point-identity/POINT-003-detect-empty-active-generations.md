# POINT-003: Detect empty active generations

**Epic:** Vector Point Identity
**Status:** planned

## Problem
Any Qdrant collection populated before POINT-001 contains generations marked ACTIVE whose points were overwritten by a sibling. They report as synced but search returns nothing, and nothing flags them.

## Design
- A check (in `ragctl doctor`, or a `--verify` mode) that, for each ACTIVE generation, counts its points in the vector backend (filter by generation) and reports any with zero (or far fewer than the manifest's chunk count).
- A repair path: re-sync those generations (`--force`).

## Acceptance criteria
- [ ] Doctor reports an ACTIVE generation with no points.
- [ ] Re-sync restores searchable points, verified by a query.
