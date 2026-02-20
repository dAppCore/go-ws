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

## Phase 2: Auth (complete)

Token-based authentication on WebSocket upgrade handshake. Pure Go, no JWT library dependency — consumers bring their own validation logic via an interface.

### 2.1 Authenticator Interface

- [x] **Create `auth.go`** — Define the auth abstraction:
  - `type AuthResult struct { Valid bool; UserID string; Claims map[string]any; Error error }` — result of authentication
  - `type Authenticator interface { Authenticate(r *http.Request) AuthResult }` — validates the HTTP request during upgrade. Implementations can check headers (`Authorization: Bearer <token>`), query params (`?token=xxx`), or cookies.
  - `type AuthenticatorFunc func(r *http.Request) AuthResult` — adapter for using functions as Authenticators (implements the interface)
  - `type APIKeyAuthenticator struct { Keys map[string]string }` — built-in authenticator that validates `Authorization: Bearer <key>` against a static key→userID map. Provided as a convenience; consumers can use their own JWT-based authenticator.
  - `func NewAPIKeyAuth(keys map[string]string) *APIKeyAuthenticator` — constructor

### 2.2 Wire Into Hub

- [x] **Add `Authenticator` to `HubConfig`** — Optional field. When nil, all connections are accepted (backward compatible). When set, `Handler()` calls `Authenticate(r)` before upgrading.
- [x] **Update `Handler()`** — If `h.config.Authenticator != nil`, call `Authenticate(r)`. If `!result.Valid`, respond with `http.StatusUnauthorized` (or `http.StatusForbidden` if `result.Error` indicates a different status) and return without upgrading. If valid, store `result.UserID` and `result.Claims` on the `Client` struct.
- [x] **Add auth fields to `Client`** — `UserID string` and `Claims map[string]any` fields. Set during authenticated upgrade. Empty for unauthenticated hubs (nil authenticator).
- [x] **Expose `OnAuthFailure` callback** — Optional `OnAuthFailure func(r *http.Request, result AuthResult)` on `HubConfig` for logging/metrics on rejected connections.

### 2.3 Tests

- [x] **Unit tests** — (a) APIKeyAuthenticator valid key, (b) invalid key, (c) missing header, (d) malformed header ("Bearer" without token, wrong scheme), (e) AuthenticatorFunc adapter, (f) nil Authenticator (backward compat — all connections accepted)
- [x] **Integration tests** — Using httptest + gorilla/websocket Dial:
  - (a) Authenticated connect with valid API key → upgrade succeeds, client.UserID set
  - (b) Rejected connect with invalid key → HTTP 401, no WebSocket upgrade
  - (c) Rejected connect with no auth header → HTTP 401
  - (d) Nil authenticator → all connections accepted (existing behaviour preserved)
  - (e) OnAuthFailure callback fires on rejection
  - (f) Multiple clients with different API keys → each gets correct UserID
- [x] **Existing tests still pass** — No authenticator set = backward compatible

## Phase 3: Scaling

- [ ] Support Redis pub/sub as backend for multi-instance hub coordination
- [ ] Broadcast messages across hub instances via Redis channels
- [ ] Add sticky sessions or connection-affinity documentation for load balancers

---

## Workflow

1. Virgil in core/go writes tasks here after research
2. This repo's dedicated session picks up tasks in phase order
3. Mark `[x]` when done, note commit hash
