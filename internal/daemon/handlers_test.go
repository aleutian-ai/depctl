package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// fakeEngine embeds Engine (nil) so a test only has to implement the one
// method it actually exercises — calling any other panics, which is
// exactly what should happen if a test reaches further than it meant to.
type fakeEngine struct {
	Engine
	scan func(ctx context.Context, root string, out io.Writer, lockProject func(string) func()) ([]string, error)
}

func (f *fakeEngine) Scan(ctx context.Context, root string, out io.Writer, lockProject func(string) func()) ([]string, error) {
	return f.scan(ctx, root, out, lockProject)
}

func newTestServer(eng Engine) *Server {
	s := New(Options{Engine: eng, Socket: "unused-in-this-test"})
	s.scheduler = NewScheduler(context.Background(), eng.Sync, eng.GC, nil)
	return s
}

// TestHandleResolveBoundedByMaxActionDuration is the direct regression
// test for scan's missing ceiling: before handleResolve wrapped its call
// to Engine.Scan in maxActionDuration, a hung resolver had no bound at
// all — sync/GC already did (Scheduler.execute), scan didn't, since it
// doesn't go through the scheduler.
func TestHandleResolveBoundedByMaxActionDuration(t *testing.T) {
	original := maxActionDuration
	maxActionDuration = 50 * time.Millisecond
	defer func() { maxActionDuration = original }()

	eng := &fakeEngine{
		scan: func(ctx context.Context, root string, out io.Writer, lockProject func(string) func()) ([]string, error) {
			<-ctx.Done() // never returns on its own
			return nil, ctx.Err()
		},
	}
	s := newTestServer(eng)

	body, _ := json.Marshal(api.ResolveRequest{Root: "/tmp/whatever"})
	req := httptest.NewRequest(http.MethodPost, api.PathResolve, bytes.NewReader(body))
	w := httptest.NewRecorder()

	start := time.Now()
	s.handleResolve(w, req)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("handleResolve took %s against a hung Scan, want it bounded by maxActionDuration (50ms)", elapsed)
	}

	// Streamed responses (see stream.go) always answer 200 and carry the
	// outcome in the last NDJSON line, not the HTTP status — the real
	// assertion is that the last line is an error, not a result.
	dec := json.NewDecoder(w.Body)
	var last api.StreamLine
	for {
		var line api.StreamLine
		if err := dec.Decode(&line); err != nil {
			break
		}
		last = line
	}
	if last.Error == "" {
		t.Errorf("last stream line = %+v, want an Error for a Scan that never completed", last)
	}
}
