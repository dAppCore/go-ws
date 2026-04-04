---
title: Architecture
description: Internal design of go-ws -- the hub pattern, connection lifecycle, channel subscriptions, authentication, the Redis pub/sub bridge, and the concurrency model.
---

# Architecture

This document explains how `go-ws` works internally. It covers the hub pattern, connection management, channel subscriptions, message types, authentication, the Redis bridge, and the reconnecting client.

## Hub Pattern

The `Hub` is the central broker. It owns all connection state and serialises mutations through Go channels:

```
                    +-----------------------------+
                    |            Hub              |
  HTTP upgrade ---> |  register   chan *Client    |
  disconnect   ---> |  unregister chan *Client    |
  server send  ---> |  broadcast  chan []byte     |
                    |                             |
                    |  clients  map[*Client]bool  |
                    |  channels map[string]map... |
                    +-----------------------------+
```

`Hub.Run(ctx)` is a single-goroutine select loop that processes all state transitions. Create the hub, start the loop, then mount the HTTP handler:

```go
hub := ws.NewHub()
go hub.Run(ctx)

http.HandleFunc("/ws", hub.Handler())
```

Cancel the context to shut down gracefully. `Run` closes every client's `send` channel, which causes the write pumps to send a WebSocket close frame and exit.

### Configuration

`NewHub()` uses sensible defaults. For custom timing or callbacks, use `NewHubWithConfig`:

```go
hub := ws.NewHubWithConfig(ws.HubConfig{
    HeartbeatInterval: 15 * time.Second,
    PongTimeout:       45 * time.Second,
    WriteTimeout:      5 * time.Second,
    OnConnect:         func(c *ws.Client) { log.Println("connected:", c.UserID) },
    OnDisconnect:      func(c *ws.Client) { log.Println("disconnected:", c.UserID) },
    Authenticator:     myAuth,
    OnAuthFailure:     func(r *http.Request, result ws.AuthResult) { /* ... */ },
})
```

## Connection Lifecycle

Each connected client is represented by a `Client` struct:

```go
type Client struct {
    hub           *Hub
    conn          *websocket.Conn
    send          chan []byte       // buffered, capacity 256
    subscriptions map[string]bool
    mu            sync.RWMutex
    UserID        string            // set during authenticated upgrade
    Claims        map[string]any    // set during authenticated upgrade
}
```

When a browser or Go client connects to the WebSocket endpoint, `Handler` creates a `Client`, sends it to `hub.register`, then starts two goroutines:

- **readPump** -- reads inbound frames, dispatches subscribe/unsubscribe/ping messages, and enforces the pong timeout. When the connection drops, it sends the client to `hub.unregister`.
- **writePump** -- drains `client.send`, batches queued frames into a single write call, and sends server-side WebSocket ping frames on a heartbeat tick.

On disconnect (read error, write error, or context cancellation), `readPump` sends the client to `hub.unregister`. The hub removes the client from all channel maps and closes `client.send`. Closing the send channel is the signal that causes `writePump` to send a close frame and exit.

### Buffer Overflow

Each client's `send` channel has capacity 256. When `Broadcast` or `SendToChannel` cannot deliver to a full channel, the behaviour differs:

- **Broadcast** (inside `Run`): schedules an unregister via a goroutine, effectively disconnecting the stalled client.
- **SendToChannel**: silently drops the message for that client only. The client remains connected.

### Timing Defaults

| Constant | Default | Purpose |
|---|---|---|
| `DefaultHeartbeatInterval` | 30s | Server-side WebSocket ping cadence |
| `DefaultPongTimeout` | 60s | Read deadline; reset on each pong |
| `DefaultWriteTimeout` | 10s | Write deadline per frame |

All three are configurable via `HubConfig`.

## Channel Subscriptions

Channels are arbitrary named strings. Clients subscribe by sending a JSON message:

```json
{"type": "subscribe", "data": "process:build-42"}
```

`readPump` intercepts `subscribe` and `unsubscribe` frames and calls `hub.Subscribe` / `hub.Unsubscribe`. The hub maintains two parallel indices:

- `hub.channels[channelName][*Client]` -- for targeted delivery.
- `client.subscriptions[channelName]` -- for cleanup on disconnect.

Both indices are kept in sync. Unsubscribing from a channel with no remaining subscribers removes the channel entry entirely (no empty map accumulation).

### Sending to a Channel

```go
hub.SendToChannel("process:build-42", ws.Message{
    Type: ws.TypeProcessOutput,
    Data: "output line",
})
```

`SendToChannel` acquires a read lock, copies the subscriber slice, releases the lock, then delivers to each client's `send` channel. The copy-under-lock pattern prevents a data race with `hub.unregister`, which mutates the map under a write lock concurrently.

### Process Helpers

Two convenience methods wrap `SendToChannel` with an idiomatic `process:<id>` channel naming convention:

```go
hub.SendProcessOutput("build-42", "Compiling...")
hub.SendProcessStatus("build-42", "exited", 0)
```

`SendProcessOutput` sends a `process_output` message. `SendProcessStatus` sends a `process_status` message with a `status` string and `exitCode` integer in the `Data` field.

