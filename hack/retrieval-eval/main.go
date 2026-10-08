// Command retrieval-eval measures how well keyword (BM25) search, vector
// search and a hybrid of the two find the right documentation chunk, on
// one synced ragctl install that holds both indexes (retrieval.mode
// auto). A dev tool, not part of ragctl itself.
//
// Usage (stop the install's daemon first; this opens its stores):
//
//	go run ./hack/retrieval-eval gen  -config <config.yaml> -out generated.json [-per-dep 25]
//	go run ./hack/retrieval-eval index -config <config.yaml> -name eg2-270m -embed embeddinggemma-2:270m \
//	    [-query-format 'task: code retrieval | query: {q}'] [-doc-format 'title: {title} | text: {text}'] [-dims 256]
//	go run ./hack/retrieval-eval run  -config <config.yaml> -questions a.json,b.json [-report report.md] \
//	    [-split tune] [-misses misses.md] [-rerank <chat model>]
//
// index embeds every chunk with any Ollama embedding model, optional
// prompt formats and optional truncation, into eval-<name>.db beside the
// install's files (with eval-<name>.json describing it). {title} is the
// chunk's qualified symbol, else its source path, else "none";
// {section_title} adds a prose chunk's heading path. run
// compares keyword search, the install's own vectors (ragctl as shipped)
// and every eval index, each alone and fused with keyword search.
//
// Questions are split deterministically into a tune half and a test half
// (by a hash of the ID): decide changes on tune, report test. The test
// half is frozen as the benchmark; new questions use "dev-" IDs, which
// are always tune. A lenient
// score also accepts a result equivalent to the right answer (the same
// qualified symbol, or at least 80% of the same words).
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
	"net"
	"net/http"
	"os"
	"os/exec"
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
	style := fs.String("style", "paraphrase", "gen: \"paraphrase\" (no identifiers from the text) or \"agent\" (asked mid-task by a coding agent, naming the identifiers it has; IDs start with dev-)")
	questions := fs.String("questions", "", "run: comma-separated question files")
	name := fs.String("name", "", "index: name of the eval index")
	embedModel := fs.String("embed", "", "index: Ollama embedding model")
	queryFormat := fs.String("query-format", "{q}", "index: how questions are prompted ({q})")
	docFormat := fs.String("doc-format", "{text}", "index: how chunks are prompted ({title}, {text})")
	dims := fs.Int("dims", 0, "index: truncate vectors to this many dimensions (0 = full)")
	daemonProject := fs.String("daemon-project", "", "run: also ask every question through the running daemon's search API, for this project ID (start the daemon after the other methods' stores are closed: use -daemon-only)")
	daemonOnly := fs.Bool("daemon-only", false, "run: only the daemon method (its daemon holds the stores open)")
	report := fs.String("report", "", "run: also write the report to this file")
	half := fs.String("split", "all", "run: only ask the \"tune\" or \"test\" half (\"all\" = both); decide changes on tune")
	misses := fs.String("misses", "", "run: write the last method's misses (right answer not first) here, with what ranked above it")
	rerankModel := fs.String("rerank", "", "run: also rerank the shipped hybrid's top 20 with this Ollama chat model")
	symbols := fs.Bool("symbol", false, "run: also fuse a third ranking into the shipped hybrid: API docs whose symbol the question names")
	fs.Parse(os.Args[2:])
	if *cfgPath == "" {
		log.Fatal("-config is required")
	}
	c, err := loadCorpus(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if *questions != "" && os.Args[1] != "run" {
		qs, err := readQuestions(strings.Split(*questions, ","))
		if err != nil {
			log.Fatal(err)
		}
		c.only(qs) // gen and index cover just the dependencies these ask about
	}
	switch os.Args[1] {
	case "gen":
		if err := generate(c, *model, *perDep, *seed, *style, *out); err != nil {
			log.Fatal(err)
		}
	case "index":
		if *name == "" || *embedModel == "" {
			log.Fatal("index needs -name and -embed")
		}
		if err := buildIndex(c, evalIndex{Name: *name, Model: *embedModel, QueryFormat: *queryFormat, DocFormat: *docFormat, Dims: *dims}); err != nil {
			log.Fatal(err)
		}
	case "run":
		if err := run(c, strings.Split(*questions, ","), runOptions{report: *report, daemonProject: *daemonProject, daemonOnly: *daemonOnly, split: *half, misses: *misses, rerank: *rerankModel, symbol: *symbols}); err != nil {
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

func generate(c *corpus, model string, perDep int, seed int64, style, out string) error {
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
			prompt := paraphrasePrompt
			if style == "agent" {
				prompt = agentPrompt
			}
			q, err := askModel(model, fmt.Sprintf(prompt, dep, string(ch.Content)))
			if err != nil {
				return err
			}
			// A paraphrase must not reuse the chunk's words; an agent's
			// question names identifiers but mustn't copy a sentence.
			if q == "" || style != "agent" && leaks(q, string(ch.Content), ch.Metadata["symbol"]) || style == "agent" && copies(q, string(ch.Content)) {
				rejected++
				continue
			}
			made++
			id, source := fmt.Sprintf("gen-%03d", len(qs)+1), "generated"
			if style == "agent" {
				id, source = fmt.Sprintf("dev-agent-%03d", len(qs)+1), "generated (agent)"
			}
			qs = append(qs, Question{ID: id, Source: source, Kind: style, Dependency: dep, Question: q, Gold: Gold{ChunkIDs: []string{ch.ID}}})
			log.Printf("%-40s %s", dep, q)
		}
	}
	log.Printf("generated %d questions, rejected %d that reused identifiers or phrasing", len(qs), rejected)
	b, _ := json.MarshalIndent(qs, "", "  ")
	return os.WriteFile(out, b, 0o644)
}

// paraphrasePrompt asks for a question in plain words, without the
// documentation's identifiers or phrasing.
const paraphrasePrompt = `Below is a piece of documentation from the Go dependency %s.

Write ONE question that a developer who has NOT read this documentation would ask, and that this documentation answers. Rules:
- Describe what they want to do in plain words.
- Do not use any function, type, method, field, constant or package names from the text.
- Do not copy phrases from the text.
- Output only the question, on one line.

Documentation:
%s`

// agentPrompt asks for the question a coding agent would search with
// while working: it knows some names (a type, a function it saw in the
// code) but not the answer.
const agentPrompt = `Below is a piece of documentation from the Go dependency %s.

A coding agent is in the middle of a task in a project that uses this dependency, and needs this documentation. Write the ONE search query it would type. Rules:
- It may name the package, type or function it is working with, as code does (e.g. pgxpool.Config), but must not quote sentences from the text.
- Ask about the specific thing it needs: a behavior, an option, an error, a default, how to do something.
- Output only the query, on one line.

Documentation:
%s`

func askModel(model, prompt string) (string, error) {
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

// copies reports a question that copies a five-word run from content.
func copies(q, content string) bool {
	lq := strings.ToLower(q)
	words := strings.Fields(strings.ToLower(content))
	for i := 0; i+5 <= len(words); i++ {
		if strings.Contains(lq, strings.Join(words[i:i+5], " ")) {
			return true
		}
	}
	return false
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
	return copies(q, content)
}

// --- evaluation

// evalIndex describes one vector index built by `index`.
type evalIndex struct {
	Name        string `json:"name"`
	Model       string `json:"model"`
	QueryFormat string `json:"query_format"`
	DocFormat   string `json:"doc_format"`
	Dims        int    `json:"dims"` // 0 = the model's full output
}

type method struct {
	name   string
	search func(q Question, f backend.Filter, k int) ([]string, error)
}

type tally struct {
	n, hit1, hit3, hit10, lenient3 int
	rr, lenientRR                  float64
}

func (t *tally) add(rank, lenientRank int) {
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
	if lenientRank >= 1 && lenientRank <= 3 {
		t.lenient3++
	}
	if lenientRank >= 1 && lenientRank <= 10 {
		t.lenientRR += 1 / float64(lenientRank)
	}
}

// runOptions are run's flags.
type runOptions struct {
	report, daemonProject string
	daemonOnly            bool
	split                 string // "all", "tune" or "test"
	misses                string // file for the last method's misses
	rerank                string // Ollama chat model reranking the shipped hybrid, or ""
	symbol                bool   // add the symbol-match signal
}

func readQuestions(files []string) ([]Question, error) {
	var qs []Question
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var part []Question
		if err := json.Unmarshal(b, &part); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		qs = append(qs, part...)
	}
	return qs, nil
}

// only narrows the corpus to the dependencies qs ask about, so an index
// built for them skips the rest of the install.
func (c *corpus) only(qs []Question) {
	keep := map[string]bool{}
	for _, q := range qs {
		keep[q.Dependency] = true
	}
	for dep := range c.byDep {
		if !keep[dep] {
			delete(c.byDep, dep)
		}
	}
}

func run(c *corpus, files []string, opts runOptions) error {
	all, err := readQuestions(files)
	if err != nil {
		return err
	}
	var qs []Question
	for _, q := range all {
		if opts.split == "all" || split(q.ID) == opts.split {
			qs = append(qs, q)
		}
	}
	// A question whose right answer isn't in the corpus would count as a
	// miss for every method and quietly skew the comparison: refuse it.
	gold := map[string][]domain.Chunk{} // question ID -> its right answers
	var unanswerable []string
	for _, q := range qs {
		f, ok := c.versions[q.Dependency]
		if !ok {
			continue
		}
		for _, ch := range c.byDep[f.Dependency] {
			if c.isGold(ch.ID, q.Gold) {
				gold[q.ID] = append(gold[q.ID], ch)
			}
		}
		if len(gold[q.ID]) == 0 {
			unanswerable = append(unanswerable, q.ID+" ("+q.Question+")")
		}
	}
	if len(unanswerable) > 0 {
		return fmt.Errorf("%d questions have no right answer in the corpus:\n  %s", len(unanswerable), strings.Join(unanswerable, "\n  "))
	}

	ctx := context.Background()
	dir := filepath.Dir(c.cfg.Storage.Control.Path)
	kw := keyword.New(filepath.Join(dir, "keyword.db"))
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
	vectorSearch := func(name, path string, ix evalIndex) func(Question, backend.Filter, int) ([]string, error) {
		store := embedded.New(path)
		emb := ollama.New(c.cfg.Embedding.Endpoint, ix.Model)
		return func(q Question, f backend.Filter, k int) ([]string, error) {
			return timed(name, func() ([]string, error) {
				v, err := emb.Embed(ctx, []string{strings.ReplaceAll(ix.QueryFormat, "{q}", q.Question)})
				if err != nil {
					return nil, err
				}
				res, err := store.Query(ctx, backend.QueryRequest{Namespace: ns, Vector: truncate(v[0], ix.Dims), TopK: k, Filter: &f})
				return ids(res), err
			})
		}
	}
	// Reciprocal rank fusion of each list's top 50 (k = 60, the usual
	// constant): a simple hybrid that needs no score calibration.
	fuse := func(lists ...func(Question, backend.Filter, int) ([]string, error)) func(Question, backend.Filter, int) ([]string, error) {
		return func(q Question, f backend.Filter, k int) ([]string, error) {
			score := map[string]float64{}
			for _, search := range lists {
				got, err := search(q, f, 50)
				if err != nil {
					return nil, err
				}
				for i, id := range got {
					score[id] += 1 / float64(60+i+1)
				}
			}
			merged := sortedKeys(score)
			sort.SliceStable(merged, func(i, j int) bool { return score[merged[i]] > score[merged[j]] })
			if len(merged) > k {
				merged = merged[:k]
			}
			return merged, nil
		}
	}

	// ragctl as shipped: the install's own vectors, plain prompts.
	// Embedded exactly as the install's config says: its model, query
	// prompt and vector size.
	shippedFormat := c.cfg.Embedding.QueryPrompt
	if shippedFormat == "" {
		shippedFormat = "{q}"
	}
	shipped := vectorSearch(c.cfg.Embedding.Model+" (as shipped)", filepath.Join(dir, "vectors.db"), evalIndex{Model: c.cfg.Embedding.Model, QueryFormat: shippedFormat, Dims: c.cfg.Embedding.Dimensions})
	methods := []method{
		{"keyword (BM25)", keywordSearch},
		{"vector: " + c.cfg.Embedding.Model + " (as shipped)", shipped},
		{"hybrid: keyword + " + c.cfg.Embedding.Model, fuse(keywordSearch, shipped)},
	}
	specs, _ := filepath.Glob(filepath.Join(dir, "eval-*.json"))
	sort.Strings(specs)
	for _, spec := range specs {
		var ix evalIndex
		b, err := os.ReadFile(spec)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &ix); err != nil {
			return fmt.Errorf("%s: %w", spec, err)
		}
		v := vectorSearch(ix.Name, strings.TrimSuffix(spec, ".json")+".db", ix)
		methods = append(methods, method{"vector: " + ix.Name, v}, method{"hybrid: keyword + " + ix.Name, fuse(keywordSearch, v)})
	}

	if opts.symbol {
		methods = append(methods, method{"hybrid + symbol match", fuse(keywordSearch, shipped, c.symbolSearch)})
	}
	if opts.rerank != "" {
		methods = append(methods, method{"rerank: " + opts.rerank + " over the shipped hybrid's top 20", rerank(c, opts.rerank, fuse(keywordSearch, shipped), timed)})
	}
	if opts.daemonOnly {
		methods = nil
	}
	if opts.daemonProject != "" {
		search, err := daemonSearch(dir, opts.daemonProject, timed)
		if err != nil {
			return err
		}
		methods = append(methods, method{"ragctl search API (" + c.cfg.Retrieval.ModeOrDefault() + " mode)", search})
	}

	results := map[string]map[string]*tally{} // method -> group -> tally
	var perQuestion []string
	var missLog strings.Builder
	for _, q := range qs {
		f := c.versions[q.Dependency]
		answer := "prose (README, guides)"
		for _, g := range gold[q.ID] {
			if g.Metadata["symbol"] != "" {
				answer = "API doc"
				break
			}
		}
		line := fmt.Sprintf("%-9s %-5s %-11s %-28s", q.ID, split(q.ID), q.Kind, trim(q.Question, 28))
		for _, m := range methods {
			got, err := m.search(q, f, 10)
			if err != nil {
				return fmt.Errorf("%s on %s: %w", m.name, q.ID, err)
			}
			rank, lenient := 0, 0
			for i, id := range got {
				if rank == 0 && c.isGold(id, q.Gold) {
					rank = i + 1
				}
				if lenient == 0 && c.equivalent(id, gold[q.ID]) {
					lenient = i + 1
				}
			}
			if results[m.name] == nil {
				results[m.name] = map[string]*tally{}
			}
			for _, g := range []string{"all questions", "held-out test half", "tune half", "source: " + q.Source, "kind: " + q.Kind, "answer: " + answer} {
				if g == "held-out test half" && split(q.ID) != "test" || g == "tune half" && split(q.ID) != "tune" {
					continue
				}
				if results[m.name][g] == nil {
					results[m.name][g] = &tally{}
				}
				results[m.name][g].add(rank, lenient)
			}
			line += fmt.Sprintf(" %3s", rankLabel(rank))
			if m.name == methods[len(methods)-1].name && rank != 1 {
				fmt.Fprintf(&missLog, "## %s (%s, %s) rank %s\n%s\nright: %s\n", q.ID, q.Dependency, answer, rankLabel(rank), q.Question, describe(gold[q.ID][0]))
				for i, id := range got[:min(3, len(got))] {
					fmt.Fprintf(&missLog, "  %d. %s\n", i+1, describe(c.chunks[id]))
				}
				missLog.WriteString("\n")
			}
		}
		perQuestion = append(perQuestion, line)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d questions, %d dependencies, %d chunks\n\n", len(qs), len(c.versions), len(c.chunks))
	order := []string{"held-out test half", "all questions", "tune half"}
	seen := map[string]bool{}
	for _, g := range order {
		seen[g] = true
	}
	for _, byGroup := range results {
		for g := range byGroup {
			if !seen[g] {
				order = append(order, g)
				seen[g] = true
			}
		}
	}
	sort.Strings(order[3:])
	for _, g := range order {
		fmt.Fprintf(&b, "### %s\n\n| method | n | hit@1 | hit@3 | hit@10 | MRR | lenient hit@3 | lenient MRR |\n|---|---|---|---|---|---|---|---|\n", g)
		for _, m := range methods {
			t := results[m.name][g]
			if t == nil {
				continue
			}
			n := float64(t.n)
			fmt.Fprintf(&b, "| %s | %d | %.0f%% | %.0f%% | %.0f%% | %.3f | %.0f%% | %.3f |\n", m.name, t.n, pct(t.hit1, t.n), pct(t.hit3, t.n), pct(t.hit10, t.n), t.rr/n, pct(t.lenient3, t.n), t.lenientRR/n)
		}
		b.WriteString("\n")
	}
	b.WriteString("| search | median | p95 |\n|---|---|---|\n")
	for _, name := range sortedKeys(latency) {
		d := latency[name]
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, d[len(d)/2].Round(time.Millisecond/10), d[len(d)*95/100].Round(time.Millisecond/10))
	}
	b.WriteString("\n(vector times include embedding the question with Ollama)\n\nPer question, rank of the first right answer (- = not in the top 10), in method order:\n")
	for i, m := range methods {
		fmt.Fprintf(&b, "%d. %s\n", i+1, m.name)
	}
	b.WriteString("\n```\n" + strings.Join(perQuestion, "\n") + "\n```\n")
	fmt.Print(b.String())
	if opts.misses != "" {
		if err := os.WriteFile(opts.misses, []byte(missLog.String()), 0o644); err != nil {
			return err
		}
	}
	if opts.report != "" {
		return os.WriteFile(opts.report, []byte(b.String()), 0o644)
	}
	return nil
}

