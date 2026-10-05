package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon/client"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
)

// staleJobAge is how long a job may sit in RUNNING before doctor flags
// it. ragctl has no job leases — jobs run synchronously inside a single
// CLI invocation — so a RUNNING job this old means that process died
// mid-run.
const staleJobAge = time.Hour

// resolverExecutables maps an ecosystem to the executable its resolver
// shells out to. Only the Go resolver runs a package manager; the node and
// python resolvers parse lockfiles directly and need nothing on PATH.
var resolverExecutables = map[domain.Ecosystem]string{
	domain.EcosystemGo: "go",
}

// Severity is one check's outcome. Its numeric value is also doctor's
// exit code, so the worst result across all checks is the process exit
// status.
type Severity int

const (
	SeverityOK Severity = iota
	SeverityWarning
	SeverityUnhealthy
)

// CheckResult is one doctor check's outcome.
type CheckResult struct {
	Name     string
	Severity Severity
	Detail   string
}

// doctorEnv is everything the checks inspect, opened once up front. Open
// failures are kept, not returned: the check that owns a subsystem reports
// its error, and checks that depend on it report that they couldn't run.
type doctorEnv struct {
	cfg                config.Config
	cfgErr             error
	store              *bboltstore.Store
	storeErr           error
	badger             *badgerstore.Store
	badgerErr          error
	registry           *registry.Registry
	registryErr        error
	lookPath           func(file string) (string, error)
	now                time.Time
	embeddingReadiness *embeddingReadiness
	vectorReadiness    *vectorReadiness
}

// doctorChecks is the fixed, ordered check list.
var doctorChecks = []struct {
	name string
	run  func(context.Context, *doctorEnv) (Severity, string)
}{
	{"config", checkConfig},
	{"control DB (bbolt) open", checkControlDB},
	{"schema version", checkSchemaVersion},
	{"Badger open", checkBadger},
	{"stale jobs", checkStaleJobs},
	{"active generations", checkActiveGenerations},
	{"active generation manifests", checkActiveManifests},
	{"backend replicas", checkBackendReplicas},
	{"empty active generations", checkEmptyActiveGenerations},
	{"referenced but never built", checkReferencedButNeverBuilt},
	{"vector backend reachable", checkBackendReachable},
	{"embedding model compatibility", checkEmbeddingModel},
	{"registry validity", checkRegistry},
	{"embedding backend", checkEmbeddingBackend},
	{"vector backend", checkVectorBackend},
	{"git on PATH", checkGit},
	{"package managers on PATH", checkPackageManagers},
}

// String renders s as the label doctor prints.
func (s Severity) String() string {
	switch s {
	case SeverityOK:
		return "OK"
	case SeverityWarning:
		return "WARN"
	default:
		return "UNHEALTHY"
	}
}

