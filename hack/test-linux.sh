#!/bin/sh
# Runs go build/vet/test for depctl inside an Alpine Linux container via
# Podman. Useful for a quick cross-platform sanity check (e.g. XDG path
# behavior) without needing a real Linux machine.
#
# Also forwards the container's localhost:11434/:6333 to the host's real
# Ollama/Qdrant (reachable from inside a Podman-machine container as
# host.containers.internal) via socat, best-effort: without this, the
# handful of tests that exercise a real embedder/vector backend
# (TestDaemonQueryServiceRoundTripsThroughRealDaemon,
# TestServeOverRealStdioTransport) fail with a readiness-gate error
# instead of testing what they're meant to, since the container is
# otherwise network-isolated from the host's loopback. If the host
# isn't running Ollama/Qdrant, those specific tests still fail the same
# way they always would — this doesn't hide that.
#
# Usage: hack/test-linux.sh [extra shell commands to run after the suite]
set -eu

cd "$(dirname "$0")/.."

GO_IMAGE="${DEPCTL_TEST_GO_IMAGE:-golang:1.25-alpine}"

podman run --rm -v "$PWD":/src:Z -w /src "$GO_IMAGE" sh -c "
set -e
apk add --no-cache git socat >/dev/null
socat TCP-LISTEN:11434,fork,reuseaddr TCP:host.containers.internal:11434 2>/dev/null &
socat TCP-LISTEN:6333,fork,reuseaddr TCP:host.containers.internal:6333 2>/dev/null &
sleep 1
go build ./...
go vet ./...
go test ./...
${1:-}
"
