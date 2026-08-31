#!/bin/sh
# Runs go build/vet/test for ragctl inside an Alpine Linux container via
# Podman. Useful for a quick cross-platform sanity check (e.g. XDG path
# behavior) without needing a real Linux machine.
#
# Usage: hack/test-linux.sh [extra shell commands to run after the suite]
set -eu

cd "$(dirname "$0")/.."

GO_IMAGE="${RAGCTL_TEST_GO_IMAGE:-golang:1.25-alpine}"

podman run --rm -v "$PWD":/src:Z -w /src "$GO_IMAGE" sh -c "
set -e
apk add --no-cache git >/dev/null
go build ./...
go vet ./...
go test ./...
${1:-}
"
