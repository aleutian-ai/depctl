package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	Sources       []SourceEntry    `json:"sources,omitempty"`
	ActiveVersion string           `json:"active_version,omitempty"`
	GenerationID  string           `json:"generation_id,omitempty"`
	ChunkCount    int              `json:"chunk_count,omitempty"`
	ObjectCount   int              `json:"object_count,omitempty"`
	ObjectsReused int              `json:"objects_reused,omitempty"`
	ReplicaStatus string           `json:"replica_status,omitempty"`
	ReplicaPoints int              `json:"replica_points,omitempty"`
}

// SourceEntry is one registry-declared source, with the TrustClass its
// objects would carry if synced (SEC-001) — declared, not measured; see
// DESC-001's simplicity constraint on why this isn't computed from
// actual Badger content.
type SourceEntry struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	URL        string            `json:"url"`
	Authority  int               `json:"authority"`
	TrustClass domain.TrustClass `json:"trust_class"`
}

func newDescribeCmd() *cobra.Command {
	var htmlOut bool
	var outPath string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "describe [ecosystem/package]",
		Short: "Describe what knowledge ragctl has: packages, sources, and how much content backs each",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var filter string
			if len(args) == 1 {
				filter = args[0]
			}
			return runDescribe(cmd, filter, htmlOut, outPath, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&htmlOut, "html", false, "write a static HTML report instead of printing text")
	cmd.Flags().StringVar(&outPath, "out", "ragctl-describe.html", "output path for --html")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	return cmd
}

func runDescribe(cmd *cobra.Command, filter string, htmlOut bool, outPath string, jsonOut bool) error {
	ctx := context.Background()

	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	badgerStore, err := openDataStore()
	if err != nil {
		return fmt.Errorf("open data store: %w", err)
	}
	defer badgerStore.Close()

	backendName, err := configuredVectorBackendName()
	if err != nil {
		return err
	}

	reg, err := loadRegistryForCLI(ctx)
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	report, err := buildReport(ctx, store, badgerStore, reg, backendName, filter)
	if err != nil {
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
		printDescribeText(cmd, report, filter != "")
		return nil
	}
}

// buildReport gathers every describe row. filter, if non-empty, must be
// "<ecosystem>/<package>" and scopes the report to that one pair;
// otherwise every (ecosystem, package) pair ragctl has ever been asked
// to reference (across every project) is included, per DESC-001's
// design — this is "what's been asked for," not a dump of the entire
// registry catalog (that's `ragctl registry list`).
func buildReport(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, reg *registry.Registry, backendName, filter string) (Report, error) {
	report := Report{
		GeneratedAt: time.Now(),
		Registry: RegistrySummary{
			ManifestCount: len(reg.ManifestNames()),
			Warnings:      reg.Warnings,
		},
	}

	var pairs []depPair
	if filter != "" {
		eco, pkg, err := parsePackageFilter(filter)
		if err != nil {
			return Report{}, err
		}
		pairs = []depPair{{ecosystem: eco, pkg: pkg}}
	} else {
		refs, err := store.ListAllReferences(ctx)
		if err != nil {
			return Report{}, fmt.Errorf("list references: %w", err)
		}
		pairs = distinctPairs(refs)
	}

	for _, p := range pairs {
		entry, err := buildPackageEntry(ctx, store, badgerStore, reg, backendName, p.ecosystem, p.pkg)
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

func parsePackageFilter(filter string) (domain.Ecosystem, string, error) {
	eco, pkg, ok := strings.Cut(filter, "/")
	if !ok || eco == "" || pkg == "" {
		return "", "", fmt.Errorf("invalid package filter %q, want \"<ecosystem>/<package>\"", filter)
	}
	return domain.Ecosystem(eco), pkg, nil
}

// buildPackageEntry assembles one PackageEntry. A package with no
// registry match, or with a match but no active generation, is a normal
// result here — not an error — since surfacing exactly that gap is
// describe's reason to exist.
func buildPackageEntry(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, reg *registry.Registry, backendName string, eco domain.Ecosystem, pkg string) (PackageEntry, error) {
	entry := PackageEntry{Ecosystem: eco, Package: pkg}

	if manifest, ok := reg.Match(eco, pkg); ok {
		entry.ManifestMatch = true
		for _, s := range manifest.Sources {
			entry.Sources = append(entry.Sources, SourceEntry{
				ID:         s.ID,
				Type:       s.Type,
				URL:        s.URL,
				Authority:  s.Authority,
				TrustClass: generation.TrustClassForSourceType(s.Type),
			})
		}
	}

	gen, err := store.GetActiveGeneration(ctx, eco, pkg, backendName)
	if errors.Is(err, bboltstore.ErrNotFound) {
		return entry, nil
	}
	if err != nil {
		return PackageEntry{}, fmt.Errorf("get active generation for %s/%s: %w", eco, pkg, err)
	}
	entry.ActiveVersion = gen.Dependency.Version
	entry.GenerationID = gen.ID

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
	fmt.Fprintln(tw, "ECOSYSTEM\tPACKAGE\tACTIVE VERSION\tSOURCES\tCHUNKS\tREPLICA")
	for _, p := range report.Packages {
		version := p.ActiveVersion
		if version == "" {
			version = "-"
		}
		sources := fmt.Sprintf("%d", len(p.Sources))
		if !p.ManifestMatch {
			sources = "no manifest"
		}
		chunks := "-"
		if p.ChunkCount > 0 {
			chunks = fmt.Sprintf("%d", p.ChunkCount)
		}
		replica := p.ReplicaStatus
		if replica == "" {
			replica = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Ecosystem, p.Package, version, sources, chunks, replica)
	}
	tw.Flush()
}

func printPackageDetail(out io.Writer, p PackageEntry) {
	fmt.Fprintf(out, "%s/%s\n", p.Ecosystem, p.Package)
	if !p.ManifestMatch {
		fmt.Fprintln(out, "  no registry manifest")
	}
	for _, s := range p.Sources {
		fmt.Fprintf(out, "  source %-12s type=%-16s authority=%-4d trust=%s\n  %s\n", s.ID, s.Type, s.Authority, s.TrustClass, s.URL)
	}
	if p.ActiveVersion == "" {
		fmt.Fprintln(out, "  never synced (no active generation)")
		return
	}
	fmt.Fprintf(out, "  active version %s (generation %s)\n", p.ActiveVersion, p.GenerationID)
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
<tr><th>Ecosystem</th><th>Package</th><th>Active version</th><th>Sources</th><th>Chunks</th><th>Replica</th></tr>
{{range .Packages}}<tr>
  <td>{{.Ecosystem}}</td>
  <td>{{.Package}}</td>
  <td>{{if .ActiveVersion}}{{.ActiveVersion}}{{else}}—{{end}}</td>
  <td>{{if .ManifestMatch}}{{range .Sources}}<div class="trust-{{.TrustClass}}">{{.Type}} <code>{{.URL}}</code> (authority {{.Authority}}, {{.TrustClass}})</div>{{end}}{{else}}<span class="warn">no manifest</span>{{end}}</td>
  <td>{{if .ChunkCount}}{{.ChunkCount}}{{else}}—{{end}}</td>
  <td>{{if .ReplicaStatus}}{{.ReplicaStatus}}{{else}}—{{end}}</td>
</tr>{{end}}
</table>
</body>
</html>
`))
