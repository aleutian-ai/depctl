package git

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/aleutian-ai/depctl/internal/executil"
)

// FileStatus classifies how a file changed between two commits.
type FileStatus string

const (
	FileAdded    FileStatus = "added"
	FileModified FileStatus = "modified"
	FileDeleted  FileStatus = "deleted"
	FileRenamed  FileStatus = "renamed"
)

// FileDelta describes one file's change between two commits. OldPath is
// set only when Status is FileRenamed.
type FileDelta struct {
	Path    string
	OldPath string
	Status  FileStatus
}

// emptyTreeCommit is Git's well-known empty-tree object, diffed against
// when there is no prior commit (first-ever sync of a package).
const emptyTreeCommit = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Delta computes the file-level diff between oldCommit and newCommit in
// repoPath, as an optimization hint for normalization — not as the
// authoritative change signal, which remains content hashing. An empty
// oldCommit diffs against Git's empty tree, reporting every file in
// newCommit as added.
func (c *Cache) Delta(ctx context.Context, repoPath, oldCommit, newCommit string) ([]FileDelta, error) {
	from := oldCommit
	if from == "" {
		from = emptyTreeCommit
	}

	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "diff", "--name-status", from, newCommit},
		Timeout: defaultLocalTimeout,
	})
	if err != nil {
		return nil, &CacheError{Op: "Delta", Kind: ErrKindPermanent, Cause: err}
	}
	if result.ExitCode != 0 {
		return nil, &CacheError{
			Op:    "Delta",
			Kind:  ErrKindPermanent,
			Cause: fmt.Errorf("git diff --name-status %s %s: %s", from, newCommit, bytes.TrimSpace(result.Stderr)),
		}
	}

	deltas, err := parseNameStatus(result.Stdout)
	if err != nil {
		return nil, &CacheError{Op: "Delta", Kind: ErrKindPermanent, Cause: err}
	}
	return deltas, nil
}

// parseNameStatus parses `git diff --name-status` output into FileDeltas.
func parseNameStatus(out []byte) ([]FileDelta, error) {
	var deltas []FileDelta
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return nil, fmt.Errorf("git: malformed diff --name-status line %q", line)
		}

		code := fields[0]
		switch {
		case code == "A":
			deltas = append(deltas, FileDelta{Path: fields[1], Status: FileAdded})
		case code == "M":
			deltas = append(deltas, FileDelta{Path: fields[1], Status: FileModified})
		case code == "D":
			deltas = append(deltas, FileDelta{Path: fields[1], Status: FileDeleted})
		case strings.HasPrefix(code, "R"):
			if len(fields) < 3 {
				return nil, fmt.Errorf("git: malformed rename line %q", line)
			}
			deltas = append(deltas, FileDelta{OldPath: fields[1], Path: fields[2], Status: FileRenamed})
		default:
			return nil, fmt.Errorf("git: unrecognized diff status %q in line %q", code, line)
		}
	}
	return deltas, nil
}
