# AGENTS.md

This repository is the `dappco.re/go/ws` WebSocket package. It is a single
Go package named `ws` with no subpackages. Keep changes local to the package
root unless documentation under `docs/` is part of the task.

## Repository Shape

The package is split by responsibility:

- `ws.go` contains the hub, client lifecycle, message types, channel
  subscription logic, HTTP upgrade handler, and reconnecting client.
- `auth.go` contains the authentication result model, API key auth, bearer
  token auth, query token auth, and claim cloning helpers.
- `redis.go` contains the Redis pub/sub bridge used to mirror hub broadcasts
  across processes.
- `errors.go` contains exported sentinel errors used by authentication and
  subscription validation.

Tests are white-box and remain in `package ws`. Public-symbol compliance tests
belong in the sibling `<file>_test.go` file using
`Test<File>_<Symbol>_{Good,Bad,Ugly}`. Public examples belong in the sibling
`<file>_example_test.go` file and should print with `dappco.re/go` helpers, not
`fmt`.

## Local Commands

Run verification with workspace mode disabled:

```bash
GOWORK=off go mod tidy
GOWORK=off go vet ./...
GOWORK=off go test -count=1 ./...
gofmt -l .
bash /Users/snider/Code/core/go/tests/cli/v090-upgrade/audit.sh .
```

The audit script is the compliance contract for the v0.9.0 migration. Do not
replace direct stdlib imports with local stdlib-shaped shim packages; use the
core wrappers from `dappco.re/go` directly.

## Development Notes

The hub owns connection state and serialises state changes through channels in
`Hub.Run`. Tests that need a live hub should start `Run` with a cancellable
context and wait for `isRunning` before asserting on registration or delivery.

Redis integration tests must skip when Redis at `10.69.69.87:6379` is not
available. Do not make Redis mandatory for the ordinary package test run.

The module uses EUPL-1.2 licensing. Keep the SPDX header on new Go files:

```go
// SPDX-Licence-Identifier: EUPL-1.2
```
