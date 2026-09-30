// Package observability provides ragctl's structured logging (OBS-001):
// one small helper building a configured *slog.Logger (standard library
// only — no zap/zerolog), plus a context-carried accessor so lifecycle
// code can log with structured fields without every function in the call
// chain taking a *slog.Logger parameter. Every function already takes
// context.Context; this package makes that the injection point, exactly
// as OBS-001's own ticket allows ("via context or explicit parameter").
//
// Field naming is deliberately not ragctl-invented where a real,
// external standard already exists, so OBS-002 (OpenTelemetry) can later
// bridge these same key names straight into span attributes with no
// rename: gen_ai.* is the OpenTelemetry GenAI semantic convention
// (https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/),
// and embedding.model_name/retrieval.* are OpenInference's
// (https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md,
// the convention Arize Phoenix traces use) — both chosen because ragctl's
// own embed/query operations are exactly what those two conventions
// describe (an embeddings call, a retriever call), and Promptfoo's own
// OTLP ingestion understands the same OTel-rooted attribute shape. Every
// other field here is ragctl's own domain vocabulary (which project,
// which dependency, which generation) that no external convention
// covers — those are namespaced under "ragctl." specifically so they can
// never collide with, or be mistaken for, a real semantic-convention key
// a downstream tool interprets specially.
package observability

import (
	"context"
	"io"
	"log/slog"
)

// Standard field keys, used consistently across every lifecycle stage
// that logs through this package. See the package doc comment for why
// gen_ai.*/embedding.*/retrieval.* are spelled exactly as their external
// conventions define them, while everything else is ragctl.*-namespaced.
const (
	// ragctl's own domain vocabulary — no external convention covers these.
	KeyProjectID  = "ragctl.project_id"
	KeyDependency = "ragctl.dependency"
	KeyVersion    = "ragctl.version"
	KeyJobID      = "ragctl.job_id"
	KeyGeneration = "ragctl.generation"
	KeyBackend    = "ragctl.backend"
	KeyStage      = "ragctl.stage"

	// KeyDurationMS is a plain, unnamespaced field — not part of either
	// external convention, but a common enough structured-logging idiom
	// that namespacing it would just add noise.
	KeyDurationMS = "duration_ms"

	// OpenTelemetry GenAI semantic conventions (gen_ai.*) — used at the
	// MCP tool-call boundary and the embed lifecycle stage, the two
	// places ragctl's own operations are exactly what this convention
	// names.
	KeyGenAIOperationName = "gen_ai.operation.name"
	KeyGenAIToolName      = "gen_ai.tool.name"

	// OpenInference (Arize Phoenix's convention, built on OpenTelemetry)
	// — used at the embed and query/retrieve lifecycle stages, which are
	// exactly OpenInference's own EMBEDDING and RETRIEVER span kinds.
	KeyEmbeddingModelName = "embedding.model_name"
	KeyRetrievalTopK      = "retrieval.top_k"
	KeyRetrievalCount     = "retrieval.documents.count"
)

// NewLogger builds a *slog.Logger writing to w — a JSON handler when
// json is true (for non-TTY/log-aggregation use), a human-readable text
// handler otherwise — at the given minimum level.
func NewLogger(w io.Writer, json bool, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if json {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

// contextKey is unexported so no other package can collide with it by
// using the same key value in a context.WithValue call of their own.
type contextKey struct{}

// WithLogger returns a context carrying logger, retrievable via
// FromContext by any function further down the same call chain —
// ragctl's chosen alternative to threading a *slog.Logger parameter
// through every lifecycle function, per OBS-001's own ticket.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext returns the logger WithLogger stored on ctx, or
// slog.Default() if none was ever set — logging must never panic or
// block correctness (OBS-001's own failure-behavior requirement), so a
// caller that forgot to wire a logger still gets a safe, real one
// rather than a nil-pointer panic.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}