// symbolSearch ranks the dependency's API docs by how much of their
// qualified symbol the question names, counting only identifier-shaped
// words in it (dotted, camelCase or snake_case, matched with their case):
// all of it ("pgxpool.Config.MaxConns"), the last two parts
// ("Config.MaxConns"), or just the name ("MaxConns"). Chunks it names
// nothing of are left out, so a question without identifiers adds nothing.
func (c *corpus) symbolSearch(q Question, f backend.Filter, k int) ([]string, error) {
	idents := map[string]bool{}
	for _, id := range identRE.FindAllString(q.Question, -1) {
		idents[id] = true
		parts := strings.Split(id, ".")
		for i := range parts { // "pgxpool.Config.MaxConns" also names "Config.MaxConns" and "MaxConns"
			idents[strings.Join(parts[i:], ".")] = true
		}
	}
	if len(idents) == 0 {
		return nil, nil
	}
	type hit struct {
		id    string
		score int
	}
	var hits []hit
	for _, ch := range c.byDep[f.Dependency] {
		sym := ch.Metadata["symbol"]
		if sym == "" {
			continue
		}
		full := sym
		if p := ch.Metadata["package"]; p != "" {
			full = p + "." + sym
		}
		parts := strings.Split(full, ".")
		score := 0
		switch {
		case idents[full]:
			score = 3
		case len(parts) >= 2 && idents[strings.Join(parts[len(parts)-2:], ".")]:
			score = 2
		case idents[parts[len(parts)-1]] && identRE.MatchString(parts[len(parts)-1]):
			score = 1
		}
		if score > 0 {
			hits = append(hits, hit{ch.ID, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []string
	for _, h := range hits[:min(k, len(hits))] {
		out = append(out, h.id)
	}
	return out, nil
}

// describe is a chunk in one line for the misses file: its title, section
// and the start of its text.
func describe(ch domain.Chunk) string {
	return fmt.Sprintf("[%s | %s] %s", title(ch), ch.Metadata["heading_path"], trim(strings.Join(strings.Fields(string(ch.Content)), " "), 110))
}

// rerank asks a chat model to reorder base's top 20: a listwise reranker,
// to see how much a better ordering of the same candidates is worth.
func rerank(c *corpus, model string, base func(Question, backend.Filter, int) ([]string, error), timed func(string, func() ([]string, error)) ([]string, error)) func(Question, backend.Filter, int) ([]string, error) {
	return func(q Question, f backend.Filter, k int) ([]string, error) {
		cands, err := base(q, f, 20)
		if err != nil {
			return nil, err
		}
		return timed("rerank "+model, func() ([]string, error) {
			var p strings.Builder
			fmt.Fprintf(&p, "A developer using the Go dependency %s asked:\n%s\n\nBelow are %d passages from its documentation. Rank them by how well they answer the question, best first. Output only the passage numbers, comma-separated, best first, at least the best 5.\n\n", q.Dependency, q.Question, len(cands))
			for i, id := range cands {
				ch := c.chunks[id]
				fmt.Fprintf(&p, "[%d] %s\n%s\n\n", i+1, title(ch), trim(string(ch.Content), 700))
			}
			body, _ := json.Marshal(map[string]any{
				"model": model, "prompt": p.String(), "stream": false, "think": false,
				"options": map[string]any{"num_ctx": 12288, "temperature": 0},
			})
			resp, err := http.Post("http://127.0.0.1:11434/api/generate", "application/json", bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			var r struct {
				Response string `json:"response"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
				return nil, err
			}
			answer := regexp.MustCompile(`(?s)<think>.*?</think>`).ReplaceAllString(r.Response, "")
			var out []string
			used := map[int]bool{}
			for _, n := range regexp.MustCompile(`\d+`).FindAllString(answer, -1) {
				var i int
				fmt.Sscan(n, &i)
				if i >= 1 && i <= len(cands) && !used[i] {
					used[i] = true
					out = append(out, cands[i-1])
				}
			}
			for i, id := range cands { // anything the model left out keeps its order after
				if !used[i+1] {
					out = append(out, id)
				}
			}
			return out[:min(k, len(out))], nil
		})
	}
}

// daemonSearch asks questions through the install's daemon, exactly as an
// agent's search_dependency_docs call does. The corpus is loaded first
// (it opens the stores, which needs the daemon stopped); `ragctl status`
// then auto-starts the daemon, using ragctl from PATH and this process's
// environment, which must point at the same install.
func daemonSearch(dir, projectID string, timed func(string, func() ([]string, error)) ([]string, error)) (func(Question, backend.Filter, int) ([]string, error), error) {
	if out, err := exec.Command("ragctl", "status").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("start the daemon with `ragctl status`: %v: %s", err, out)
	}
	sock := filepath.Join(dir, "ragctld.sock")
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	return func(q Question, _ backend.Filter, k int) ([]string, error) {
		return timed("ragctl search API", func() ([]string, error) {
			body, _ := json.Marshal(map[string]any{"project_id": projectID, "text": q.Question, "dependency": q.Dependency, "mode": "project", "top_k": k})
			resp, err := client.Post("http://ragctl/v1/search", "application/json", bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			var r struct {
				Chunks []struct {
					ChunkID string `json:"chunk_id"`
				} `json:"chunks"`
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
				return nil, err
			}
			if r.Error != "" {
				return nil, fmt.Errorf("daemon: %s", r.Error)
			}
			var ids []string
			for _, ch := range r.Chunks {
				ids = append(ids, ch.ChunkID)
			}
			return ids, nil
		})
	}, nil
}

// split assigns a question to the tune or test half, fixed by its ID.
// The test half of questions-hand.json and questions-generated.json is
// the frozen benchmark: decide changes on tune questions and run the
// test half only to confirm one. New development questions take a "dev-"
// ID and are always tune, so adding them never changes the benchmark.
func split(id string) string {
	if strings.HasPrefix(id, "dev-") {
		return "tune"
	}
	var h uint32 = 2166136261
	for i := 0; i < len(id); i++ {
		h = (h ^ uint32(id[i])) * 16777619
	}
	if h%2 == 0 {
		return "tune"
	}
	return "test"
}

// equivalent reports whether chunk id is as good an answer as one of the
// right ones: it is one, or has the same qualified symbol, or shares at
// least 80% of its words (the same doc repeated, e.g. in a README and a
// package doc).
func (c *corpus) equivalent(id string, right []domain.Chunk) bool {
	got, ok := c.chunks[id]
	if !ok {
		return false
	}
	for _, r := range right {
		if r.ID == id {
			return true
		}
		if s := got.Metadata["symbol"]; s != "" && s == r.Metadata["symbol"] && got.Metadata["package"] == r.Metadata["package"] {
			return true
		}
		if jaccard(string(got.Content), string(r.Content)) >= 0.8 {
			return true
		}
	}
	return false
}

func jaccard(a, b string) float64 {
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.Fields(strings.ToLower(s)) {
			m[w] = true
		}
		return m
	}
	x, y := set(a), set(b)
	inter := 0
	for w := range x {
		if y[w] {
			inter++
		}
	}
	union := len(x) + len(y) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// buildIndex embeds every corpus chunk for one eval index.
func buildIndex(c *corpus, ix evalIndex) error {
	ctx := context.Background()
	dir := filepath.Dir(c.cfg.Storage.Control.Path)
	base := filepath.Join(dir, "eval-"+ix.Name)
	os.Remove(base + ".db")
	store := embedded.New(base + ".db")
	emb := ollama.New(c.cfg.Embedding.Endpoint, ix.Model)
	dims := ix.Dims
	if dims == 0 {
		full, err := emb.Dimensions(ctx)
		if err != nil {
			return err
		}
		dims = full
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
				texts[i] = strings.NewReplacer("{title}", title(ch), "{section_title}", sectionTitle(ch), "{text}", string(ch.Content)).Replace(ix.DocFormat)
			}
			vecs, err := emb.Embed(ctx, texts)
			if err != nil {
				return err
			}
			points := make([]backend.Point, len(batch))
			for i, ch := range batch {
				points[i] = backend.Point{ID: ch.ID, Vector: truncate(vecs[i], ix.Dims), Metadata: backend.PointMetadata{Ecosystem: f.Ecosystem, Dependency: f.Dependency, Version: f.Version, Generation: f.Generation}}
			}
			if err := store.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: points}); err != nil {
				return err
			}
		}
		log.Printf("%-42s %5d chunks  (%s elapsed)", dep, len(chunks), time.Since(t0).Round(time.Second))
	}
	spec, _ := json.MarshalIndent(ix, "", "  ")
	return os.WriteFile(base+".json", spec, 0o644)
}

// title is a chunk's {title} for document prompts: its qualified symbol,
// else its source path, else "none".
func title(ch domain.Chunk) string {
	if s := ch.Metadata["symbol"]; s != "" {
		if p := ch.Metadata["package"]; p != "" {
			return p + "." + s
		}
		return s
	}
	if p := ch.Metadata["source_path"]; p != "" {
		return p
	}
	return "none"
}

// sectionTitle is title, plus the heading path for a prose chunk with
// one ("README.md > Usage > Pools"), so its title says where it sits.
func sectionTitle(ch domain.Chunk) string {
	if h := ch.Metadata["heading_path"]; h != "" && ch.Metadata["symbol"] == "" {
		return title(ch) + " > " + h
	}
	return title(ch)
}

// truncate keeps a Matryoshka embedding's first dims values (0 = all).
// No renormalizing: cosine similarity divides by length anyway.
func truncate(v []float32, dims int) []float32 {
	if dims == 0 || dims >= len(v) {
		return v
	}
	return v[:dims]
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
