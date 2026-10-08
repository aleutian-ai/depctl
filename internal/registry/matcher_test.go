package registry

import (
	"context"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func loadBuiltinRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewLoader("", "").Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return reg
}

// TestFastapiManifestUsesVerifiedRefTemplatesNotABareVPrefixedRef is the
// regression for a live-found bug in the curated seed manifest itself:
// fastapi/fastapi tags releases as bare "X.Y.Z" — of its entire tag
// history, exactly one release ("v0.1.16") has a "v" prefix, every real
// version since does not (verified live via `git ls-remote --tags`,
// e.g. 0.115.8) — so the old singular `ref: "v${version}"` silently
// failed acquisition for virtually every real version. Both candidates
// are now tried, each verified to actually resolve before being trusted,
// same principle as REG-012/013's fallback manifests applied to a
// hand-curated one.
func TestFastapiManifestUsesVerifiedRefTemplatesNotABareVPrefixedRef(t *testing.T) {
	reg := loadBuiltinRegistry(t)
	m, ok := reg.Match(domain.EcosystemPython, "fastapi")
	if !ok {
		t.Fatal("Match returned false for fastapi")
	}
	if len(m.Sources) == 0 || m.Sources[0].ID != "repository" {
		t.Fatalf("Sources = %+v, want a repository source first", m.Sources)
	}
	src := m.Sources[0]
	if src.Ref != "" {
		t.Errorf("repository source Ref = %q, want empty — RefTemplates must be used instead of a single unverified guess", src.Ref)
	}
	want := []string{"${version}", "v${version}"}
	if len(src.RefTemplates) != len(want) {
		t.Fatalf("RefTemplates = %v, want %v", src.RefTemplates, want)
	}
	for i, w := range want {
		if src.RefTemplates[i] != w {
			t.Errorf("RefTemplates[%d] = %q, want %q (order matters: the real, current convention tried first)", i, src.RefTemplates[i], w)
		}
	}
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
