package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
)

// isolateEnv points HOME, XDG_CONFIG_HOME, and XDG_DATA_HOME at fresh temp
// dirs, since os.UserConfigDir() only honors XDG_CONFIG_HOME on Linux and
// falls back to $HOME/Library/Application Support on Darwin regardless.
//
// Uses its own retrying cleanup rather than t.TempDir(): tests that spawn a
// real `go` subprocess under an isolated HOME (see requireGo in
// scan_test.go) occasionally race a background process the go toolchain
// leaves briefly running against HOME's directory tree (observed even with
// telemetry disabled), which makes a single-attempt os.RemoveAll flaky
// under the Alpine/Podman test container specifically.
func isolateEnv(t *testing.T) {
	t.Helper()
	home := tempDirRetryCleanup(t)
	dataDir := tempDirRetryCleanup(t)
	configHome := tempDirRetryCleanup(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", configHome)
}

func tempDirRetryCleanup(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ragctl-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		var err error
		for i := 0; i < 10; i++ {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("cleanup %s: %v", dir, err)
	})
	return dir
}

func TestInitCreatesExpectedTree(t *testing.T) {
	isolateEnv(t)

	root := NewRootCmd()
	root.SetArgs([]string{"init"})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	dataDir, err := config.DefaultDataDir()
	if err != nil {
		t.Fatalf("DefaultDataDir: %v", err)
	}
	configPath, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}

	for _, p := range []string{
		filepath.Join(dataDir, "control.db"),
		filepath.Join(dataDir, "badger"),
		filepath.Join(dataDir, "git"),
		filepath.Join(dataDir, "registry"),
		configPath,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	if !strings.Contains(out.String(), "created") {
		t.Errorf("expected output to report created items, got:\n%s", out.String())
	}
}

func TestInitTwiceIsIdempotent(t *testing.T) {
	isolateEnv(t)

	run := func() string {
		root := NewRootCmd()
		root.SetArgs([]string{"init"})
		var out bytes.Buffer
		root.SetOut(&out)
		if err := root.Execute(); err != nil {
			t.Fatalf("init failed: %v", err)
		}
		return out.String()
	}

	run()

	configPath, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	marker := "# marker: do-not-overwrite\n"
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if err := os.WriteFile(configPath, append(original, []byte(marker)...), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	second := run()
	if !strings.Contains(second, "already present") {
		t.Errorf("expected second run to report already-present items, got:\n%s", second)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after second init: %v", err)
	}
	if !strings.Contains(string(after), marker) {
		t.Error("second init overwrote the config file; marker was lost")
	}
}
