package generation

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	"aleutian-ai/ragctl/internal/normalize/releasenotes"
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

	acquired, err := acquireGitSources(ctx, gitCache, gen.Dependency.Version, sources)
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
func acquireGitSources(ctx context.Context, gitCache *git.Cache, version string, sources []registry.Source) ([]acquiredSource, error) {
	var acquired []acquiredSource
	for _, s := range sources {
		if s.Type != "git" {
			continue
		}

		ref, err := gitRef(s, version)
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: %v", ErrAcquisition, s.ID, err)
		}

		repoPath, err := gitCache.EnsureMirror(ctx, s.URL)
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: mirror: %v", ErrAcquisition, s.ID, err)
		}

		commit, err := gitCache.ResolveRef(ctx, repoPath, ref)
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: resolve ref %q: %v", ErrAcquisition, s.ID, ref, err)
		}

		worktreeDir, cleanup, err := gitCache.MaterializeWorktree(ctx, repoPath, commit)
		if err != nil {
			return acquired, fmt.Errorf("%w: source %s: worktree: %v", ErrAcquisition, s.ID, err)
		}

		acquired = append(acquired, acquiredSource{source: s, worktreeDir: worktreeDir, commit: commit, cleanup: cleanup})
	}
	return acquired, nil
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

	var objects []domain.KnowledgeObject
	for _, a := range acquired {
		err := filepath.WalkDir(a.worktreeDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirNames[d.Name()] {
					return filepath.SkipDir
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
		obj.TrustClass = trustClassForSourceType(source.Type)
		objects = append(objects, obj)
	}
	return objects
}

// trustClassForSourceType maps a registry.Source's type to the
// TrustClass SEC-001 requires every KnowledgeObject to carry. git/godoc
// both derive directly from the package's own repository; website and
// github-releases are registry-declared official sources. TrustCommunity
// and TrustUser are assigned elsewhere, by whatever future mechanism
// lets a manifest be added outside the built-in/reviewed tier — nothing
// currently produces an object through that path.
func trustClassForSourceType(sourceType string) domain.TrustClass {
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
	for _, obj := range objects {
		id, contentHash, reused, err := resolveObjectIdentity(ctx, badgerStore, gen, obj)
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
			if err := badgerStore.PutKnowledgeObject(ctx, obj); err != nil {
				return fmt.Errorf("%w: store object %s: %v", ErrNormalization, obj.ID, err)
			}
			if err := badgerStore.PutContentHashIndex(ctx, contentHash, obj.ID); err != nil {
				return fmt.Errorf("%w: index object %s: %v", ErrNormalization, obj.ID, err)
			}
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
			if err := badgerStore.PutChunk(ctx, gen.ID, c); err != nil {
				return fmt.Errorf("%w: store chunk %s: %v", ErrNormalization, c.ID, err)
			}
			writtenChunkIDs[c.ID] = true
			manifest.ChunkCount++
		}
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
func resolveObjectIdentity(ctx context.Context, badgerStore *badger.Store, gen domain.Generation, obj domain.KnowledgeObject) (id, contentHash string, reused bool, err error) {
	sourceIdentity := obj.SourceID + "@" + obj.Commit
	digest := fingerprint.Fingerprint(sourceIdentity, obj.LogicalPath, obj.Metadata[normalizerNameKey], obj.Metadata[normalizerVersionKey], obj.Content)
	contentHash = dchunk.ContentHash(obj.Content)

	if existingID, lookupErr := badgerStore.GetContentHashIndex(ctx, contentHash); lookupErr == nil {
		return existingID, contentHash, true, nil
	} else if lookupErr != badger.ErrNotFound {
		return "", "", false, lookupErr
	}

	return fingerprint.ObjectID(digest), contentHash, false, nil
}
