package registry

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func loadBuiltinRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewLoader("", "").Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return reg
}

func TestMatchExactGoModule(t *testing.T) {
	reg := loadBuiltinRegistry(t)
	m, ok := reg.Match(domain.EcosystemGo, "google.golang.org/grpc")
	if !ok {
		t.Fatal("Match returned false for google.golang.org/grpc")
	}
	if m.Metadata.Name != "grpc-go" {
		t.Errorf("matched manifest = %q, want grpc-go", m.Metadata.Name)
	}
}

func TestMatchUnknownPackageReturnsFalse(t *testing.T) {
	reg := loadBuiltinRegistry(t)
	_, ok := reg.Match(domain.EcosystemGo, "example.com/totally-unknown")
	if ok {
		t.Error("Match = true for an unregistered package, want false")
	}
}

func TestMatchAllSixSeedManifests(t *testing.T) {
	reg := loadBuiltinRegistry(t)
	cases := []struct {
		eco  domain.Ecosystem
		pkg  string
		name string
	}{
		{domain.EcosystemGo, "google.golang.org/grpc", "grpc-go"},
		{domain.EcosystemGo, "google.golang.org/protobuf", "protobuf-go"},
		{domain.EcosystemGo, "github.com/dgraph-io/badger/v4", "badger"},
		{domain.EcosystemGo, "go.etcd.io/bbolt", "bbolt"},
		{domain.EcosystemPython, "pydantic", "pydantic"},
		{domain.EcosystemPython, "fastapi", "fastapi"},
	}
	for _, c := range cases {
		m, ok := reg.Match(c.eco, c.pkg)
		if !ok {
			t.Errorf("Match(%s, %s) = false, want manifest %q", c.eco, c.pkg, c.name)
			continue
		}
		if m.Metadata.Name != c.name {
			t.Errorf("Match(%s, %s) = %q, want %q", c.eco, c.pkg, m.Metadata.Name, c.name)
		}
	}
}

func TestMatchEcosystemMismatch(t *testing.T) {
	reg := loadBuiltinRegistry(t)
	// pydantic is registered for python, not go.
	_, ok := reg.Match(domain.EcosystemGo, "pydantic")
	if ok {
		t.Error("Match = true for pydantic under the go ecosystem, want false")
	}
}
