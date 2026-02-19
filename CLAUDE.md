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

## Coding Standards

- UK English
- `go test ./...` must pass before commit
- Conventional commits: `type(scope): description`
- Co-Author: `Co-Authored-By: Virgil <virgil@lethean.io>`
