package config

import (
	"errors"
	"os"
	"path/filepath"
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

	if got != want {
		t.Errorf("round-trip mismatch:\n got:  %+v\n want: %+v", got, want)
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
