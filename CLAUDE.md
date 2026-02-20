# CLAUDE.md

## What This Is

WebSocket hub with channel-based pub/sub for real-time streaming. Module: `forge.lthn.ai/core/go-ws`

## Commands

```bash
go test ./...          # Run all tests
go test -v -run Name   # Run single test
```

## Architecture

- `Hub` manages WebSocket connections and channel subscriptions
- Messages types: `process_output`, `process_status`, `event`, `error`, `ping/pong`, `subscribe/unsubscribe`
- `hub.SendProcessOutput(id, line)` broadcasts to subscribers
- `HubConfig` provides configurable heartbeat, pong timeout, write timeout, and connection callbacks
- `Authenticator` interface for token-based auth on upgrade — nil means all connections accepted (backward compat)
- `APIKeyAuthenticator` validates `Authorization: Bearer <key>` against a static key→userID map
- `BearerTokenAuth` validates `Authorization: Bearer <token>` via caller-supplied function (JWT, introspection, etc.)
- `QueryTokenAuth` validates `?token=<value>` query parameter (for browser WebSocket clients)
- `AuthenticatorFunc` adapter lets plain functions satisfy the `Authenticator` interface
- `Client.UserID` and `Client.Claims` populated during authenticated upgrade
- `OnAuthFailure` callback on `HubConfig` for logging/metrics on rejected connections
- `ReconnectingClient` provides client-side reconnection with exponential backoff
- `ConnectionState`: `StateDisconnected`, `StateConnecting`, `StateConnected`
- Coverage: 95.9%

## Coding Standards

- UK English
- `go test ./...` must pass before commit
- Conventional commits: `type(scope): description`
- Co-Author: `Co-Authored-By: Virgil <virgil@lethean.io>`
