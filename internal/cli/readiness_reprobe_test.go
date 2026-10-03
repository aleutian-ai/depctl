package cli

import (
	"errors"
	"testing"
)

// TestVectorReadinessRecoversWithoutRestart is OPS-008's live finding
// (VEC-016): the backend was down when the daemon started, then came
// back. checkReady must notice on the next call, not require a restart.
func TestVectorReadinessRecoversWithoutRestart(t *testing.T) {
	r := newVectorReadiness()
	r.set(vectorStateUnreachable, "qdrant not reachable")
	backendUp := false
	r.reprobe = func() error {
		if !backendUp {
			return errors.New("still down")
		}
		return nil
	}

	if err := r.checkReady(); err == nil {
		t.Fatal("checkReady while still down = nil, want an unreachable error")
	}

	backendUp = true
	r.lastReprobe = r.lastReprobe.Add(-vectorReprobeCooldown) // past the cooldown
	if err := r.checkReady(); err != nil {
		t.Fatalf("checkReady after the backend came back = %v, want nil", err)
	}
	if state, _ := r.get(); state != vectorStateReady {
		t.Errorf("state after recovery = %s, want %s", state, vectorStateReady)
	}
}

func TestVectorReadinessReprobeRespectsCooldown(t *testing.T) {
	r := newVectorReadiness()
	r.set(vectorStateUnreachable, "qdrant not reachable")
	probes := 0
	r.reprobe = func() error { probes++; return errors.New("still down") }

	for i := 0; i < 5; i++ {
		_ = r.checkReady()
	}
	if probes != 1 {
		t.Errorf("probes for 5 back-to-back calls = %d, want 1 (cooldown)", probes)
	}
}

func TestVectorReadinessNoReprobeWhenReady(t *testing.T) {
	r := newVectorReadiness()
	r.set(vectorStateReady, "")
	r.reprobe = func() error { t.Fatal("reprobe called while ready"); return nil }
	if err := r.checkReady(); err != nil {
		t.Fatalf("checkReady = %v", err)
	}
}

func TestEmbeddingReadinessRecoversWithoutRestart(t *testing.T) {
	r := newEmbeddingReadiness()
	r.set(embeddingStateUnreachable, "Ollama not reachable")
	up := false
	r.reprobe = func() error {
		if !up {
			return errors.New("still down")
		}
		return nil
	}

	if err := r.checkReady(); err == nil {
		t.Fatal("checkReady while still down = nil, want an unreachable error")
	}
	up = true
	r.lastReprobe = r.lastReprobe.Add(-embeddingReprobeCooldown)
	if err := r.checkReady(); err != nil {
		t.Fatalf("checkReady after Ollama came back = %v, want nil", err)
	}
}

func TestEmbeddingReadinessReprobeRespectsCooldown(t *testing.T) {
	r := newEmbeddingReadiness()
	r.set(embeddingStateUnreachable, "Ollama not reachable")
	probes := 0
	r.reprobe = func() error { probes++; return errors.New("still down") }

	for i := 0; i < 5; i++ {
		_ = r.checkReady()
	}
	if probes != 1 {
		t.Errorf("probes for 5 back-to-back calls = %d, want 1 (cooldown)", probes)
	}
}
