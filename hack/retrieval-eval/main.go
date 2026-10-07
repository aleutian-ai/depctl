// Command retrieval-eval measures how well keyword (BM25) search, vector
// search and a hybrid of the two find the right documentation chunk, on
// one synced ragctl install that holds both indexes (retrieval.mode
// auto). A dev tool, not part of ragctl itself.
//
// Usage (stop the install's daemon first; this opens its stores):
//
//	go run ./hack/retrieval-eval gen  -config <config.yaml> -out generated.json [-per-dep 25]
//	go run ./hack/retrieval-eval prefixed -config <config.yaml>
//	go run ./hack/retrieval-eval run  -config <config.yaml> -questions a.json,b.json [-report report.md]
//
// prefixed re-embeds every chunk with the "search_document: " task prefix
// that nomic embedding models are trained with, into a separate
// vectors-prefixed.db beside the install's files; run then adds a vector
// method that also prefixes questions with "search_query: ". ragctl itself
// embeds without prefixes, so this measures what adding them would gain.
//
// gen samples chunks and asks a local chat model (Ollama) for a question
// each chunk answers, written without the chunk's identifiers or phrasing;
// that chunk is the question's right answer. run asks every question
// through each search method, scoped to the dependency's synced version,
// and reports how often the right chunk ranks first, in the top 3 and top
// 10, and the mean reciprocal rank, overall and by question kind.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/embedded"
	"aleutian-ai/ragctl/internal/backend/keyword"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding/ollama"
)

// Question is one labeled eval case. A chunk is a right answer when it
// matches any listed gold criterion.
type Question struct {
	ID         string `json:"id"`
	Source     string `json:"source"` // "hand" or "generated"
	Kind       string `json:"kind"`   // "identifier", "description" or "paraphrase"
	Dependency string `json:"dependency"`
	Question   string `json:"question"`
	Gold       Gold   `json:"gold"`
}

// Gold lists what counts as a right answer: exact chunk IDs, qualified or
// bare symbols ("pgxpool.New" or "New"), or substrings of the content.
type Gold struct {
	ChunkIDs []string `json:"chunk_ids,omitempty"`
	Symbols  []string `json:"symbols,omitempty"`
	Contains []string `json:"contains,omitempty"`
}

