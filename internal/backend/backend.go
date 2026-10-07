// Package backend defines ragctl's narrow VectorBackend interface and the
// mandatory point-metadata model every adapter (starting with Qdrant,
// internal/backend/qdrant) implements. A lowest-common-denominator query
// DSL is deliberately avoided — Capabilities just reports what an
// adapter can do, per the design spec.
package backend

import "context"

// VectorBackend stores and queries embedded chunks. Originally six
// methods, matching the design spec exactly — no speculative batch-update
// or aggregate-query methods until a real use case needs them. Count is
// the first (and so far only) addition past that original six, justified
// by POINT-003: doctor needs to know how many points a generation
// actually has, not a vector-similarity guess via Query, to detect an
// ACTIVE generation whose points were silently overwritten by a sibling
// (the exact failure class POINT-001's point-ID fix eliminated going
// forward, but couldn't retroactively repair for a collection populated
// before that fix shipped).
type VectorBackend interface {
	// Name identifies this backend (e.g. "qdrant").
	Name() string
	// Capabilities reports what this backend supports.
	Capabilities(ctx context.Context) (Capabilities, error)
	// EnsureNamespace creates ns if it doesn't already exist. Idempotent.
	EnsureNamespace(ctx context.Context, ns Namespace) error
	// Upsert writes or overwrites req.Points.
	Upsert(ctx context.Context, req UpsertRequest) error
	// Delete removes points by ID or by filter.
	Delete(ctx context.Context, req DeleteRequest) error
	// Query returns the top matches for req.Vector, constrained by
	// req.Filter.
	Query(ctx context.Context, req QueryRequest) (QueryResult, error)
	// Count reports exactly how many points in namespace match filter
	// (nil filter counts the whole namespace) — an exact count, not a
	// TopK-bounded approximation.
	Count(ctx context.Context, namespace string, filter *Filter) (int, error)
	// Health is a cheap read-only reachability check, used by
	// `ragctl doctor`/`status`.
	Health(ctx context.Context) error
}

// Capabilities reports what a VectorBackend implementation supports, so
// callers can adapt rather than assume a lowest common denominator.
type Capabilities struct {
	VectorSearch   bool
	KeywordSearch  bool
	HybridSearch   bool
	MetadataFilter bool
	DeleteByFilter bool
}

// Namespace identifies the collection/index a backend operates against.
// Dimensions and Distance are needed up front to create a vector index
// (e.g. Qdrant's collection config) — they come from the
// embedding.EmbeddingIdentity that produced the vectors being stored.
type Namespace struct {
	Name       string
	Dimensions int
	Distance   string // e.g. "cosine"
}

// PointMetadata is the mandatory metadata attached to every stored
// vector, fixed to v0.1's required filter set — not a generic arbitrary
// key/value bag. Ecosystem/Dependency/Version/Generation are what let a
// query be constrained to an exact dependency version; SourceType/
// Authority carry provenance through to retrieval.
type PointMetadata struct {
	Ecosystem  string
	Dependency string
	Version    string
	Generation string
	SourceType string
	Authority  int
}

// Point is one vector plus its mandatory metadata. ID is the ragctl
// chunk ID, and a point's identity is (Metadata.Generation, ID): chunk
// IDs are content-derived, so two generations (e.g. two versions of a
// dependency) routinely share one, and each must keep its own point. A
// DeleteRequest's IDs remove that chunk from every generation. Vector is
// nil when no embedder was used; Text is the chunk's content, which a
// keyword backend indexes and vector backends ignore.
type Point struct {
	ID       string
	Vector   []float32
	Text     string
	Metadata PointMetadata
}

// UpsertRequest writes Points into Namespace.
type UpsertRequest struct {
	Namespace string
	Points    []Point
}

// Filter constrains a Query or Delete to points whose metadata matches
// every non-empty field.
type Filter struct {
	Ecosystem  string
	Dependency string
	Version    string
	Generation string
}

// DeleteRequest removes points by explicit IDs, by Filter, or both
// (union) — an adapter with Capabilities.DeleteByFilter == false must
// reject a request with a non-nil Filter.
type DeleteRequest struct {
	Namespace string
	IDs       []string
	Filter    *Filter
}

// QueryRequest asks for the TopK best matches within Namespace,
// constrained by Filter (nil means unconstrained): nearest to Vector for
// a vector backend, best keyword matches for Text for a keyword backend.
type QueryRequest struct {
	Namespace string
	Vector    []float32
	Text      string
	TopK      int
	Filter    *Filter
}

// ScoredPoint is one query match.
type ScoredPoint struct {
	ID       string
	Score    float32
	Metadata PointMetadata
}

// QueryResult is the ranked output of a Query call.
type QueryResult struct {
	Points []ScoredPoint
}
