package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/registry"
)

func withNpmServer(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prior := npmRegistryURL
	npmRegistryURL = srv.URL
	t.Cleanup(func() { npmRegistryURL = prior })
}

func withPypiServer(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prior := pypiURL
	pypiURL = srv.URL
	t.Cleanup(func() { pypiURL = prior })
}

func TestDiscoverNodeNormalizesGitPlusHTTPSRepository(t *testing.T) {
	withNpmServer(t, `{"repository":{"url":"git+https://github.com/expressjs/express.git"}}`)

	sources, err := Discover(context.Background(), domain.EcosystemNode, "express")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sources) != 1 || sources[0].URL != "https://github.com/expressjs/express" || sources[0].Type != "git" {
		t.Errorf("sources = %+v, want one git source at https://github.com/expressjs/express", sources)
	}
}

func TestDiscoverNodeNoRepositoryFieldReturnsEmptyNotError(t *testing.T) {
	withNpmServer(t, `{}`)

	sources, err := Discover(context.Background(), domain.EcosystemNode, "some-pkg")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("sources = %+v, want empty", sources)
	}
}

func TestDiscoverPythonProducesGitAndWebsiteCandidates(t *testing.T) {
	withPypiServer(t, `{"info":{"project_urls":{"Source":"https://github.com/psf/requests","Documentation":"https://requests.readthedocs.io"}}}`)

	sources, err := Discover(context.Background(), domain.EcosystemPython, "requests")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want 2", sources)
	}
	byID := map[string]string{}
	for _, s := range sources {
		byID[s.ID] = s.Type
	}
	if byID["source"] != "git" {
		t.Errorf("Source url got type %q, want git", byID["source"])
	}
	if byID["documentation"] != "website" {
		t.Errorf("Documentation url got type %q, want website", byID["documentation"])
	}
}

func TestDiscoverUnsupportedEcosystemErrors(t *testing.T) {
	if _, err := Discover(context.Background(), domain.EcosystemGo, "example.com/foo"); err == nil {
		t.Error("Discover(go) = nil error, want error (unsupported)")
	}
}

func TestNormalizeGitURLSkipsNonHTTPValues(t *testing.T) {
	if got := normalizeGitURL("not-a-url"); got != "" {
		t.Errorf("normalizeGitURL(not-a-url) = %q, want empty", got)
	}
}

// TestDraftManifestRoundTripsThroughParseManifest covers REG-006's own
// acceptance criterion: the draft YAML `depctl registry discover` prints
// must be valid input to the same schema validator a real manifest goes
// through, not just well-formed YAML.
func TestDraftManifestRoundTripsThroughParseManifest(t *testing.T) {
	withNpmServer(t, `{"repository":{"url":"https://github.com/expressjs/express"}}`)
	sources, err := Discover(context.Background(), domain.EcosystemNode, "express")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	draft := registry.Manifest{
		APIVersion: "depctl.dev/v1alpha1",
		Kind:       "KnowledgePackage",
		Metadata:   registry.Metadata{Name: "express"},
		Match:      registry.Match{Ecosystems: []domain.Ecosystem{domain.EcosystemNode}, Packages: []string{"express"}},
		Version:    registry.VersionStrategy{Strategy: "none"},
		Sources:    sources,
	}
	data, err := yaml.Marshal(draft)
	if err != nil {
		t.Fatalf("marshal draft: %v", err)
	}
	if _, err := registry.ParseManifest(data); err != nil {
		t.Errorf("ParseManifest(draft) = %v, want nil (draft must validate against REG-001's schema)", err)
	}
}
