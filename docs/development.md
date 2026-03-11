---
title: Development Guide
description: How to build, test, lint, and contribute to go-ws.
---

# Development Guide

This document covers everything needed to work on `go-ws`: prerequisites, building, testing, coding standards, and contribution workflow.

## Prerequisites

- **Go 1.26** or later. Benchmarks use `b.Loop()` which requires Go 1.25+; the `go.mod` declares 1.26.
- **Redis** (optional). The `RedisBridge` integration tests connect to a Redis instance at `10.69.69.87:6379`. When Redis is unreachable, these tests are automatically skipped. You do not need Redis for general development.
- **No CGO.** The module has no C dependencies and builds on Linux, macOS, and Windows without additional toolchains.

## Building

```bash
go build ./...
```

There is no Taskfile or Makefile. All automation goes through the standard `go` toolchain or via `core go` commands if you have the Core CLI installed.

## Testing

```bash
# Full test suite
go test ./...

# With race detector (required before every commit)
go test -race ./...

# Single test by name
go test -v -run TestHub_Run ./...

# Benchmarks
go test -bench=. -benchmem ./...

# Specific benchmark
go test -bench=BenchmarkBroadcast_100 -benchmem ./...
```

### Test Organisation

Tests live in the same package (`package ws`), giving white-box access to unexported fields. Four test files exist:

| File | What it covers |
|---|---|
| `ws_test.go` | Hub lifecycle, broadcast, channel send, subscribe/unsubscribe, readPump, writePump, integration via `httptest`, auth integration, reconnecting client |
| `auth_test.go` | `APIKeyAuthenticator`, `BearerTokenAuth`, `QueryTokenAuth`, `AuthenticatorFunc`, nil authenticator, `OnAuthFailure` callback |
| `redis_test.go` | `RedisBridge` creation, lifecycle, broadcast, channel delivery, cross-bridge messaging, loop prevention, concurrent publishes, graceful shutdown, context cancellation |
| `ws_bench_test.go` | 9 benchmarks: broadcast (100 clients), channel send (50 subscribers), parallel variants, JSON marshal, WebSocket end-to-end round-trip, subscribe/unsubscribe cycle, multi-channel fanout, concurrent subscribers |

### Redis Tests

Redis integration tests call `skipIfNoRedis(t)` at the top, which pings the configured address and calls `t.Skip` if unreachable:

```go
func TestRedisBridge_PublishBroadcast(t *testing.T) {
    client := skipIfNoRedis(t)
    // ...
}
```

Each Redis test uses a unique time-based prefix (`testPrefix(t)`) to avoid key collisions between parallel runs. Cleanup runs via `t.Cleanup`.

### Integration Test Pattern

Integration tests use `httptest.NewServer` with `hub.Handler()` and connect a real `gorilla/websocket` client:

```go
server := httptest.NewServer(hub.Handler())
defer server.Close()

url := "ws" + strings.TrimPrefix(server.URL, "http")
conn, _, err := websocket.DefaultDialer.Dial(url, nil)
```

This exercises the full upgrade handshake, readPump, writePump, and message serialisation.

### Benchmark Pattern

Benchmarks use `b.Loop()` (Go 1.25+) and `b.ReportAllocs()`:

```go
func BenchmarkBroadcast_100(b *testing.B) {
    // setup ...
    b.ResetTimer()
    b.ReportAllocs()
    for b.Loop() {
        _ = hub.Broadcast(msg)
    }
    b.StopTimer()
    // drain ...
}
```

## Linting

The project uses `golangci-lint` with the configuration in `.golangci.yml`. Enabled linters include `govet`, `errcheck`, `staticcheck`, `unused`, `gosimple`, `ineffassign`, `typecheck`, `gocritic`, and `gofmt`.

```bash
# Lint
golangci-lint run ./...

# Or via Core CLI
core go lint

# Vet (also run by golangci-lint, but useful standalone)
go vet ./...
```

## Coding Standards

### UK English

Use UK English throughout: "initialise", "colour", "behaviour", "cancelled", "unauthorised", "organisation", "centre". Never American spellings.

### Licence Header

Every `.go` source file (including tests) must begin with:

```go
// SPDX-Licence-Identifier: EUPL-1.2
```

### Go Conventions

- All exported symbols must have doc comments.
- Error strings are lowercase and do not end with punctuation.
- Use named return values only when they materially clarify intent.
- Prefer `require.NoError` over `assert.NoError` when a test cannot continue after failure.
- Standard `gofmt` formatting. No additional formatter configuration.

### Formatting and Indentation

Go files use tabs (as per `gofmt`). Markdown, YAML, and JSON files use 2-space indentation (configured in `.editorconfig`).

## Commit Guidelines

Commits follow the [Conventional Commits](https://www.conventionalcommits.org/) specification:

```
type(scope): description
```

Common types: `feat`, `fix`, `test`, `docs`, `refactor`, `bench`.

Scope is the logical area: `ws`, `auth`, `redis`, `reconnect`.

Every commit includes the co-author trailer:

```
Co-Authored-By: Virgil <virgil@lethean.io>
```

Example:

```
feat(redis): add envelope pattern for loop prevention

Generated a random sourceID per bridge instance at construction.
Listener drops messages where sourceID matches local bridge.

Co-Authored-By: Virgil <virgil@lethean.io>
```

## Common Development Tasks

### Adding a New Message Type

1. Add a `MessageType` constant to the block in `ws.go`.
2. Handle the new type in `readPump` if it is client-initiated.
3. Add a helper method on `Hub` if it is server-initiated (following the pattern of `SendProcessOutput`).
4. Write tests in `ws_test.go`.
5. Update the message type table in `docs/architecture.md`.

### Adding a New Authenticator

Implement the `Authenticator` interface:

```go
type MyJWTAuth struct { /* ... */ }

func (a *MyJWTAuth) Authenticate(r *http.Request) ws.AuthResult {
    token := r.Header.Get("Authorization")
    claims, err := validateJWT(token)
    if err != nil {
        return ws.AuthResult{Valid: false, Error: err}
    }
    return ws.AuthResult{
        Valid:  true,
        UserID: claims.Subject,
        Claims: map[string]any{"roles": claims.Roles},
    }
}
```

Pass it to `HubConfig.Authenticator`. The existing `APIKeyAuthenticator`, `BearerTokenAuth`, and `QueryTokenAuth` in `auth.go` serve as reference implementations. JWT libraries are intentionally not imported; consumers bring their own.

### Running the Full QA Suite

If you have the Core CLI installed:

```bash
core go qa          # fmt + vet + lint + test
core go qa full     # + race, vuln, security
```

Otherwise, run each step manually:

```bash
gofmt -l .
go vet ./...
golangci-lint run ./...
go test -race ./...
```

## Repository

| | |
|---|---|
| **Forge** | `ssh://git@forge.lthn.ai:2223/core/go-ws.git` |
| **Push** | SSH only; HTTPS authentication is not configured on Forge |
| **Licence** | EUPL-1.2 |
