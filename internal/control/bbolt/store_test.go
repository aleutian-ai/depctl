package bbolt

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenFailsFastWhenLocked(t *testing.T) {
	orig := lockTimeout
	lockTimeout = 100 * time.Millisecond
	t.Cleanup(func() { lockTimeout = orig })

	path := filepath.Join(t.TempDir(), "control.db")
	holder, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })

	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open = %v, want ErrLocked", err)
	}
}
