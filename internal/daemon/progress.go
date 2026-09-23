package daemon

import (
	"sort"
	"sync"

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
}

// SetTotal records how many actions the run planned.
func (p *SyncProgress) SetTotal(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = n
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
// if it was there.
func (p *SyncProgress) Finish(name string, failed bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inFlight, name)
	p.done++
	if failed {
		p.failed++
	}
}

// Snapshot returns a consistent copy of the counters, with in-flight
// dependencies sorted by name.
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
	return snap
}
