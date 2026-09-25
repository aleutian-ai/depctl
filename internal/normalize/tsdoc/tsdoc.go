// Package tsdoc implements ragctl's TypeScript declaration normalizer
// (NORM-008): structured, per-symbol API documentation extracted from a
// Node package's own published .d.ts entry point, or — when no .d.ts
// exists at all — JSDoc comments over its plain .js entry point. The
// JS/TS analog of internal/normalize/godoc, syntactic rather than
// semantic, reading only what the package itself ships and never
// executing any of it.
//
// Extraction runs as a small, dependency-free Node script embedded in
// this package (extract.js) — no npm package is installed or required,
// only the `node` binary itself on PATH, so there is nothing to pin or
// vendor beyond the script already embedded here. `node` missing, or no
// resolvable entry point (neither .d.ts nor plain .js), is a clean,
// silent skip (Supports returns false) — a bundled script that fails to
// run once node IS present is a ragctl packaging defect instead,
// surfaced loudly as an error rather than swallowed (see Normalize's own
// doc comment).
//
// A published .d.ts always wins over the JSDoc fallback when both exist
// (see resolveEntry, entrypoint.go) — real declarations are more
// complete and need no comment-quality assumption. Deliberately
// syntactic, not semantic (matching godoc's own scope): no cross-file
// import resolution, no bundler-level entry-point resolution beyond
// package.json's own declared fields (`types`/`typings`/`module`/`main`),
// and no whole-object-literal CommonJS export resolution
// (`module.exports = { foo, bar }`) — see extract.js's own header
// comment for that specific, disclosed limitation.
package tsdoc

import (
	"os/exec"

	"aleutian-ai/ragctl/internal/domain"
)

// Normalizer implements normalize.Normalizer for a Node package
// directory's published TypeScript declarations or plain-JS/JSDoc
// fallback. Like godoc.Normalizer, it operates per-directory (a package
// root), not per-file — invoked directly by generation.normalizeSources
// rather than through normalize.Registry's per-file dispatch.
type Normalizer struct{}

// New returns a ready-to-use TypeScript declaration normalizer.
func New() *Normalizer {
	return &Normalizer{}
}

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "tsdoc-normalizer" }

// Version identifies this normalizer's extraction logic revision.
func (n *Normalizer) Version() string { return "v2" }

// Supports reports whether src points at a Node package directory with a
// resolvable entry point (.d.ts, or a JSDoc-fallback .js), with `node`
// available on PATH. Both conditions are checked here, not split across
// Supports/Normalize, so a caller can treat a false Supports() as the
// complete "nothing to extract here" signal, matching every other
// Normalizer's contract.
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool {
	if _, err := exec.LookPath("node"); err != nil {
		return false
	}
	_, _, ok := resolveEntry(src.LocalPath)
	return ok
}
