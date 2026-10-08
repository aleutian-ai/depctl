#!/bin/sh
# Runs INSIDE the Podman container started by hack/ollama-smoke.sh: builds
# nothing, just syncs a fixed list of dependencies against the host's
# Ollama and Qdrant (forwarded with socat) from a cold embedding cache, and
# records how long it took. Git mirrors persist across phases (a symlink to
# /out/git) so network variance doesn't pollute the comparison; everything
# else (Badger, embedding cache, Qdrant collection) starts empty each run.
set -eu
apk add --no-cache git socat curl >/dev/null 2>&1
socat TCP-LISTEN:11434,fork,reuseaddr TCP:host.containers.internal:11434 2>/dev/null &
socat TCP-LISTEN:6333,fork,reuseaddr TCP:host.containers.internal:6333 2>/dev/null &
sleep 1

export GOMODCACHE=/out/gomodcache GOTOOLCHAIN=local HOME=/tmp/smoke_home
rm -rf "$HOME"; mkdir -p "$HOME/.local/share/depctl" /out/git
ln -s /out/git "$HOME/.local/share/depctl/git"
/out/depctl init >/dev/null 2>&1
CFG="$HOME/.config/depctl/config.yaml"
sed -i "s/managed: true/managed: false/" "$CFG"
sed -i "s/max_concurrency: .*/max_concurrency: ${CONC:-2}/" "$CFG"
sed -i "s/disable_ambient: false/disable_ambient: true/" "$CFG"

curl -s -X DELETE http://127.0.0.1:6333/collections/depctl >/dev/null
/out/depctl scan /project >/dev/null 2>&1

FLAGS=""
while IFS= read -r dep; do
  [ -n "$dep" ] && FLAGS="$FLAGS --dependency $dep"
done < "/out/${DEPS_FILE}"

START=$(date +%s)
# shellcheck disable=SC2086
/out/depctl sync $FLAGS > "/out/${PHASE}.sync.log" 2>&1 || true
echo $(( $(date +%s) - START )) > "/out/${PHASE}.elapsed"
OK=$(grep -c "^OK " "/out/${PHASE}.sync.log" || true)
FAIL=$(grep -c "^FAIL " "/out/${PHASE}.sync.log" || true)
POINTS=$(curl -s http://127.0.0.1:6333/collections/depctl | sed -n 's/.*"points_count":\([0-9]*\).*/\1/p')
echo "ok=$OK fail=$FAIL points=${POINTS:-0}" > "/out/${PHASE}.counts"
/out/depctl daemon stop >/dev/null 2>&1 || true