### Per-Channel Access Control

`HubConfig.ChannelAuthoriser` can gate subscriptions on a per-channel basis. It runs when a client sends a `subscribe` frame and receives both the connected `Client` and the requested channel name:

```go
hub := ws.NewHubWithConfig(ws.HubConfig{
    ChannelAuthoriser: func(client *ws.Client, channel string) bool {
        role, _ := client.Claims["role"].(string)
        return role == "admin" || strings.HasPrefix(channel, "public:")
    },
})
```

When the authoriser returns `false`, the subscription is rejected and the client receives a `TypeError` message. The hook can inspect `client.UserID` and `client.Claims`, so it composes naturally with the upgrade-time authenticator.

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
| `process_output` | server to client | Real-time subprocess stdout/stderr line |
| `process_status` | server to client | Process state change (running, exited) with exit code |
| `event` | server to client | Generic application event |
| `error` | server to client | Error notification |
| `ping` | client to server | Application-level keep-alive request |
| `pong` | server to client | Response to client `ping` |
| `subscribe` | client to server | Subscribe to a named channel |
| `unsubscribe` | client to server | Unsubscribe from a named channel |

There are two separate ping/pong mechanisms. Server-side heartbeats use the WebSocket protocol's native ping frame (not a JSON message). Client-sent `ping` messages (JSON) result in a JSON `pong` response via `client.send`.

## Authentication

Authentication is optional and backward compatible. When `HubConfig.Authenticator` is nil, all connections are accepted without change.

When set, `Handler` calls `Authenticate(r)` before the WebSocket upgrade. If authentication fails, the HTTP response is `401 Unauthorised` and no upgrade occurs.

### The Authenticator Interface

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

Implementations may inspect any part of the HTTP request: headers, query parameters, cookies.

### Built-in Authenticators

**APIKeyAuthenticator** validates `Authorization: Bearer <key>` against a static key-to-user-ID map:

```go
auth := ws.NewAPIKeyAuth(map[string]string{
    "secret-key": "user-1",
})
```

**BearerTokenAuth** extracts the bearer token and delegates validation to a caller-supplied function. This is the hook for JWT verification, token introspection, or any custom bearer scheme:

```go
auth := &ws.BearerTokenAuth{
    Validate: func(token string) ws.AuthResult {
        claims, err := verifyJWT(token)
        if err != nil {
            return ws.AuthResult{Valid: false, Error: err}
        }
        return ws.AuthResult{
            Valid:  true,
            UserID: claims.Subject,
            Claims: map[string]any{"roles": claims.Roles},
        }
    },
}
```

**QueryTokenAuth** extracts a `?token=` query parameter instead of an Authorization header. This is useful for browser clients where the native WebSocket API does not support custom headers:

```go
auth := &ws.QueryTokenAuth{
    Validate: func(token string) ws.AuthResult {
        // validate and return result
    },
}
```

**AuthenticatorFunc** is an adapter that allows any function with the right signature to satisfy the `Authenticator` interface without defining a named type:

```go
auth := ws.AuthenticatorFunc(func(r *http.Request) ws.AuthResult {
    // custom logic
})
```

### Authentication Errors

Three sentinel errors are defined in `errors.go`:

- `ErrMissingAuthHeader` -- no `Authorization` header present.
- `ErrMalformedAuthHeader` -- header is not in `Bearer <token>` format.
- `ErrInvalidAPIKey` -- token does not match any known key.

### Auth Fields on Client

On successful authentication, `client.UserID` and `client.Claims` are populated before the `OnConnect` callback fires. These fields are readable from any code that holds a reference to the `Client`.

### OnAuthFailure Callback

`HubConfig.OnAuthFailure` fires on every rejected connection attempt. It receives the original `*http.Request` and the `AuthResult`, making it useful for logging, metrics, or rate-limit triggers.

## Redis Pub/Sub Bridge

`RedisBridge` connects a local `Hub` to Redis pub/sub, enabling multiple hub instances (across processes or servers) to coordinate broadcasts and channel-targeted messages transparently.

```
  Instance A                   Redis                  Instance B
  +---------+    publish    +---------+   receive    +---------+
  |  Hub A  |-------------->| ws:*    |------------->|  Hub B  |
  | BridgeA |               |channels |              | BridgeB |
  +---------+               +---------+              +---------+
```

### Setup

```go
bridge, err := ws.NewRedisBridge(hub, ws.RedisConfig{
    Addr:     "10.69.69.87:6379",
    Password: "",       // optional
    DB:       0,        // optional
    Prefix:   "ws",     // optional, defaults to "ws"
    TLSConfig: nil,     // optional, set for encrypted Redis connections
})
if err != nil {
    log.Fatal(err)
}

if err := bridge.Start(ctx); err != nil {
    log.Fatal(err)
}
defer bridge.Stop()
```

`NewRedisBridge` validates connectivity with a `PING` before returning. `Start` subscribes via `PSUBSCRIBE` to both the broadcast channel and a wildcard pattern for all named channels. The listener goroutine forwards received messages to the local hub.

### Redis Channel Naming

