//go:build unix

package cli

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
)

func TestDaemonExitsCleanlyOnSIGTERM(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	h := startDaemon(t)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	h.wait(t)

	if !strings.Contains(h.out.String(), "stopped") {
		t.Errorf("output = %q, want a final 'stopped' line", h.out.String())
	}
	if _, err := os.Stat(h.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still present after SIGTERM: %v", err)
	}

	controlPath, err := controlDBPath()
	if err != nil {
		t.Fatalf("controlDBPath: %v", err)
	}
	store, err := bboltstore.Open(controlPath)
	if err != nil {
		t.Fatalf("control.db still locked after SIGTERM: %v", err)
	}
	store.Close()
}
