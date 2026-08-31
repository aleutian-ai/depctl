package cli

import (
	"path/filepath"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
)

// controlDBPath and badgerDirPath centralize the on-disk layout under the
// platform data dir (see config.DefaultDataDir), so every command agrees
// on where control.db and badger/ live.

func controlDBPath() (string, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "control.db"), nil
}

func badgerDirPath() (string, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "badger"), nil
}

func userRegistryDirPath() (string, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "registry"), nil
}

// openControlStore opens the bbolt control-plane store at its default
// location. Callers are responsible for closing it.
func openControlStore() (*bboltstore.Store, error) {
	path, err := controlDBPath()
	if err != nil {
		return nil, err
	}
	return bboltstore.Open(path)
}
