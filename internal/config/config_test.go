package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	want := Default("/data")
	want.Embedding.Model = "custom-model"

	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Not got != want: Daemon.Autostart is a *bool (nil means "unset"),
	// so equal round-tripped values still land in distinct allocations.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got:  %+v\n want: %+v", got, want)
	}
}

func TestFingerprintDetectsARealChangeNotARoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := Default(dir)

	f1, err := original.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}

	// A save-then-load round trip must not register as a change: the
	// whole point is comparing what the daemon actually loaded against
	// a fresh load, not raw file bytes.
	path := filepath.Join(dir, "config.yaml")
	if err := original.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f2, err := reloaded.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint (reloaded): %v", err)
	}
	if f1 != f2 {
		t.Errorf("fingerprint changed across a save/load round trip: %s vs %s", f1, f2)
	}

	// An actual semantic change must register.
	changed := reloaded
	changed.Embedding.Model = "a-different-model"
	f3, err := changed.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint (changed): %v", err)
	}
	if f3 == f2 {
		t.Error("fingerprint did not change after a real config edit")
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "does-not-exist.yaml"))
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound, got %v", err)
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "not: [valid: yaml")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for malformed YAML, got nil")
	}
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{
			name:    "empty control path",
			mutate:  func(c *Config) { c.Storage.Control.Path = "" },
			wantErr: "storage.control.path",
		},
		{
			name:    "empty data path",
			mutate:  func(c *Config) { c.Storage.Data.Path = "" },
			wantErr: "storage.data.path",
		},
		{
			name:    "negative grace period",
			mutate:  func(c *Config) { c.Retention.GracePeriod = -time.Hour },
			wantErr: "retention.grace_period",
		},
		{
			name:    "negative orphan age",
			mutate:  func(c *Config) { c.Retention.OrphanAge = -time.Hour },
			wantErr: "retention.orphan_age",
		},
		{
			name:    "negative debounce",
			mutate:  func(c *Config) { c.Watch.Debounce = -time.Second },
			wantErr: "watch.debounce",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default("/data")
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("expected a validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	c := Default("/data")
	if c.Retention.GracePeriod != 336*time.Hour {
		t.Errorf("grace_period = %v, want 336h", c.Retention.GracePeriod)
	}
	if c.Retention.OrphanAge != 24*time.Hour {
		t.Errorf("orphan_age = %v, want 24h", c.Retention.OrphanAge)
	}
	if !c.Watch.Enabled {
		t.Error("watch.enabled should default to true")
	}
	if c.Server.HTTP.Listen != "127.0.0.1:7447" {
		t.Errorf("server.http.listen = %q, want 127.0.0.1:7447", c.Server.HTTP.Listen)
	}
	if strings.Contains(c.Server.HTTP.Listen, "0.0.0.0") {
		t.Error("default server address must not bind 0.0.0.0")
	}
}

func TestDefaultVectorManagedIsTrueForFreshInstalls(t *testing.T) {
	c := Default("/data")
	if !c.Vector.Managed {
		t.Error("vector.managed should default to true for a fresh install (WATCH-016)")
	}
}

func TestConfigWithNoManagedKeyLoadsAsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// A config file written before WATCH-016 existed — no "managed" key
	// under vector at all.
	body := "version: 1\n" +
		"storage:\n  control:\n    type: bbolt\n    path: " + dir + "/control.db\n  data:\n    type: badger\n    path: " + dir + "/badger\n" +
		"embedding:\n  provider: ollama\n  model: nomic-embed-text-v2-moe\n  endpoint: http://127.0.0.1:11434\n" +
		"vector:\n  backend: qdrant\n  endpoint: http://127.0.0.1:6333\n  collection: ragctl\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Vector.Managed {
		t.Error("a config predating vector.managed must load as false — no surprise second Qdrant instance for existing users")
	}
}

func TestSecretsNeverSerialized(t *testing.T) {
	c := Default("/data")
	c.Vector.APIKeyEnv = "QDRANT_API_KEY"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data := readFile(t, path)
	if strings.Contains(data, "api_key:") {
		t.Error("resolved secret value must never be serialized, only the env var reference")
	}
	if !strings.Contains(data, "api_key_env: QDRANT_API_KEY") {
		t.Error("expected api_key_env reference to be serialized")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestAutostartDefaultsOnWhenKeyAbsent(t *testing.T) {
	// A config written before the daemon existed has no daemon key at
	// all, and Load never fills in defaults — auto-start must still be on.
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `version: 1
storage:
  control:
    type: bbolt
    path: /tmp/ragctl/control.db
  data:
    type: badger
    path: /tmp/ragctl/badger
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Daemon.AutostartEnabled() {
		t.Error("AutostartEnabled() = false for a config with no daemon key, want true")
	}

	off := false
	cfg.Daemon.Autostart = &off
	if cfg.Daemon.AutostartEnabled() {
		t.Error("AutostartEnabled() = true with autostart: false")
	}
}
