package daemon

import (
	"sort"
	"sync"
	"time"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// SyncProgress holds the live counters of one sync run (SCOPE-001):
// planned actions done and failed of a known total, plus how far along
// each dependency currently being built is. Nil-receiver-safe throughout,
// like SyncPriority, so a caller that never sets one pays nothing.
type SyncProgress struct {
	mu       sync.Mutex
	total    int
	done     int
	failed   int
	inFlight map[string]*api.InFlightDependency

	// BATCH-001 Option D: agent-facing scope-planning data, additive to
	// the counters above. started/durations exist purely to compute
	// ObservedTiming/SyncEstimate in Snapshot — no caller reads them
	// directly.
	started   map[string]time.Time // per-dependency wall-clock start, set by Begin
	durations []time.Duration      // completed SYNC_VERSION actions' real elapsed time, in completion order
	pending   map[string]struct{}  // planned dependency names not yet Finish'd
}

// minSamplesForConfidentEstimate is the sample-count floor below which
// SyncEstimate is always labeled "low" confidence, regardless of how
// tight the observed spread looks — live timing data (STRESS-005/006)
// is heavily skewed (p90 roughly 4x the median), so a handful of early
// completions routinely aren't representative of what's left. Still an
// arbitrary cutoff, deliberately chosen larger than "the first 3-4" to
// be meaningfully less misleading, not because 10 itself is principled.
const minSamplesForConfidentEstimate = 10

// SetTotal records how many actions the run planned.
func (p *SyncProgress) SetTotal(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = n
}

// SetPlanned records the dependency names this run's SYNC_VERSION
// actions cover — BATCH-001 Option D's "what's left" list, distinct
// from Total's plain count. Called once, alongside SetTotal, before any
// action starts.
func (p *SyncProgress) SetPlanned(names []string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = make(map[string]struct{}, len(names))
	for _, n := range names {
		p.pending[n] = struct{}{}
	}
}

// Begin marks name as being built.
func (p *SyncProgress) Begin(name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight == nil {
		p.inFlight = map[string]*api.InFlightDependency{}
	}
	p.inFlight[name] = &api.InFlightDependency{Name: name}
	if p.started == nil {
		p.started = map[string]time.Time{}
	}
	p.started[name] = time.Now()
}

// SetChunks records how many of name's chunks are embedded so far.
func (p *SyncProgress) SetChunks(name string, done, total int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if d, ok := p.inFlight[name]; ok {
		d.ChunksDone, d.ChunksTotal = done, total
	}
}

// Finish counts one action complete, removing name from those in flight
// (and from Pending) if it was there, and records its real elapsed time
// if Begin was called for it (only true SYNC_VERSION actions are — see
// runSyncAction's own scoping, MCP-004).
func (p *SyncProgress) Finish(name string, failed bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inFlight, name)
	delete(p.pending, name)
	if start, ok := p.started[name]; ok {
		p.durations = append(p.durations, time.Since(start))
		delete(p.started, name)
	}
	p.done++
	if failed {
		p.failed++
	}
}

// Snapshot returns a consistent copy of the counters, with in-flight
// dependencies sorted by name, plus BATCH-001 Option D's observed-timing
// estimate and pending list once there's at least one real sample.
func (p *SyncProgress) Snapshot() api.SyncProgress {
	if p == nil {
		return api.SyncProgress{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := api.SyncProgress{Done: p.done, Failed: p.failed, Total: p.total}
	for _, d := range p.inFlight {
		snap.InFlight = append(snap.InFlight, *d)
	}
	sort.Slice(snap.InFlight, func(i, j int) bool { return snap.InFlight[i].Name < snap.InFlight[j].Name })

	for name := range p.pending {
		snap.Pending = append(snap.Pending, name)
	}
	sort.Strings(snap.Pending)

	if n := len(p.durations); n > 0 {
		sorted := append([]time.Duration(nil), p.durations...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		median := sorted[n/2]
		p90i := int(float64(n) * 0.9)
		if p90i >= n {
			p90i = n - 1
		}
		p90 := sorted[p90i]

		snap.Observed = &api.ObservedTiming{
			Samples:                 n,
			MedianDependencySeconds: median.Seconds(),
			P90DependencySeconds:    p90.Seconds(),
		}
		snap.Estimate = &api.SyncEstimate{
			RemainingSeconds: median.Seconds() * float64(len(p.pending)),
			Confidence:       estimateConfidence(n, median, p90),
		}
	}
	return snap
}

// estimateConfidence labels a SyncEstimate from sample count and
// observed skew (p90/median) — never a substitute for the raw numbers
// themselves, which are always reported alongside it.
func estimateConfidence(samples int, median, p90 time.Duration) string {
	skew := 1.0
	if median > 0 {
		skew = p90.Seconds() / median.Seconds()
	}
	switch {
	case samples < minSamplesForConfidentEstimate:
		return "low"
	case samples < 25 || skew > 3:
		return "medium"
	default:
		return "high"
	}
}
