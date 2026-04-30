[![Go Reference](https://pkg.go.dev/badge/forge.lthn.ai/core/go-ws.svg)](https://pkg.go.dev/forge.lthn.ai/core/go-ws)
[![License: EUPL-1.2](https://img.shields.io/badge/License-EUPL--1.2-blue.svg)](LICENSE.md)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat&logo=go)](go.mod)

# go-ws

WebSocket hub for real-time streaming in Go. Implements the hub pattern with centralised connection management, named channel pub/sub, token-based authentication on upgrade, client-side reconnection with exponential backoff, and a Redis pub/sub bridge for coordinating broadcasts across multiple hub instances. The envelope pattern with a per-bridge source ID prevents loop amplification when the Redis bridge is in use.

**Module**: `forge.lthn.ai/core/go-ws`
**Licence**: EUPL-1.2
**Language**: Go 1.25

## Quick Start

```go
import "forge.lthn.ai/core/go-ws"

hub := ws.NewHub()
go hub.Run(ctx)

// Mount on any HTTP mux
http.HandleFunc("/ws", hub.Handler())

// Send process output to subscribers of "process:abc"
hub.SendProcessOutput("abc", "output line")

// Redis bridge for multi-instance coordination
bridgeResult := ws.NewRedisBridge(hub, ws.RedisConfig{Addr: "localhost:6379"})
if !bridgeResult.OK {
    return bridgeResult
}
bridge := bridgeResult.Value.(*ws.RedisBridge)
if r := bridge.Start(ctx); !r.OK {
    return r
}
```

## Documentation

- [Architecture](docs/architecture.md) — hub pattern, channel subscriptions, authentication, Redis bridge, envelope loop prevention
- [Development Guide](docs/development.md) — prerequisites, test patterns, coding standards
- [Project History](docs/history.md) — completed phases with commit hashes, known limitations

## Build & Test

```bash
go test ./...
go test -race ./...
go test -bench=. -benchmem ./...
go build ./...
```

## Licence

European Union Public Licence 1.2 — see [LICENCE](LICENCE) for details.
