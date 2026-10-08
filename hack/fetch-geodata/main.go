// Command fetch-geodata downloads real-world datasets (Census
// TIGER/Line, NOAA nautical charts, maritime boundaries, and their
// standalone documentation) from a plain-text manifest of URLs — the
// raw-HTTP counterpart to hack/fetch-corpus, which only handles git
// repos. A .zip URL is extracted; anything else (e.g. a PDF technical
// doc) is downloaded as a plain file. Not part of the depctl binary — a
// dev tool for building an offline corpus.
package main

import (
	"archive/zip"
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// source is one manifest line: a URL to fetch, checked out under a
// category subdirectory of the corpus root — same idea as fetch-corpus's
// repoSpec, but for a direct file download instead of a git repo.
type source struct {
	Category string
	URL      string
}

func main() {
	manifestPath := flag.String("manifest", "hack/fetch-geodata/sources.txt", "path to the source list (\"<category> <url>\" per line)")
	root := flag.String("root", defaultRoot(), "directory to download and extract into, one subdir per category")
	concurrency := flag.Int("concurrency", 3, "number of downloads to run concurrently (kept low — these are large single-server files, not GitHub)")
	backupDir := flag.String("backup", "", "if set, rsync the corpus root's contents there after fetching (additive only — never deletes)")
	timeout := flag.Duration("timeout", 20*time.Minute, "per-file download timeout")
	flag.Parse()

	sources, err := readManifest(*manifestPath)
	if err != nil {
		log.Fatalf("read manifest: %v", err)
	}

	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string

	for _, s := range sources {
		wg.Add(1)
		sem <- struct{}{}
		go func(s source) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := fetchOne(*root, s, *timeout); err != nil {
				mu.Lock()
				failed = append(failed, fmt.Sprintf("%s: %v", s.URL, err))
				mu.Unlock()
				fmt.Printf("FAIL  %-30s %s: %v\n", s.Category, s.URL, err)
			}
		}(s)
	}
	wg.Wait()

	exitCode := 0
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d of %d sources failed:\n", len(failed), len(sources))
		for _, f := range failed {
			fmt.Fprintln(os.Stderr, "  "+f)
		}
		exitCode = 1
	}

	if *backupDir != "" {
		fmt.Printf("\nbacking up %s -> %s\n", *root, *backupDir)
		if err := backupCorpus(*root, *backupDir); err != nil {
			fmt.Fprintf(os.Stderr, "backup failed: %v\n", err)
			exitCode = 1
		} else {
			fmt.Println("backup complete")
		}
	}

	os.Exit(exitCode)
}

// fetchOne downloads s.URL into <root>/<s.Category>/<basename>.zip and
// extracts it into a same-named directory (extension stripped) if it's a
// .zip, skipping entirely if that extraction directory already exists.
// A non-.zip URL (e.g. a PDF technical doc) is downloaded as a plain
// file instead — no extraction — skipping if that file already exists.
func fetchOne(root string, s source, timeout time.Duration) error {
	filename := path.Base(s.URL)
	targetDir := filepath.Join(root, s.Category)

	if strings.EqualFold(filepath.Ext(filename), ".zip") {
		return fetchAndExtractZip(targetDir, s, filename, timeout)
	}
	return fetchPlainFile(targetDir, s, filename, timeout)
}

func fetchAndExtractZip(targetDir string, s source, filename string, timeout time.Duration) error {
	extractDir := filepath.Join(targetDir, strings.TrimSuffix(filename, filepath.Ext(filename)))

	if info, err := os.Stat(extractDir); err == nil && info.IsDir() {
		fmt.Printf("SKIP  %-30s %s (already extracted)\n", s.Category, s.URL)
		return nil
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	zipPath := filepath.Join(targetDir, filename)
	if err := download(s.URL, zipPath, timeout); err != nil {
		os.Remove(zipPath)
		return fmt.Errorf("download: %w", err)
	}

	if err := extractZip(zipPath, extractDir); err != nil {
		os.RemoveAll(extractDir)
		return fmt.Errorf("extract: %w", err)
	}

	// The zip's content now lives in extractDir; keeping the archive
	// around too just doubles disk usage for no benefit (re-running this
	// tool re-downloads on demand — extractDir's presence is the only
	// idempotency signal it needs).
	os.Remove(zipPath)

	fmt.Printf("OK    %-30s %s\n", s.Category, s.URL)
	return nil
}

// fetchPlainFile downloads a non-.zip URL straight into targetDir with
// no extraction step — the file's own presence is the idempotency
// signal, same role extractDir plays for a zip source.
func fetchPlainFile(targetDir string, s source, filename string, timeout time.Duration) error {
	destPath := filepath.Join(targetDir, filename)

	if _, err := os.Stat(destPath); err == nil {
		fmt.Printf("SKIP  %-30s %s (already downloaded)\n", s.Category, s.URL)
		return nil
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := download(s.URL, destPath, timeout); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("download: %w", err)
	}

	fmt.Printf("OK    %-30s %s\n", s.Category, s.URL)
	return nil
}

// download fetches url into destPath.
func download(url, destPath string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// extractZip unpacks zipPath into destDir, rejecting any entry whose
// path would escape destDir (zip-slip) — these archives come from
// external URLs, so that guard isn't optional.
func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	for _, f := range r.File {
		entryPath := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(entryPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %q escapes destination directory", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(entryPath, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, entryPath); err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
	}
	return nil
}

func extractFile(f *zip.File, destPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// backupCorpus rsyncs root's contents into dst, additively — same
// convention as hack/fetch-corpus's -backup flag.
func backupCorpus(root, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir backup dest: %w", err)
	}
	src := strings.TrimSuffix(root, string(filepath.Separator)) + string(filepath.Separator)

	cmd := exec.Command("rsync", "-a", "--stats", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rsync: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Print(string(out))
	return nil
}

// readManifest parses lines of "<category> <url>"; blank lines and
// lines starting with # are ignored. category may contain '/'.
func readManifest(path string) ([]source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var sources []source
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed line %q: want \"<category> <url>\"", line)
		}
		sources = append(sources, source{Category: fields[0], URL: fields[1]})
	}
	return sources, scanner.Err()
}

func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./offline-knowledge"
	}
	return filepath.Join(home, "offline-knowledge")
}