// corpus is the synced install: each dependency's active generation and
// its chunks.
type corpus struct {
	cfg      config.Config
	versions map[string]backend.Filter // dependency -> filter for its active generation
	chunks   map[string]domain.Chunk   // chunk ID -> chunk (IDs are unique across these generations)
	byDep    map[string][]domain.Chunk
}

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: retrieval-eval gen|run [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	cfgPath := fs.String("config", "", "the install's config.yaml")
	out := fs.String("out", "generated.json", "gen: where to write generated questions")
	perDep := fs.Int("per-dep", 25, "gen: questions to generate per dependency")
	model := fs.String("model", "ornith-1.5:9b", "gen: Ollama chat model")
	seed := fs.Int64("seed", 1, "gen: sampling seed")
	questions := fs.String("questions", "", "run: comma-separated question files")
	report := fs.String("report", "", "run: also write the report to this file")
	fs.Parse(os.Args[2:])
	if *cfgPath == "" {
		log.Fatal("-config is required")
	}
	c, err := loadCorpus(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	switch os.Args[1] {
	case "gen":
		if err := generate(c, *model, *perDep, *seed, *out); err != nil {
			log.Fatal(err)
		}
	case "prefixed":
		if err := embedPrefixed(c); err != nil {
			log.Fatal(err)
		}
	case "run":
		if err := run(c, strings.Split(*questions, ","), *report); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown subcommand %q", os.Args[1])
	}
}

func loadCorpus(cfgPath string) (*corpus, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	ctl, err := bboltstore.Open(cfg.Storage.Control.Path)
	if err != nil {
		return nil, fmt.Errorf("open control store (is the daemon stopped?): %w", err)
	}
	defer ctl.Close()
	data, err := badgerstore.Open(cfg.Storage.Data.Path)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	pointers, err := ctl.ListActivePointers(ctx, cfg.Vector.Backend)
	if err != nil {
		return nil, err
	}
	c := &corpus{cfg: cfg, versions: map[string]backend.Filter{}, chunks: map[string]domain.Chunk{}, byDep: map[string][]domain.Chunk{}}
	for _, p := range pointers {
		c.versions[p.Dependency] = backend.Filter{Ecosystem: string(p.Ecosystem), Dependency: p.Dependency, Version: p.Version, Generation: p.GenerationID}
		chunks, err := data.ListGenerationChunks(ctx, p.GenerationID)
		if err != nil {
			return nil, err
		}
		for _, ch := range chunks {
			c.chunks[ch.ID] = ch
		}
		c.byDep[p.Dependency] = chunks
	}
	return c, nil
}

// --- question generation

var identRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+|[a-z][a-z0-9]*[A-Z][A-Za-z0-9]*|[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*|[a-z0-9]+_[a-z0-9_]+`)

func generate(c *corpus, model string, perDep int, seed int64, out string) error {
	rng := rand.New(rand.NewSource(seed))
	var qs []Question
	rejected := 0
	deps := sortedKeys(c.byDep)
	for _, dep := range deps {
		var pool []domain.Chunk
		for _, ch := range c.byDep[dep] {
			n := len(ch.Content)
			path := strings.ToLower(ch.Metadata["source_path"])
			if n < 200 || n > 3000 || strings.Contains(path, "license") || strings.Contains(path, "changelog") {
				continue
			}
			pool = append(pool, ch)
		}
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		made := 0
		for _, ch := range pool {
			if made == perDep {
				break
			}
			q, err := askModel(model, dep, string(ch.Content))
			if err != nil {
				return err
			}
			if q == "" || leaks(q, string(ch.Content), ch.Metadata["symbol"]) {
				rejected++
				continue
			}
			made++
			qs = append(qs, Question{ID: fmt.Sprintf("gen-%03d", len(qs)+1), Source: "generated", Kind: "paraphrase", Dependency: dep, Question: q, Gold: Gold{ChunkIDs: []string{ch.ID}}})
			log.Printf("%-40s %s", dep, q)
		}
	}
	log.Printf("generated %d questions, rejected %d that reused identifiers or phrasing", len(qs), rejected)
	b, _ := json.MarshalIndent(qs, "", "  ")
	return os.WriteFile(out, b, 0o644)
}

func askModel(model, dep, content string) (string, error) {
	prompt := fmt.Sprintf(`Below is a piece of documentation from the Go dependency %s.

Write ONE question that a developer who has NOT read this documentation would ask, and that this documentation answers. Rules:
- Describe what they want to do in plain words.
- Do not use any function, type, method, field, constant or package names from the text.
- Do not copy phrases from the text.
- Output only the question, on one line.

Documentation:
%s`, dep, content)
	body, _ := json.Marshal(map[string]any{
		"model": model, "prompt": prompt, "stream": false, "think": false,
		"options": map[string]any{"num_ctx": 4096, "temperature": 0.3},
	})
	resp, err := http.Post("http://127.0.0.1:11434/api/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	q := regexp.MustCompile(`(?s)<think>.*?</think>`).ReplaceAllString(r.Response, "")
	q = strings.TrimSpace(strings.Split(strings.TrimSpace(q), "\n")[0])
	return strings.Trim(q, `"`), nil
}

// leaks reports a generated question that reuses the chunk's identifiers
// or copies a five-word run from it, which would make it an easy keyword
// match rather than a real paraphrase.
func leaks(q, content, symbol string) bool {
	lq := strings.ToLower(q)
	if symbol != "" && strings.Contains(lq, strings.ToLower(symbol)) {
		return true
	}
	for _, id := range identRE.FindAllString(content, -1) {
		if len(id) >= 4 && strings.Contains(lq, strings.ToLower(id)) {
			return true
		}
	}
	words := strings.Fields(strings.ToLower(content))
	for i := 0; i+5 <= len(words); i++ {
		if strings.Contains(lq, strings.Join(words[i:i+5], " ")) {
			return true
		}
	}
	return false
}

// --- evaluation

type method struct {
	name   string
	search func(q Question, f backend.Filter, k int) ([]string, error)
}

type tally struct {
	n, hit1, hit3, hit10 int
	rr                   float64
}

func (t *tally) add(rank int) {
	t.n++
	if rank == 1 {
		t.hit1++
	}
	if rank >= 1 && rank <= 3 {
		t.hit3++
	}
	if rank >= 1 && rank <= 10 {
		t.hit10++
		t.rr += 1 / float64(rank)
	}
}

func run(c *corpus, files []string, reportPath string) error {
	var qs []Question
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var part []Question
		if err := json.Unmarshal(b, &part); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		qs = append(qs, part...)
	}
	// A question whose right answer isn't in the corpus would count as a
	// miss for every method and quietly skew the comparison: refuse it.
	var unanswerable []string
	for _, q := range qs {
		f, ok := c.versions[q.Dependency]
		if !ok {
			continue
		}
		found := false
		for _, ch := range c.byDep[f.Dependency] {
			if c.isGold(ch.ID, q.Gold) {
				found = true
				break
			}
		}
		if !found {
			unanswerable = append(unanswerable, q.ID+" ("+q.Question+")")
		}
	}
	if len(unanswerable) > 0 {
		return fmt.Errorf("%d questions have no right answer in the corpus:\n  %s", len(unanswerable), strings.Join(unanswerable, "\n  "))
	}

	ctx := context.Background()
	dir := filepath.Dir(c.cfg.Storage.Control.Path)
	kw := keyword.New(filepath.Join(dir, "keyword.db"))
	vec := embedded.New(filepath.Join(dir, "vectors.db"))
	emb := ollama.New(c.cfg.Embedding.Endpoint, c.cfg.Embedding.Model)
	ns := c.cfg.Vector.Collection

	latency := map[string][]time.Duration{}
	timed := func(name string, fn func() ([]string, error)) ([]string, error) {
		t0 := time.Now()
		ids, err := fn()
		latency[name] = append(latency[name], time.Since(t0))
		return ids, err
	}
	keywordSearch := func(q Question, f backend.Filter, k int) ([]string, error) {
		return timed("keyword", func() ([]string, error) {
			res, err := kw.Query(ctx, backend.QueryRequest{Namespace: ns, Text: q.Question, TopK: k, Filter: &f})
			return ids(res), err
		})
	}
	vectorSearch := func(q Question, f backend.Filter, k int) ([]string, error) {
		return timed("vector", func() ([]string, error) {
			v, err := emb.Embed(ctx, []string{q.Question})
			if err != nil {
				return nil, err
			}
			res, err := vec.Query(ctx, backend.QueryRequest{Namespace: ns, Vector: v[0], TopK: k, Filter: &f})
			return ids(res), err
		})
	}
	// Reciprocal rank fusion of the two top-50 lists (k = 60, the usual
	// constant): a simple hybrid that needs no score calibration.
	hybrid := func(q Question, f backend.Filter, k int) ([]string, error) {
		a, err := keywordSearch(q, f, 50)
		if err != nil {
			return nil, err
		}
		b, err := vectorSearch(q, f, 50)
		if err != nil {
			return nil, err
		}
		score := map[string]float64{}
		for i, id := range a {
			score[id] += 1 / float64(60+i+1)
		}
		for i, id := range b {
			score[id] += 1 / float64(60+i+1)
		}
		merged := sortedKeys(score)
		sort.SliceStable(merged, func(i, j int) bool { return score[merged[i]] > score[merged[j]] })
		if len(merged) > k {
			merged = merged[:k]
		}
		return merged, nil
	}
	methods := []method{{"keyword (BM25)", keywordSearch}, {"vector (Ollama + embedded)", vectorSearch}, {"hybrid (RRF)", hybrid}}
	prefixedPath := filepath.Join(dir, "vectors-prefixed.db")
	if _, err := os.Stat(prefixedPath); err == nil {
		pvec := embedded.New(prefixedPath)
		methods = append(methods, method{"vector, nomic task prefixes", func(q Question, f backend.Filter, k int) ([]string, error) {
			return timed("vector, prefixed", func() ([]string, error) {
				v, err := emb.Embed(ctx, []string{"search_query: " + q.Question})
				if err != nil {
					return nil, err
				}
				res, err := pvec.Query(ctx, backend.QueryRequest{Namespace: ns, Vector: v[0], TopK: k, Filter: &f})
				return ids(res), err
			})
		}})
	}

	results := map[string]map[string]*tally{} // method -> group -> tally
	type miss struct{ q, method string }
	var perQuestion []string
	skipped := 0
	for _, q := range qs {
		f, ok := c.versions[q.Dependency]
		if !ok {
			skipped++
			continue
		}
		line := fmt.Sprintf("%-9s %-11s %-28s", q.ID, q.Kind, trim(q.Question, 28))
		for _, m := range methods {
			got, err := m.search(q, f, 10)
			if err != nil {
				return fmt.Errorf("%s on %s: %w", m.name, q.ID, err)
			}
			rank := 0
			for i, id := range got {
				if c.isGold(id, q.Gold) {
					rank = i + 1
					break
				}
			}
			if results[m.name] == nil {
				results[m.name] = map[string]*tally{}
			}
			for _, g := range []string{"all", "source: " + q.Source, "kind: " + q.Kind} {
				if results[m.name][g] == nil {
					results[m.name][g] = &tally{}
				}
				results[m.name][g].add(rank)
			}
			line += fmt.Sprintf(" %3s", rankLabel(rank))
		}
		perQuestion = append(perQuestion, line)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d questions (%d skipped: dependency not synced), %d dependencies, %d chunks\n\n", len(qs)-skipped, skipped, len(c.versions), len(c.chunks))
	groups := map[string]bool{}
	for _, byGroup := range results {
		for g := range byGroup {
			groups[g] = true
		}
	}
	for _, g := range sortedKeys(groups) {
		fmt.Fprintf(&b, "%s\n\n| method | n | hit@1 | hit@3 | hit@10 | MRR@10 |\n|---|---|---|---|---|---|\n", g)
		for _, m := range methods {
			t := results[m.name][g]
			if t == nil {
				continue
			}
			fmt.Fprintf(&b, "| %s | %d | %.0f%% | %.0f%% | %.0f%% | %.3f |\n", m.name, t.n, pct(t.hit1, t.n), pct(t.hit3, t.n), pct(t.hit10, t.n), t.rr/float64(t.n))
		}
		b.WriteString("\n")
	}
	b.WriteString("| search | median | p95 |\n|---|---|---|\n")
	for _, name := range []string{"keyword", "vector", "vector, prefixed"} {
		if len(latency[name]) == 0 {
			continue
		}
		d := latency[name]
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, d[len(d)/2].Round(time.Millisecond/10), d[len(d)*95/100].Round(time.Millisecond/10))
	}
	b.WriteString("\n(vector includes embedding the question with Ollama; hybrid is the two together)\n\nPer question, rank of the first right answer (- = not in the top 10): keyword, vector, hybrid\n\n```\n")
	b.WriteString(strings.Join(perQuestion, "\n"))
	b.WriteString("\n```\n")
	fmt.Print(b.String())
	if reportPath != "" {
		return os.WriteFile(reportPath, []byte(b.String()), 0o644)
	}
	return nil
}

