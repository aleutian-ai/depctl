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

// KnowledgeObject is normalized, retrieval-shaped content extracted from
// a SourceSnapshot by a Normalizer. Chunk-level splitting happens later
// (CHUNK-*); a KnowledgeObject is normalization's unit of output.
type KnowledgeObject struct {
	ID          string
	Dependency  DependencyVersion
	SourceID    string
	SourceURI   string
	SourceType  string
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
