package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
)

// Report is describe's complete output: what packages ragctl has been
// asked to know about, fleet-wide, built once and rendered as text,
// JSON, or HTML — see DESC-001's simplicity constraint against
// maintaining separate data-gathering paths per output format.
type Report struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Registry    RegistrySummary `json:"registry"`
	Packages    []PackageEntry  `json:"packages"`
}

// RegistrySummary is a coarse view of the loaded registry itself, not
// any one package.
type RegistrySummary struct {
	ManifestCount int      `json:"manifest_count"`
	Warnings      []string `json:"warnings,omitempty"`
}

// PackageEntry is one (ecosystem, package)'s complete describe row: what
// the registry declares, and what's actually been built/replicated.
type PackageEntry struct {
	Ecosystem     domain.Ecosystem `json:"ecosystem"`
	Package       string           `json:"package"`
	ManifestMatch bool             `json:"manifest_match"`
	// Alias is the matched manifest's metadata.name — what
	// `ragctl describe <alias>` resolves back to this row — empty when
	// ManifestMatch is false. Manifest names are already unique within
	// one loaded registry (REG-002's loader dedupes by name, last
	// source wins), but nothing stops two DIFFERENT names from looking
	// similar (a locally-added "bbolt" versus the built-in manifest for
	// the real go.etcd.io/bbolt are both valid, distinct names) — showing
	// the exact alias here is what lets a person notice that before
	// guessing wrong.
	Alias         string        `json:"alias,omitempty"`
	Sources       []SourceEntry `json:"sources,omitempty"`
	ActiveVersion string        `json:"active_version,omitempty"`
	GenerationID  string        `json:"generation_id,omitempty"`
	// OtherActiveVersions lists any other versions of this package that
	// are also active (ADR-012), oldest-first by version string.
	OtherActiveVersions []string `json:"other_active_versions,omitempty"`
	ChunkCount          int      `json:"chunk_count,omitempty"`
	ObjectCount         int      `json:"object_count,omitempty"`
	ObjectsReused       int      `json:"objects_reused,omitempty"`
	ReplicaStatus       string   `json:"replica_status,omitempty"`
	ReplicaPoints       int      `json:"replica_points,omitempty"`
}

// SourceEntry is one registry-declared source, with the TrustClass its
// objects would carry if synced (SEC-001) — declared, not measured; see
// DESC-001's simplicity constraint on why this isn't computed from
// actual Badger content.
type SourceEntry struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	URL        string            `json:"url,omitempty"`
	Module     string            `json:"module,omitempty"` // set instead of URL for type "godoc"
	Authority  int               `json:"authority"`
	TrustClass domain.TrustClass `json:"trust_class"`
	// Liveness is set only when --check-liveness is passed (DESC-ADV-001)
	// — a real network/git call per source, so never populated otherwise.
	Liveness *registry.LivenessResult `json:"liveness,omitempty"`
}

// Location returns whichever of URL/Module this source actually has —
// a "godoc" source has no URL of its own, it documents whatever Go
// files show up in the package's git source worktree (see GEN-002).
func (s SourceEntry) Location() string {
	if s.URL != "" {
		return s.URL
	}
	return s.Module
}

func newDescribeCmd() *cobra.Command {
	var htmlOut bool
	var outPath string
	var jsonOut bool
	var checkLiveness bool

	cmd := &cobra.Command{
		Use:   "describe [alias | ecosystem package]",
		Short: "Describe what knowledge ragctl has: packages, sources, and how much content backs each",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribe(cmd, args, htmlOut, outPath, jsonOut, checkLiveness)
		},
	}
	cmd.Flags().BoolVar(&htmlOut, "html", false, "write a static HTML report instead of printing text")
	cmd.Flags().StringVar(&outPath, "out", "ragctl-describe.html", "output path for --html")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	cmd.Flags().BoolVar(&checkLiveness, "check-liveness", false, "probe each declared source's reachability (real network/git calls, opt-in)")
	return cmd
}

