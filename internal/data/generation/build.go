package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/data/badger"
	dchunk "aleutian-ai/ragctl/internal/data/chunk"
	chunkmd "aleutian-ai/ragctl/internal/data/chunk/markdown"
	"aleutian-ai/ragctl/internal/data/chunk/symbol"
	"aleutian-ai/ragctl/internal/data/fingerprint"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/normalize"
	"aleutian-ai/ragctl/internal/normalize/godoc"
	"aleutian-ai/ragctl/internal/normalize/markdown"
	"aleutian-ai/ragctl/internal/normalize/plaintext"
	"aleutian-ai/ragctl/internal/normalize/pydoc"
	"aleutian-ai/ragctl/internal/normalize/releasenotes"
	"aleutian-ai/ragctl/internal/normalize/tsdoc"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/source/git"
)

// normalizerNameKey/normalizerVersionKey stash which normalizer (and
// version) produced a KnowledgeObject in its own Metadata, so
// resolveObjectIdentity can feed them into the content fingerprint
// (HASH-001) without normalizeSources having to thread them through a
// separate parameter.
const (
	normalizerNameKey    = "_normalizer_name"
	normalizerVersionKey = "_normalizer_version"
)

// skipDirNames are directories never worth walking into for
// documentation content — same convention hack/chunk-sweep uses.
var skipDirNames = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "dist": true,
	"build": true, ".venv": true, "venv": true, "__pycache__": true, "target": true,
}

// acquiredSource is one KnowledgeSource's materialized worktree, kept
// alive (via cleanup) until normalization has read everything it needs
// from it.
type acquiredSource struct {
	source      registry.Source
	worktreeDir string
	commit      string
	cleanup     func() error
}

// Build drives gen through source acquisition, normalization, and
// chunking, staging the result in Badger and updating gen's lifecycle
// state in bbolt at each phase. Only registry.Source entries of type
// "git" are acquired — a "godoc" source is informational (it just means
// "extract Go doc from whatever .go files show up in the acquired
// worktree"), and other source types are out of scope until their own
// acquisition milestone (e.g. HTTP-001) exists.
//
// This is deliberately one linear function, not a pluggable pipeline
// framework — see GEN-002's simplicity constraints. Any stage failure
// aborts the whole build and marks gen FAILED; a normalizer error on one
// file fails the entire generation rather than silently dropping that
// file, so a generation's content is never ambiguously partial.
func Build(ctx context.Context, gen domain.Generation, sources []registry.Source, gitCache *git.Cache, store *bbolt.Store, badgerStore *badger.Store) error {
	if err := setState(ctx, store, &gen, domain.GenAcquiring); err != nil {
		return err
	}

	acquired, err := acquireGitSources(ctx, gitCache, gen.Dependency.Dependency.Name, gen.Dependency.Version, gen.Dependency.Dependency.Ecosystem, sources)
	defer cleanupAll(acquired)
	if err != nil {
		fail(ctx, store, &gen, err.Error())
		return err
	}

	if err := setState(ctx, store, &gen, domain.GenNormalizing); err != nil {
		return err
	}
	objects, err := normalizeSources(ctx, gen.Dependency, acquired)
	if err != nil {
		fail(ctx, store, &gen, err.Error())
		return err
	}

	if err := setState(ctx, store, &gen, domain.GenIndexing); err != nil {
		return err
	}
	if err := indexObjects(ctx, gen, objects, badgerStore); err != nil {
		fail(ctx, store, &gen, err.Error())
		return err
	}

	return nil
}

