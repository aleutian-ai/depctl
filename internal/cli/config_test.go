package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/config"
)

func TestConfigValidateMissingFile(t *testing.T) {
	isolateEnv(t)

	root := NewRootCmd()
	root.SetArgs([]string{"config", "validate"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for a missing config file")
	}
	if !strings.Contains(err.Error(), "run `depctl init`") {
		t.Errorf("expected error to suggest `depctl init`, got: %v", err)
	}
}

func TestConfigValidateAfterInit(t *testing.T) {
	isolateEnv(t)

	root := NewRootCmd()
	root.SetArgs([]string{"init"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	root = NewRootCmd()
	root.SetArgs([]string{"config", "validate"})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("config validate failed: %v", err)
	}
	if !strings.Contains(out.String(), "config OK") {
		t.Errorf("expected \"config OK\" in output, got: %s", out.String())
	}
}

func TestConfigValidateBadConfig(t *testing.T) {
	isolateEnv(t)

	root := NewRootCmd()
	root.SetArgs([]string{"init"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	configPath, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Storage.Control.Path = ""
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	root = NewRootCmd()
	root.SetArgs([]string{"config", "validate"})
	err = root.Execute()
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if !strings.Contains(err.Error(), "storage.control.path") {
		t.Errorf("expected error to name the offending field, got: %v", err)
	}
}

func TestConfigValidateExplicitPathFlag(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	configPath := dir + "/custom-config.yaml"
	if err := config.Default("/data").Save(configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	root := NewRootCmd()
	root.SetArgs([]string{"config", "validate", "--config", configPath})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("config validate failed: %v", err)
	}
	if !strings.Contains(out.String(), configPath) {
		t.Errorf("expected output to reference %s, got: %s", configPath, out.String())
	}
}
