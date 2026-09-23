package golang

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"aleutian-ai/ragctl/internal/executil"
	"aleutian-ai/ragctl/internal/resolver"
)

// defaultListTimeout bounds how long `go list` is allowed to run — module
// resolution can hit the network (GOPROXY) so this is generous.
const defaultListTimeout = 60 * time.Second

// unresolvedLocalReplaceVersion is the zero-time placeholder pseudo-version
// Go substitutes for a require whose version only ever existed via another
// module's local replace directive — the shape produced by a Go
// workspace's sibling modules (each with its own go.mod replacing only the
// siblings it itself knows about) when resolved standalone instead of
// through the go.work that ties them together, e.g.
// github.com/hashicorp/terraform's per-backend submodules.
const unresolvedLocalReplaceVersion = "00010101000000-000000000000"

// isUnresolvedLocalReplaceError reports whether err is go list failing to
// resolve unresolvedLocalReplaceVersion — either because a real module
// proxy has no such revision ("invalid version: unknown revision"), or
// because GOPROXY=off refuses to even look ("module lookup disabled").
// Both indicate the same root cause: a placeholder version only a
// workspace covering the sibling that replaces it could resolve.
func isUnresolvedLocalReplaceError(err error) bool {
	msg := err.Error()
	if !strings.Contains(msg, unresolvedLocalReplaceVersion) {
		return false
	}
	return strings.Contains(msg, "invalid version") || strings.Contains(msg, "module lookup disabled")
}

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
// JSON module objects (not a JSON array — concatenated objects). If the
// standalone attempt fails with unresolvedLocalReplaceMarker, it retries
// once inside a synthesized go.work covering every go.mod under root's
// repository — see workspace_fallback.go.
func listModules(ctx context.Context, root string) ([]goModule, error) {
	result, standaloneErr := runGoList(ctx, root, standaloneListEnv())
	if standaloneErr == nil {
		return decodeModules(root, result.Stdout)
	}
	if !isUnresolvedLocalReplaceError(standaloneErr) {
		return nil, standaloneErr
	}

	work, cleanup, workErr := synthesizeWorkspace(ctx, root)
	if workErr != nil {
		// The standalone error is more informative than a workspace-
		// synthesis failure (e.g. no repo root found, or only one go.mod
		// in the repo, meaning a workspace wouldn't help anyway).
		return nil, standaloneErr
	}
	defer cleanup()

	wsResult, wsErr := runGoList(ctx, root, workspaceListEnv(work))
	if wsErr != nil {
		return nil, standaloneErr
	}
	return decodeModules(root, wsResult.Stdout)
}

// runGoList runs `go list -m -json all` in root with the given extra
// environment, returning a resolver.ResolutionError on any failure to
// start, time out, or exit non-zero.
func runGoList(ctx context.Context, root string, env []string) (executil.RunResult, error) {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     root,
		Args:    []string{"go", "list", "-m", "-json", "all"},
		Timeout: defaultListTimeout,
		Env:     env,
	})
	if err != nil {
		return result, &resolver.ResolutionError{Resolver: "go", Root: root, Cause: err}
	}
	if result.ExitCode != 0 {
		return result, &resolver.ResolutionError{
			Resolver: "go",
			Root:     root,
			Cause:    fmt.Errorf("go list exited %d: %s", result.ExitCode, bytes.TrimSpace(result.Stderr)),
		}
	}
	return result, nil
}

// standaloneListEnv is the default, fast-path environment: resolve this
// go.mod on its own, ignoring any ancestor go.work.
func standaloneListEnv() []string {
	return []string{
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
	}
}

// workspaceListEnv points GOWORK at a synthesized workspace file instead of
// disabling workspace mode. -mod=mod is invalid in workspace mode, so
// GOFLAGS is cleared rather than set (overriding any inherited env value).
func workspaceListEnv(goWorkPath string) []string {
	return []string{
		"GOFLAGS=",
		"GOWORK=" + goWorkPath,
		"GOTOOLCHAIN=local",
	}
}

// decodeModules decodes listModules' streamed `go list -m -json` output.
func decodeModules(root string, stdout []byte) ([]goModule, error) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
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