| Redis channel | Maps to |
|---|---|
| `{prefix}:broadcast` | `hub.Broadcast` -- all connected clients on all instances |
| `{prefix}:channel:{name}` | `hub.SendToChannel(name, ...)` -- subscribers of `{name}` on all instances |

### Publishing

```go
// Broadcast to all clients across all instances.
bridge.PublishBroadcast(msg)

// Send to subscribers of a specific channel across all instances.
bridge.PublishToChannel("process:build-42", msg)
```

### Envelope Pattern and Loop Prevention

Every published message is wrapped in a `redisEnvelope`:

```go
type redisEnvelope struct {
    SourceID string  `json:"sourceId"`
    Message  Message `json:"message"`
}
```

`SourceID` is a 16-byte cryptographically random hex string, generated once per bridge instance at construction time. The listener goroutine silently drops any envelope whose `SourceID` matches its own. Without this guard, a `PublishBroadcast` call would immediately echo back to the publishing instance, creating an infinite loop.

### Graceful Shutdown

Call `bridge.Stop()` to cancel the listener goroutine, close the pub/sub subscription, and close the Redis client connection. `Stop` waits for the listener goroutine to exit before returning.

## Reconnecting Client

`ReconnectingClient` provides client-side resilience. It wraps a `gorilla/websocket` connection with an automatic reconnect loop using exponential backoff:

```go
client := ws.NewReconnectingClient(ws.ReconnectConfig{
    URL:               "ws://server:8080/ws",
    InitialBackoff:    1 * time.Second,
    MaxBackoff:        30 * time.Second,
    BackoffMultiplier: 2.0,
    MaxRetries:        0, // 0 = unlimited
    OnConnect:         func() { log.Println("connected") },
    OnDisconnect:      func() { log.Println("disconnected") },
    OnReconnect:       func(attempt int) { log.Printf("reconnected after %d attempts", attempt) },
    OnMessage:         func(msg ws.Message) { log.Println("received:", msg) },
    Headers:           http.Header{"Authorization": []string{"Bearer my-token"}},
})

// Blocks until the context is cancelled or MaxRetries is exceeded.
err := client.Connect(ctx)
```

### Backoff Calculation

The backoff doubles on each failed attempt, capped at `MaxBackoff`:

```
attempt 1: InitialBackoff                                    (1s)
attempt 2: InitialBackoff * Multiplier                       (2s)
attempt 3: InitialBackoff * Multiplier^2                     (4s)
...
attempt N: min(InitialBackoff * Multiplier^(N-1), MaxBackoff)
```

The attempt counter resets to zero after each successful connection.

### Connection States

```go
const (
    StateDisconnected ConnectionState = iota  // not connected
    StateConnecting                           // attempting to connect
    StateConnected                            // active connection
)
```

`client.State()` returns the current state under a read lock. Useful for health checks and UI indicators.

### Sending Messages

```go
err := client.Send(ws.Message{
    Type: ws.TypeSubscribe,
    Data: "process:build-42",
})
```

`Send` returns an error if the client is not currently connected.

## Concurrency Model

| Resource | Protection |
|---|---|
| `hub.clients`, `hub.channels` | `sync.RWMutex` on Hub |
| `client.subscriptions` | `sync.RWMutex` on Client |
| `ReconnectingClient.conn`, `.state` | `sync.RWMutex` on ReconnectingClient |
| Hub state transitions | Serialised through `register`/`unregister` channels in `Run` loop |

The key invariant: `hub.clients` and `hub.channels` are mutated from within `hub.Run` (via channel receive) or from `Subscribe`/`Unsubscribe` under a write lock. `SendToChannel` copies the subscriber slice under a read lock before releasing it, so iteration happens outside any lock.

All code passes `go test -race ./...`.

## Inspecting Hub State

The hub exposes several read-only methods for monitoring:

```go
hub.ClientCount()                    // number of connected clients
hub.ChannelCount()                   // number of active channels
hub.ChannelSubscriberCount("ch")     // subscribers on a specific channel
hub.Stats()                          // HubStats{Clients, Channels}

// Iterators (Go 1.23+ range-over-func)
for client := range hub.AllClients() { /* ... */ }
for channel := range hub.AllChannels() { /* ... */ }
```

These all acquire a read lock and return a snapshot. The iterators copy keys under the lock to avoid holding it during iteration.

## Known Limitations

**Local-only subscriber state.** The Redis bridge relays messages but does not share subscription state. `hub.ChannelSubscriberCount` and `hub.Stats` reflect only the local instance. There is no global subscriber registry. Sticky sessions at the load balancer level (IP hash or cookie) are the recommended approach for most deployments.

**Permissive origin check.** The WebSocket upgrader accepts all origins (`CheckOrigin` returns true). This is appropriate for development and internal tooling. Production deployments should add origin validation in the `Authenticator` or behind a reverse proxy.

**Fixed broadcast buffer.** The hub's broadcast channel has capacity 256. High-throughput broadcast workloads can saturate this buffer, causing `hub.Broadcast` to return an error. Callers should handle this and decide whether to drop or queue at the application level.
