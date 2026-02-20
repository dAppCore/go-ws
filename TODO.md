# TODO.md — go-ws

Dispatched from core/go orchestration. Pick up tasks in order.

---

## Phase 0: Hardening & Test Coverage

- [ ] **Expand test coverage** — `ws_test.go` exists. Add tests for: `Hub.Run()` lifecycle (start, register client, broadcast, shutdown), `Subscribe`/`Unsubscribe` channel management, `SendToChannel` with no subscribers (should not error), `SendProcessOutput`/`SendProcessStatus` helpers, client `readPump` message parsing (subscribe, unsubscribe, ping), client `writePump` batch sending, client buffer overflow (send channel full → client disconnected), concurrent broadcast + subscribe (race test).
- [ ] **Integration test** — Use `httptest.NewServer` + real WebSocket client. Connect, subscribe to channel, send message, verify receipt. Test multiple clients on same channel.
- [ ] **Benchmark** — `BenchmarkBroadcast` with 100 connected clients. `BenchmarkSendToChannel` with 50 subscribers. Measure message throughput.
- [ ] **`go vet ./...` clean** — Fix any warnings.

## Phase 1: Connection Resilience

- [ ] Add client-side reconnection support (exponential backoff)
- [ ] Tune heartbeat interval and pong timeout for flaky networks
- [ ] Add connection state callbacks (onConnect, onDisconnect, onReconnect)

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
