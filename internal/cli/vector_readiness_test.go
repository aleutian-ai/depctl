package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/config"
)

func TestVectorReadinessCheckReadyNilIsAlwaysReady(t *testing.T) {
	var r *vectorReadiness
	if err := r.checkReady(); err != nil {
		t.Errorf("nil readiness checkReady() = %v, want nil", err)
	}
}

func TestVectorReadinessCheckReadyPerState(t *testing.T) {
	cases := []struct {
		state       vectorState
		wantErr     bool
		wantContain string
	}{
		{vectorStateUnknown, false, ""},
		{vectorStateReady, false, ""},
		{vectorStateChecking, true, "checking"},
		{vectorStateUnreachable, true, "unreachable"},
		{vectorStateError, true, "not ready"},
	}
	for _, c := range cases {
		r := newVectorReadiness()
		r.set(c.state, "detail-text")
		err := r.checkReady()
		if c.wantErr && err == nil {
			t.Errorf("state %s: checkReady() = nil, want an error", c.state)
			continue
		}
		if !c.wantErr && err != nil {
			t.Errorf("state %s: checkReady() = %v, want nil", c.state, err)
			continue
		}
		if c.wantErr && !strings.Contains(err.Error(), c.wantContain) {
			t.Errorf("state %s: checkReady() = %q, want it to mention %q", c.state, err, c.wantContain)
		}
	}
}

func TestCheckVectorReadinessUnreachable(t *testing.T) {
	cfg := config.Default("/data")
	cfg.Vector.Endpoint = "http://127.0.0.1:1" // nothing listens here
	readiness := newVectorReadiness()

	checkVectorReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, detail := readiness.get()
	if state != vectorStateUnreachable {
		t.Errorf("state = %s, want unreachable", state)
	}
	if detail == "" {
		t.Error("detail is empty, want it to name the endpoint")
	}
}

func TestCheckVectorReadinessReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Default("/data")
	cfg.Vector.Endpoint = srv.URL
	readiness := newVectorReadiness()

	checkVectorReadiness(context.Background(), cfg, readiness, func(string, ...any) {})

	state, _ := readiness.get()
	if state != vectorStateReady {
		t.Errorf("state = %s, want ready", state)
	}
}
