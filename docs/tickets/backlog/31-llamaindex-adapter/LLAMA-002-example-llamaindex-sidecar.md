# LLAMA-002: Example LlamaIndex sidecar

**Epic:** LlamaIndex adapter
**Status:** planned
**Depends on:** LLAMA-001
**Estimated size:** small

## Goal
Ship a reference Python implementation of the LLAMA-001 stdio protocol backed by LlamaIndex, under `examples/llamaindex-sidecar/`, as documentation-by-example — not as a maintained core component.

## Non-goals
- Not part of the core Go build or release binary.
- Not covered by the Go test suite (may have its own lightweight Python test, optional).

## Simplicity constraints
- A single small Python script reading stdin JSON, invoking a couple of relevant LlamaIndex readers/node parsers, writing stdout JSON. Do not build a packaged/pip-installable project, CLI framework, or config system for this example.

## Design
Directory: `examples/llamaindex-sidecar/`

```text
examples/llamaindex-sidecar/
  main.py          # reads LLAMA-001 request JSON from stdin, writes response JSON to stdout
  requirements.txt # llama-index-core and whichever reader is demonstrated
  README.md        # how to point depctl's external normalizer config at this script
```

`main.py` should map a LlamaIndex `Document`/`Node` split into the `objects` array from the LLAMA-001 response schema (`logical_path`, `title`, `content_type`, `content`, etc.).

## Inputs / Outputs
- Input: LLAMA-001 request JSON via stdin.
- Output: LLAMA-001 response JSON via stdout.

## Failure behavior
Script exits non-zero with an error message on stderr for any parse failure; `ExternalNormalizer` (LLAMA-001) surfaces this as a normal normalizer error.

## Tests
- Manual/documented smoke test: run `echo '<sample request>' | python main.py` and verify valid response JSON. Not required in CI.

## Acceptance criteria
- [ ] `examples/llamaindex-sidecar/main.py` implements the LLAMA-001 protocol correctly.
- [ ] README documents how to configure `depctl` to use it as an external normalizer.
- [ ] Clearly labeled as an example, not a supported core component.
