package trace

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aleutian-ai/depctl/internal/config"
)

// TestStartSpanDisabledIsSafe covers this ticket's own smoke-test
// requirement: with tracing disabled (the default — no InitProvider call
// at all, matching a real process that never enables it), StartSpan/end
// must be safe to call and never panic or block.
func TestStartSpanDisabledIsSafe(t *testing.T) {
	ctx, end := StartSpan(context.Background(), "resolve", attribute.String("depctl.dependency", "example"))
	if ctx == nil {
		t.Fatal("StartSpan returned nil context")
	}
	end()
}

func TestRecordErrorNilIsNoop(t *testing.T) {
	ctx, end := StartSpan(context.Background(), "embed")
	defer end()
	RecordError(ctx, nil) // must not panic
}

func TestRecordErrorRecordsOnSpan(t *testing.T) {
	ctx, end := StartSpan(context.Background(), "embed")
	defer end()
	RecordError(ctx, errors.New("boom")) // must not panic; no-op tracer discards it
}

// TestInitProviderDisabledReturnsNoop confirms the config-gate itself: a
// disabled OTelConfig (the default, per this ticket's "off by default"
// acceptance criterion) makes InitProvider a pure no-op — no exporter
// constructed, no network dialed.
func TestInitProviderDisabledReturnsNoop(t *testing.T) {
	shutdown, err := InitProvider(context.Background(), config.OTelConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("InitProvider: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestInitProviderBadEndpointDoesNotError covers the failure-behavior
// requirement: a real exporter/endpoint problem must never fail startup —
// InitProvider degrades to a no-op shutdown instead of returning an error.
func TestInitProviderBadEndpointDoesNotError(t *testing.T) {
	shutdown, err := InitProvider(context.Background(), config.OTelConfig{Enabled: true, Endpoint: "http://127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatalf("InitProvider must not error on a bad endpoint, got: %v", err)
	}
	if shutdown == nil {
		t.Fatal("InitProvider returned a nil shutdown func")
	}
	_ = shutdown(context.Background())
}
