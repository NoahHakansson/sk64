# Contributing

Thanks for your interest. sk64 is a tool people point at production secrets, so the bar for changes is correctness first, simplicity second.

## Getting set up

You need Go 1.26 or newer. Everything else is optional.

```sh
git clone https://github.com/NoahHakansson/sk64
cd sk64
go build ./...
go test -race ./...
```

`helm` and `kustomize` are optional: the repo scanner renders with them when present and falls back to literal extraction when absent. Tests that need the real binaries skip with a message when they're missing; the exec boundary itself is tested with stub executables, so a full local run needs neither.

## Before you open a pull request

```sh
gofmt -l .              # must print nothing
go build ./...
go vet ./...
go test -race ./...
(cd e2e && go test -race ./...)
golangci-lint run       # must report 0 issues, in the root and in e2e/
(cd e2e && golangci-lint run)
./hack/covergate.sh     # ≥85% on k8s, store, project, editor, diff
goreleaser check
```

CI runs all of the above plus `go mod verify`, `govulncheck`, a short fuzz run, cross-compilation for linux and darwin on amd64 and arm64, and an end-to-end job that starts its own throwaway `kube-apiserver`+`etcd` control plane on loopback. Root-module test binaries that can load Kubernetes clients isolate kubeconfig and disable the in-cluster fallback process-wide; the separate e2e module accepts only the loopback `rest.Config` produced by its harness. No test in this repository can reach a cluster you own; see `AGENTS.md`.

## What we look for

**Tests ship with the change, not after it.** New reference locations, error paths, and screens each need a test. TUI screens are covered by golden files — drive the model with synthetic messages and assert on `View()`:

```sh
go test ./internal/tui -update   # regenerate goldens after an intentional change
```

Review the golden diff before committing it. A golden that changed unexpectedly is a bug report.

**Simplicity is a requirement.** The smallest clear implementation wins. No speculative abstraction, no wrapper layers with one call site, no options structs nobody asked for. "Delete this" is a valid review outcome.

**Errors are wrapped with `%w` and useful context**, matched with `errors.Is`/`errors.As` rather than string comparison. No `TODO`s, no stubbed error paths, no `panic` outside genuinely unreachable code.

**Never block the Bubble Tea event loop.** Every cluster call and every SQLite call runs as a `tea.Cmd` returning a message, through the shared loader in `internal/tui/loader.go`, and long operations are cancellable with `esc`.

## The two absolute rules

Both are enforced by tests that reflect over package APIs and fail on any method able to accept value bytes:

1. **No secret value ever reaches the SQLite store.**
2. **No secret value ever reaches the debug log.**

If a change needs to break either rule, it needs a different design.

## Dependencies

New dependencies need justification: actively maintained, no advisories, no cheaper standard-library alternative. Copy import paths verbatim from an authoritative source rather than typing them from memory — typosquatting is the realistic threat in the Go ecosystem. `go.sum` is a lockfile; unexplained diffs will be questioned.

## Platforms

sk64 targets Linux and macOS. Windows is explicitly not supported — please don't add build tags, fallbacks, or CI targets for it.

## Commits

Conventional commit prefixes (`feat:`, `fix:`, `docs:`, `test:`, `chore:`) group nicely in generated release notes. Keep the subject line about what changed and why, not how.