// acquireGitSources materializes a worktree for every "git" source,
// pinned to the ref its Ref template resolves to for version. Partial
// progress is left to the caller's deferred cleanupAll — a failure here
// doesn't need to unwind what's already been acquired since Build cleans
// up everything unconditionally.
//
// Worktrees are materialized sparse (GIT-005), scoped to
// normalize.SparsePatterns(ecosystem) — the doc-shaped file patterns
// the normalizers in this package's own registry actually read — paired
// with EnsureMirror's blobless clone so a dependency's non-doc content
// (source trees in other languages, binary assets, vendored
// dependencies) is never fetched at all, not just skipped during
// normalization.
func acquireGitSources(ctx context.Context, gitCache *git.Cache, depName, version string, ecosystem domain.Ecosystem, sources []registry.Source) ([]acquiredSource, error) {
	basePatterns := normalize.SparsePatterns(ecosystem)
	var acquired []acquiredSource
	for _, s := range sources {
		if s.Type != "git" {
			continue
		}

		refs, err := refCandidates(s, version)
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: %v", ErrAcquisition, s.ID, err)
		}

		repoPath, err := gitCache.EnsureMirror(ctx, s.URL)
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: mirror: %v", ErrAcquisition, s.ID, err)
		}

		commit, err := resolveFirstRef(ctx, gitCache, repoPath, refs)
		if err != nil {
			// A cached mirror never learns of tags created after it was
			// cloned, so a version released since then looks missing:
			// refresh tags once and try again before giving up.
			if fetchErr := gitCache.FetchTags(ctx, repoPath); fetchErr == nil {
				commit, err = resolveFirstRef(ctx, gitCache, repoPath, refs)
			}
		}
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: resolve %s: %v", ErrAcquisition, s.ID, strings.Join(refs, " or "), err)
		}

		subdir := s.Subdir
		if subdir == "" && ecosystem == domain.EcosystemNode {
			// npm's registry "directory" field (the only source Subdir is
			// ever derived from for Node) is sometimes simply absent even
			// for a package that genuinely lives in a subdirectory of a
			// real monorepo (confirmed live: @opentelemetry/api in
			// open-telemetry/opentelemetry-js). Unlike Go, whose import
			// path IS the directory, npm gives no structural way to know
			// this from the package name alone — so find it the same way
			// a person would: search the tree for the package.json that
			// actually claims this name.
			found, verified, ambiguous, findErr := discoverNodeSubdir(ctx, gitCache, repoPath, commit, depName)
			if findErr == nil && verified {
				subdir = found
			} else if findErr == nil && ambiguous {
				// A genuine multi-package repo where this dependency's own
				// package.json couldn't be found anywhere at this commit —
				// live-found upstream metadata drift, not a scoping gap.
				// Indexing repo-root content under this dependency's name
				// would be confidently wrong, not just unscoped — fail
				// cleanly instead, same principle as a missing git tag.
				return acquired, fmt.Errorf("%w: source %s: could not locate %s's own package.json within a multi-package repository at commit %s", ErrAcquisition, s.ID, depName, commit)
			}
		}

		patterns := basePatterns
		if ecosystem == domain.EcosystemNode {
			// NORM-008's JSDoc fallback: fold the plain-.js entry point
			// into this same single sparse-checkout call — see
			// nodeJSEntryPatterns' own doc comment for why this avoids a
			// second materialize.
			patterns = append(append([]string{}, basePatterns...), nodeJSEntryPatterns(ctx, gitCache, repoPath, commit, subdir)...)
		}
		worktreeDir, cleanup, err := gitCache.MaterializeWorktree(ctx, repoPath, commit, normalize.ScopeToSubdir(patterns, subdir))
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: worktree: %v", ErrAcquisition, s.ID, err)
		}
		if subdir != "" {
			// A module inside a monorepo owns only its own directory;
			// walking from the repo root would index every sibling
			// module's content under this dependency.
			scoped := filepath.Join(worktreeDir, filepath.FromSlash(subdir))
			if info, statErr := os.Stat(scoped); statErr != nil || !info.IsDir() {
				cleanup()
				return acquired, fmt.Errorf("%w: source %s: subdir %q not found at commit %s", ErrAcquisition, s.ID, subdir, commit)
			}
			worktreeDir = scoped
		}

		acquired = append(acquired, acquiredSource{source: s, worktreeDir: worktreeDir, commit: commit, cleanup: cleanup})
	}
	return acquired, nil
}

