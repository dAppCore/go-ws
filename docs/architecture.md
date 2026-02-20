# go-ws Architecture

Module: `forge.lthn.ai/core/go-ws`

---

## Overview

go-ws is a WebSocket hub for real-time streaming in Go. It implements the hub pattern for centralised connection management with channel-based pub/sub delivery, token-based authentication on upgrade, client-side reconnection with exponential backoff, and a Redis pub/sub bridge for coordinating multiple hub instances.

---

## Hub Pattern

The `Hub` struct is the central broker. It owns all connection state and serialises mutations through goroutine-safe channels:

```
                    ┌─────────────────────────────┐
                    │             Hub             │
  HTTP upgrade ──►  │  register   chan *Client    │
  disconnect   ──►  │  unregister chan *Client    │
  server send  ──►  │  broadcast  chan []byte     │
                    │                             │
                    │  clients  map[*Client]bool  │
                    │  channels map[string]map…   │
                    └─────────────────────────────┘
```

`Hub.Run(ctx)` is a single select-loop goroutine that processes all state transitions. Mutations to `clients` and `channels` occur only inside `Run`, protected by `sync.RWMutex` for reads from concurrent senders. This eliminates the need for channel-specific mutexes on write paths, while `SendToChannel` uses `RLock` plus a client-slice copy to prevent iterator invalidation races.

### Lifecycle

1. Call `hub.Run(ctx)` in a goroutine before accepting connections.
2. Mount `hub.Handler()` on any `http.ServeMux` or router.
3. Cancel the context to shut down: `Run` closes all client send channels, which causes `writePump` goroutines to send a WebSocket close frame and exit.

---

## Connection Management

Each connected client is represented by a `Client`:

```go
type Client struct {
    hub           *Hub
    conn          *websocket.Conn
    send          chan []byte       // buffered, capacity 256
    subscriptions map[string]bool
    mu            sync.RWMutex

    UserID string
    Claims map[string]any
}
```

On upgrade, `Handler` creates a `Client`, sends it to `hub.register`, then starts two goroutines:

- `readPump` — reads inbound frames, dispatches subscribe/unsubscribe/ping, enforces pong timeout.
- `writePump` — drains `client.send`, batches queued frames into a single write, sends server-side ping on heartbeat tick.

On disconnect (read error, write error, or context cancel), `readPump` sends the client to `hub.unregister`. `Run` removes it from `clients` and all channel maps, then closes `client.send`. Closing `client.send` is the signal that causes `writePump` to send a close frame and exit.

### Buffer Overflow

Each client's `send` channel has capacity 256. When `Broadcast` or `SendToChannel` cannot deliver to a full channel, the client is considered stalled. For broadcasts, `Run` schedules an unregister via a goroutine. For `SendToChannel`, the message is silently dropped for that client only.

### Timing Defaults

| Constant | Default | Purpose |
|---|---|---|
| `DefaultHeartbeatInterval` | 30 s | Server-side ping cadence |
| `DefaultPongTimeout` | 60 s | Read deadline after each pong |
| `DefaultWriteTimeout` | 10 s | Write deadline per frame |

All three are configurable via `HubConfig`.

---

## Channel Subscriptions

Channels are named strings; there are no predefined names. Clients subscribe by sending a JSON message:

```json
{"type": "subscribe", "data": "process:proc-1"}
```

`readPump` intercepts subscribe/unsubscribe frames and calls `hub.Subscribe` / `hub.Unsubscribe` directly. The hub maintains two parallel indices:

- `hub.channels[channelName][*Client]` — for targeted send
- `client.subscriptions[channelName]` — for cleanup on disconnect

Both indices are kept consistent. Unsubscribing from a channel with no remaining subscribers removes the channel entry entirely.

### Sending to a Channel

```go
hub.SendToChannel("process:proc-1", ws.Message{
    Type: ws.TypeProcessOutput,
    Data: "output line",
})
```

`SendToChannel` acquires `RLock`, copies the subscriber slice, releases the lock, then delivers to each client's `send` channel. The copy-under-lock pattern is critical: it prevents a data race with `hub.unregister` which mutates the map under write lock concurrently.

### Process Helpers

Two convenience methods wrap `SendToChannel` with idiomatic channel naming (`process:<id>`):

```go
hub.SendProcessOutput(processID, line)
hub.SendProcessStatus(processID, "exited", exitCode)
```

---

## Message Types

All frames are JSON-encoded `Message` structs:

```go
type Message struct {
    Type      MessageType `json:"type"`
    Channel   string      `json:"channel,omitempty"`
    ProcessID string      `json:"processId,omitempty"`
    Data      any         `json:"data,omitempty"`
    Timestamp time.Time   `json:"timestamp"`
}
```

| Type | Direction | Purpose |
|---|---|---|
| `process_output` | server → client | Real-time subprocess stdout/stderr line |
| `process_status` | server → client | Process state change (`running`, `exited`) with exit code |
| `event` | server → client | Generic application event |
| `error` | server → client | Error notification |
| `ping` | client → server | Keep-alive request |
| `pong` | server → client | Response to client ping |
| `subscribe` | client → server | Subscribe to a named channel |
| `unsubscribe` | client → server | Unsubscribe from a named channel |

Server-side pings use the WebSocket protocol ping frame (not a JSON `ping` message). Client-sent `ping` messages result in a JSON `pong` response via `client.send`.

---

## Authentication

Authentication is optional and backward compatible. When `HubConfig.Authenticator` is nil, all connections are accepted unchanged. When set, `Handler` calls `Authenticate(r)` before the WebSocket upgrade:

```go
auth := ws.NewAPIKeyAuth(map[string]string{
    "secret-key": "user-1",
})
hub := ws.NewHubWithConfig(ws.HubConfig{Authenticator: auth})
```

