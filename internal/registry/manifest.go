// Package registry answers "where can I obtain trustworthy knowledge for
// this package/version?" — separate from internal/resolver, which answers
// "what package/version does this project use?" The registry is data
// (YAML manifests), not code, so packages can be added without touching Go
// internals. See docs/tickets/planned/07-knowledge-registry.
package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"aleutian-ai/ragctl/internal/domain"
)

//go:embed schema/knowledge-package.schema.json
var schemaJSON []byte

var compiledSchema = mustCompileSchema()

func mustCompileSchema() *jsonschema.Schema {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("knowledge-package.schema.json", bytes.NewReader(schemaJSON)); err != nil {
		panic(fmt.Sprintf("registry: invalid embedded schema: %v", err))
	}
	schema, err := compiler.Compile("knowledge-package.schema.json")
	if err != nil {
		panic(fmt.Sprintf("registry: invalid embedded schema: %v", err))
	}
	return schema
}

// Manifest is a KnowledgePackage: it maps an ecosystem+package identity to
// the knowledge sources ragctl can acquire for it.
type Manifest struct {
	APIVersion string          `yaml:"apiVersion" json:"apiVersion"`
	Kind       string          `yaml:"kind" json:"kind"`
	Metadata   Metadata        `yaml:"metadata" json:"metadata"`
	Match      Match           `yaml:"match" json:"match"`
	Version    VersionStrategy `yaml:"version" json:"version"`
	Sources    []Source        `yaml:"sources" json:"sources"`
}

// Metadata identifies a manifest, independent of what it matches.
type Metadata struct {
	Name string `yaml:"name" json:"name"`
}

// Match is the exact-membership predicate: a package matches this manifest
// iff its ecosystem is in Ecosystems and its name is in Packages.
type Match struct {
	Ecosystems []domain.Ecosystem `yaml:"ecosystems" json:"ecosystems"`
	Packages   []string           `yaml:"packages" json:"packages"`
}

// VersionStrategy describes how a resolved dependency version maps to a
// point in the knowledge source (e.g. a git tag).
type VersionStrategy struct {
	Strategy   string `yaml:"strategy" json:"strategy"` // semver-tag | none | manual
	Repository string `yaml:"repository" json:"repository"`
}

// Source is one place knowledge can be acquired from, ranked by Authority.
type Source struct {
	ID        string `yaml:"id" json:"id"`
	Type      string `yaml:"type" json:"type"` // git | godoc | website | github-releases
	URL       string `yaml:"url,omitempty" json:"url,omitempty"`
	Ref       string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Module    string `yaml:"module,omitempty" json:"module,omitempty"`
	Authority int    `yaml:"authority" json:"authority"`
}

// ManifestError wraps a manifest that failed JSON Schema validation or
// YAML parsing, naming the specific field(s) that failed rather than a
// generic parse error.
type ManifestError struct {
	Cause error
}

func (e *ManifestError) Error() string {
	return fmt.Sprintf("invalid manifest: %v", e.Cause)
}

func (e *ManifestError) Unwrap() error {
	return e.Cause
}

// ParseManifest parses and validates a KnowledgePackage manifest: YAML is
// decoded generically, round-tripped through JSON, and validated against
// the embedded JSON Schema before being decoded into a Manifest.
func ParseManifest(data []byte) (Manifest, error) {
	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return Manifest{}, &ManifestError{Cause: fmt.Errorf("parse YAML: %w", err)}
	}

	jsonBytes, err := json.Marshal(generic)
	if err != nil {
		return Manifest{}, &ManifestError{Cause: fmt.Errorf("convert to JSON for validation: %w", err)}
	}

	var jsonValue any
	if err := json.Unmarshal(jsonBytes, &jsonValue); err != nil {
		return Manifest{}, &ManifestError{Cause: fmt.Errorf("decode JSON for validation: %w", err)}
	}
	if err := compiledSchema.Validate(jsonValue); err != nil {
		return Manifest{}, &ManifestError{Cause: err}
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, &ManifestError{Cause: fmt.Errorf("decode manifest: %w", err)}
	}
	return m, nil
}
