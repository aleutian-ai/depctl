#!/bin/sh
# Builds the depctl image and runs it as a container, with a named Podman
# volume for persistence (config + data both live under $HOME inside the
# container, see the Dockerfile).
#
# Usage: hack/run.sh [--no-build] [depctl args...]
#   hack/run.sh init
#   hack/run.sh status
#   hack/run.sh --no-build status   # skip the rebuild, reuse the last image
#   hack/run.sh                     # defaults to --help
set -eu

cd "$(dirname "$0")/.."

IMAGE="${DEPCTL_IMAGE:-depctl:dev}"
VOLUME="${DEPCTL_VOLUME:-depctl-data}"

if [ "${1:-}" = "--no-build" ]; then
    shift
else
    podman build -t "$IMAGE" .
fi

podman run --rm -it -v "$VOLUME":/home/depctl "$IMAGE" "$@"
