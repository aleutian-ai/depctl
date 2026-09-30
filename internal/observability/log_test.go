package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerJSONHandlerProducesValidJSONWithStandardFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, true, slog.LevelInfo)

	logger.Info("sync completed",
		KeyProjectID, "proj_abc",
		KeyDependency, "github.com/example/foo",
		KeyVersion, "v1.2.3",
		KeyGeneration, "gen_xyz",
		KeyBackend, "qdrant",
		KeyDurationMS, 42,
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	for _, key := range []string{KeyProjectID, KeyDependency, KeyVersion, KeyGeneration, KeyBackend, KeyDurationMS} {
		if _, ok := record[key]; !ok {
			t.Errorf("JSON output missing field %q: %v", key, record)
		}
	}
	if record["msg"] != "sync completed" {
		t.Errorf(`record["msg"] = %v, want "sync completed"`, record["msg"])
	}
}

func TestNewLoggerTextHandlerIsHumanReadable(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, false, slog.LevelInfo)
	logger.Info("hello", KeyProjectID, "proj_abc")
	out := buf.String()
	if !strings.Contains(out, "hello") || !strings.Contains(out, KeyProjectID) {
		t.Errorf("text output = %q, want it to contain the message and field key", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("text output = %q, looks like JSON, want plain text", out)
	}
}

func TestNewLoggerLevelFilteringSuppressesDebugAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, true, slog.LevelInfo)
	logger.Debug("should not appear")
	logger.Info("should appear")
	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Errorf("debug log leaked through at info level: %s", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("info log missing: %s", out)
	}
}

func TestFromContextReturnsTheLoggerWithLoggerStored(t *testing.T) {
	var buf bytes.Buffer
	want := NewLogger(&buf, true, slog.LevelInfo)
	ctx := WithLogger(context.Background(), want)
	got := FromContext(ctx)
	if got != want {
		t.Error("FromContext did not return the logger WithLogger stored")
	}
}

func TestFromContextFallsBackToDefaultWithoutPanicking(t *testing.T) {
	got := FromContext(context.Background())
	if got == nil {
		t.Fatal("FromContext returned nil for a context with no logger — callers must always get a usable logger")
	}
	// Must not panic on a real call.
	got.Info("no logger was ever set, this should just use slog.Default()")
}
