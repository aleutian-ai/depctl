# POS-001: Positioning language update (repository-state authority, not "better docs RAG")

**Epic:** Competitive Validation
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
The Grounded Docs comparison's clearest actionable recommendation was about framing, not code: don't position ragctl as a better general-purpose documentation RAG system — Grounded Docs (and Tessl, differently) already occupy real, credible territory there, including local-first, MIT-licensed, broader-format acquisition. ragctl's precise, defensible claim is narrower and already written down: *"ragctl keeps a coding agent's dependency knowledge synchronized with the actual state of the repository it's working on."* Update the docs that currently lead with the broader framing to lead with this one instead.

## Non-goals
- No new marketing copy invented here beyond what's already in the project's own one-pagers/specs — this ticket applies existing language, it doesn't draft new positioning from scratch.
- No removal of legitimate "also locally hosted, also private by default" claims — those remain true and worth stating, just not as the lead differentiator (per the comparison: "do not position ragctl as the only locally hosted or private option").

## Simplicity constraints
- Doc-only change: `README.md`'s opening framing, `docs/architecture.md`'s introduction if it leads with a general-RAG framing, and any one-pager/spec doc surfaced in this comparison that still says something like "documentation RAG for AI agents" without the repository-state qualifier.

## Design
Lead framing becomes some variant of:
> ragctl keeps a coding agent's dependency knowledge synchronized with the actual state of the repository it's working on — native dependency resolution, local acquisition, demand-driven synchronization, version-scoped retrieval, and explicit missing-knowledge states (never a silent cross-version fallback).

Followed by an explicit, honest scoping statement modeled on the comparison's own framing: ragctl is not the only locally-hosted or private documentation option, and does not (yet) match broader tools' acquisition format coverage (arbitrary websites, PDFs, Office docs) — its bet is that automatic, validated repository-state synchronization matters more for a coding agent than acquisition breadth.

## Inputs / Outputs
- Input: current `README.md` / `docs/architecture.md` framing language.
- Output: updated opening sections in both, consistent with each other and with the one-pager language already in use.

## Failure behavior
- N/A (documentation-only).

## Tests
- N/A (documentation-only) — reviewed for consistency with `docs/architecture.md`'s existing tone (per `CLAUDE.md`: diagrams and language scoped strictly to shipped behavior, not aspirational claims).

## Acceptance criteria
- [x] `README.md`'s opening framing leads with repository-state synchronization, not general documentation RAG.
- [x] The explicit non-exclusivity scoping statement (not the only local/private option; narrower acquisition breadth than some alternatives) is present somewhere in the top-level docs, not just implied.

## Post-implementation note
This ticket's first assumption — that `README.md`/`docs/architecture.md` currently lead with a generic "documentation RAG for AI agents" framing — turned out already false by the time this ticket was picked up: `README.md`'s opening paragraph already emphasized "detects the exact dependency versions... keeps a retrieval index in sync as dependencies change... exposes version-correct knowledge," and `docs/architecture.md` never carried positioning language at all (it's a pure "what's implemented" doc). Confirmed via a direct grep for "documentation RAG"/"general RAG"/"RAG system" across both files and every top-level doc — zero matches. The real, remaining gap was narrower than the ticket assumed: the honest non-exclusivity scoping statement (not the only local/private option; narrower acquisition breadth than Grounded Docs specifically) was genuinely missing. Added as a new paragraph in `README.md` right after the opening description.
