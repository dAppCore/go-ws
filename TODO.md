# TODO.md — go-ws

Dispatched from core/go orchestration. Pick up tasks in order.

---

## Phase 0: Hardening & Test Coverage

- [x] **Expand test coverage** — Added tests for: `Hub.Run()` shutdown closing all clients, broadcast to client with full buffer (unregister path), `SendToChannel` with full client buffer (skip path), `Broadcast`/`SendToChannel` marshal errors, `Handler` upgrade error on non-WebSocket request, `Client.Close()`, `readPump` malformed JSON, subscribe/unsubscribe with non-string data, unknown message types, `writePump` close-on-channel-close and batch sending, concurrent subscribe/unsubscribe race test, multiple clients on same channel, end-to-end process output and status tests. Coverage: 88.4% → 98.5%.
- [x] **Integration test** — Full end-to-end tests using `httptest.NewServer` + real WebSocket clients. Multi-client channel delivery, process output streaming, process status updates.
- [x] **Benchmark** — `BenchmarkBroadcast` with 100 clients, `BenchmarkSendToChannel` with 50 subscribers.
- [x] **`go vet ./...` clean** — No warnings.
- [x] **Race condition fix** — Fixed data race in `SendToChannel` where client map was iterated outside the read lock. Clients are now copied under lock before iteration.

## Phase 1: Connection Resilience

- [x] Add client-side reconnection support (exponential backoff) — `ReconnectingClient` with `ReconnectConfig`. Configurable initial backoff, max backoff, multiplier, max retries.
- [x] Tune heartbeat interval and pong timeout for flaky networks — `HubConfig` with `HeartbeatInterval`, `PongTimeout`, `WriteTimeout`. `NewHubWithConfig()` constructor. Defaults: 30s heartbeat, 60s pong timeout, 10s write timeout.
- [x] Add connection state callbacks (onConnect, onDisconnect, onReconnect) — Hub-level `OnConnect`/`OnDisconnect` callbacks in `HubConfig`. Client-level `OnConnect`/`OnDisconnect`/`OnReconnect` callbacks in `ReconnectConfig`. `ConnectionState` enum: `StateDisconnected`, `StateConnecting`, `StateConnected`.

## Phase 2: Auth

- [ ] Add token-based authentication on WebSocket upgrade handshake
- [ ] Validate JWT or API key before promoting HTTP connection to WebSocket
- [ ] Reject unauthenticated connections with appropriate HTTP status

## Phase 3: Scaling

- [ ] Support Redis pub/sub as backend for multi-instance hub coordination
- [ ] Broadcast messages across hub instances via Redis channels
- [ ] Add sticky sessions or connection-affinity documentation for load balancers

---

## Workflow

1. Virgil in core/go writes tasks here after research
2. This repo's dedicated session picks up tasks in phase order
3. Mark `[x]` when done, note commit hash
