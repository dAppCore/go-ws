---
title: go-ws
description: WebSocket hub for real-time streaming in Go, with channel pub/sub, token authentication, reconnecting clients, and a Redis bridge for multi-instance coordination.
---

# go-ws

`go-ws` is a WebSocket hub library for Go. It implements centralised connection management using the hub pattern, named channel pub/sub, optional token-based authentication at upgrade time, a client-side reconnecting wrapper with exponential backoff, and a Redis pub/sub bridge that coordinates broadcasts across multiple hub instances.

| | |
|---|---|
| **Module** | `forge.lthn.ai/core/go-ws` |
| **Go version** | 1.26+ |
| **Licence** | EUPL-1.2 |
| **Repository** | `ssh://git@forge.lthn.ai:2223/core/go-ws.git` |

## Quick Start

```go
package main

import (
    "context"
    "net/http"

    "forge.lthn.ai/core/go-ws"
)

func main() {
    hub := ws.NewHub()
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go hub.Run(ctx)

    http.HandleFunc("/ws", hub.Handler())
    http.ListenAndServe(":8080", nil)
}
```

Once running, clients connect via WebSocket and can subscribe to named channels. The server pushes messages to all connected clients or to subscribers of a specific channel.

### Sending Messages

```go
// Broadcast to every connected client.
hub.SendEvent("deploy:started", map[string]any{"env": "production"})

// Send process output to subscribers of "process:build-42".
hub.SendProcessOutput("build-42", "Compiling main.go...")

// Send a process status change.
hub.SendProcessStatus("build-42", "exited", 0)
```

### Adding Authentication

```go
auth := ws.NewAPIKeyAuth(map[string]string{
    "secret-key-1": "user-alice",
    "secret-key-2": "user-bob",
})

hub := ws.NewHubWithConfig(ws.HubConfig{
    Authenticator: auth,
    OnAuthFailure: func(r *http.Request, result ws.AuthResult) {
        log.Printf("rejected connection from %s: %v", r.RemoteAddr, result.Error)
    },
})
go hub.Run(ctx)
```

Clients connect with `Authorization: Bearer <key>`. Without a valid key, the upgrade is rejected with HTTP 401. When no `Authenticator` is set, all connections are accepted.

### Redis Bridge for Multi-Instance Deployments

```go
bridge, err := ws.NewRedisBridge(hub, ws.RedisConfig{
    Addr:   "localhost:6379",
    Prefix: "ws",
})
if err != nil {
    log.Fatal(err)
}
if err := bridge.Start(ctx); err != nil {
    log.Fatal(err)
}
defer bridge.Stop()

// Messages published via the bridge reach clients on all instances.
bridge.PublishBroadcast(ws.Message{Type: ws.TypeEvent, Data: "hello from instance A"})
bridge.PublishToChannel("process:build-42", ws.Message{
    Type: ws.TypeProcessOutput,
    Data: "output line",
})
```

## Package Layout

The entire library lives in a single Go package (`ws`). There are no sub-packages.

| File | Purpose |
|---|---|
| `ws.go` | Hub, Client, Message, ReconnectingClient, connection pumps, channel subscription |
| `auth.go` | Authenticator interface, AuthResult, APIKeyAuthenticator, BearerTokenAuth, QueryTokenAuth |
| `errors.go` | Sentinel authentication errors |
| `redis.go` | RedisBridge, RedisConfig, envelope pattern for loop prevention |
| `ws_test.go` | Hub lifecycle, broadcast, channel, subscription, and integration tests |
| `auth_test.go` | Authentication unit and integration tests |
| `redis_test.go` | Redis bridge integration tests (skipped when Redis is unavailable) |
| `ws_bench_test.go` | 9 benchmarks covering broadcast, channel send, subscribe/unsubscribe, fanout, and end-to-end WebSocket round-trips |

## Dependencies

| Module | Version | Role |
|---|---|---|
| `github.com/gorilla/websocket` | v1.5.3 | WebSocket server and client implementation |
| `github.com/redis/go-redis/v9` | v9.18.0 | Redis pub/sub bridge (runtime opt-in) |
| `github.com/stretchr/testify` | v1.11.1 | Test assertions (test-only) |

The Redis dependency is a compile-time import but a runtime opt-in. Applications that never create a `RedisBridge` incur no Redis connections. There are no CGO requirements; the module builds cleanly on Linux, macOS, and Windows.

## Further Reading

- [Architecture](architecture.md) -- Hub pattern, channels, authentication, Redis bridge, concurrency model
- [Development Guide](development.md) -- Building, testing, coding standards, contribution workflow
