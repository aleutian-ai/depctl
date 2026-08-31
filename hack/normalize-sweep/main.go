// Command normalize-sweep runs internal/normalize's normalizers across a
// broad, evenly-sampled slice of a real corpus (e.g. ~/offline-knowledge)
// and reports errors, panics, and parse_warning flags. It's the same
// "run it against real content, not just fixtures" methodology used to
// stress-test the Go resolver (GO-002) and the Markdown normalizer
// (NORM-002's cobra README bug) — a dev tool, not part of ragctl itself.
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

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/normalize"
	"aleutian-ai/ragctl/internal/normalize/godoc"
	"aleutian-ai/ragctl/internal/normalize/markdown"
	"aleutian-ai/ragctl/internal/normalize/plaintext"
	"aleutian-ai/ragctl/internal/normalize/releasenotes"
)

// skipDirNames are directories whose contents aren't representative
// documentation (vendored deps, build output, VCS internals) — skipped
// during the walk rather than sampled.
var skipDirNames = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "dist": true,
	"build": true, ".venv": true, "venv": true, "__pycache__": true, "target": true,
}

const maxSamplesPerCategory = 20

func main() {
	root := flag.String("root", defaultRoot(), "corpus root to sweep")
	maxPerExt := flag.Int("max-per-ext", 1500, "max files per extension to sample, evenly strided across the whole corpus rather than the first N alphabetically")
	maxPackages := flag.Int("max-packages", 500, "max Go package directories to sample")
	flag.Parse()

	fileCandidates := map[string][]string{".md": nil, ".mdx": nil, ".txt": nil, ".rst": nil}
	var pkgDirs []string

	err := filepath.WalkDir(*root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry — skip it, don't abort the sweep
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

	reg := normalize.NewRegistry(releasenotes.New(), markdown.New(), plaintext.New())

	var total, fileErrs, warnings int
	var errSamples, warnSamples []string

	for _, ext := range []string{".md", ".mdx", ".txt", ".rst"} {
		paths := fileCandidates[ext]
		sampled := stride(paths, *maxPerExt)
		fmt.Printf("%-6s %6d candidates, sampling %d\n", ext, len(paths), len(sampled))

		for _, p := range sampled {
			rel, _ := filepath.Rel(*root, p)
			src := domain.SourceSnapshot{LocalPath: p, LogicalPath: rel}
			n, ok := reg.Select(src)
			if !ok {
				continue
			}
			total++
			objs, err := safeNormalize(n, src)
			if err != nil {
				fileErrs++
				errSamples = appendSample(errSamples, fmt.Sprintf("%s (%s): %v", rel, n.Name(), err))
				continue
			}
			for _, o := range objs {
				if o.Metadata["parse_warning"] == "true" {
					warnings++
					warnSamples = appendSample(warnSamples, rel)
				}
			}
		}
	}

	sampledPkgs := stride(pkgDirs, *maxPackages)
	fmt.Printf("go-pkg %6d candidates, sampling %d\n", len(pkgDirs), len(sampledPkgs))

	gd := godoc.New()
	var pkgErrs int
	var pkgErrSamples []string
	for _, dir := range sampledPkgs {
		rel, _ := filepath.Rel(*root, dir)
		src := domain.SourceSnapshot{LocalPath: dir, LogicalPath: rel}
		if !gd.Supports(src) {
			continue
		}
		total++
		if _, err := safeNormalize(gd, src); err != nil {
			pkgErrs++
			pkgErrSamples = appendSample(pkgErrSamples, fmt.Sprintf("%s: %v", rel, err))
		}
	}

	fmt.Printf("\n--- summary ---\n")
	fmt.Printf("processed: %d files/packages\n", total)
	fmt.Printf("errors: %d (markdown/plaintext/release-notes) + %d (go packages)\n", fileErrs, pkgErrs)
	fmt.Printf("parse_warning flags: %d\n", warnings)

	printSamples("file/text errors", errSamples)
	printSamples("go package errors", pkgErrSamples)
	printSamples("parse_warning files", warnSamples)
}

// safeNormalize recovers from a panic inside a normalizer so one bad
// file can't kill the whole sweep — a panic is reported the same way as
// a returned error.
func safeNormalize(n normalize.Normalizer, src domain.SourceSnapshot) (objs []domain.KnowledgeObject, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return n.Normalize(context.Background(), src)
}

func appendSample(samples []string, s string) []string {
	if len(samples) >= maxSamplesPerCategory {
		return samples
	}
	return append(samples, s)
}

func printSamples(label string, samples []string) {
	if len(samples) == 0 {
		return
	}
	fmt.Printf("\nsample %s (up to %d shown):\n", label, maxSamplesPerCategory)
	for _, s := range samples {
		fmt.Println("  " + s)
	}
}

// hasGoFiles reports whether dir directly contains at least one .go
// file (non-recursive — matches godoc.Normalizer's own per-package scope).
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

// stride evenly samples up to max items from items, rather than taking
// the first max — a first-N sample would be biased toward whatever
// sorts alphabetically first (e.g. always "act" and "AdGuardHome" ahead
// of "kubernetes" or "pytorch").
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
