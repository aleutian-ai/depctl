package backendtest

import (
	"testing"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/conformance"
)

// TestFakeConformance holds the in-memory fake to the same contract as
// the real adapters, so tests built on it can't drift from real behavior.
func TestFakeConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) backend.VectorBackend { return New() })
}
