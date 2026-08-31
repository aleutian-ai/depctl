# Multi-stage build: compile a static ragctl binary, then run it on a
# minimal Alpine base as a non-root user.
#
# NOTE: as later epics land (git acquisition, language resolvers, etc.) this
# runtime stage will need matching host tools (git, go, npm/pnpm, cargo,
# mvn/gradle...) added via `apk add`. Keep it minimal until a ticket
# actually needs a given tool — don't pre-install for milestones that don't
# exist yet.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ragctl ./cmd/ragctl

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && adduser -D -h /home/ragctl ragctl
COPY --from=build /out/ragctl /usr/local/bin/ragctl

USER ragctl
ENV HOME=/home/ragctl
WORKDIR /home/ragctl
# Everything ragctl writes (config + data, per internal/config/paths.go's
# Linux XDG defaults) lives under $HOME — mount one volume here for
# persistence across container runs.
VOLUME ["/home/ragctl"]

ENTRYPOINT ["ragctl"]
CMD ["--help"]
