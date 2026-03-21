# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Module: `dappco.re/go/core/ws`

## Commands

```bash
go build ./...                          # Build
go test ./...                           # Run all tests
go test -race ./...                     # Race detector (required before commit)
go test -v -run TestName ./...          # Run single test
go test -bench=. -benchmem ./...        # Benchmarks
go vet ./...                            # Vet (must be clean before commit)
golangci-lint run ./...                 # Lint (config in .golangci.yml)
```

Full QA sequence before commit: `gofmt -l . && go vet ./... && golangci-lint run ./... && go test -race ./...`

## Coding Standards

- UK English throughout: "initialise", "colour", "behaviour", "cancelled", "unauthorised"
- `// SPDX-Licence-Identifier: EUPL-1.2` header in every `.go` file (including tests)
- All exported symbols must have doc comments
- Conventional commits: `type(scope): description` — scopes: `ws`, `auth`, `redis`, `reconnect`
- Co-Author trailer: `Co-Authored-By: Virgil <virgil@lethean.io>`

## Architecture

Single flat package `ws` — all code lives in the package root with no sub-packages.

**Hub** (`ws.go`) — central broker using the hub pattern. `Hub.Run(ctx)` is a single-goroutine select loop that serialises all state mutations through Go channels (`register`, `unregister`, `broadcast`). Each connected client runs two goroutines: `readPump` (inbound frames, subscribe/unsubscribe dispatch, pong timeout) and `writePump` (outbound drain with frame batching, server-side ping heartbeat). Clients subscribe to named channels (e.g. `process:<id>`) for targeted message delivery.

**Auth** (`auth.go`, `errors.go`) — optional `Authenticator` interface checked during HTTP upgrade, before the WebSocket connection is established. Built-in implementations: `APIKeyAuthenticator`, `BearerTokenAuth`, `QueryTokenAuth`, `AuthenticatorFunc`. When `HubConfig.Authenticator` is nil, all connections are accepted.

**Redis bridge** (`redis.go`) — `RedisBridge` connects a Hub to Redis pub/sub for cross-instance coordination. Uses an envelope pattern with a per-bridge random `sourceID` to prevent infinite echo loops. Redis channels: `{prefix}:broadcast` and `{prefix}:channel:{name}`.

**Reconnecting client** (`ws.go`) — `ReconnectingClient` wraps a gorilla/websocket connection with automatic reconnection using exponential backoff. Client-side component, blocks on `Connect(ctx)`.

## Testing Notes

- Tests are white-box (same `package ws`), using `httptest.NewServer` + real gorilla/websocket clients for integration tests
- Redis tests auto-skip via `skipIfNoRedis(t)` when Redis at `10.69.69.87:6379` is unreachable — Redis is not required for general development
- Benchmarks use `b.Loop()` (Go 1.24+)
- Use `require.NoError` over `assert.NoError` when a test cannot continue after failure

## Documentation

See `docs/` for detailed reference:
- `docs/architecture.md` — full internal design, concurrency model, known limitations
- `docs/development.md` — prerequisites, test patterns, contribution workflow
- `docs/history.md` — completed phases with commit hashes
