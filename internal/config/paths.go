package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// configRoot returns ~/Library/Application Support/depctl on macOS,
// $XDG_CONFIG_HOME/depctl (or ~/.config/depctl) on Linux, via
// os.UserConfigDir(). Note os.UserConfigDir() only honors $XDG_CONFIG_HOME
// on Linux — macOS always uses ~/Library/Application Support regardless of
// that env var.
func configRoot() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "depctl"), nil
}

// DefaultConfigPath returns the platform-appropriate path for config.yaml.
func DefaultConfigPath() (string, error) {
	dir, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// DefaultDataDir returns where control.db, badger/, git/, and registry/
// live.
//
// On macOS there is no separate data-directory convention worth honoring
// for a single-user CLI tool, so data is co-located with config under one
// ~/Library/Application Support/depctl root — everything depctl owns lives
// in one place.
//
// On Linux (and other Unix), config and data are kept separate per the XDG
// Base Directory spec: $XDG_DATA_HOME/depctl, falling back to
// ~/.local/share/depctl.
func DefaultDataDir() (string, error) {
	if runtime.GOOS == "darwin" {
		return configRoot()
	}

	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "depctl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "depctl"), nil
}
