// Package domain defines ragctl's core data types, shared by every other
// package. It has no dependencies on storage, network, or CLI packages —
// see docs/tickets/planned/02-core-domain-storage/CORE-001-domain-types.md.
//
// Types are added here incrementally, as the commands that need them are
// implemented — not all at once up front.
package domain

import (
	"fmt"
	"time"
)

// Ecosystem identifies a package-manager ecosystem. Values are canonical,
// lowercase, and never free-form.
type Ecosystem string

const (
	EcosystemGo     Ecosystem = "go"
	EcosystemPython Ecosystem = "python"
	EcosystemNode   Ecosystem = "node"
	EcosystemRust   Ecosystem = "rust"
	EcosystemJava   Ecosystem = "java"
)

// Project is a registered local project root.
type Project struct {
	ID        string
	Root      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (p Project) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("project: ID must not be empty")
	}
	if p.Root == "" {
		return fmt.Errorf("project: Root must not be empty")
	}
	return nil
}

// Dependency identifies a package within an ecosystem, independent of the
// version resolved for it in any particular project.
type Dependency struct {
	Ecosystem Ecosystem
	Name      string
	Direct    bool
}

// DependencyVersion is a Dependency pinned to the version a resolver
// actually resolved it to.
type DependencyVersion struct {
	Dependency Dependency
	Version    string
	ResolvedBy string
	Checksum   string
}

// SourceSnapshot is materialized source content — typically a file or
// package directory checked out via a GIT-002 worktree — ready for
// normalization, plus enough metadata to attribute output back to its
// origin. LogicalPath is source-relative (e.g. "docs/README.md") and is
// what Normalizer.Supports matches against; LocalPath is where the
// content actually sits on disk.
type SourceSnapshot struct {
	ID          string // stable snapshot identifier, e.g. SourceID+Commit
	SourceID    string
	URI         string // origin URI, e.g. the repository URL
	LocalPath   string
	LogicalPath string
	Version     string
	Commit      string
	FetchedAt   time.Time
	Metadata    map[string]string
}

// TrustClass classifies how much a downstream consumer (an agent reading
// MCP search results) should trust a KnowledgeObject's content, distinct
// from Authority's numeric ranking among sources for the same package.
type TrustClass string

const (
	TrustOfficial   TrustClass = "official"   // registry-declared official docs/release source
	TrustRepository TrustClass = "repository" // the package's own source repository
	TrustCommunity  TrustClass = "community"
	TrustUser       TrustClass = "user"
	TrustUnknown    TrustClass = "unknown" // no registry match determined this object's provenance
)

// KnowledgeObject is normalized, retrieval-shaped content extracted from
// a SourceSnapshot by a Normalizer. Chunk-level splitting happens later
// (CHUNK-*); a KnowledgeObject is normalization's unit of output.
type KnowledgeObject struct {
	ID          string
	Dependency  DependencyVersion
	SourceID    string
	SourceURI   string
	SourceType  string
	TrustClass  TrustClass
	ContentType string
	LogicalPath string
	Title       string
	Heading     []string // heading breadcrumb path, e.g. ["Getting Started", "Installation"]
	Language    string
	Version     string
	Commit      string
	Authority   int
	Content     []byte
	ContentHash string
	Metadata    map[string]string
}

// Validate reports whether obj carries the minimum provenance metadata
// every downstream consumer relies on — an empty SourceURI or TrustClass
// means content could be surfaced to an agent with no way to reason
// about where it came from or how much to trust it.
func (obj KnowledgeObject) Validate() error {
	if obj.SourceURI == "" {
		return fmt.Errorf("knowledge object %s: SourceURI is empty", obj.ID)
	}
	if obj.TrustClass == "" {
		return fmt.Errorf("knowledge object %s: TrustClass is empty", obj.ID)
	}
	return nil
}