// runDescribe interprets args per DESC-001's three forms:
//   - none: fleet-wide, every (ecosystem, package) ragctl has been asked
//     to reference.
//   - one: a registry manifest alias (its metadata.name — the same short
//     name every manifest already has, e.g. "badger") — resolved to
//     every (ecosystem, package) pair that manifest's match declares.
//   - two: an exact (ecosystem, package) pair, for cases with no
//     registry manifest at all (an alias has nothing to resolve) or to
//     disambiguate. Never one joined "<ecosystem>/<package>" string — a
//     Go module path (e.g. github.com/dgraph-io/badger/v4) contains
//     slashes itself, which made a single argument ambiguous for
//     exactly the ecosystem most likely to need this command.
func runDescribe(cmd *cobra.Command, args []string, htmlOut bool, outPath string, jsonOut, checkLiveness bool) error {
	ctx := cmd.Context()

	c, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	var report Report
	if err := c.Describe(ctx, args, checkLiveness, &report); err != nil {
		return err
	}

	switch {
	case jsonOut:
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case htmlOut:
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", outPath, err)
		}
		defer f.Close()
		if err := describeHTMLTemplate.Execute(f, report); err != nil {
			return fmt.Errorf("render html: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", outPath)
		return nil
	default:
		printDescribeText(cmd, report, len(args) > 0)
		return nil
	}
}

// aliasPairs resolves a registry manifest's short name (metadata.name)
// to every (ecosystem, package) pair its match declares — usually
// exactly one, but a manifest may list several of either.
func aliasPairs(reg *registry.Registry, alias string) ([]depPair, error) {
	m, ok := reg.Manifest(alias)
	if !ok {
		return nil, fmt.Errorf("no registry manifest named %q — see `ragctl registry list` for available names, or use `ragctl describe <ecosystem> <package>`", alias)
	}
	var pairs []depPair
	for _, eco := range m.Match.Ecosystems {
		for _, pkg := range m.Match.Packages {
			pairs = append(pairs, depPair{ecosystem: eco, pkg: pkg})
		}
	}
	return pairs, nil
}

// buildReport gathers every describe row. filterPairs, if non-empty,
// scopes the report to exactly those pairs; otherwise every (ecosystem,
// package) pair ragctl has ever been asked to reference (across every
// project) is included, per DESC-001's design — this is "what's been
// asked for," not a dump of the entire registry catalog (that's
// `ragctl registry list`).
func buildReport(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, reg *registry.Registry, backendName string, filterPairs []depPair, checkLiveness bool) (Report, error) {
	report := Report{
		GeneratedAt: time.Now(),
		Registry: RegistrySummary{
			ManifestCount: len(reg.ManifestNames()),
			Warnings:      reg.Warnings,
		},
	}

	pairs := filterPairs
	if len(pairs) == 0 {
		refs, err := store.ListAllReferences(ctx)
		if err != nil {
			return Report{}, fmt.Errorf("list references: %w", err)
		}
		pairs = distinctPairs(refs)
	}

	for _, p := range pairs {
		entry, err := buildPackageEntry(ctx, store, badgerStore, reg, backendName, p.ecosystem, p.pkg, checkLiveness)
		if err != nil {
			return Report{}, err
		}
		report.Packages = append(report.Packages, entry)
	}
	return report, nil
}

type depPair struct {
	ecosystem domain.Ecosystem
	pkg       string
}

func distinctPairs(refs []domain.VersionReference) []depPair {
	seen := map[depPair]bool{}
	var pairs []depPair
	for _, r := range refs {
		p := depPair{ecosystem: r.Ecosystem, pkg: r.Package}
		if seen[p] {
			continue
		}
		seen[p] = true
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].ecosystem != pairs[j].ecosystem {
			return pairs[i].ecosystem < pairs[j].ecosystem
		}
		return pairs[i].pkg < pairs[j].pkg
	})
	return pairs
}

