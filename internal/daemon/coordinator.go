package daemon

import (
	"aleutian-ai/ragctl/internal/domain"
	"fmt"
	"golang.org/x/sync/singleflight"
	"sync"
	"time"
)

// BuildCoordinator gates concurrent generation builds againt gc and against
// duplicate builds of the identical generation. You can end up with a bunch
// of RLocks because of different generations building at once. GC excludes
// all of them while it has a lock/runs. Two callers building the identical
// dependency+version join one real build via singleflight.
type BuildCoordinator struct {
	gate  sync.RWMutex
	group singleflight.Group

	// onTiming, if set, is called after every Build/ProtectFromGC call
	// completes (success or failure) with its GateWait/Work breakdown —
	// distinguishing "was blocked waiting for GC to release the gate"
	// from "did real, unavoidable work" (epic 53/COORD-002). Nil-safe:
	// every call is guarded, so this costs nothing when unset.
	onTiming func(key string, t BuildTiming)
}

// BuildTiming is one Build/ProtectFromGC call's duration breakdown.
type BuildTiming struct {
	// GateWait is how long the call blocked on RLock() before it could
	// even start — non-zero specifically means GC held the gate.
	GateWait time.Duration
	// Work is how long fn itself ran once the gate was acquired. For a
	// Build call that joined an in-flight singleflight call rather than
	// originating it, this is the wait for someone else's real work,
	// not idle queueing — a legitimately different kind of "wait" than
	// GateWait, not merged into it.
	Work time.Duration
}

// NewBuildCoordinator returns a BuildCoordinator.
func NewBuildCoordinator() *BuildCoordinator {
	return &BuildCoordinator{}
}

// SetTimingHook installs fn to observe every future Build/ProtectFromGC
// call's timing breakdown — for tests and diagnostics; nil disables it
// (the default). key is buildKey's own rendering for Build calls, or
// "" for a plain ProtectFromGC call (no build identity to key on).
func (c *BuildCoordinator) SetTimingHook(fn func(key string, t BuildTiming)) {
	c.onTiming = fn
}

// buildKey renders the dependency into a singleflight key = ecosystem, name,
// version. That matches generation.Create's own identity scope.
func buildKey(dep domain.DependencyVersion) string {
	s := fmt.Sprintf("%s/%s@%s",
		string(dep.Dependency.Ecosystem), dep.Dependency.Name, dep.Version)
	return s
}

// Build runs fn under the shared build/GC gate's read side. This coalesces
// concurrent callers for the identical dep into one reacl call to fn. That
// means every caller waiting on the same in-flight build gets its result.
// fn must already be closed over a context independent of any single caller's
// own cancellation. Build takes no ctx of its own specifically so that a
// timed-out caller can never cancel a build on which other callers rely.
func (c *BuildCoordinator) Build(dep domain.DependencyVersion, fn func() error) error {
	key := buildKey(dep)
	return c.run(key, func() error {
		return c.doOnce(key, fn)
	})
}

// ProtectFromGC runs fn under the shared gate's read side, with no
// build-identity deduplication — for state mutations GC must never race
// against but that have no "same build" concept to coalesce, unlike
// Build. retention.PlanGC reads the references bucket directly
// (store.ListReferences/ListAllReferences), so any write to it
// (addReference, retention.DropReference) needs this same exclusion,
// even though it isn't itself a generation build.
func (c *BuildCoordinator) ProtectFromGC(fn func() error) error {
	return c.run("", fn)
}

// run is Build/ProtectFromGC's shared gate-acquisition and timing logic.
// GateWait measures purely the RLock() wait (non-zero specifically
// means GC held the gate); Work measures fn's own run — for a Build
// call that joins an in-flight singleflight call rather than
// originating it, that's the wait for someone else's real work.
func (c *BuildCoordinator) run(key string, fn func() error) error {
	waitStart := time.Now()
	c.gate.RLock()
	gateWait := time.Since(waitStart)
	defer c.gate.RUnlock()

	workStart := time.Now()
	err := fn()
	work := time.Since(workStart)

	if c.onTiming != nil {
		c.onTiming(key, BuildTiming{GateWait: gateWait, Work: work})
	}
	return err
}

// doOnce adapts fn's func() error shape to a singleflight.Group Do's generic
// func() (any, error) signature. This discards the unused value return.
func (c *BuildCoordinator) doOnce(key string, fn func() error) error {
	_, err, _ := c.group.Do(key, func() (any, error) {
		return nil, fn()
	})
	return err
}

// ExcludeForGC blocks until all of the in-flight Build calls finish, then
// it holds the gate's write side until release is explicitly called.
func (c *BuildCoordinator) ExcludeForGC() (release func()) {
	c.gate.Lock()
	return c.gate.Unlock
}
