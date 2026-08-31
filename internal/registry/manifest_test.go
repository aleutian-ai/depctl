package registry

import (
	"errors"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

const validManifest = `
apiVersion: ragctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: grpc-go
match:
  ecosystems: [go]
  packages: [google.golang.org/grpc]
version:
  strategy: semver-tag
  repository: grpc/grpc-go
sources:
  - id: repository
    type: git
    url: https://github.com/grpc/grpc-go
    ref: "v${version}"
    authority: 100
  - id: package-docs
    type: godoc
    module: google.golang.org/grpc
    authority: 95
`

func TestParseManifestValid(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Metadata.Name != "grpc-go" {
		t.Errorf("Name = %q, want grpc-go", m.Metadata.Name)
	}
	if len(m.Match.Ecosystems) != 1 || m.Match.Ecosystems[0] != domain.EcosystemGo {
		t.Errorf("Ecosystems = %v, want [go]", m.Match.Ecosystems)
	}
	if len(m.Sources) != 2 || m.Sources[0].Authority != 100 {
		t.Errorf("Sources = %+v, want 2 sources, first authority 100", m.Sources)
	}
}

func TestParseManifestMissingPackages(t *testing.T) {
	bad := `
apiVersion: ragctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: grpc-go
match:
  ecosystems: [go]
version:
  strategy: semver-tag
sources:
  - id: repository
    type: git
    authority: 100
`
	_, err := ParseManifest([]byte(bad))
	if err == nil {
		t.Fatal("expected error for missing match.packages, got nil")
	}
	var merr *ManifestError
	if !errors.As(err, &merr) {
		t.Errorf("error is not a *ManifestError: %v", err)
	}
}

func TestParseManifestBadTypeEnum(t *testing.T) {
	bad := `
apiVersion: ragctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: grpc-go
match:
  ecosystems: [go]
  packages: [google.golang.org/grpc]
version:
  strategy: semver-tag
sources:
  - id: repository
    type: ftp
    authority: 100
`
	_, err := ParseManifest([]byte(bad))
	if err == nil {
		t.Fatal("expected error for invalid source type, got nil")
	}
}

func TestParseManifestAuthorityOutOfRange(t *testing.T) {
	bad := `
apiVersion: ragctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: grpc-go
match:
  ecosystems: [go]
  packages: [google.golang.org/grpc]
version:
  strategy: semver-tag
sources:
  - id: repository
    type: git
    authority: 150
`
	_, err := ParseManifest([]byte(bad))
	if err == nil {
		t.Fatal("expected error for authority out of range, got nil")
	}
}

func TestParseManifestMalformedYAML(t *testing.T) {
	_, err := ParseManifest([]byte("not: valid: yaml: at all: [["))
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}
