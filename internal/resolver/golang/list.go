package golang

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"aleutian-ai/ragctl/internal/executil"
	"aleutian-ai/ragctl/internal/resolver"
)

// defaultListTimeout bounds how long `go list` is allowed to run — module
// resolution can hit the network (GOPROXY) so this is generous.
const defaultListTimeout = 60 * time.Second

// goModule is the subset of `go list -m -json` fields ragctl needs.
type goModule struct {
	Path     string
	Version  string
	Main     bool
	Indirect bool
	Replace  *goModule
	GoMod    string
}

// listModules runs `go list -m -json all` in root and decodes the streamed
// JSON module objects (not a JSON array — concatenated objects).
func listModules(ctx context.Context, root string) ([]goModule, error) {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     root,
		Args:    []string{"go", "list", "-m", "-json", "all"},
		Timeout: defaultListTimeout,
		Env: []string{
			// A vendored project (e.g. kubernetes) makes `go list all`
			// fail with "can't compute 'all' using the vendor
			// directory" under Go's default -mod=vendor auto-detection;
			// -mod=mod forces module-graph resolution regardless.
			"GOFLAGS=-mod=mod",
			// -mod=mod is invalid in workspace mode ("-mod may only be
			// set to readonly or vendor"), which triggers if any
			// ancestor directory has a go.work file (e.g. kubernetes).
			// We detect and resolve per go.mod, not per workspace, so
			// disable workspace mode rather than special-casing it.
			"GOWORK=off",
			// Without this, a go.mod requesting a newer Go than what's
			// installed makes the toolchain try to download that
			// release over the network, which can eat most of
			// defaultListTimeout before failing. Fail fast instead.
			"GOTOOLCHAIN=local",
		},
	})
	if err != nil {
		return nil, &resolver.ResolutionError{Resolver: "go", Root: root, Cause: err}
	}
	if result.ExitCode != 0 {
		return nil, &resolver.ResolutionError{
			Resolver: "go",
			Root:     root,
			Cause:    fmt.Errorf("go list exited %d: %s", result.ExitCode, bytes.TrimSpace(result.Stderr)),
		}
	}

	dec := json.NewDecoder(bytes.NewReader(result.Stdout))
	var modules []goModule
	for i := 0; ; i++ {
		var m goModule
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &resolver.ResolutionError{
				Resolver: "go",
				Root:     root,
				Cause:    fmt.Errorf("decode go list module at index %d: %w", i, err),
			}
		}
		modules = append(modules, m)
	}
	return modules, nil
}
