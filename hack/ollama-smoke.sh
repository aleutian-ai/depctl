#!/bin/sh
# Measures what happens when a ragctl sync and a local chat model share one
# Ollama (and one machine): three phases against the same dependency list.
#
#   A  chat model alone            -> its tokens/sec
#   B  sync alone (chat unloaded)  -> sync wall time, points written
#   C  both at once                -> chat tokens/sec and sync wall time
#
# and, throughout, which models Ollama had loaded, the lowest free-memory
# percentage, and peak swap. Read the comparison at the end: how much the
# chat model slows during a sync, how much the sync slows next to a chat
# model, and whether the two models stayed loaded together.
#
# Needs: Ollama with $MODEL and the embedding model pulled, Qdrant on :6333,
# Podman (the sync runs in a Go container so the project's toolchain
# version doesn't matter), and a Go project to scan.
#
# Usage: PROJECT=/path/to/go/project DEPS_FILE=deps.txt hack/ollama-smoke.sh
#   MODEL       chat model to probe          (default ornith-1.5:35b)
#   PROJECT     Go project to scan           (required)
#   DEPS_FILE   dependency names, one per line, relative to $WORKDIR (required)
#   WORKDIR     scratch dir, git-ignored     (default .smoke)
#   CONC        sync.max_concurrency         (default 2)
#   ENDPOINT    Ollama endpoint              (default http://127.0.0.1:11434)
#   GO_IMAGE    container image              (default golang:1.26-alpine)
set -eu
cd "$(dirname "$0")/.."

MODEL="${MODEL:-ornith-1.5:35b}"
PROJECT="${PROJECT:?set PROJECT to a Go project directory}"
DEPS_FILE="${DEPS_FILE:?set DEPS_FILE to a file of dependency names inside WORKDIR}"
WORKDIR="${WORKDIR:-.smoke}"
CONC="${CONC:-2}"
ENDPOINT="${ENDPOINT:-http://127.0.0.1:11434}"
GO_IMAGE="${GO_IMAGE:-golang:1.26-alpine}"

mkdir -p "$WORKDIR"
WORKDIR="$(cd "$WORKDIR" && pwd)"
[ -f "$WORKDIR/$DEPS_FILE" ] || { echo "missing $WORKDIR/$DEPS_FILE" >&2; exit 1; }

echo "== preflight"
curl -sf "$ENDPOINT/api/tags" | grep -q "\"$MODEL\"" || { echo "Ollama has no model $MODEL" >&2; exit 1; }
curl -sf http://127.0.0.1:6333/healthz >/dev/null || { echo "Qdrant not reachable on :6333" >&2; exit 1; }
podman ps >/dev/null 2>&1 || { echo "Podman not reachable" >&2; exit 1; }
echo "model=$MODEL  concurrency=$CONC  deps=$(grep -c . "$WORKDIR/$DEPS_FILE")  project=$PROJECT"
echo "machine: $(sysctl -n machdep.cpu.brand_string), $(( $(sysctl -n hw.memsize) / 1073741824 )) GB RAM, podman VM $(podman machine list --format '{{.Memory}}' 2>/dev/null | head -1)"

go build -o "$WORKDIR/ollama-smoke" ./hack/ollama-smoke
echo "== building ragctl for the container"
podman run --rm --pull=never -v "$PWD":/src:Z -v "$WORKDIR":/out:Z -w /src "$GO_IMAGE" \
  sh -c 'export GOMODCACHE=/out/gomodcache; go build -o /out/ragctl ./cmd/ragctl'

unload() { curl -s "$ENDPOINT/api/generate" -d "{\"model\":\"$MODEL\",\"keep_alive\":0}" >/dev/null; sleep 3; }
warm()   { curl -s "$ENDPOINT/api/generate" -d "{\"model\":\"$MODEL\",\"prompt\":\"hi\",\"stream\":false,\"options\":{\"num_predict\":1}}" >/dev/null; }

run_sync() { # $1 = phase
  podman run --rm --pull=never -e PHASE="$1" -e CONC="$CONC" -e DEPS_FILE="$DEPS_FILE" \
    -v "$PWD":/src:Z -v "$WORKDIR":/out:Z -v "$PROJECT":/project:Z -w /src "$GO_IMAGE" \
    sh /src/hack/ollama-smoke-sync.sh
}

rm -f "$WORKDIR"/A.* "$WORKDIR"/B.* "$WORKDIR"/C.* "$WORKDIR"/W.* "$WORKDIR"/stop.*

echo "== priming git mirrors (not measured, so B and C don't differ by clone time)"
unload
run_sync W

echo "== phase A: chat model alone"
unload; warm
"$WORKDIR/ollama-smoke" -model "$MODEL" -duration 70s -label A -out "$WORKDIR/A.jsonl" > "$WORKDIR/A.summary.json"

echo "== phase B: sync alone (chat model unloaded)"
unload
"$WORKDIR/ollama-smoke" -duration 3600s -stop-file "$WORKDIR/stop.B" -label B -out "$WORKDIR/B.jsonl" > "$WORKDIR/B.summary.json" &
SAMPLER=$!
run_sync B
touch "$WORKDIR/stop.B"; wait $SAMPLER

echo "== phase C: sync and chat model together"
unload; warm
"$WORKDIR/ollama-smoke" -model "$MODEL" -duration 3600s -stop-file "$WORKDIR/stop.C" -label C -out "$WORKDIR/C.jsonl" > "$WORKDIR/C.summary.json" &
PROBER=$!
run_sync C
touch "$WORKDIR/stop.C"; wait $PROBER

echo
echo "== results"
python3 - "$WORKDIR" <<'PY'
import json, sys
w = sys.argv[1]
def summ(p):
    try: return json.load(open(f"{w}/{p}.summary.json"))
    except Exception: return {}
def num(p, ext):
    try: return open(f"{w}/{p}.{ext}").read().strip()
    except Exception: return "?"
A, B, C = summ("A"), summ("B"), summ("C")
tA, tC = A.get("tok_per_sec_median"), C.get("tok_per_sec_median")
sB, sC = num("B", "elapsed"), num("C", "elapsed")
print(f"chat model alone      : {tA:.1f} tok/s median over {A.get('probes')} probes" if tA else "chat model alone      : no probes")
print(f"chat model during sync: {tC:.1f} tok/s median over {C.get('probes')} probes" if tC else "chat model during sync: no completed probes")
if tA and tC: print(f"  -> chat slowed {100*(1-tC/tA):.0f}% while a sync ran")
print(f"sync alone            : {sB}s   ({num('B','counts')})")
print(f"sync beside chat model: {sC}s   ({num('C','counts')})")
if sB.isdigit() and sC.isdigit() and int(sB) > 0: print(f"  -> sync took {100*(int(sC)/int(sB)-1):+.0f}% with the chat model loaded")
for name, s in (("A", A), ("B", B), ("C", C)):
    print(f"phase {name}: models loaded {s.get('loaded_model_sets_seen')}  min mem free {s.get('min_mem_free_pct')}%  peak swap {s.get('max_swap_used_mb')} MB  max model load {s.get('max_load_ms')} ms")
PY
