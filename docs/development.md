# go-ws Development Guide

Module: `forge.lthn.ai/core/go-ws`

---

## Prerequisites

- Go 1.25 or later (benchmarks use `b.Loop()`, which requires 1.25+)
- A running Redis instance if exercising `RedisBridge` integration tests (default: `10.69.69.87:6379`)
- No CGO dependencies; builds on Linux, macOS, and Windows without toolchain additions

---

## Build and Test

```bash
# Run the full test suite
go test ./...

# Run with race detector (required before every commit)
go test -race ./...

# Run a single test by name
go test -v -run TestHub_Run ./...

# Run benchmarks
go test -bench=. -benchmem ./...

# Run a specific benchmark
go test -bench=BenchmarkBroadcast_100 -benchmem ./...
```

There is no Taskfile in this module. All automation goes through the standard `go` toolchain.

---

## Test Organisation

Tests live in the same package (`package ws`), giving direct access to unexported fields for white-box assertions. Three test files exist:

| File | Coverage |
|---|---|
| `ws_test.go` | Hub lifecycle, broadcast, channel send, subscribe/unsubscribe, readPump, writePump, integration via httptest, auth integration |
| `auth_test.go` | `APIKeyAuthenticator`, `AuthenticatorFunc`, nil authenticator, `OnAuthFailure` callback |
| `redis_test.go` | `RedisBridge` unit and integration (skipped when Redis unavailable) |
| `ws_bench_test.go` | 9 benchmarks: broadcast, channel send, parallel variants, marshal, WebSocket end-to-end, subscribe/unsubscribe cycle, multi-channel fanout |

### Test Naming Convention

Integration tests use `httptest.NewServer` + `gorilla/websocket` Dial for end-to-end coverage. Unit tests drive the Hub directly via channels and method calls.

### Redis Tests

Redis integration tests call `skipIfNoRedis(t)` at the top, which pings the configured address and skips if unreachable:

```go
func TestRedisBridge_PublishBroadcast(t *testing.T) {
    client := skipIfNoRedis(t)
    // ...
}
```

Each Redis test uses a unique time-based prefix to avoid collisions between parallel runs. Cleanup removes any leftover keys in `t.Cleanup`.

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

---

## Coding Standards

### Language

UK English throughout: "initialise", "colour", "behaviour", "cancelled", "unauthorised". Never American spellings.

### Go Style

- `declare(strict_types=1)` is a PHP concept; in Go, enforce correctness via the type system and `go vet`.
- All exported symbols must have doc comments.
- Error strings are lowercase and do not end with punctuation (Go convention).
- Use named return values only when they materially clarify intent.
- Prefer `require.NoError` over `assert.NoError` when a test cannot continue after failure.

### Licence Header

Every `.go` source file begins with:

```go
// SPDX-Licence-Identifier: EUPL-1.2
```

This applies to all files including test files.

### Formatting

Standard `gofmt`. No additional linter configuration is committed. Run `go vet ./...` before committing; the suite must be clean.

---

## Commit Guidelines

Commits follow Conventional Commits:

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

---

## Adding a New Message Type

1. Add a constant to the `MessageType` block in `ws.go`.
2. Handle the new type in `readPump` if it is client-initiated.
3. Add a helper method on `Hub` if it is server-initiated (following the pattern of `SendProcessOutput`).
4. Add unit tests covering the new type in `ws_test.go`.
5. Update `docs/architecture.md` message type table.

---

## Adding a New Authenticator

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

Pass it to `HubConfig.Authenticator`. The built-in `APIKeyAuthenticator` in `auth.go` serves as a reference implementation. JWT validation libraries are intentionally not imported; consumers bring their own.

---

## Repository

- Forge: `ssh://git@forge.lthn.ai:2223/core/go-ws.git`
- Push via SSH only; HTTPS authentication is not configured on Forge.
- Licence: EUPL-1.2