// embedPrefixed builds vectors-prefixed.db: every corpus chunk embedded
// with the "search_document: " prefix, same metadata as the real index.
func embedPrefixed(c *corpus) error {
	ctx := context.Background()
	dir := filepath.Dir(c.cfg.Storage.Control.Path)
	path := filepath.Join(dir, "vectors-prefixed.db")
	os.Remove(path)
	store := embedded.New(path)
	emb := ollama.New(c.cfg.Embedding.Endpoint, c.cfg.Embedding.Model)
	dims, err := emb.Dimensions(ctx)
	if err != nil {
		return err
	}
	ns := backend.Namespace{Name: c.cfg.Vector.Collection, Dimensions: dims, Distance: "cosine"}
	if err := store.EnsureNamespace(ctx, ns); err != nil {
		return err
	}
	t0 := time.Now()
	for _, dep := range sortedKeys(c.byDep) {
		f := c.versions[dep]
		chunks := c.byDep[dep]
		for start := 0; start < len(chunks); start += 64 {
			batch := chunks[start:min(start+64, len(chunks))]
			texts := make([]string, len(batch))
			for i, ch := range batch {
				texts[i] = "search_document: " + string(ch.Content)
			}
			vecs, err := emb.Embed(ctx, texts)
			if err != nil {
				return err
			}
			points := make([]backend.Point, len(batch))
			for i, ch := range batch {
				points[i] = backend.Point{ID: ch.ID, Vector: vecs[i], Metadata: backend.PointMetadata{Ecosystem: f.Ecosystem, Dependency: f.Dependency, Version: f.Version, Generation: f.Generation}}
			}
			if err := store.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: points}); err != nil {
				return err
			}
		}
		log.Printf("%-42s %5d chunks  (%s elapsed)", dep, len(chunks), time.Since(t0).Round(time.Second))
	}
	return nil
}

func (c *corpus) isGold(id string, g Gold) bool {
	for _, want := range g.ChunkIDs {
		if id == want {
			return true
		}
	}
	ch, ok := c.chunks[id]
	if !ok {
		return false
	}
	sym, pkg := ch.Metadata["symbol"], ch.Metadata["package"]
	for _, want := range g.Symbols {
		if sym != "" && (want == sym || want == pkg+"."+sym) {
			return true
		}
	}
	for _, want := range g.Contains {
		if strings.Contains(string(ch.Content), want) {
			return true
		}
	}
	return false
}

func ids(res backend.QueryResult) []string {
	out := make([]string, len(res.Points))
	for i, p := range res.Points {
		out[i] = p.ID
	}
	return out
}

func rankLabel(r int) string {
	if r == 0 {
		return "-"
	}
	return fmt.Sprint(r)
}

func pct(a, n int) float64 { return 100 * float64(a) / float64(max(n, 1)) }

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