// discoverNodeSubdir searches repoPath's tree at commit for the
// package.json that actually declares depName, structurally rather than
// trusting npm registry metadata (which sometimes omits "directory" even
// for a genuine monorepo package — see acquireGitSources).
//
// verified is true only when a package.json exactly matching depName was
// found — dir is "" for a root-level match, the subdirectory otherwise.
// verified is false when no match was found; ambiguous then distinguishes
// two very different situations: a single-package repo where nothing
// else exists to accidentally blend in (ambiguous false — safe to fall
// back to the root) versus a genuine multi-package repo where this
// specific dependency's own location simply couldn't be confirmed
// (ambiguous true) — live-found: @babel/plugin-syntax-object-rest-spread's
// registry-reported repository URL names a subdirectory that doesn't
// exist at that version's actual tag (upstream metadata drift, not a
// ragctl bug), and babel/babel's own root package.json is a different,
// unrelated package ("babel", the private monorepo-tooling root) —
// falling back to indexing root-level content under the dependency's
// name in that case would be exactly the wrong-content-under-a-
// confident-label failure class BOUND-001/REG-012 already exist to
// prevent, just via a different root cause (broken upstream metadata
// rather than merely incomplete metadata).
func discoverNodeSubdir(ctx context.Context, gitCache *git.Cache, repoPath, commit, depName string) (dir string, verified, ambiguous bool, err error) {
	paths, err := gitCache.ListFiles(ctx, repoPath, commit, "package.json")
	if err != nil {
		return "", false, false, err
	}
	for _, p := range paths {
		content, readErr := gitCache.ReadFile(ctx, repoPath, commit, p)
		if readErr != nil {
			continue
		}
		name, ok := packageJSONName(content)
		if !ok || name != depName {
			continue
		}
		d := strings.TrimSuffix(p, "/package.json")
		if d == "package.json" {
			return "", true, false, nil
		}
		return d, true, false, nil
	}
	// Not found anywhere. Any package.json existing at all (even just
	// one, at the root) and NOT naming this dependency is a real red
	// flag, not just an unusual layout — this is precisely the shape
	// REG-012's own eslint-visitor-keys finding was: a bare tag resolved
	// to a real, single-package commit that predated a monorepo
	// restructure, whose one root package.json had an entirely
	// different, unrelated name. len(paths) > 1 alone would have missed
	// that exact case (only one package.json existed at that commit).
	// Only a commit with literally zero package.json files anywhere
	// carries no name signal to contradict at all — falling back there
	// is irreducible uncertainty, not a known mismatch.
	return "", false, len(paths) > 0, nil
}

// packageJSONName extracts the "name" field from raw package.json content.
func packageJSONName(content []byte) (string, bool) {
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(content, &doc); err != nil || doc.Name == "" {
		return "", false
	}
	return doc.Name, true
}

// nodeJSEntryPatterns returns extra sparse-checkout patterns (relative to
// subdir, git-path style forward slashes) covering the plain-.js entry
// point tsdoc's JSDoc fallback (NORM-008's deferred scope) reads once a
// worktree exists — reusing this package's own no-checkout
// gitCache.ReadFile plumbing (already built for discoverNodeSubdir above)
// to read package.json's "module"/"main" fields *before* the one
// MaterializeWorktree call below, so the entry file is actually present
// in the sparse checkout rather than requiring a second, costlier
// materialize (the "two-phase acquisition" NORM-008's own ticket
// originally assumed was necessary — avoided here since the plumbing to
// read package.json pre-worktree already existed for a different
// purpose). Errors reading/parsing package.json are not fatal here —
// package.json is already in basePatterns and gets its own real
// acquisition error later if genuinely missing; this only ever narrows
// or defaults the extra pattern, never blocks acquisition. A
// non-matching sparse pattern is harmless (git simply fetches nothing
// for it), so no existence check is needed before returning —
// tsdoc.entryJSFile's own on-disk fileExists checks are what actually
// decide which candidate, if any, is real.
func nodeJSEntryPatterns(ctx context.Context, gitCache *git.Cache, repoPath, commit, subdir string) []string {
	pkgPath := "package.json"
	if subdir != "" {
		pkgPath = subdir + "/package.json"
	}
	content, err := gitCache.ReadFile(ctx, repoPath, commit, pkgPath)
	if err != nil {
		return []string{"index.js"}
	}
	var pkg struct {
		Main   string `json:"main"`
		Module string `json:"module"`
	}
	if err := json.Unmarshal(content, &pkg); err != nil {
		return []string{"index.js"}
	}

	var patterns []string
	for _, c := range []string{pkg.Module, pkg.Main} {
		if c == "" {
			continue
		}
		c = strings.TrimPrefix(path.Clean(c), "./")
		patterns = append(patterns, c)
		if path.Ext(c) == "" {
			// package.json's "main" is commonly extensionless — Node
			// itself resolves that to "<main>.js"; mirror that one
			// bounded case (matches tsdoc.entryJSFile's own local-disk
			// version of this same fallback).
			patterns = append(patterns, c+".js")
		}
	}
	return append(patterns, "index.js")
}

// gitRef substitutes "${version}" in source.Ref with version, stripped of
// any leading "v" — so both Go module versions ("v1.2.3") and
// non-prefixed versions (e.g. PyPI's "1.2.3") produce the same tag shape
// a manifest's "v${version}" template expects.
func gitRef(source registry.Source, version string) (string, error) {
	if source.Ref == "" {
		return "", fmt.Errorf("source %s has no ref template", source.ID)
	}
	trimmed := strings.TrimPrefix(version, "v")
	return strings.ReplaceAll(source.Ref, "${version}", trimmed), nil
}