func (e *doctorEnv) close() {
	if e.store != nil {
		e.store.Close()
	}
	if e.badger != nil {
		e.badger.Close()
	}
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run health diagnostics",
		Long: `Run a fixed list of health checks and print one line per check.

Exit codes: 0 all checks OK, 1 at least one warning, 2 at least one
unhealthy check.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd)
		},
	}
}

// runDoctor deliberately never goes through ensureDaemon's autostart —
// diagnosing a stopped or broken daemon is doctor's job, so running it
// must never have the side effect of starting one. It only dials: if a
// daemon answers, the store-dependent checks run there (more useful, not
// less — the daemon already holds the stores open); if not, doctor falls
// back to opening them itself, exactly as it always has, so a genuinely
// locked or corrupted store still gets a specific diagnosis instead of
// an opaque "no daemon."
func runDoctor(cmd *cobra.Command) error {
	ctx := cmd.Context()

	socket, err := socketPath()
	if err != nil {
		return err
	}
	var results []CheckResult
	if c, dialErr := client.Dial(ctx, socket); dialErr == nil {
		results, err = runDoctorViaDaemon(ctx, c)
		if err != nil {
			return err
		}
	} else {
		results = runDoctorDirect(ctx)
	}

	printDoctorReport(cmd.OutOrStdout(), results)
	if code := int(worstSeverity(results)); code != 0 {
		return ExitCodeError{Code: code}
	}
	return nil
}

// runDoctorDirect is the original, pre-daemon behavior: open the stores
// itself and run every check locally. The fallback when no daemon
// answers.
func runDoctorDirect(ctx context.Context) []CheckResult {
	env := &doctorEnv{lookPath: exec.LookPath, now: time.Now()}
	env.cfg, env.cfgErr = loadRagctlConfig()
	env.store, env.storeErr = openControlStore()
	env.badger, env.badgerErr = openDataStore()
	env.registry, env.registryErr = loadRegistryForCLI(ctx)
	defer env.close()
	return runChecks(ctx, env)
}

// runDoctorViaDaemon takes the store-dependent checks from a reachable
// daemon and runs the two PATH-only checks locally, against this
// process's own shell PATH — which is the whole reason they can never
// run inside the daemon on the caller's behalf.
func runDoctorViaDaemon(ctx context.Context, c *client.Client) ([]CheckResult, error) {
	resp, err := c.Doctor(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]CheckResult, len(resp.Checks))
	for i, cr := range resp.Checks {
		results[i] = CheckResult{Name: cr.Name, Severity: Severity(cr.Severity), Detail: cr.Detail}
	}

	pathEnv := &doctorEnv{lookPath: exec.LookPath}
	gitSev, gitDetail := checkGit(ctx, pathEnv)
	results = append(results, CheckResult{Name: "git on PATH", Severity: gitSev, Detail: gitDetail})

	// If the daemon couldn't even determine what's needed, it already
	// appended its own "package managers on PATH" failure entry above —
	// don't duplicate it.
	if !hasCheckNamed(results, "package managers on PATH") {
		needed := make(map[string]int, len(resp.NeededExecutables))
		for _, n := range resp.NeededExecutables {
			needed[n.Name] = n.Count
		}
		sev, detail := checkPackageManagersOnPath(exec.LookPath, needed)
		results = append(results, CheckResult{Name: "package managers on PATH", Severity: sev, Detail: detail})
	}

	cfgSev, cfgDetail := checkConfigFreshness(ctx, c)
	results = append(results, CheckResult{Name: "config matches running daemon", Severity: cfgSev, Detail: cfgDetail})

	verSev, verDetail := checkVersionFreshness(ctx, c)
	results = append(results, CheckResult{Name: "daemon build matches this command", Severity: verSev, Detail: verDetail})
	return results, nil
}

// checkVersionFreshness is doctor's client-side check that the running
// daemon (ADR-011: one long-running process, reused by every later
// command) was built from the same revision as the binary running this
// check — an upgrade (git pull + rebuild, a new release) leaves the old
// daemon running until explicitly stopped, silently missing any route
// or tool added since. Live-found: an MCP session hitting an
// unexplained 404 on a real tool call, with no obvious cause until this
// was traced back to daemon/binary drift. Same shape as
// checkConfigFreshness, not a *doctorEnv check — it needs the daemon's
// Health, not local state.
func checkVersionFreshness(ctx context.Context, c *client.Client) (Severity, string) {
	health, err := c.Health(ctx)
	if err != nil {
		return SeverityUnhealthy, err.Error()
	}
	if versionStaleWarning(health.PID, health.Version, ragctlVersion) != "" {
		return SeverityWarning, fmt.Sprintf("daemon build %s, this command's build %s; run `ragctl daemon stop` to pick it up", health.Version, ragctlVersion)
	}
	return SeverityOK, "matches"
}

// checkConfigFreshness is doctor's client-side check that config.yaml
// hasn't changed since the daemon it's talking to started — config is
// loaded once for the daemon's whole lifetime, so an edit silently has
// no effect until a restart. Not shaped like the other check functions
// (ctx, *doctorEnv): it needs the daemon's Health, not a doctorEnv.
func checkConfigFreshness(ctx context.Context, c *client.Client) (Severity, string) {
	health, err := c.Health(ctx)
	if err != nil {
		return SeverityUnhealthy, err.Error()
	}
	stale, err := configIsStale(health.ConfigFingerprint)
	if err != nil {
		return SeverityUnhealthy, err.Error()
	}
	if stale {
		return SeverityWarning, fmt.Sprintf("config.yaml has changed since the daemon (pid %d) started; run `ragctl daemon stop` to pick it up", health.PID)
	}
	return SeverityOK, "matches"
}

func hasCheckNamed(results []CheckResult, name string) bool {
	for _, r := range results {
		if r.Name == name {
			return true
		}
	}
	return false
}

func runChecks(ctx context.Context, env *doctorEnv) []CheckResult {
	results := make([]CheckResult, 0, len(doctorChecks))
	for _, c := range doctorChecks {
		sev, detail := c.run(ctx, env)
		results = append(results, CheckResult{Name: c.name, Severity: sev, Detail: detail})
	}
	return results
}

func worstSeverity(results []CheckResult) Severity {
	worst := SeverityOK
	for _, r := range results {
		worst = max(worst, r.Severity)
	}
	return worst
}

func printDoctorReport(out io.Writer, results []CheckResult) {
	var counts [3]int
	for _, r := range results {
		fmt.Fprintf(out, "%-10s %-30s %s\n", r.Severity, r.Name, r.Detail)
		counts[r.Severity]++
	}
	fmt.Fprintf(out, "\n%d ok, %d warning, %d unhealthy\n", counts[SeverityOK], counts[SeverityWarning], counts[SeverityUnhealthy])
}

func notChecked(dependency string) (Severity, string) {
	return SeverityUnhealthy, "not checked: " + dependency + " unavailable"
}

// summarize lists up to three items, so one broken dependency among
// hundreds still fits on a single report line.
func summarize(items []string) string {
	const shown = 3
	if len(items) <= shown {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(items[:shown], ", "), len(items)-shown)
}

func checkConfig(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.cfgErr != nil {
		return SeverityUnhealthy, env.cfgErr.Error()
	}
	return SeverityOK, fmt.Sprintf("vector backend %q, embedding model %q", env.cfg.Vector.Backend, env.cfg.Embedding.Model)
}

func checkControlDB(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.storeErr == nil {
		return SeverityOK, ""
	}
	if errors.Is(env.storeErr, fs.ErrNotExist) {
		return SeverityUnhealthy, env.storeErr.Error() + " (run `ragctl init`)"
	}
	return SeverityUnhealthy, env.storeErr.Error()
}

// checkSchemaVersion reports the on-disk version. A version newer than
// this binary supports is rejected by bboltstore.Open itself, so it shows
// up here as the open error, which already names both versions.
func checkSchemaVersion(ctx context.Context, env *doctorEnv) (Severity, string) {
	if errors.Is(env.storeErr, bboltstore.ErrUnsupportedSchemaVersion) {
		return SeverityUnhealthy, env.storeErr.Error()
	}
	if env.store == nil {
		return notChecked("control DB")
	}
	v, err := env.store.SchemaVersion(ctx)
	if err != nil {
		return SeverityUnhealthy, err.Error()
	}
	return SeverityOK, fmt.Sprintf("version %d (this binary supports up to %d)", v, bboltstore.CurrentSchemaVersion)
}

func checkBadger(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.badgerErr != nil {
		return SeverityUnhealthy, env.badgerErr.Error()
	}
	return SeverityOK, ""
}

func checkStaleJobs(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.store == nil {
		return notChecked("control DB")
	}
	jobs, err := env.store.ListJobs(ctx)
	if err != nil {
		return SeverityUnhealthy, err.Error()
	}
	var stale []string
	for _, j := range jobs {
		if j.State == domain.JobRunning && env.now.Sub(j.UpdatedAt) > staleJobAge {
			stale = append(stale, fmt.Sprintf("%s %s %s", j.Type, j.Dependency.Dependency.Name, j.Dependency.Version))
		}
	}
	if len(stale) > 0 {
		return SeverityWarning, fmt.Sprintf("%d job(s) stuck in RUNNING for over %s, likely from an interrupted run (re-run `ragctl gc`): %s", len(stale), staleJobAge, summarize(stale))
	}
	return SeverityOK, fmt.Sprintf("none stuck (%d total)", len(jobs))
}

// activePointers lists the configured backend's active pointers, or
// reports why the calling check can't run.
func activePointers(ctx context.Context, env *doctorEnv) ([]bboltstore.ActivePointer, Severity, string, bool) {
	if env.cfgErr != nil {
		sev, detail := notChecked("config")
		return nil, sev, detail, false
	}
	if env.store == nil {
		sev, detail := notChecked("control DB")
		return nil, sev, detail, false
	}
	pointers, err := env.store.ListActivePointers(ctx, env.cfg.Vector.Backend)
	if err != nil {
		return nil, SeverityUnhealthy, err.Error(), false
	}
	return pointers, SeverityOK, "", true
}

func pointerLabel(p bboltstore.ActivePointer) string {
	return fmt.Sprintf("%s %s", p.Ecosystem, p.Dependency)
}

func checkActiveGenerations(ctx context.Context, env *doctorEnv) (Severity, string) {
	pointers, sev, detail, ok := activePointers(ctx, env)
	if !ok {
		return sev, detail
	}
	if len(pointers) == 0 {
		return SeverityWarning, fmt.Sprintf("no active generations for backend %q yet (run `ragctl scan` then `ragctl sync`)", env.cfg.Vector.Backend)
	}
	var broken []string
	for _, p := range pointers {
		gen, err := env.store.GetGeneration(ctx, p.GenerationID)
		switch {
		case errors.Is(err, bboltstore.ErrNotFound):
			broken = append(broken, pointerLabel(p)+" (points at missing generation "+p.GenerationID+")")
		case err != nil:
			return SeverityUnhealthy, err.Error()
		case gen.State != domain.GenActive:
			broken = append(broken, fmt.Sprintf("%s (generation %s is %s)", pointerLabel(p), gen.ID, gen.State))
		}
	}
	if len(broken) > 0 {
		return SeverityUnhealthy, fmt.Sprintf("%d of %d active pointers are inconsistent: %s", len(broken), len(pointers), summarize(broken))
	}
	return SeverityOK, fmt.Sprintf("%d active", len(pointers))
}

func checkActiveManifests(ctx context.Context, env *doctorEnv) (Severity, string) {
	pointers, sev, detail, ok := activePointers(ctx, env)
	if !ok {
		return sev, detail
	}
	if env.badger == nil {
		return notChecked("Badger")
	}
	var missing []string
	for _, p := range pointers {
		_, err := env.badger.GetManifest(ctx, p.GenerationID)
		switch {
		case errors.Is(err, badgerstore.ErrNotFound):
			missing = append(missing, pointerLabel(p))
		case err != nil:
			return SeverityUnhealthy, err.Error()
		}
	}
	if len(missing) > 0 {
		return SeverityUnhealthy, fmt.Sprintf("%d active generation(s) have no manifest in Badger: %s", len(missing), summarize(missing))
	}
	return SeverityOK, fmt.Sprintf("%d present", len(pointers))
}

func checkBackendReplicas(ctx context.Context, env *doctorEnv) (Severity, string) {
	pointers, sev, detail, ok := activePointers(ctx, env)
	if !ok {
		return sev, detail
	}
	var bad []string
	for _, p := range pointers {
		replica, err := env.store.GetBackendReplica(ctx, p.GenerationID, env.cfg.Vector.Backend)
		switch {
		case errors.Is(err, bboltstore.ErrNotFound):
			bad = append(bad, pointerLabel(p)+" (no replica)")
		case err != nil:
			return SeverityUnhealthy, err.Error()
		case replica.Status != "complete":
			bad = append(bad, fmt.Sprintf("%s (replica %s)", pointerLabel(p), replica.Status))
		}
	}
	if len(bad) > 0 {
		return SeverityUnhealthy, fmt.Sprintf("%d active generation(s) lack a complete %s replica: %s", len(bad), env.cfg.Vector.Backend, summarize(bad))
	}
	return SeverityOK, fmt.Sprintf("%d complete", len(pointers))
}

// checkEmptyActiveGenerations is POINT-003: a collection populated
// before POINT-001's point-ID fix can hold an ACTIVE generation whose
// points were silently overwritten by a sibling (two generations with
// byte-identical content used to collide on one shared point). It
// reports as synced (its manifest, replica, and bbolt state all look
// fine — checkActiveManifests/checkBackendReplicas above pass) but a
// search against it returns nothing. Scoped deliberately to the exact-
// zero case, not a fuzzy "far fewer than expected" threshold: GEN-003's
// content-reuse dedup means a legitimate generation can share most of
// its points with an earlier one, so a lower-than-manifest count isn't
// on its own evidence of anything wrong — zero, for a generation whose
// own manifest claims real chunks, always is.
// emptyGenerationsCheckBudget bounds checkEmptyActiveGenerations' own
// work independently of the whole `doctor` request's timeout (OPS-003,
// epic 61): this check makes one real network call per active
// generation with a non-empty manifest — at real scale (confirmed live
// at 558 generations) against a degraded backend, that serial cost can
// exceed the whole request's own timeout, aborting every check after it
// too, not just this one. A var so tests can shrink it.
var emptyGenerationsCheckBudget = 30 * time.Second

// emptyGenerationsConcurrency bounds how many Count calls run at once,
// mirroring COORD-003's own bounded-worker-pool shape at a much smaller
// scale (a health check, not a bulk sync) — real wall-clock now scales
// with worker count against a real backend, not generation count.
const emptyGenerationsConcurrency = 8

func checkEmptyActiveGenerations(ctx context.Context, env *doctorEnv) (Severity, string) {
	pointers, sev, detail, ok := activePointers(ctx, env)
	if !ok {
		return sev, detail
	}
	if env.badger == nil {
		return notChecked("Badger")
	}
	if env.cfgErr != nil {
		return notChecked("config")
	}
	vb, err := buildVectorBackend(env.cfg)
	if err != nil {
		return SeverityUnhealthy, err.Error()
	}

	// Manifest reads are fast, local Badger lookups — only the vector
	// backend Count call below is a real network round trip, so only
	// that part needs bounding/parallelizing.
	type target struct {
		p bboltstore.ActivePointer
		m generation.Manifest
	}
	var targets []target
	for _, p := range pointers {
		raw, err := env.badger.GetManifest(ctx, p.GenerationID)
		if errors.Is(err, badgerstore.ErrNotFound) {
			continue // checkActiveManifests already flags this
		}
		if err != nil {
			return SeverityUnhealthy, err.Error()
		}
		var m generation.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return SeverityUnhealthy, fmt.Sprintf("decode manifest for %s: %v", pointerLabel(p), err)
		}
		if m.ChunkCount == 0 {
			continue // nothing to have points for
		}
		targets = append(targets, target{p, m})
	}

	budgetCtx, cancel := context.WithTimeout(ctx, emptyGenerationsCheckBudget)
	defer cancel()

	type outcome struct {
		empty   bool
		label   string
		err     error
		skipped bool
	}
	outcomes := make([]outcome, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, emptyGenerationsConcurrency)
	for i, t := range targets {
		if budgetCtx.Err() != nil {
			outcomes[i] = outcome{skipped: true}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t target) {
			defer wg.Done()
			defer func() { <-sem }()
			if budgetCtx.Err() != nil {
				outcomes[i] = outcome{skipped: true}
				return
			}
			n, err := vb.Count(budgetCtx, env.cfg.Vector.Collection, &backend.Filter{Generation: t.p.GenerationID})
			if err != nil {
				// Distinguish "our own budget ran out mid-call" (expected,
				// honestly reported as skipped) from a real backend error
				// unrelated to the budget (a genuine problem worth
				// surfacing as UNHEALTHY) — both look like a context error
				// from vb.Count's point of view, but only budgetCtx.Err()
				// tells you which one actually happened.
				if budgetCtx.Err() != nil {
					outcomes[i] = outcome{skipped: true}
					return
				}
				outcomes[i] = outcome{err: err}
				return
			}
			if n == 0 {
				outcomes[i] = outcome{empty: true, label: fmt.Sprintf("%s (manifest claims %d chunks, backend has 0 points)", pointerLabel(t.p), t.m.ChunkCount)}
			}
		}(i, t)
	}
	wg.Wait()

	var empty []string
	var firstErr error
	checked, skipped := 0, 0
	for _, o := range outcomes {
		switch {
		case o.skipped:
			skipped++
		case o.err != nil:
			checked++
			if firstErr == nil {
				firstErr = o.err
			}
		case o.empty:
			checked++
			empty = append(empty, o.label)
		default:
			checked++
		}
	}

	if len(empty) > 0 {
		msg := fmt.Sprintf("%d active generation(s) have zero points despite a non-empty manifest — `ragctl sync --force` will NOT fix this (the planner skips a dependency whose version hasn't changed, regardless of its real backend content); run `ragctl sync --rebuild --dependency <name>` for each (OPS-004): %s", len(empty), summarize(empty))
		if skipped > 0 {
			msg += fmt.Sprintf(" (%d of %d generations skipped within the %s check budget — re-run to check the rest)", skipped, len(targets), emptyGenerationsCheckBudget)
		}
		return SeverityUnhealthy, msg
	}
	if firstErr != nil {
		return SeverityUnhealthy, fmt.Sprintf("count points: %v", firstErr)
	}
	if skipped > 0 {
		return SeverityWarning, fmt.Sprintf("checked %d of %d active generations within the %s check budget — re-run to check the rest, or the backend may be degraded (this check exists partly to detect that)", checked, len(targets), emptyGenerationsCheckBudget)
	}
	return SeverityOK, fmt.Sprintf("%d checked, none empty", checked)
}

// referencedButNeverBuiltGrace excludes a reference younger than this from
// checkReferencedButNeverBuilt — a dependency whose sync genuinely hasn't
// finished yet (a real, in-flight first build) must never be reported as
// stuck. A var so tests can shrink it.
var referencedButNeverBuiltGrace = 5 * time.Minute

// checkReferencedButNeverBuilt is OPS-005 (epic 61): a kill or crash
// landing after a new dependency's reference is recorded but before its
// matching build ever reaches ACTIVE leaves it permanently un-retriable —
// internal/planner/planner.go's Plan emits ActionNoop unconditionally
// once a reference exists at the current version, regardless of whether
// an active generation has ever existed at all. Live-found (STRESS-006,
// epic 49): no existing doctor check catches this, since it isn't about
// an active generation being wrong (checkEmptyActiveGenerations's job) —
// there's no active generation at all to inspect. `ragctl describe` was
// live-confirmed not to help either — it only reports a dependency's
// currently-active generation, so a referenced-but-never-built one is
// invisible there too, not flagged as a problem.
func checkReferencedButNeverBuilt(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.store == nil {
		return notChecked("control DB")
	}
	if env.cfgErr != nil {
		return notChecked("config")
	}
	refs, err := env.store.ListAllReferences(ctx)
	if err != nil {
		return SeverityUnhealthy, err.Error()
	}

	type key struct {
		ecosystem, name, version string
	}
	seen := map[key]bool{}
	checked := 0
	var stuck, noSource []string
	for _, r := range refs {
		k := key{string(r.Ecosystem), r.Package, r.Version}
		if seen[k] {
			continue // multiple projects/reasons can reference the same tuple
		}
		seen[k] = true
		if env.now.Sub(r.FirstSeenAt) < referencedButNeverBuiltGrace {
			continue // plausibly still mid-first-sync
		}
		checked++
		if _, err := env.store.GetActiveGeneration(ctx, r.Ecosystem, r.Package, r.Version, env.cfg.Vector.Backend); errors.Is(err, bboltstore.ErrNotFound) {
			label := fmt.Sprintf("%s %s@%s", r.Ecosystem, r.Package, r.Version)
			if has, err := env.store.HasNoSource(ctx, r.Ecosystem, r.Package, r.Version); err == nil && has {
				noSource = append(noSource, label)
				continue
			}
			stuck = append(stuck, label)
		}
	}
	// PLAN-005: a plain `ragctl sync` now retries every one of these, so
	// one still listed means its build keeps failing — the daemon log has
	// the per-dependency error.
	if len(stuck) > 0 {
		return SeverityUnhealthy, fmt.Sprintf("%d dependency(ies) referenced but not built; each `ragctl sync` retries them, so these keep failing — see the daemon log for why: %s", len(stuck), summarize(stuck))
	}
	if len(noSource) > 0 {
		return SeverityWarning, fmt.Sprintf("%d checked; %d have no known docs source (no registry manifest or fallback) and aren't retried: %s", checked, len(noSource), summarize(noSource))
	}
	return SeverityOK, fmt.Sprintf("%d checked, none stuck", checked)
}

func checkBackendReachable(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.cfgErr != nil {
		return notChecked("config")
	}
	target := fmt.Sprintf("%s at %s", env.cfg.Vector.Backend, env.cfg.Vector.Endpoint)
	if err := probeBackend(ctx, env.cfg); err != nil {
		return SeverityUnhealthy, fmt.Sprintf("%s: %v", target, err)
	}
	return SeverityOK, target
}

// checkEmbeddingModel compares each active replica's embedding model with
// the configured one: queries embed with the configured model, so vectors
// stored under a different model can't be searched meaningfully.
// Generations without a replica are left to checkBackendReplicas.
func checkEmbeddingModel(ctx context.Context, env *doctorEnv) (Severity, string) {
	pointers, sev, detail, ok := activePointers(ctx, env)
	if !ok {
		return sev, detail
	}
	want := env.cfg.Embedding.Model
	var mismatched []string
	for _, p := range pointers {
		replica, err := env.store.GetBackendReplica(ctx, p.GenerationID, env.cfg.Vector.Backend)
		switch {
		case errors.Is(err, bboltstore.ErrNotFound):
			continue
		case err != nil:
			return SeverityUnhealthy, err.Error()
		case replica.EmbeddingModel != want:
			mismatched = append(mismatched, fmt.Sprintf("%s (%s)", pointerLabel(p), replica.EmbeddingModel))
		}
	}
	if len(mismatched) > 0 {
		return SeverityUnhealthy, fmt.Sprintf("%d active generation(s) were embedded with a different model than the configured %q: %s", len(mismatched), want, summarize(mismatched))
	}
	return SeverityOK, fmt.Sprintf("all active generations use %q", want)
}

func checkEmbeddingBackend(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.cfgErr != nil {
		return notChecked("config")
	}
	readiness := env.embeddingReadiness
	if readiness == nil {
		// No daemon = do a direct, synchronous check instead of reading a cached state.
		readiness = newEmbeddingReadiness()
		checkEmbeddingReadiness(ctx, env.cfg, readiness, func(string, ...any) {})
	}
	state, detail := readiness.get()
	return embeddingSeverityFor(state), embeddingStatusLabel(string(state), detail)
}

func embeddingSeverityFor(state embeddingState) Severity {
	switch state {
	case embeddingStateReady, embeddingStateUnknown, "":
		return SeverityOK
	case embeddingStateChecking, embeddingStatePulling:
		return SeverityWarning
	default:
		return SeverityUnhealthy
	}
}

func checkVectorBackend(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.cfgErr != nil {
		return notChecked("config")
	}
	readiness := env.vectorReadiness
	if readiness == nil {
		// No daemon = do a direct, synchronous check instead of reading a cached state.
		readiness = newVectorReadiness()
		checkVectorReadiness(ctx, env.cfg, readiness, func(string, ...any) {})
	}
	state, detail := readiness.get()
	if state == vectorStateReady || state == vectorStateUnknown || state == "" {
		detail = fmt.Sprintf("%s reachable at %s",
			env.cfg.Vector.Backend, redactDSN(env.cfg.Vector.Endpoint))
		if env.cfg.Vector.Managed && env.cfg.Vector.Backend == "qdrant" {
			detail = fmt.Sprintf("%s. managed: %s", detail, qdrantContainerName)
			return SeverityOK, detail
		}
		return vectorSeverityFor(state), detail

	}
	return vectorSeverityFor(state), vectorStatusLabel(string(state), detail)
}

func vectorSeverityFor(state vectorState) Severity {
	switch state {
	case vectorStateReady, vectorStateUnknown, "":
		return SeverityOK
	case vectorStateChecking, vectorStateStarting:
		return SeverityWarning
	default:
		return SeverityUnhealthy
	}
}

func checkRegistry(ctx context.Context, env *doctorEnv) (Severity, string) {
	if env.registryErr != nil {
		return SeverityUnhealthy, env.registryErr.Error()
	}
	if n := len(env.registry.Warnings); n > 0 {
		return SeverityWarning, fmt.Sprintf("%d manifest warning(s): %s", n, summarize(env.registry.Warnings))
	}
	return SeverityOK, fmt.Sprintf("%d manifests", len(env.registry.ManifestNames()))
}

func checkGit(ctx context.Context, env *doctorEnv) (Severity, string) {
	path, err := env.lookPath("git")
	if err != nil {
		return SeverityUnhealthy, "git not found on PATH; `ragctl sync` cannot acquire any source"
	}
	return SeverityOK, path
}

// checkPackageManagers only looks for executables some registered,
// scanned project's resolver actually needs. Used by the no-daemon
// fallback path, which has both a store and a lookPath in the same
// process; when a daemon is reachable, packageManagersNeeded (store-
// dependent, runs daemon-side) and checkPackageManagersOnPath (PATH-only,
// always runs client-side) do the same work split across the two.
func checkPackageManagers(ctx context.Context, env *doctorEnv) (Severity, string) {
	needed, sev, detail, ok := packageManagersNeeded(ctx, env)
	if !ok {
		return sev, detail
	}
	return checkPackageManagersOnPath(env.lookPath, needed)
}

// packageManagersNeeded tallies which package-manager executables the
// registered, resolved projects require, and how many projects need
// each — store-dependent, so it runs wherever the store is open.
func packageManagersNeeded(ctx context.Context, env *doctorEnv) (map[string]int, Severity, string, bool) {
	if env.store == nil {
		sev, detail := notChecked("control DB")
		return nil, sev, detail, false
	}
	projects, err := env.store.ListProjects(ctx)
	if err != nil {
		return nil, SeverityUnhealthy, err.Error(), false
	}
	needed := map[string]int{}
	for _, p := range projects {
		res, err := env.store.GetResolution(ctx, p.ID)
		if errors.Is(err, bboltstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, SeverityUnhealthy, err.Error(), false
		}
		if exe, ok := resolverExecutables[res.Ecosystem]; ok {
			needed[exe]++
		}
	}
	return needed, SeverityOK, "", true
}

// checkPackageManagersOnPath looks up needed's executables against
// lookPath — always the calling user's own shell PATH, which is why
// this step can never run inside the daemon on the client's behalf.
func checkPackageManagersOnPath(lookPath func(string) (string, error), needed map[string]int) (Severity, string) {
	if len(needed) == 0 {
		return SeverityOK, "none needed by registered projects"
	}

	exes := make([]string, 0, len(needed))
	for exe := range needed {
		exes = append(exes, exe)
	}
	sort.Strings(exes)

	var found, missing []string
	for _, exe := range exes {
		if _, err := lookPath(exe); err != nil {
			missing = append(missing, fmt.Sprintf("%s (needed by %d project(s))", exe, needed[exe]))
		} else {
			found = append(found, exe)
		}
	}
	if len(missing) > 0 {
		return SeverityUnhealthy, "not found on PATH: " + strings.Join(missing, ", ")
	}
	return SeverityOK, strings.Join(found, ", ")
}
