// Command chunk-sweep runs the full normalize -> fingerprint -> chunk
// pipeline across a broad, evenly-sampled slice of a real corpus (e.g.
// ~/offline-knowledge), assigning each KnowledgeObject an ID exactly the
// way a future generation builder would (HASH-001+HASH-002), then
// chunking it and checking basic invariants: no panics/errors, no empty
// chunks, no duplicate chunk IDs within an object, and no wildly
// oversized chunks. A dev tool, not part of ragctl itself — the
// chunking analog of hack/normalize-sweep.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
)

var skipDirNames = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "dist": true,
	"build": true, ".venv": true, "venv": true, "__pycache__": true, "target": true,
}

const maxSamples = 20

// oversizeTolerance flags a chunk as suspicious once it's this many
// times over maxChunkBytes — the ticket accepts a lone oversized
// paragraph as-is, but a chunk many multiples over the limit suggests
// packSection isn't actually splitting.
const oversizeTolerance = 5

func main() {
	root := flag.String("root", defaultRoot(), "corpus root to sweep")
	maxPerExt := flag.Int("max-per-ext", 1500, "max files per extension to sample")
	maxPackages := flag.Int("max-packages", 500, "max Go package directories to sample")
	maxChunkBytes := flag.Int("max-chunk-bytes", chunkmd.DefaultMaxChunkBytes, "chunk size bound passed to the markdown chunker")
	flag.Parse()

	fileCandidates := map[string][]string{".md": nil, ".mdx": nil, ".txt": nil, ".rst": nil}
	var pkgDirs []string

	err := filepath.WalkDir(*root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			if hasGoFiles(path) {
				pkgDirs = append(pkgDirs, path)
			}
			return nil
		}
		if paths, ok := fileCandidates[strings.ToLower(filepath.Ext(path))]; ok {
			fileCandidates[strings.ToLower(filepath.Ext(path))] = append(paths, path)
		}
		return nil
	})
	if err != nil {
		log.Fatalf("walk %s: %v", *root, err)
	}

	normReg := normalize.NewRegistry(releasenotes.New(), markdown.New(), plaintext.New())
	chunkReg := dchunk.NewRegistry().
		Register(chunkmd.New(*maxChunkBytes), "markdown", "text").
		Register(symbol.New(), "symbol_doc", "package_doc")

	var objectsSeen, chunksSeen, normErrs, chunkErrs, emptyChunks, dupIDs, oversized int
	var samples []string

	processObject := func(rel string, obj domain.KnowledgeObject, normalizerName, normalizerVersion string) {
		digest := fingerprint.Fingerprint(rel, obj.LogicalPath, normalizerName, normalizerVersion, obj.Content)
		obj.ID = fingerprint.ObjectID(digest)
		objectsSeen++

		chunker, ok := chunkReg.Select(obj)
		if !ok {
			return
		}
		chunks, err := safeChunk(chunker, obj)
		if err != nil {
			chunkErrs++
			samples = appendSample(samples, fmt.Sprintf("%s (%s): %v", rel, obj.ContentType, err))
			return
		}

		seenIDs := map[string]bool{}
		for _, c := range chunks {
			chunksSeen++
			if len(c.Content) == 0 {
				emptyChunks++
				if emptyChunks <= 10 {
					fmt.Printf("EMPTY CHUNK: %s content_type=%s symbol=%q signature=%q\n", rel, obj.ContentType, c.Metadata["symbol"], c.Metadata["signature"])
				}
			}
			if seenIDs[c.ID] {
				dupIDs++
				samples = appendSample(samples, fmt.Sprintf("%s: duplicate chunk ID %s", rel, c.ID))
			}
			seenIDs[c.ID] = true
			if len(c.Content) > *maxChunkBytes*oversizeTolerance {
				oversized++
				samples = appendSample(samples, fmt.Sprintf("%s: chunk %d is %d bytes (>%dx limit)", rel, c.Ordinal, len(c.Content), oversizeTolerance))
			}
		}
	}

	for _, ext := range []string{".md", ".mdx", ".txt", ".rst"} {
		paths := fileCandidates[ext]
		sampled := stride(paths, *maxPerExt)
		fmt.Printf("%-6s %6d candidates, sampling %d\n", ext, len(paths), len(sampled))

		for _, p := range sampled {
			rel, _ := filepath.Rel(*root, p)
			src := domain.SourceSnapshot{LocalPath: p, LogicalPath: rel}
			n, ok := normReg.Select(src)
			if !ok {
				continue
			}
			objs, err := safeNormalize(n, src)
			if err != nil {
				normErrs++
				fmt.Printf("NORMALIZE ERROR: %s (%s): %v\n", rel, n.Name(), err)
				continue
			}
			for _, obj := range objs {
				processObject(rel, obj, n.Name(), n.Version())
			}
		}
	}

	sampledPkgs := stride(pkgDirs, *maxPackages)
	fmt.Printf("go-pkg %6d candidates, sampling %d\n", len(pkgDirs), len(sampledPkgs))
	gd := godoc.New()
	for _, dir := range sampledPkgs {
		rel, _ := filepath.Rel(*root, dir)
		src := domain.SourceSnapshot{LocalPath: dir, LogicalPath: rel}
		if !gd.Supports(src) {
			continue
		}
		objs, err := safeNormalize(gd, src)
		if err != nil {
			normErrs++
			samples = appendSample(samples, fmt.Sprintf("%s (godoc, normalize): %v", rel, err))
			continue
		}
		for _, obj := range objs {
			processObject(rel, obj, gd.Name(), gd.Version())
		}
	}

	fmt.Printf("\n--- summary ---\n")
	fmt.Printf("objects chunked: %d, chunks produced: %d\n", objectsSeen, chunksSeen)
	fmt.Printf("normalize errors: %d, chunk errors: %d\n", normErrs, chunkErrs)
	fmt.Printf("empty chunks: %d, duplicate IDs: %d, oversized (>%dx limit): %d\n", emptyChunks, dupIDs, oversizeTolerance, oversized)
	if len(samples) > 0 {
		fmt.Printf("\nsamples (up to %d):\n", maxSamples)
		for _, s := range samples {
			fmt.Println("  " + s)
		}
	}
}

func safeNormalize(n normalize.Normalizer, src domain.SourceSnapshot) (objs []domain.KnowledgeObject, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return n.Normalize(context.Background(), src)
}

func safeChunk(c dchunk.Chunker, obj domain.KnowledgeObject) (chunks []domain.Chunk, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return c.Chunk(context.Background(), obj)
}

func appendSample(samples []string, s string) []string {
	if len(samples) >= maxSamples {
		return samples
	}
	return append(samples, s)
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

func stride(items []string, max int) []string {
	if max <= 0 || len(items) <= max {
		return items
	}
	sort.Strings(items)
	step := float64(len(items)) / float64(max)
	out := make([]string, 0, max)
	for i := 0; i < max; i++ {
		out = append(out, items[int(float64(i)*step)])
	}
	return out
}

func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./offline-knowledge"
	}
	return filepath.Join(home, "offline-knowledge")
}
