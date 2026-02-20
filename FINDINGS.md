# FINDINGS.md -- go-ws

## 2026-02-19: Split from core/go (Virgil)

### Origin

Extracted from `forge.lthn.ai/core/go` `pkg/ws/` on 19 Feb 2026.

### Architecture

- Hub pattern: central `Hub` manages client registration, unregistration, and message routing
- Channel-based subscriptions: clients subscribe to named channels for targeted messaging
- Broadcast support: send to all connected clients or to a specific channel
- Message types: `process_output`, `process_status`, `event`, `error`, `ping/pong`, `subscribe/unsubscribe`
- `writePump` batches outbound messages for efficiency (reduces syscall overhead)
- `readPump` handles inbound messages and automatic ping/pong keepalive

### Dependencies

- `github.com/gorilla/websocket` -- WebSocket server implementation

### Notes

- Hub must be started with `go hub.Run(ctx)` before accepting connections
- HTTP handler exposed via `hub.Handler()` for mounting on any router
- `hub.SendProcessOutput(processID, line)` is the primary API for streaming subprocess output

## 2026-02-20: Phase 0 & Phase 1 (Charon)

### Race condition fix

- `SendToChannel` had a data race: it acquired `RLock`, read the channel's client map, released `RUnlock`, then iterated clients outside the lock. If `Hub.Run` processed an unregister concurrently, the map was modified during iteration.
- Fix: copy client pointers into a slice under `RLock`, then iterate the copy after releasing the lock.

### Phase 0: Test coverage 88.4% to 98.5%

- Added 16 new test functions covering: hub shutdown, broadcast overflow, channel send overflow, marshal errors, upgrade error, `Client.Close`, malformed JSON, non-string subscribe/unsubscribe data, unknown message types, writePump close/batch, concurrent subscribe/unsubscribe, multi-client channel delivery, end-to-end process output/status.
- Added `BenchmarkBroadcast` (100 clients) and `BenchmarkSendToChannel` (50 subscribers).
- `go vet ./...` clean; `go test -race ./...` clean.

### Phase 1: Connection resilience

- `HubConfig` struct: `HeartbeatInterval`, `PongTimeout`, `WriteTimeout`, `OnConnect`, `OnDisconnect` callbacks.
- `NewHubWithConfig(config)`: constructor with validation and defaults.
- `readPump`/`writePump` now use hub config values instead of hardcoded durations.
- `ReconnectingClient`: client-side reconnection with exponential backoff.
  - `ReconnectConfig`: URL, InitialBackoff (1s), MaxBackoff (30s), BackoffMultiplier (2.0), MaxRetries, Dialer, Headers, OnConnect/OnDisconnect/OnReconnect/OnMessage callbacks.
  - `Connect(ctx)`: blocking reconnect loop; returns on context cancel or max retries.
  - `Send(msg)`: thread-safe message send; returns error if not connected.
  - `State()`: returns `StateDisconnected`, `StateConnecting`, or `StateConnected`.
  - `Close()`: cancels context and closes underlying connection.
  - Exponential backoff: `calculateBackoff(attempt)` doubles each attempt, capped at MaxBackoff.

### API surface additions

- `HubConfig`, `DefaultHubConfig()`, `NewHubWithConfig()`
- `ConnectionState` enum: `StateDisconnected`, `StateConnecting`, `StateConnected`
- `ReconnectConfig`, `ReconnectingClient`, `NewReconnectingClient()`
- `DefaultHeartbeatInterval`, `DefaultPongTimeout`, `DefaultWriteTimeout` constants
- `NewHub()` still works unchanged (uses `DefaultHubConfig()` internally)