// buildPackageEntry assembles one PackageEntry. A package with no
// registry match, or with a match but no active generation, is a normal
// result here — not an error — since surfacing exactly that gap is
// describe's reason to exist.
func buildPackageEntry(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, reg *registry.Registry, backendName string, eco domain.Ecosystem, pkg string, checkLiveness bool) (PackageEntry, error) {
	entry := PackageEntry{Ecosystem: eco, Package: pkg}

	if manifest, ok := reg.Match(eco, pkg); ok {
		entry.ManifestMatch = true
		entry.Alias = manifest.Metadata.Name
		for _, s := range manifest.Sources {
			se := SourceEntry{
				ID:         s.ID,
				Type:       s.Type,
				URL:        s.URL,
				Module:     s.Module,
				Authority:  s.Authority,
				TrustClass: generation.TrustClassForSourceType(s.Type),
			}
			if checkLiveness {
				result := registry.CheckLiveness(ctx, s)
				se.Liveness = &result
			}
			entry.Sources = append(entry.Sources, se)
		}
	}

	// Several versions can be active at once (ADR-012). The entry's
	// counts describe the most recently promoted one; the rest are listed.
	gen, ok := latestActiveGenerationAnyVersion(ctx, store, eco, pkg, backendName)
	if !ok {
		return entry, nil
	}
	entry.ActiveVersion = gen.Dependency.Version
	entry.GenerationID = gen.ID
	if pointers, err := store.ListActivePointers(ctx, backendName); err == nil {
		for _, p := range pointers {
			if p.Ecosystem == eco && p.Dependency == pkg && p.Version != gen.Dependency.Version {
				entry.OtherActiveVersions = append(entry.OtherActiveVersions, p.Version)
			}
		}
		sort.Strings(entry.OtherActiveVersions)
	}

	if m, err := readGenerationManifest(ctx, badgerStore, gen.ID); err == nil {
		entry.ChunkCount = m.ChunkCount
		entry.ObjectCount = m.ObjectCount
		entry.ObjectsReused = m.ObjectsReused
	}

	if replica, err := store.GetBackendReplica(ctx, gen.ID, backendName); err == nil {
		entry.ReplicaStatus = replica.Status
		entry.ReplicaPoints = replica.PointCount
	} else {
		entry.ReplicaStatus = "unknown"
	}

	return entry, nil
}

func printDescribeText(cmd *cobra.Command, report Report, drillDown bool) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "registry: %d manifest(s) loaded", report.Registry.ManifestCount)
	if len(report.Registry.Warnings) > 0 {
		fmt.Fprintf(out, ", %d warning(s)", len(report.Registry.Warnings))
	}
	fmt.Fprintln(out)
	for _, w := range report.Registry.Warnings {
		fmt.Fprintf(out, "  ! %s\n", w)
	}
	fmt.Fprintln(out)

	if drillDown {
		for _, p := range report.Packages {
			printPackageDetail(out, p)
		}
		return
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ECOSYSTEM\tPACKAGE\tALIAS\tACTIVE VERSION\tSOURCES\tCHUNKS\tREPLICA")
	for _, p := range report.Packages {
		version := p.ActiveVersion
		if version == "" {
			version = "-"
		}
		sources := fmt.Sprintf("%d", len(p.Sources))
		if !p.ManifestMatch {
			sources = "no manifest"
		}
		alias := p.Alias
		if alias == "" {
			alias = "-"
		}
		chunks := "-"
		if p.ChunkCount > 0 {
			chunks = fmt.Sprintf("%d", p.ChunkCount)
		}
		replica := p.ReplicaStatus
		if replica == "" {
			replica = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.Ecosystem, p.Package, alias, version, sources, chunks, replica)
	}
	tw.Flush()
}

