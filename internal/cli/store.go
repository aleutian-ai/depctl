package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
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

// maxSocketPath is a conservative bound on a Unix socket path: sun_path
// holds 104 bytes on macOS and 108 on Linux, including the terminator.
const maxSocketPath = 100

// socketPath returns the daemon's socket, beside control.db so it's tied
// to the store it guards. A data dir deep enough to overrun sun_path
// (a test's temp HOME, typically) falls back to a short path derived
// from that dir, so client and daemon still agree on it.
func socketPath() (string, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "ragctld.sock")
	if len(path) <= maxSocketPath {
		return path, nil
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("ragctld-%x.sock", sum[:6])), nil
}

// daemonLogPath returns the log file an auto-started daemon writes to,
// beside control.db.
func daemonLogPath() (string, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "ragctld.log"), nil
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

// openDataStore opens the Badger data-plane store at its default
// location. Callers are responsible for closing it.
func openDataStore() (*badgerstore.Store, error) {
	path, err := badgerDirPath()
	if err != nil {
		return nil, err
	}
	return badgerstore.Open(path)
}
