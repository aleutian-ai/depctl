//go:build unix

package cli

import (
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
)

// TestDaemonResyncsProjectWhenGoModChanges exercises the whole watch
// path in the daemon: real fsnotify events, debounce, the scheduler, and
// a resolve-then-sync run.
func TestDaemonResyncsProjectWhenGoModChanges(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	deadEndpointsConfig(t, func(c *config.Config) { c.Watch.Debounce = 100 * time.Millisecond })
	root := scanDepFixture(t)

	h := startDaemon(t)
	waitFor(t, "the daemon to watch the fixture", func() bool {
		return strings.Contains(h.out.String(), "watching "+root+" (go.mod")
	})

	addBarDependency(t, root)
	waitFor(t, "the change to be resolved and synced", func() bool {
		return strings.Contains(h.out.String(), "synced,")
	})

	text := h.out.String()
	for _, want := range []string{"change in " + root + ": go.mod", "resolved " + root + ": 2 dependencies"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}