func printPackageDetail(out io.Writer, p PackageEntry) {
	fmt.Fprintf(out, "%s/%s\n", p.Ecosystem, p.Package)
	if !p.ManifestMatch {
		fmt.Fprintln(out, "  no registry manifest")
	} else {
		fmt.Fprintf(out, "  alias: %s\n", p.Alias)
	}
	for _, s := range p.Sources {
		fmt.Fprintf(out, "  source %-12s type=%-16s authority=%-4d trust=%s\n  %s\n", s.ID, s.Type, s.Authority, s.TrustClass, s.Location())
		if s.Liveness != nil {
			status := "reachable"
			if !s.Liveness.Reachable {
				status = "UNREACHABLE: " + s.Liveness.Error
			}
			fmt.Fprintf(out, "  liveness: %s\n", status)
		}
	}
	if p.ActiveVersion == "" {
		fmt.Fprintln(out, "  never synced (no active generation)")
		return
	}
	fmt.Fprintf(out, "  active version %s (generation %s)\n", p.ActiveVersion, p.GenerationID)
	if len(p.OtherActiveVersions) > 0 {
		fmt.Fprintf(out, "  also active: %s\n", strings.Join(p.OtherActiveVersions, ", "))
	}
	fmt.Fprintf(out, "  objects=%d (reused=%d) chunks=%d\n", p.ObjectCount, p.ObjectsReused, p.ChunkCount)
	fmt.Fprintf(out, "  backend replica: status=%s points=%d\n", p.ReplicaStatus, p.ReplicaPoints)
}

var describeHTMLTemplate = template.Must(template.New("describe").Parse(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>ragctl describe</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: -apple-system, system-ui, sans-serif; margin: 2rem; background: Canvas; color: CanvasText; }
  h1 { font-size: 1.25rem; }
  table { border-collapse: collapse; width: 100%; margin-top: 1rem; }
  th, td { text-align: left; padding: 0.4rem 0.8rem; border-bottom: 1px solid color-mix(in srgb, CanvasText 20%, transparent); }
  th { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; opacity: 0.7; }
  .warn { color: #b45309; }
  .trust-repository, .trust-official { opacity: 0.85; }
  .trust-unknown { color: #b45309; }
  code { font-size: 0.85em; opacity: 0.8; }
</style>
</head>
<body>
<h1>ragctl describe — {{.GeneratedAt.Format "2006-01-02 15:04:05"}}</h1>
<p>{{.Registry.ManifestCount}} manifest(s) loaded{{if .Registry.Warnings}}, {{len .Registry.Warnings}} warning(s){{end}}</p>
{{range .Registry.Warnings}}<p class="warn">! {{.}}</p>{{end}}
<table>
<tr><th>Ecosystem</th><th>Package</th><th>Alias</th><th>Active version</th><th>Sources</th><th>Chunks</th><th>Replica</th></tr>
{{range .Packages}}<tr>
  <td>{{.Ecosystem}}</td>
  <td>{{.Package}}</td>
  <td>{{if .Alias}}<code>{{.Alias}}</code>{{else}}—{{end}}</td>
  <td>{{if .ActiveVersion}}{{.ActiveVersion}}{{else}}—{{end}}</td>
  <td>{{if .ManifestMatch}}{{range .Sources}}<div class="trust-{{.TrustClass}}">{{.Type}} <code>{{.Location}}</code> (authority {{.Authority}}, {{.TrustClass}}){{if .Liveness}}{{if .Liveness.Reachable}} — reachable{{else}} — <span class="warn">unreachable</span>{{end}}{{end}}</div>{{end}}{{else}}<span class="warn">no manifest</span>{{end}}</td>
  <td>{{if .ChunkCount}}{{.ChunkCount}}{{else}}—{{end}}</td>
  <td>{{if .ReplicaStatus}}{{.ReplicaStatus}}{{else}}—{{end}}</td>
</tr>{{end}}
</table>
</body>
</html>
`))
