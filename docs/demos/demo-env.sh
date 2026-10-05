# Source this before any demo: `source docs/demos/demo-env.sh`
#
# Runs ragctl in a throwaway sandbox under /tmp/ragctl-demo, so demos never
# touch your real ragctl install. Your real install keeps running; the demo
# gets its own daemon, config, and data.

DEMO_ROOT=/tmp/ragctl-demo

# Podman finds its machine through $HOME, so resolve the socket before
# switching HOME to the sandbox (macOS; on Linux this is a no-op).
if [ -z "${CONTAINER_HOST:-}" ] && command -v podman >/dev/null 2>&1; then
  _sock=$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}' 2>/dev/null | head -1)
  if [ -n "$_sock" ]; then
    export CONTAINER_HOST="unix://$_sock" DOCKER_HOST="unix://$_sock"
  fi
fi

mkdir -p "$DEMO_ROOT/home" "$DEMO_ROOT/data" "$DEMO_ROOT/config"
export HOME="$DEMO_ROOT/home" XDG_DATA_HOME="$DEMO_ROOT/data" XDG_CONFIG_HOME="$DEMO_ROOT/config"

# ragctl's own files in the sandbox (macOS layout; Linux uses $XDG_DATA_HOME/ragctl).
if [ "$(uname)" = Darwin ]; then
  DEMO_RAGCTL_DIR="$HOME/Library/Application Support/ragctl"
else
  DEMO_RAGCTL_DIR="$XDG_DATA_HOME/ragctl"
fi

# demo_project <version>: a tiny Go project depending on github.com/google/uuid.
demo_project() {
  local v=${1:-v1.6.0} sum
  case "$v" in
    v1.6.0) sum=h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0= ;;
    v1.5.0) sum=h1:1p67kYwdtXjb0gL0BPiP1Av9wiZPo5A8z2cWkTZ+eyU= ;;
    v1.4.0) sum=h1:MtMxsa51/r9yyhkyLsVeVt0B+BGQZzpQiTQ4eHZ8bc4= ;;
    *) echo "demo_project: use v1.4.0, v1.5.0 or v1.6.0" >&2; return 1 ;;
  esac
  local dir="$DEMO_ROOT/proj-$v"
  mkdir -p "$dir"
  printf 'module example.com/demo\n\ngo 1.21\n\nrequire github.com/google/uuid %s\n' "$v" > "$dir/go.mod"
  printf 'github.com/google/uuid %s %s\ngithub.com/google/uuid %s/go.mod h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo=\n' "$v" "$sum" "$v" > "$dir/go.sum"
  printf 'package main\n\nimport "github.com/google/uuid"\n\nfunc main() { _ = uuid.New() }\n' > "$dir/main.go"
  echo "$dir"
}

# demo_project_id <dir>: the ragctl project ID for a scanned directory.
demo_project_id() {
  local dir
  dir=$(cd "$1" && pwd -P)
  ragctl project list | awk -v r="$dir" '$2==r {print $1}'
}

# demo_search <project-id> "<question>": search ragctl's own index the way
# an agent's search_dependency_docs call does, via the daemon's local API.
demo_search() {
  curl -s --unix-socket "$DEMO_RAGCTL_DIR/ragctld.sock" -X POST http://ragctl/v1/search \
    -H 'Content-Type: application/json' \
    -d "{\"project_id\":\"$1\",\"text\":\"$2\",\"dependency\":\"github.com/google/uuid\",\"mode\":\"project\",\"top_k\":3}" |
    python3 -c 'import json,sys; d=json.load(sys.stdin); [print(c["version"], "|", c["content"][:90].replace("\n"," ")) for c in d.get("chunks") or []] or print(d)'
}

# demo_reset: stop the sandbox daemon and delete the sandbox.
demo_reset() {
  ragctl daemon stop >/dev/null 2>&1
  chmod -R u+w "$DEMO_ROOT" 2>/dev/null
  rm -rf "$DEMO_ROOT"
  echo "sandbox removed; open a new shell (HOME is still pointed at it in this one)"
}

echo "ragctl demo sandbox: $DEMO_ROOT (helpers: demo_project, demo_project_id, demo_search, demo_reset)"
