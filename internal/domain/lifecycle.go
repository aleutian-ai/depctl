package domain

// GenerationState is a Generation's position in its build/promotion/GC
// lifecycle.
type GenerationState string

const (
	GenDiscovered  GenerationState = "DISCOVERED"
	GenPlanned     GenerationState = "PLANNED"
	GenAcquiring   GenerationState = "ACQUIRING"
	GenNormalizing GenerationState = "NORMALIZING"
	GenIndexing    GenerationState = "INDEXING"
	GenValidating  GenerationState = "VALIDATING"
	GenReady       GenerationState = "READY"
	GenActive      GenerationState = "ACTIVE"
	GenFailed      GenerationState = "FAILED"
	GenSuperseded  GenerationState = "SUPERSEDED"
	GenGCEligible  GenerationState = "GC_ELIGIBLE"
	GenDeleted     GenerationState = "DELETED"
)

// generationTransitions is the linear build/promotion/GC pipeline
// (DISCOVERED -> ... -> ACTIVE -> SUPERSEDED -> GC_ELIGIBLE -> DELETED),
// with FAILED reachable from any in-progress state.
var generationTransitions = map[GenerationState][]GenerationState{
	GenDiscovered:  {GenPlanned},
	GenPlanned:     {GenAcquiring, GenFailed},
	GenAcquiring:   {GenNormalizing, GenFailed},
	GenNormalizing: {GenIndexing, GenFailed},
	GenIndexing:    {GenValidating, GenFailed},
	GenValidating:  {GenReady, GenFailed},
	GenReady:       {GenActive},
	GenActive:      {GenSuperseded},
	GenSuperseded:  {GenGCEligible},
	GenGCEligible:  {GenDeleted},
	GenFailed:      {},
	GenDeleted:     {},
}

// ValidGenerationTransition reports whether a Generation may move directly
// from state from to state to.
func ValidGenerationTransition(from, to GenerationState) bool {
	for _, allowed := range generationTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// JobState is a background job's execution state.
type JobState string

const (
	JobPending   JobState = "PENDING"
	JobRunning   JobState = "RUNNING"
	JobRetry     JobState = "RETRY"
	JobSucceeded JobState = "SUCCEEDED"
	JobFailed    JobState = "FAILED"
	JobCancelled JobState = "CANCELLED"
)

// jobTransitions is the job execution/retry state machine.
var jobTransitions = map[JobState][]JobState{
	JobPending:   {JobRunning, JobCancelled},
	JobRunning:   {JobSucceeded, JobFailed, JobRetry, JobCancelled},
	JobRetry:     {JobRunning, JobCancelled},
	JobSucceeded: {},
	JobFailed:    {},
	JobCancelled: {},
}

// ValidJobTransition reports whether a Job may move directly from state
// from to state to.
func ValidJobTransition(from, to JobState) bool {
	for _, allowed := range jobTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}