// goPseudoVersion matches the tail of a Go pseudo-version
// (vX.Y.Z-yyyymmddhhmmss-abcdef123456), which names a commit, not a tag.
var goPseudoVersion = regexp.MustCompile(`\d{14}-([0-9a-f]{12})$`)

// refCandidates lists the refs to try, in order, for version. A Go
// pseudo-version names a commit (its last 12 hex digits), so that is the
// ref; anything else uses the source's tag template. "+incompatible" is a
// module-path marker, not part of the tag. There is deliberately no
// fallback to a branch head: a version that resolves to different content
// than its label says is worse than one that fails to acquire.
func refCandidates(source registry.Source, version string) ([]string, error) {
	version = strings.TrimSuffix(version, "+incompatible")
	if len(source.RefTemplates) > 0 {
		trimmed := strings.TrimPrefix(version, "v")
		candidates := make([]string, len(source.RefTemplates))
		for i, tmpl := range source.RefTemplates {
			candidates[i] = strings.ReplaceAll(tmpl, "${version}", trimmed)
		}
		return candidates, nil
	}
	if m := goPseudoVersion.FindStringSubmatch(version); m != nil {
		return []string{m[1]}, nil
	}
	ref, err := gitRef(source, version)
	if err != nil {
		return nil, err
	}
	return []string{ref}, nil
}

