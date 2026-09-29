package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/domain"
)

// fakeQdrantCount returns an httptest server whose /points/count always
// reports count, so checkForeignCollectionData's own behavior can be
// tested without a real Qdrant instance.
func fakeQdrantCount(t *testing.T, count int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/count") {
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": count}, "status": "ok"})
			return
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// logCapture collects every logf call as a rendered string, for
// assertion.
func logCapture() (func(format string, args ...any), *[]string) {
	var mu sync.Mutex
	var lines []string
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}, &lines
}

// TestCheckForeignCollectionDataWarnsOnFreshInstanceWithExistingData is
// SAFE-001's (epic 61) own regression proof: a fresh control.db (never
// registered anything) pointed at a non-empty collection must produce a
// loud warning — the exact live incident this session hit, twice.
func TestCheckForeignCollectionDataWarnsOnFreshInstanceWithExistingData(t *testing.T) {
	store, _, _, _ := statusTestStores(t)
	cfg := config.Default(t.TempDir())
	cfg.Vector.Endpoint = fakeQdrantCount(t, 732078) // matches this session's own real incident scale
	logf, lines := logCapture()

	checkForeignCollectionData(context.Background(), cfg, store, logf)

	if len(*lines) != 1 || !strings.Contains((*lines)[0], "WARNING") || !strings.Contains((*lines)[0], "732078") {
		t.Errorf("logf calls = %+v, want exactly one WARNING mentioning the real point count", *lines)
	}
}

// TestCheckForeignCollectionDataSilentWhenAlreadyRegistered confirms the
// check never fires for a normal, already-in-use instance, however much
// data its own collection legitimately holds.
func TestCheckForeignCollectionDataSilentWhenAlreadyRegistered(t *testing.T) {
	store, badgerStore, _, _ := statusTestStores(t)
	seedActiveGeneration(t, store, badgerStore, domain.EcosystemGo, "example.com/foo", "v1.0.0", 5)
	cfg := config.Default(t.TempDir())
	cfg.Vector.Endpoint = fakeQdrantCount(t, 1_000_000)
	logf, lines := logCapture()

	checkForeignCollectionData(context.Background(), cfg, store, logf)

	if len(*lines) != 0 {
		t.Errorf("logf calls = %+v, want none — this instance has already registered an active generation of its own", *lines)
	}
}

// TestCheckForeignCollectionDataSilentWhenCollectionEmpty confirms a
// genuinely fresh collection (the normal, non-colliding case) never
// warns.
func TestCheckForeignCollectionDataSilentWhenCollectionEmpty(t *testing.T) {
	store, _, _, _ := statusTestStores(t)
	cfg := config.Default(t.TempDir())
	cfg.Vector.Endpoint = fakeQdrantCount(t, 0)
	logf, lines := logCapture()

	checkForeignCollectionData(context.Background(), cfg, store, logf)

	if len(*lines) != 0 {
		t.Errorf("logf calls = %+v, want none — the collection is genuinely empty", *lines)
	}
}
