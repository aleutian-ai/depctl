package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeProgressReader struct {
	out SyncProgressOut
	err error
}

func (f fakeProgressReader) SyncProgress(_ context.Context, _ string) (SyncProgressOut, error) {
	return f.out, f.err
}

func TestSyncProgressHandlerNotesEachState(t *testing.T) {
	cases := []struct {
		name string
		out  SyncProgressOut
		want string
	}{
		{"running", SyncProgressOut{Syncing: true, Done: 412, Failed: 3, Total: 560}, "412 of 560 done (3 failed)"},
		{"finished", SyncProgressOut{Done: 557, Failed: 3, Total: 560}, "no sync is running; the last run finished 557 of 560"},
		{"never ran", SyncProgressOut{}, "no sync has run for this project yet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got, err := syncProgressHandler(fakeProgressReader{out: tc.out})(context.Background(), nil, SyncProgressIn{ProjectID: "proj_1"})
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if !strings.Contains(got.Note, tc.want) {
				t.Errorf("note = %q, want it to contain %q", got.Note, tc.want)
			}
			if got.Syncing != tc.out.Syncing || got.Done != tc.out.Done || got.Total != tc.out.Total {
				t.Errorf("counters changed in transit: got %+v, want %+v", got, tc.out)
			}
		})
	}
}

func TestSyncProgressHandlerWithoutReaderReportsNotConfigured(t *testing.T) {
	_, _, err := syncProgressHandler(nil)(context.Background(), nil, SyncProgressIn{ProjectID: "proj_1"})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("err = %v, want a not-configured error", err)
	}
}

// TestSyncProgressToolIsRegisteredAndCallableOverMCP proves the whole
// wiring — registration, schema, JSON shape — over a real in-memory MCP
// client/server pair, not just the handler in isolation.
func TestSyncProgressToolIsRegisteredAndCallableOverMCP(t *testing.T) {
	env := newTestEnv(t)
	reader := fakeProgressReader{out: SyncProgressOut{
		Syncing: true, Done: 2, Total: 9,
		InFlight: []InFlightDependencyOut{{Name: "example.com/big", ChunksDone: 40, ChunksTotal: 400}},
	}}
	server := New(Deps{Query: env.svc, Progress: reader})

	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	res, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "sync_progress", Arguments: map[string]any{"project_id": "proj_1"}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("error result: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got SyncProgressOut
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if !got.Syncing || got.Done != 2 || got.Total != 9 || len(got.InFlight) != 1 || got.InFlight[0].ChunksDone != 40 {
		t.Errorf("result = %+v, want the reader's progress passed through", got)
	}
}
