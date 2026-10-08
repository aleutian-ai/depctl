# Epic: Bootstrap

Establish a buildable, CI-checked repository before any application logic exists. This is pure scaffolding: directory layout, required project files, a pinned Go toolchain version, and a CI baseline running `go test`/`go vet`/`go fmt`. Nothing here should require future milestones to be rewritten.

## Tickets
- [BOOT-001 — Create repository skeleton](BOOT-001-create-repository-skeleton.md): directory layout, root project files, minimal `cmd/depctl/main.go`.
- [BOOT-002 — Lock Go toolchain version](BOOT-002-lock-go-toolchain.md): pin one Go version across `go.mod`, README, and CI.
- [BOOT-003 — Add CI baseline](BOOT-003-baseline-ci.md): GitHub Actions workflow running test/vet/fmt (and optionally race) on every push/PR.