If authentication fails, the HTTP response is `401 Unauthorised` and no upgrade occurs.

### Authenticator Interface

```go
type Authenticator interface {
    Authenticate(r *http.Request) AuthResult
}

type AuthResult struct {
    Valid   bool
    UserID  string
    Claims  map[string]any
    Error   error
}
```

Implementations may inspect any part of the request: headers, query parameters, cookies. The `AuthenticatorFunc` adapter allows plain functions to satisfy the interface without defining a named type.

### Built-in: APIKeyAuthenticator

`APIKeyAuthenticator` validates `Authorization: Bearer <key>` against a static key-to-userID map. It is intended for simple deployments and internal tooling. For JWT or OAuth, implement the `Authenticator` interface directly.

### Auth Fields on Client

On successful authentication, `client.UserID` and `client.Claims` are populated. These are readable from `OnConnect` callbacks and any code that holds a reference to the client.

### OnAuthFailure Callback

`HubConfig.OnAuthFailure` fires on every rejected connection. Use it for logging, metrics, or rate-limit triggers. It receives the original `*http.Request` and the `AuthResult`.

---

## Redis Pub/Sub Bridge

`RedisBridge` connects a local `Hub` to Redis, enabling multiple hub instances (across multiple processes or servers) to coordinate broadcasts and channel messages.

```
  Instance A                   Redis                  Instance B
  ┌─────────┐    publish    ┌─────────┐   receive    ┌─────────┐
  │  Hub A  │──────────────►│ ws:*    │─────────────►│  Hub B  │
  │ Bridge A│               │ channels│               │ Bridge B│
  └─────────┘               └─────────┘               └─────────┘
```

### Redis Channel Naming

| Redis channel | Meaning |
|---|---|
| `{prefix}:broadcast` | Global broadcast to all clients across all instances |
| `{prefix}:channel:{name}` | Targeted delivery to subscribers of `{name}` |

The default prefix is `ws`. Override via `RedisConfig.Prefix`.

### Usage

```go
bridge, err := ws.NewRedisBridge(hub, ws.RedisConfig{
    Addr:   "10.69.69.87:6379",
    Prefix: "ws",
})
bridge.Start(ctx)
defer bridge.Stop()

// Publish to all instances
bridge.PublishBroadcast(msg)

// Publish to subscribers of "process:abc" on all instances
bridge.PublishToChannel("process:abc", msg)
```

`NewRedisBridge` validates connectivity with a `PING` before returning. `Start` uses `PSubscribe` with a pattern that matches both the broadcast channel and all `{prefix}:channel:*` channels. The listener goroutine forwards received messages to the local hub via `hub.Broadcast` or `hub.SendToChannel` as appropriate.

### Envelope Pattern and Loop Prevention

Every published message is wrapped in a `redisEnvelope` before serialisation:

```go
type redisEnvelope struct {
    SourceID string  `json:"sourceId"`
    Message  Message `json:"message"`
}
```

`SourceID` is a 16-byte cryptographically random hex string generated once per bridge instance at construction time. The listener goroutine drops any envelope whose `SourceID` matches the local bridge's own ID. This prevents a bridge from re-delivering its own published messages to its own hub.

Without this guard, a single `PublishBroadcast` call would immediately echo back to the publishing instance, creating an infinite loop.

---

## ReconnectingClient

`ReconnectingClient` provides client-side resilience. It wraps a gorilla/websocket connection with an automatic reconnect loop:

```go
client := ws.NewReconnectingClient(ws.ReconnectConfig{
    URL:               "ws://server/ws",
    InitialBackoff:    1 * time.Second,
    MaxBackoff:        30 * time.Second,
    BackoffMultiplier: 2.0,
    MaxRetries:        0, // unlimited
    OnConnect:    func() { /* ... */ },
    OnDisconnect: func() { /* ... */ },
    OnReconnect:  func(attempt int) { /* ... */ },
    OnMessage:    func(msg ws.Message) { /* ... */ },
})

go client.Connect(ctx) // blocks until context cancelled or max retries exceeded
```

### Backoff Calculation

Backoff doubles on each failed attempt, capped at `MaxBackoff`:

```
attempt 1: InitialBackoff
attempt 2: InitialBackoff * Multiplier
attempt 3: InitialBackoff * Multiplier^2
...
attempt N: min(InitialBackoff * Multiplier^(N-1), MaxBackoff)
```

The attempt counter resets to zero after a successful connection.

### Connection States

```go
const (
    StateDisconnected ConnectionState = iota
    StateConnecting
    StateConnected
)
```

`client.State()` returns the current state under a read lock. Useful for health checks and UI indicators.

---

## Concurrency Model

| Resource | Protection |
|---|---|
| `hub.clients`, `hub.channels` | `sync.RWMutex` on Hub |
| `client.subscriptions` | `sync.RWMutex` on Client |
| `ReconnectingClient.conn`, `.state` | `sync.RWMutex` on ReconnectingClient |
| Hub state transitions | Serialised through `hub.register`/`hub.unregister` channels in `Run` loop |

The key invariant: `hub.clients` and `hub.channels` are mutated only from within `hub.Run` (via channel receive) or from `Subscribe`/`Unsubscribe` under write lock. `SendToChannel` copies the subscriber slice under read lock before releasing it, so iteration happens outside any lock without races.

All code is clean under `go test -race ./...`.

---

## Dependency Graph

```
go-ws
├── github.com/gorilla/websocket v1.5.3   (WebSocket server + client)
└── github.com/redis/go-redis/v9 v9.18.0  (Redis bridge, optional at runtime)
```

The Redis dependency is a compile-time import but a runtime opt-in. Applications that do not create a `RedisBridge` incur no Redis connections.
