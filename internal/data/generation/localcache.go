package generation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/module"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/executil"
)

// localCacheProbeTimeout bounds the cheap, purely local probes below
// (go env, python3 -c, a handful of stat/readfile calls) — none of
// these ever touch the network, so this is generous only relative to a
// slow disk, not a slow remote call.
const localCacheProbeTimeout = 5 * time.Second

// localCacheSeed reports whether ecosystem's own package-manager cache
// already has depName@version's current file tree extracted and ready
// to read, returning its directory if so (GIT-004). This only ever
// fast-paths acquiring one version's *current* file tree — anything
// needing git history (release notes, cross-version diffing) still
// goes through the real mirror clone regardless of a hit here, and a
// hit here is never treated as authoritative for anything beyond that.
//
// projectRoot is the specific project whose sync action triggered this
// build (planner.Action.ProjectID, resolved to its root by the caller)
// — used only for Node's node_modules check, which is inherently
// project-scoped, unlike Go's GOMODCACHE or Python's site-packages
// (both machine-global, no project context needed). Empty projectRoot
// just means the Node check can never hit, same as any other miss.
func localCacheSeed(ctx context.Context, ecosystem domain.Ecosystem, depName, version, projectRoot string) (string, bool) {
	switch ecosystem {
	case domain.EcosystemGo:
		return goModCacheSeed(ctx, depName, version)
	case domain.EcosystemNode:
		return nodeModulesSeed(depName, version, projectRoot)
	case domain.EcosystemPython:
		return sitePackagesSeed(ctx, depName, version)
	default:
		return "", false
	}
}

// goModCacheSeed looks for modulePath@version already extracted under
// `go env GOMODCACHE`. Go's own module cache escapes uppercase letters
// in a module path (module.EscapePath, the same encoding `go` itself
// uses to write the directory in the first place) — reusing it here
// rather than reimplementing the same rule is what keeps this correct.
// Unlike Node/Python below, no separate version check is needed: the
// directory name IS the version, and Go's module cache is immutable
// once extracted, so existence alone is sufficient.
func goModCacheSeed(ctx context.Context, modulePath, version string) (string, bool) {
	res, err := executil.Run(ctx, executil.RunOptions{
		Args: []string{"go", "env", "GOMODCACHE"}, Timeout: localCacheProbeTimeout,
	})
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	cacheDir := strings.TrimSpace(string(res.Stdout))
	if cacheDir == "" {
		return "", false
	}

	escaped, err := module.EscapePath(modulePath)
	if err != nil {
		return "", false
	}
	dir := filepath.Join(cacheDir, escaped+"@"+version)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// nodeModulesSeed checks projectRoot's own node_modules/<depName> —
// the specific project whose sync action triggered this build, not a
// global cache (npm/pnpm/Yarn/Bun's own global stores hold compressed
// tarballs or a binary manifest-cache format, not extracted, directly
// readable source trees — confirmed against real installs, not
// assumed; using them would need real archive-extraction code, a much
// bigger scope than this ticket's fast path). depName may itself
// contain a "/" for a scoped package (e.g. "@types/node") — that's
// already the correct nested path under node_modules, no special
// casing needed.
func nodeModulesSeed(depName, version, projectRoot string) (string, bool) {
	if projectRoot == "" {
		return "", false
	}
	dir := filepath.Join(projectRoot, "node_modules", filepath.FromSlash(depName))
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", false
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Version != version {
		return "", false
	}
	return dir, true
}

// sitePackagesSeed looks for depName@version already installed under
// the active `python3`'s site-packages, matched via its dist-info's
// exact recorded name+version (never a normalized-name guess against
// the distribution name — PyPI distribution names routinely differ
// from their real import directory, e.g. "PyYAML" imports as "yaml";
// guessing wrong would silently seed from the wrong directory, so a
// dist-info without a usable top_level.txt is treated as a miss, not
// a best-effort guess).
func sitePackagesSeed(ctx context.Context, depName, version string) (string, bool) {
	siteDir, ok := pythonSitePackagesDir(ctx)
	if !ok {
		return "", false
	}
	return findDistInfoSeed(siteDir, depName, version)
}

// pythonSitePackagesDir asks the active `python3` for its own purelib
// directory — split out from findDistInfoSeed below so the matching
// logic (name/version comparison, top_level.txt resolution) is directly
// testable against a fixture directory, without needing a real Python
// install in the test environment.
func pythonSitePackagesDir(ctx context.Context) (string, bool) {
	res, err := executil.Run(ctx, executil.RunOptions{
		Args:    []string{"python3", "-c", "import sysconfig; print(sysconfig.get_paths()['purelib'])"},
		Timeout: localCacheProbeTimeout,
	})
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	dir := strings.TrimSpace(string(res.Stdout))
	return dir, dir != ""
}

// findDistInfoSeed searches siteDir for a "<name>-<version>.dist-info"
// directory matching depName/version, then resolves the real import
// directory via that dist-info's own top_level.txt.
func findDistInfoSeed(siteDir, depName, version string) (string, bool) {
	entries, err := os.ReadDir(siteDir)
	if err != nil {
		return "", false
	}

	want := normalizePyDistName(depName)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".dist-info") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".dist-info")
		idx := strings.LastIndex(base, "-")
		if idx < 0 {
			continue
		}
		name, ver := base[:idx], base[idx+1:]
		if normalizePyDistName(name) != want || ver != version {
			continue
		}

		topLevel, err := os.ReadFile(filepath.Join(siteDir, e.Name(), "top_level.txt"))
		if err != nil {
			return "", false // a real match, but no reliable import-dir name — don't guess
		}
		for _, line := range strings.Split(strings.TrimSpace(string(topLevel)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			dir := filepath.Join(siteDir, filepath.FromSlash(line))
			if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
				return dir, true
			}
		}
		return "", false
	}
	return "", false
}

// normalizePyDistName applies PEP 503's normalization (lowercase, runs
// of -ـ/./_ collapsed to a single "-") so a dist-info directory's own
// recorded name compares correctly against the resolved dependency name
// regardless of case/separator differences between the two.
func normalizePyDistName(name string) string {
	var b strings.Builder
	lastWasSep := false
	for _, r := range strings.ToLower(name) {
		if r == '-' || r == '_' || r == '.' {
			if !lastWasSep && b.Len() > 0 {
				b.WriteByte('-')
			}
			lastWasSep = true
			continue
		}
		b.WriteRune(r)
		lastWasSep = false
	}
	return strings.TrimSuffix(b.String(), "-")
}
