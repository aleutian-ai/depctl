// Package discover finds candidate knowledge sources for a package from
// its own ecosystem's structured metadata (npm's repository field,
// PyPI's project_urls/home_page). Results are never applied to a loaded
// registry automatically — `depctl registry discover` only prints them as a
// draft manifest for a human to review.
package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/aleutian-ai/depctl/internal/config"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/httplimit"
	"github.com/aleutian-ai/depctl/internal/registry"
)

// npmRegistryURL/pypiURL are overridden by tests to point at a local
// httptest.Server instead of the real registries.
var (
	npmRegistryURL = "https://registry.npmjs.org"
	pypiURL        = "https://pypi.org/pypi"
)

// Discover queries pkg's ecosystem metadata API for structured source
// URLs. It never guesses — only fields the metadata literally states are
// returned, and a package with none produces an empty, non-error result.
func Discover(ctx context.Context, ecosystem domain.Ecosystem, pkg string) ([]registry.Source, error) {
	switch ecosystem {
	case domain.EcosystemNode:
		return discoverNode(ctx, pkg)
	case domain.EcosystemPython:
		return discoverPython(ctx, pkg)
	default:
		return nil, fmt.Errorf("discover: unsupported ecosystem %q (only node and python are implemented)", ecosystem)
	}
}

func discoverNode(ctx context.Context, pkg string) ([]registry.Source, error) {
	var body struct {
		Repository struct {
			URL string `json:"url"`
		} `json:"repository"`
	}
	if err := fetchJSON(ctx, npmRegistryURL+"/"+pkg, &body); err != nil {
		return nil, err
	}
	url := normalizeGitURL(body.Repository.URL)
	if url == "" {
		return nil, nil
	}
	return []registry.Source{{ID: "repository", Type: "git", URL: url, Authority: 0}}, nil
}

func discoverPython(ctx context.Context, pkg string) ([]registry.Source, error) {
	var body struct {
		Info struct {
			ProjectURLs map[string]string `json:"project_urls"`
			HomePage    string            `json:"home_page"`
		} `json:"info"`
	}
	if err := fetchJSON(ctx, pypiURL+"/"+pkg+"/json", &body); err != nil {
		return nil, err
	}

	var keys []string
	for k := range body.Info.ProjectURLs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sources []registry.Source
	for _, key := range keys {
		url := body.Info.ProjectURLs[key]
		if url == "" {
			continue
		}
		sources = append(sources, registry.Source{ID: strings.ToLower(key), Type: sourceType(url), URL: url, Authority: 0})
	}
	if body.Info.HomePage != "" {
		sources = append(sources, registry.Source{ID: "homepage", Type: sourceType(body.Info.HomePage), URL: body.Info.HomePage, Authority: 0})
	}
	return sources, nil
}

// sourceType classifies a discovered URL as "git" when it's a
// recognizable code-host URL, "website" otherwise (e.g. a docs site).
func sourceType(url string) string {
	if strings.Contains(url, "github.com") || strings.Contains(url, "gitlab.com") {
		return "git"
	}
	return "website"
}

// normalizeGitURL strips npm's "git+"/"git://" prefixes and a trailing
// ".git" so the result matches a plain https git source URL, or returns
// "" for anything that isn't an http(s) URL at all (a malformed or
// non-URL repository field is skipped, not an error).
func normalizeGitURL(raw string) string {
	url := strings.TrimPrefix(raw, "git+")
	url = strings.TrimPrefix(url, "git://")
	url = strings.TrimSuffix(url, ".git")
	if !strings.HasPrefix(url, "http") {
		return ""
	}
	return url
}

// httpClient bounds redirects (SEC-003) — a var so a test could swap it,
// matching internal/cli's own convention for these registry clients.
var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > config.DefaultMaxFetchRedirects {
			return fmt.Errorf("%w: more than %d redirects", httplimit.ErrFetchLimitExceeded, config.DefaultMaxFetchRedirects)
		}
		return nil
	},
}

func fetchJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("discover: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discover: %s returned status %d", url, resp.StatusCode)
	}
	data, err := httplimit.ReadLimited(resp.Body, config.DefaultMaxFetchFileSize)
	if err != nil {
		return fmt.Errorf("discover: read %s: %w", url, err)
	}
	return json.Unmarshal(data, out)
}
