// Command ollama-smoke measures how a local chat model behaves while
// something else (a depctl sync embedding through the same Ollama) shares
// the machine. It repeatedly asks the chat model for a fixed-size reply,
// recording Ollama's own tokens-per-second, and samples which models
// Ollama has loaded and how much memory pressure the machine is under.
//
// It is a measurement tool, not a test: it prints a JSON summary and
// writes every probe and sample as a JSON line to -out. Run by
// hack/ollama-smoke.sh; usable alone:
//
//	go run ./hack/ollama-smoke -model ornith-1.5:35b -duration 60s
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const prompt = "Explain, step by step and with a short Go example, how context cancellation propagates through a call tree."

type probe struct {
	Kind         string  `json:"kind"`
	At           string  `json:"at"`
	LoadMS       float64 `json:"load_ms"`
	PromptTokens int     `json:"prompt_tokens"`
	Tokens       int     `json:"tokens"`
	TokPerSec    float64 `json:"tok_per_sec"`
	WallMS       float64 `json:"wall_ms"`
	Err          string  `json:"err,omitempty"`
}

type sample struct {
	Kind     string   `json:"kind"`
	At       string   `json:"at"`
	Loaded   []string `json:"loaded"`
	FreePct  int      `json:"mem_free_pct"`
	SwapUsed float64  `json:"swap_used_mb"`
}

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:11434", "Ollama endpoint")
	model := flag.String("model", "", "chat model to probe (empty: sample only, no chat)")
	duration := flag.Duration("duration", 60*time.Second, "how long to run")
	stopFile := flag.String("stop-file", "", "also stop when this file appears")
	numPredict := flag.Int("num-predict", 160, "tokens to generate per probe")
	interval := flag.Duration("sample-interval", 3*time.Second, "how often to sample loaded models and memory")
	out := flag.String("out", "", "write every probe/sample as JSON lines here")
	label := flag.String("label", "run", "label printed in the summary")
	flag.Parse()

	var sink *os.File
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open out:", err)
			os.Exit(1)
		}
		defer f.Close()
		sink = f
	}
	var mu sync.Mutex
	emit := func(v any) {
		if sink == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(v)
		sink.Write(append(b, '\n'))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, *duration)
	defer cancelT()
	if *stopFile != "" {
		go func() {
			for ctx.Err() == nil {
				if _, err := os.Stat(*stopFile); err == nil {
					cancel()
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()
	}

	var samples []sample
	var probes []probe
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			s := takeSample(*endpoint)
			mu.Lock()
			samples = append(samples, s)
			mu.Unlock()
			emit(s)
			select {
			case <-ctx.Done():
				return
			case <-time.After(*interval):
			}
		}
	}()

	if *model != "" {
		for ctx.Err() == nil {
			p := chat(ctx, *endpoint, *model, *numPredict)
			if ctx.Err() != nil && p.Err != "" {
				break // interrupted mid-probe: don't count a partial one
			}
			mu.Lock()
			probes = append(probes, p)
			mu.Unlock()
			emit(p)
		}
	} else {
		<-ctx.Done()
	}
	wg.Wait()
	summarize(*label, *model, probes, samples)
}

func chat(ctx context.Context, endpoint, model string, numPredict int) probe {
	p := probe{Kind: "probe", At: time.Now().Format("15:04:05")}
	body, _ := json.Marshal(map[string]any{
		"model": model, "prompt": prompt, "stream": false,
		"options": map[string]any{"num_predict": numPredict, "temperature": 0, "seed": 1},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/generate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	p.WallMS = float64(time.Since(start).Milliseconds())
	if err != nil {
		p.Err = err.Error()
		return p
	}
	defer resp.Body.Close()
	var r struct {
		LoadDuration    int64  `json:"load_duration"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
		EvalDuration    int64  `json:"eval_duration"`
		Error           string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		p.Err = err.Error()
		return p
	}
	if r.Error != "" {
		p.Err = r.Error
		return p
	}
	p.LoadMS = float64(r.LoadDuration) / 1e6
	p.PromptTokens, p.Tokens = r.PromptEvalCount, r.EvalCount
	if r.EvalDuration > 0 {
		p.TokPerSec = float64(r.EvalCount) / (float64(r.EvalDuration) / 1e9)
	}
	return p
}

var freePct = regexp.MustCompile(`free percentage:\s*(\d+)%`)
var swapUsed = regexp.MustCompile(`used = ([\d.]+)M`)

func takeSample(endpoint string) sample {
	s := sample{Kind: "sample", At: time.Now().Format("15:04:05"), FreePct: -1}
	if resp, err := http.Get(endpoint + "/api/ps"); err == nil {
		var ps struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		json.NewDecoder(resp.Body).Decode(&ps)
		resp.Body.Close()
		for _, m := range ps.Models {
			s.Loaded = append(s.Loaded, m.Name)
		}
		sort.Strings(s.Loaded)
	}
	if out, err := exec.Command("memory_pressure").Output(); err == nil {
		if m := freePct.FindStringSubmatch(string(out)); m != nil {
			s.FreePct, _ = strconv.Atoi(m[1])
		}
	}
	if out, err := exec.Command("sysctl", "-n", "vm.swapusage").Output(); err == nil {
		if m := swapUsed.FindStringSubmatch(string(out)); m != nil {
			s.SwapUsed, _ = strconv.ParseFloat(m[1], 64)
		}
	}
	return s
}

func summarize(label, model string, probes []probe, samples []sample) {
	var rates, loads []float64
	errs := 0
	for _, p := range probes {
		if p.Err != "" {
			errs++
			continue
		}
		rates = append(rates, p.TokPerSec)
		loads = append(loads, p.LoadMS)
	}
	sort.Float64s(rates)
	loadedSets := map[string]int{}
	minFree, maxSwap := 101, 0.0
	for _, s := range samples {
		loadedSets[strings.Join(s.Loaded, "+")]++
		if s.FreePct >= 0 && s.FreePct < minFree {
			minFree = s.FreePct
		}
		if s.SwapUsed > maxSwap {
			maxSwap = s.SwapUsed
		}
	}
	maxLoad := 0.0
	for _, l := range loads {
		if l > maxLoad {
			maxLoad = l
		}
	}
	sum := map[string]any{
		"label": label, "model": model, "probes": len(rates), "probe_errors": errs,
		"loaded_model_sets_seen": loadedSets, "min_mem_free_pct": minFree, "max_swap_used_mb": maxSwap,
		"max_load_ms": maxLoad,
	}
	if len(rates) > 0 {
		sum["tok_per_sec_median"] = rates[len(rates)/2]
		sum["tok_per_sec_min"] = rates[0]
		sum["tok_per_sec_max"] = rates[len(rates)-1]
	}
	b, _ := json.MarshalIndent(sum, "", "  ")
	fmt.Println(string(b))
}
