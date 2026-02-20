# TODO.md — go-ws

Dispatched from core/go orchestration. Pick up tasks in order.

---

## Phase 0: Hardening & Test Coverage (complete)

- [x] **Expand test coverage** — 67 test functions total across two sessions. Hub.Run lifecycle (register, broadcast delivery, shutdown, unregister, duplicate unregister), Subscribe/Unsubscribe (multiple channels, idempotent, partial leave, concurrent race), SendToChannel (no subscribers, multiple subscribers, buffer full), SendProcessOutput/SendProcessStatus (no subscribers, non-zero exit), readPump (subscribe, unsubscribe, ping, invalid JSON, non-string data, unknown types), writePump (batch sending, close-on-channel-close), buffer overflow disconnect, marshal errors, Handler upgrade error, Client.Close(), broadcast reaches all clients, disconnect cleans up everything.
- [x] **Integration test** — End-to-end tests using httptest.NewServer + gorilla/websocket Dial: connect-subscribe-send-receive, multiple clients on same channel, unsubscribe stops delivery, broadcast reaches all clients, process output/status streaming, disconnect cleanup.
- [x] **Benchmark** — 9 benchmarks in ws_bench_test.go: BenchmarkBroadcast_100 (100 clients), BenchmarkSendToChannel_50 (50 subscribers), parallel variants, message marshalling, WebSocket end-to-end, subscribe/unsubscribe cycle, multi-channel fanout, concurrent subscribers. All use b.ReportAllocs() and b.Loop() (Go 1.25+). Plus 2 inline benchmarks in ws_test.go.
- [x] **`go vet ./...` clean** — No warnings. Race-free under `go test -race`.
- [x] **Race condition fix** — Fixed data race in SendToChannel: clients now copied under lock before iteration.

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