// Chunk is a retrieval-sized slice of a KnowledgeObject's content,
// produced by a Chunker (see internal/data/chunk). ID is derived
// deterministically from ObjectID+Ordinal+Content (CHUNK-001's ChunkID),
// so a chunk whose boundaries shift due to an upstream edit gets a new
// ID rather than silently reusing stale content under an old one.
type Chunk struct {
	ID          string
	ObjectID    string
	Ordinal     int
	Content     []byte
	ContentHash string
	Metadata    map[string]string
}

// Generation is one dependency version's knowledge snapshot as it moves
// through acquisition, normalization, and indexing (see
// docs/tickets/planned/11-generation-builder). ID is a ULID ("gen_"
// prefixed) — generations are lifecycle entities, not content-addressed,
// so unlike KnowledgeObject/Chunk their ID carries no derived identity.
type Generation struct {
	ID         string
	Dependency DependencyVersion
	State      GenerationState
	Error      string // set when State == GenFailed
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// BackendReplica tracks one (generation, backend, embedding model)
// tuple's replication progress into a vector backend — the bookkeeping
// validation/promotion (VAL-*) needs to know a candidate generation's
// vector data is actually present and correct before promoting it.
type BackendReplica struct {
	ID             string // ULID
	GenerationID   string
	BackendName    string
	EmbeddingModel string
	Dimensions     int
	Status         string // "pending", "replicating", "complete", "failed"
	PointCount     int
	LastError      string
	UpdatedAt      time.Time
}

// Job is a restartable background unit of work — currently only GC
// (RET-004) uses this, via Type "gc": re-invoking `ragctl gc` re-claims
// any PENDING/RETRY gc job by its deterministic ID and resumes it,
// rather than starting over or duplicating work.
type Job struct {
	ID         string // deterministic, derived from (Type, Dependency) — see internal/lifecycle/gc.JobID
	Type       string // "gc"
	Dependency DependencyVersion
	State      JobState
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ReferenceReason identifies why a VersionReference exists — the
// eligibility rule retention (RET-003) applies depends on which reasons
// are present for a version, not just whether any reference exists.
type ReferenceReason string

const (
	// ReferenceReasonProject means a registered project's resolved
	// dependencies currently include this exact version.
	ReferenceReasonProject ReferenceReason = "project"
	// ReferenceReasonLatest pins a version as the ecosystem/package's
	// current "latest," independent of any project referencing it.
	ReferenceReasonLatest ReferenceReason = "latest"
	// ReferenceReasonManualPin marks a version as user-pinned, never
	// GC-eligible regardless of grace expiry.
	ReferenceReasonManualPin ReferenceReason = "manual_pin"
	// ReferenceReasonGracePeriod is added automatically (RET-002) when a
	// version's last project reference drops, keeping it retained until
	// LastSeenAt + the configured grace period elapses.
	ReferenceReasonGracePeriod ReferenceReason = "grace_period"
)

// gracePeriodProjectID is the synthetic ProjectID a grace_period
// reference uses, since it isn't scoped to any one project.
const GracePeriodProjectID = "_grace"

// VersionReference records one reason Ecosystem/Package at Version is
// currently retained — a project depending on it, a "latest"/pin policy,
// or an active grace period. Multiple reasons (and multiple projects)
// can reference the same version at once; retention (RET-001..003) uses
// the full set to decide GC eligibility.
type VersionReference struct {
	ProjectID   string
	Ecosystem   Ecosystem
	Package     string
	Version     string
	Reason      ReferenceReason
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// Resolution is the canonical output of a Resolver.Resolve call (see
// internal/resolver) and the shape persisted by the control store. Defined
// here, not in internal/resolver, because storage/planner/every ecosystem
// resolver package needs it and none of them should import each other.
type Resolution struct {
	Ecosystem    Ecosystem
	ManifestPath string
	LockPath     string
	Dependencies []DependencyVersion
	Fingerprint  string // deterministic hash of the resolution inputs, for no-op detection
	Warnings     []string
}
