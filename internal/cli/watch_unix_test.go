//go:build unix

package cli

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
)

// startWatchCommand runs `ragctl watch` in the background and waits until
// it has installed its signal handler (it prints "Ctrl-C to stop" after).
func startWatchCommand(t *testing.T) (*syncBuffer, <-chan error) {
	t.Helper()
	out := &syncBuffer{}
	root := NewRootCmd()
	root.SetOut(out)
	root.SetArgs([]string{"watch"})
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	waitFor(t, "watch to start", func() bool { return strings.Contains(out.String(), "Ctrl-C to stop") })
	return out, done
}

func stopWithSIGTERM(t *testing.T, done <-chan error) {
	t.Helper()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch after SIGTERM returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not exit within 10s of SIGTERM")
	}
}

func TestWatchExitsCleanlyOnSIGTERM(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	out, done := startWatchCommand(t)
	stopWithSIGTERM(t, done)
	if !strings.Contains(out.String(), "stopped") {
		t.Errorf("output = %q, want a final 'stopped' line", out.String())
	}
}

func TestWatchResyncsProjectWhenGoModChanges(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	deadEndpointsConfig(t, func(c *config.Config) { c.Watch.Debounce = 100 * time.Millisecond })
	root := scanDepFixture(t)

	out, done := startWatchCommand(t)
	if !strings.Contains(out.String(), "watching "+root+" (go.mod") {
		t.Errorf("startup output = %q, want the fixture project listed with its go.mod", out.String())
	}

	addBarDependency(t, root)
	waitFor(t, "the change to be resolved and synced", func() bool {
		return strings.Contains(out.String(), "synced,")
	})

	text := out.String()
	for _, want := range []string{"change in " + root + ": go.mod", "resolved " + root + ": 2 dependencies"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	stopWithSIGTERM(t, done)
}
