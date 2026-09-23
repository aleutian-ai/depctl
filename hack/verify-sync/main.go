// Command verify-sync inspects what a real sync actually produced, for a
// given ragctl data directory: not just "did it succeed" but what content
// each synced generation holds, and whether its resolved commit is
// independently verifiable against the real repository — the two
// questions "OK" in a sync log doesn't answer on its own.
//
// Usage: go run ./hack/verify-sync <data-dir> [-ecosystem go|node|python]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	bbolt "aleutian-ai/ragctl/internal/control/bbolt"
	badger "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/source/git"
)

type genReport struct {
	dep          domain.DependencyVersion
	objectCount  int
	chunkCount   int
	contentTypes map[string]int
	// hasStructuredDocs is true if anything beyond markdown/plaintext/
	// releasenotes was extracted (symbol_doc/package_doc) — the signal
	// that real API documentation, not just README/LICENSE text, was
	// indexed. Only Go's normalizer produces these today.
	hasStructuredDocs bool
	titles            []string
	sourceURI         string
	commit            string
	tagVerified       string // "" (not checked), "yes", "no", "error: ..."
}

func main() {
	ecosystem := flag.String("ecosystem", "", "only report dependencies of this ecosystem (empty: all)")
	checkTags := flag.Bool("check-tags", true, "independently verify each generation's commit is reachable via git ls-remote")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: verify-sync <data-dir> [-ecosystem go|node|python] [-check-tags=false]")
		os.Exit(2)
	}
	data := flag.Arg(0)
	ctx := context.Background()

	store, err := bbolt.Open(filepath.Join(data, "control.db"))
	must(err)
	defer store.Close()
	bg, err := badger.Open(filepath.Join(data, "badger"))
	must(err)
	defer bg.Close()

	gens, err := store.ListAllGenerations(ctx)
	must(err)

	var reports []genReport
	for _, g := range gens {
		if g.State != domain.GenActive {
			continue
		}
		if *ecosystem != "" && string(g.Dependency.Dependency.Ecosystem) != *ecosystem {
			continue
		}
		reports = append(reports, buildReport(ctx, bg, g))
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].dep.Dependency.Name < reports[j].dep.Dependency.Name })

	if *checkTags {
		cache := git.NewCache(filepath.Join(os.TempDir(), "verify-sync-git"))
		for i := range reports {
			verifyTag(ctx, cache, &reports[i])
		}
	}

	printSummary(reports)
}

func buildReport(ctx context.Context, bg *badger.Store, g domain.Generation) genReport {
	r := genReport{dep: g.Dependency, contentTypes: map[string]int{}}
	chunks, err := bg.ListGenerationChunks(ctx, g.ID)
	must(err)
	r.chunkCount = len(chunks)

	seen := map[string]bool{}
	for _, c := range chunks {
		if seen[c.ObjectID] {
			continue
		}
		seen[c.ObjectID] = true
		obj, err := bg.GetKnowledgeObject(ctx, c.ObjectID)
		if err != nil {
			continue
		}
		r.objectCount++
		r.contentTypes[obj.ContentType]++
		if obj.ContentType == "symbol_doc" || obj.ContentType == "package_doc" {
			r.hasStructuredDocs = true
		}
		if len(r.titles) < 3 {
			r.titles = append(r.titles, obj.Title)
		}
		if r.sourceURI == "" {
			r.sourceURI, r.commit = obj.SourceURI, obj.Commit
		}
	}
	return r
}

func verifyTag(ctx context.Context, cache *git.Cache, r *genReport) {
	if r.sourceURI == "" || r.commit == "" {
		r.tagVerified = "error: no source recorded"
		return
	}
	repoPath, err := cache.EnsureMirror(ctx, r.sourceURI)
	if err != nil {
		r.tagVerified = "error: mirror: " + err.Error()
		return
	}
	out, err := exec.Command("git", "-C", repoPath, "tag", "--points-at", r.commit).CombinedOutput()
	if err != nil {
		r.tagVerified = "error: " + err.Error()
		return
	}
	tags := strings.Fields(string(out))
	if len(tags) == 0 {
		r.tagVerified = "no (commit has no tag pointing at it)"
		return
	}
	// The real correctness question: does at least one tag actually name
	// this package? A bare "v1.2.3" on a shared multi-package repo is the
	// exact ambiguity class REG-012 found live (eslint-visitor-keys).
	name := lastPathSegment(r.dep.Dependency.Name)
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), strings.ToLower(name)) {
			r.tagVerified = "yes (" + t + ")"
			return
		}
	}
	r.tagVerified = fmt.Sprintf("SUSPECT: tag(s) %v don't mention %q — may be an unrelated package's tag", tags, name)
}

func lastPathSegment(name string) string {
	name = strings.TrimPrefix(name, "@")
	parts := strings.Split(name, "/")
	return parts[len(parts)-1]
}

func printSummary(reports []genReport) {
	var readmeOnly, structured, empty, suspect int
	for _, r := range reports {
		switch {
		case r.objectCount == 0:
			empty++
		case r.hasStructuredDocs:
			structured++
		default:
			readmeOnly++
		}
		if strings.HasPrefix(r.tagVerified, "SUSPECT") {
			suspect++
		}
		fmt.Printf("%-45s %-14s objs=%-3d chunks=%-4d types=%v structured=%-5v tag=%s\n",
			r.dep.Dependency.Name, r.dep.Version, r.objectCount, r.chunkCount, r.contentTypes, r.hasStructuredDocs, r.tagVerified)
		if len(r.titles) > 0 {
			fmt.Printf("    titles: %v\n", r.titles)
		}
	}
	fmt.Printf("\n=== %d generations: %d structured-docs, %d readme/license-only, %d empty, %d SUSPECT tag matches ===\n",
		len(reports), structured, readmeOnly, empty, suspect)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
