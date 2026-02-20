# CLAUDE.md

Module: `forge.lthn.ai/core/go-ws`

## Commands

```bash
go test ./...              # Run all tests
go test -race ./...        # Race detector (required before commit)
go test -v -run Name ./... # Run single test
go test -bench=. -benchmem ./... # Benchmarks
```

## Coding Standards

- UK English throughout
- SPDX-Licence-Identifier: EUPL-1.2 header in every .go file
- `go vet ./...` and `go test -race ./...` must be clean before commit
- Conventional commits: `type(scope): description`
- Co-Author trailer: `Co-Authored-By: Virgil <virgil@lethean.io>`

## Key Interfaces

- `Authenticator` — implement for custom auth (JWT, OAuth, etc.)
- `HubConfig` — configures heartbeat, pong timeout, write timeout, callbacks, authenticator
- `ReconnectConfig` — configures client-side backoff and callbacks

## Documentation

See `docs/` for full reference:
- `docs/architecture.md` — hub pattern, channels, auth, Redis bridge, envelope/loop-prevention
- `docs/development.md` — prerequisites, test patterns, coding standards
- `docs/history.md` — completed phases with commit hashes, known limitations