// resolveFirstRef returns the commit of the first ref that resolves in
// the mirror.
func resolveFirstRef(ctx context.Context, gitCache *git.Cache, repoPath string, refs []string) (string, error) {
	var lastErr error
	for _, ref := range refs {
		commit, err := gitCache.ResolveRef(ctx, repoPath, ref)
		if err == nil {
			return commit, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func cleanupAll(acquired []acquiredSource) {
	for _, a := range acquired {
		if err := a.cleanup(); err != nil {
			fmt.Printf("generation: worktree cleanup warning for %s: %v\n", a.source.ID, err)
		}
	}
}

// normalizeSources walks every acquired worktree and normalizes its
// content into KnowledgeObjects, filling in the dependency/source
// attribution fields normalizers themselves don't set.
func normalizeSources(ctx context.Context, dep domain.DependencyVersion, acquired []acquiredSource) ([]domain.KnowledgeObject, error) {
	normReg := normalize.NewRegistry(releasenotes.New(), markdown.New(), plaintext.New())
	gd := godoc.New()
	tsd := tsdoc.New()
	pyd := pydoc.New()

	var objects []domain.KnowledgeObject
	for _, a := range acquired {
		// Unlike godoc, a Node package's published entry point is
		// conventionally singular at the package root (package.json's own
		// "types"/"main" field) regardless of how deep the real
		// implementation is nested — no per-directory walk needed, one
		// call at the already-correctly-scoped worktree root (Subdir
		// scoping, or the whole repo for a single-package one) is enough.
		if dep.Dependency.Ecosystem == domain.EcosystemNode && tsd.Supports(domain.SourceSnapshot{LocalPath: a.worktreeDir}) {
			objs, err := normalizeOne(ctx, tsd, a, a.worktreeDir)
			if err != nil {
				return nil, fmt.Errorf("%w: source %s: %v", ErrNormalization, a.source.ID, err)
			}
			objects = appendAttributed(objects, dep, a.source, objs)
		}
		// Same reasoning as tsdoc above: a Python package's entry module
		// (__init__.py, conventionally) sits at the package root; deeper
		// submodules are only ever reached through pydoc's own bounded
		// single-hop re-export resolution, not a directory walk here.
		if dep.Dependency.Ecosystem == domain.EcosystemPython && pyd.Supports(domain.SourceSnapshot{LocalPath: a.worktreeDir}) {
			objs, err := normalizeOne(ctx, pyd, a, a.worktreeDir)
			if err != nil {
				return nil, fmt.Errorf("%w: source %s: %v", ErrNormalization, a.source.ID, err)
			}
			objects = appendAttributed(objects, dep, a.source, objs)
		}

		err := filepath.WalkDir(a.worktreeDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirNames[d.Name()] {
					return filepath.SkipDir
				}
				// A subdirectory with its own go.mod is a separate Go
				// module's root, not content belonging to dep — a real,
				// large repo (e.g. google-cloud-go, whose root module
				// resolves to a single file, doc.go, alongside 200+
				// independently-versioned sibling modules in the same
				// git repo) would otherwise have every sibling module's
				// content normalized and attributed to dep, both wildly
				// inflating chunk volume and mislabeling unrelated
				// packages' docs as dep's own.
				if path != a.worktreeDir && hasGoMod(path) {
					return filepath.SkipDir
				}
				// Mirrors the go.mod boundary above for npm monorepos:
				// discoverNodeSubdir (in acquireGitSources) already tries
				// to scope the worktree to the right subdirectory, but
				// this is the safety net for when it couldn't (registry
				// metadata missing and no matching package.json found, or
				// a nested package the discovery pass didn't reach) — a
				// directory that names a *different* package than dep
				// itself is never this dependency's content.
				if dep.Dependency.Ecosystem == domain.EcosystemNode && path != a.worktreeDir {
					if name, ok := packageJSONNameAt(path); ok && name != dep.Dependency.Name {
						return filepath.SkipDir
					}
				}
				if hasGoFiles(path) {
					objs, err := normalizeOne(ctx, gd, a, path)
					if err != nil {
						return err
					}
					objects = appendAttributed(objects, dep, a.source, objs)
				}
				return nil
			}

			src := snapshotFor(a, path)
			n, ok := normReg.Select(src)
			if !ok {
				return nil
			}
			objs, err := normalizeOne(ctx, n, a, path)
			if err != nil {
				return err
			}
			objects = appendAttributed(objects, dep, a.source, objs)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%w: source %s: %v", ErrNormalization, a.source.ID, err)
		}
	}
	return objects, nil
}

func normalizeOne(ctx context.Context, n normalize.Normalizer, a acquiredSource, path string) ([]domain.KnowledgeObject, error) {
	src := snapshotFor(a, path)
	objs, err := n.Normalize(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("normalize %s (%s): %w", src.LogicalPath, n.Name(), err)
	}
	for i := range objs {
		if objs[i].Metadata == nil {
			objs[i].Metadata = map[string]string{}
		}
		objs[i].Metadata[normalizerNameKey] = n.Name()
		objs[i].Metadata[normalizerVersionKey] = n.Version()
	}
	return objs, nil
}

func snapshotFor(a acquiredSource, path string) domain.SourceSnapshot {
	rel, _ := filepath.Rel(a.worktreeDir, path)
	return domain.SourceSnapshot{
		ID:          a.source.ID + "@" + a.commit,
		SourceID:    a.source.ID,
		URI:         a.source.URL,
		LocalPath:   path,
		LogicalPath: rel,
		Commit:      a.commit,
		FetchedAt:   time.Now(),
	}
}

// appendAttributed fills in the attribution fields normalizers don't set
// themselves (they only know about the SourceSnapshot they were handed,
// not the registry match that selected this source) before appending
// objs to the running object list.
func appendAttributed(objects []domain.KnowledgeObject, dep domain.DependencyVersion, source registry.Source, objs []domain.KnowledgeObject) []domain.KnowledgeObject {
	for _, obj := range objs {
		obj.Dependency = dep
		obj.SourceType = source.Type
		obj.Authority = source.Authority
		obj.TrustClass = TrustClassForSourceType(source.Type)
		objects = append(objects, obj)
	}
	return objects
}

// TrustClassForSourceType maps a registry.Source's type to the
// TrustClass SEC-001 requires every KnowledgeObject to carry. git/godoc
// both derive directly from the package's own repository; website and
// github-releases are registry-declared official sources. TrustCommunity
// and TrustUser are assigned elsewhere, by whatever future mechanism
// lets a manifest be added outside the built-in/reviewed tier — nothing
// currently produces an object through that path.
func TrustClassForSourceType(sourceType string) domain.TrustClass {
	switch sourceType {
	case "git", "godoc":
		return domain.TrustRepository
	case "website", "github-releases":
		return domain.TrustOfficial
	default:
		return domain.TrustUnknown
	}
}

func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// hasGoMod reports whether dir directly contains a go.mod — marking it
// as a separate Go module's root within a larger checked-out worktree.
func hasGoMod(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
}

// packageJSONNameAt reads dir's own package.json (if any) from the
// checked-out worktree and returns its "name" field.
func packageJSONNameAt(dir string) (string, bool) {
	content, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", false
	}
	return packageJSONName(content)
}

// indexObjects fingerprints, dedups (GEN-003), chunks, and stores every
// object, updating gen's manifest counters as it goes.
func indexObjects(ctx context.Context, gen domain.Generation, objects []domain.KnowledgeObject, badgerStore *badger.Store) error {
	chunkReg := dchunk.NewRegistry().
		Register(chunkmd.New(chunkmd.DefaultMaxChunkBytes), "markdown", "text").
		Register(symbol.New(), "symbol_doc", "package_doc")

	manifest, err := getManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		return err
	}

	sourceSet := map[string]bool{}
	// writtenChunkIDs tracks chunk IDs already stored for gen within this
	// call. Chunk IDs are content-derived (dchunk.ChunkID(obj.ID,
	// ordinal, content)), so two KnowledgeObjects that GEN-003-dedup to
	// the same obj.ID (identical content at different logical paths, or
	// an object reused from a prior generation) produce identical chunk
	// IDs too — without this guard, badgerStore.PutChunk silently
	// overwrites the same key on the second occurrence while
	// manifest.ChunkCount still increments, so the manifest's claimed
	// count exceeds the distinct chunks actually staged and VAL-001's
	// structural check fails every time.
	writtenChunkIDs := map[string]bool{}

	// Writes are batched into a few large commits (a per-value commit made
	// a big generation pay thousands of them). A batch isn't readable
	// until Flush, so identical objects within this call are deduped
	// through storedByContent instead of the Badger content-hash index.
	batch := badgerStore.NewBatch()
	defer batch.Cancel()
	storedByContent := map[string]string{}

	for _, obj := range objects {
		id, contentHash, reused, err := resolveObjectIdentity(ctx, badgerStore, storedByContent, gen, obj)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrNormalization, err)
		}
		obj.ID = id
		obj.ContentHash = contentHash

		if reused {
			manifest.ObjectsReused++
		} else {
			if err := obj.Validate(); err != nil {
				return fmt.Errorf("%w: %v", ErrNormalization, err)
			}
			if err := batch.PutKnowledgeObject(obj); err != nil {
				return fmt.Errorf("%w: store object %s: %v", ErrNormalization, obj.ID, err)
			}
			if err := batch.PutContentHashIndex(contentHash, obj.ID); err != nil {
				return fmt.Errorf("%w: index object %s: %v", ErrNormalization, obj.ID, err)
			}
			storedByContent[contentHash] = obj.ID
			manifest.ObjectsCreated++
		}
		manifest.ObjectCount++
		sourceSet[obj.SourceID] = true

		chunker, ok := chunkReg.Select(obj)
		if !ok {
			continue
		}
		chunks, err := chunker.Chunk(ctx, obj)
		if err != nil {
			return fmt.Errorf("%w: chunk object %s: %v", ErrNormalization, obj.ID, err)
		}
		for _, c := range chunks {
			if writtenChunkIDs[c.ID] {
				continue
			}
			if err := batch.PutChunk(gen.ID, c, obj.Content); err != nil {
				return fmt.Errorf("%w: store chunk %s: %v", ErrNormalization, c.ID, err)
			}
			writtenChunkIDs[c.ID] = true
			manifest.ChunkCount++
		}
	}

	if err := batch.Flush(); err != nil {
		return fmt.Errorf("%w: commit staged objects and chunks: %v", ErrNormalization, err)
	}

	manifest.Sources = manifest.Sources[:0]
	for id := range sourceSet {
		manifest.Sources = append(manifest.Sources, id)
	}
	return putManifest(ctx, badgerStore, manifest)
}

// resolveObjectIdentity computes obj's fingerprint-derived ID and pure
// content hash, and reports whether an object with identical content
// already exists (GEN-003) — in which case its existing ID is reused
// instead of storing a duplicate payload.
func resolveObjectIdentity(ctx context.Context, badgerStore *badger.Store, storedByContent map[string]string, gen domain.Generation, obj domain.KnowledgeObject) (id, contentHash string, reused bool, err error) {
	sourceIdentity := obj.SourceID + "@" + obj.Commit
	digest := fingerprint.Fingerprint(sourceIdentity, obj.LogicalPath, obj.Metadata[normalizerNameKey], obj.Metadata[normalizerVersionKey], obj.Content)
	contentHash = dchunk.ContentHash(obj.Content)

	if existingID, ok := storedByContent[contentHash]; ok {
		return existingID, contentHash, true, nil
	}
	if existingID, lookupErr := badgerStore.GetContentHashIndex(ctx, contentHash); lookupErr == nil {
		return existingID, contentHash, true, nil
	} else if lookupErr != badger.ErrNotFound {
		return "", "", false, lookupErr
	}

	return fingerprint.ObjectID(digest), contentHash, false, nil
}
