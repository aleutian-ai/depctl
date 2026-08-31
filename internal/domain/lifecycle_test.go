package domain

import "testing"

func TestValidGenerationTransition(t *testing.T) {
	valid := []struct{ from, to GenerationState }{
		{GenDiscovered, GenPlanned},
		{GenPlanned, GenAcquiring},
		{GenAcquiring, GenNormalizing},
		{GenNormalizing, GenIndexing},
		{GenIndexing, GenValidating},
		{GenValidating, GenReady},
		{GenReady, GenActive},
		{GenActive, GenSuperseded},
		{GenSuperseded, GenGCEligible},
		{GenGCEligible, GenDeleted},
		{GenPlanned, GenFailed},
		{GenAcquiring, GenFailed},
		{GenNormalizing, GenFailed},
		{GenIndexing, GenFailed},
		{GenValidating, GenFailed},
	}
	for _, tc := range valid {
		if !ValidGenerationTransition(tc.from, tc.to) {
			t.Errorf("ValidGenerationTransition(%s, %s) = false, want true", tc.from, tc.to)
		}
	}
}

func TestInvalidGenerationTransition(t *testing.T) {
	invalid := []struct{ from, to GenerationState }{
		{GenActive, GenDiscovered},
		{GenDiscovered, GenActive},
		{GenReady, GenFailed},
		{GenFailed, GenReady},
		{GenDeleted, GenActive},
		{GenSuperseded, GenActive},
	}
	for _, tc := range invalid {
		if ValidGenerationTransition(tc.from, tc.to) {
			t.Errorf("ValidGenerationTransition(%s, %s) = true, want false", tc.from, tc.to)
		}
	}
}

func TestValidJobTransition(t *testing.T) {
	valid := []struct{ from, to JobState }{
		{JobPending, JobRunning},
		{JobPending, JobCancelled},
		{JobRunning, JobSucceeded},
		{JobRunning, JobFailed},
		{JobRunning, JobRetry},
		{JobRetry, JobRunning},
	}
	for _, tc := range valid {
		if !ValidJobTransition(tc.from, tc.to) {
			t.Errorf("ValidJobTransition(%s, %s) = false, want true", tc.from, tc.to)
		}
	}
}

func TestInvalidJobTransition(t *testing.T) {
	invalid := []struct{ from, to JobState }{
		{JobSucceeded, JobPending},
		{JobFailed, JobRunning},
		{JobCancelled, JobRunning},
		{JobPending, JobSucceeded},
	}
	for _, tc := range invalid {
		if ValidJobTransition(tc.from, tc.to) {
			t.Errorf("ValidJobTransition(%s, %s) = true, want false", tc.from, tc.to)
		}
	}
}
