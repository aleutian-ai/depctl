package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// listenUnix starts a bare HTTP server on a fresh temp socket running
// handler, cleaned up when the test ends, and returns the socket path.
func listenUnix(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// Not t.TempDir(): its path is nested under the test name and can
	// overrun sun_path's ~104-byte limit on macOS for a long test name.
	dir, err := os.MkdirTemp("", "ragctl-client-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		os.Remove(socket)
	})
	return socket
}

// TestHealthTimesOutOnAStuckDaemon is the direct regression test for the
// gap this fixes: before defaultRequestTimeout existed, a daemon-side
// handler that never returned would hang the calling command forever,
// since c.http had no Timeout of its own (the same shape of bug already
// found and fixed in the qdrant client — see internal/backend/qdrant).
func TestHealthTimesOutOnAStuckDaemon(t *testing.T) {
	original := defaultRequestTimeout
	defaultRequestTimeout = 100 * time.Millisecond
	defer func() { defaultRequestTimeout = original }()

	blocked := make(chan struct{})
	defer close(blocked)
	socket := listenUnix(t, func(w http.ResponseWriter, r *http.Request) {
		<-blocked // never responds until the test cleans up
	})

	c := New(socket)
	start := time.Now()
	_, err := c.Health(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Health against a stuck handler succeeded, want a timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Health took %s to fail, want it bounded by defaultRequestTimeout (100ms)", elapsed)
	}
}

// TestSyncUsesLongerTimeoutThanDefault proves Sync isn't bound by the
// same short default a stuck handler would trip — a real sync can
// legitimately run for minutes.
func TestSyncUsesLongerTimeoutThanDefault(t *testing.T) {
	originalDefault := defaultRequestTimeout
	originalLong := longRunningRequestTimeout
	defaultRequestTimeout = 50 * time.Millisecond
	longRunningRequestTimeout = 2 * time.Second
	defer func() {
		defaultRequestTimeout = originalDefault
		longRunningRequestTimeout = originalLong
	}()

	// The handler takes longer than defaultRequestTimeout but finishes
	// comfortably inside longRunningRequestTimeout.
	socket := listenUnix(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"log":"done"}` + "\n" + `{"result":{"project_ids":[]}}` + "\n"))
	})

	c := New(socket)
	if _, err := c.Resolve(context.Background(), "/tmp/whatever", nil); err != nil {
		t.Errorf("Resolve = %v, want it to survive a 300ms handler despite the 50ms default timeout (it should use longRunningRequestTimeout)", err)
	}
}
