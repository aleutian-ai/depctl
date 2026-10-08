# depctl

Guidance for Claude Code (and any future contributor) working in this repo.

## Go coding standard

- Every package file starts with a short package doc comment (one or two sentences on `package X`, only on the file that "owns" the package, e.g. the file defining its main type).
- Order within a file: **interfaces first, then the structs that implement them, then methods on those structs.** Don't interleave a struct's methods with unrelated types.
- Every exported type, func, and method gets a short doc comment (one line, occasionally two) starting with its name, per standard Go convention. No multi-paragraph docstrings.
- Prefer small, focused interfaces defined at the point of use (consumer-side), not speculative interfaces for a single implementation with no second caller expected.
- Keep everything else per the root-level engineering conventions already in force in this session: no speculative abstractions, no comments explaining *what* the code does (only non-obvious *why*), minimal surface area — pull in only what the current command/ticket needs.

## Project conventions

- Building CLI-first: commands in `internal/cli` are implemented one at a time, pulling in only the slice of `internal/domain`, `internal/config`, `internal/control/bbolt`, `internal/data/badger`, etc. that command actually needs — not full epic scope up front. See `docs/architecture.md`'s "Epics 1-2" section for how two early epics that spent a long time deliberately incomplete were later reconciled, not just finished.
- After every ticket/command lands: update `docs/architecture.md` (diagrams scoped strictly to shipped behavior) and re-verify tests both natively (macOS) and on Linux via `hack/test-linux.sh` (Podman/Alpine).
- `docs/tickets/` has three directories: `completed/` (every ticket in the epic is `Status: done`, verified against `docs/architecture.md`), `planned/` (epics being built right now; empty when nothing is in progress, and work pulled from `backlog/` may go straight to `completed/` once done), and `backlog/` (explicitly deferred scope — don't pull from backlog without the user asking). An epic moves to `completed/` only once every ticket in it is individually verified done — closing a ticket sometimes means reconciling it to a better shape a later epic proved out, not building the original sketch literally; see `docs/tickets/README.md`.
