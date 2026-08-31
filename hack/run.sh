#!/bin/sh
# Builds the ragctl image and runs it as a container, with a named Podman
# volume for persistence (config + data both live under $HOME inside the
# container, see the Dockerfile).
#
# Usage: hack/run.sh [--no-build] [ragctl args...]
#   hack/run.sh init
#   hack/run.sh status
#   hack/run.sh --no-build status   # skip the rebuild, reuse the last image
#   hack/run.sh                     # defaults to --help
set -eu

cd "$(dirname "$0")/.."

IMAGE="${RAGCTL_IMAGE:-ragctl:dev}"
VOLUME="${RAGCTL_VOLUME:-ragctl-data}"

if [ "${1:-}" = "--no-build" ]; then
    shift
else
    podman build -t "$IMAGE" .
fi

podman run --rm -it -v "$VOLUME":/home/ragctl "$IMAGE" "$@"
