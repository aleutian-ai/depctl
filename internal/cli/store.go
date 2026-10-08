package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aleutian-ai/depctl/internal/config"
	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
	badgerstore "github.com/aleutian-ai/depctl/internal/data/badger"
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
	path := filepath.Join(dataDir, "depctld.sock")
	if len(path) <= maxSocketPath {
		return path, nil
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("depctld-%x.sock", sum[:6])), nil
}

// daemonLogPath returns the log file an auto-started daemon writes to,
// beside control.db.
func daemonLogPath() (string, error) {
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "depctld.log"), nil
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

// daemonStartupLockTimeout bounds how long `depctl daemon run` itself
// waits for control.db's file lock, far shorter than openControlStore's
// default: a losing candidate in a concurrent auto-start race must find
// out and exit almost immediately, not park inside the lock wait long
// enough to inherit it later if the winner is asked to shut down in the
// meantime (see spawnDaemonOnce).
const daemonStartupLockTimeout = 200 * time.Millisecond

// openControlStoreForDaemonRun is openControlStore with a fail-fast lock
// wait, for `depctl daemon run`'s own attempt to become the owner.
func openControlStoreForDaemonRun() (*bboltstore.Store, error) {
	path, err := controlDBPath()
	if err != nil {
		return nil, err
	}
	return bboltstore.OpenWithTimeout(path, daemonStartupLockTimeout)
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
