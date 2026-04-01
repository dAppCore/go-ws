# go-ws Project History

---

## Origin

go-ws was extracted from `forge.lthn.ai/core/go` on 19 February 2026. The original code lived at `pkg/ws/` within the core/go monorepo. It was split into a standalone module to allow independent versioning, dedicated test coverage, and direct use by consumers that do not need the full core/go framework.

Extraction commit: `e942500 feat: extract go-ws from core/go pkg/ws`

---

## Phases

### Phase 0: Hardening and Test Coverage

Commit: `53d8a15 test: expand Phase 0 coverage — 16 new tests, 9 benchmarks, SPDX headers`
Commit: `13d9422 feat(ws): phase 0 coverage (98.5%) + phase 1 connection resilience`

Coverage rose from 88.4% to 98.5%. Work included:

- 16 new test functions: hub shutdown, broadcast channel overflow, `SendToChannel` buffer full, marshal errors, upgrade error, `Client.Close`, malformed JSON, non-string subscribe/unsubscribe data, unknown message types, `writePump` close and batch paths, concurrent subscribe/unsubscribe race, multi-client channel delivery, end-to-end process output and status streaming.
- 9 benchmarks in `ws_bench_test.go`: `BenchmarkBroadcast_100`, `BenchmarkSendToChannel_50`, parallel variants, message marshalling, WebSocket end-to-end, subscribe/unsubscribe cycle, multi-channel fanout, concurrent subscribers.
- All benchmarks use `b.Loop()` (Go 1.25+) and `b.ReportAllocs()`.
- Race condition fix: `SendToChannel` previously read the channel's client map under `RLock`, released the lock, then iterated. A concurrent `unregister` could mutate the map during iteration. Fixed by copying client pointers to a slice under `RLock` and iterating the copy after releasing the lock.
- SPDX licence headers added to all source files.
- `go vet ./...` clean; `go test -race ./...` clean.

### Phase 1: Connection Resilience

Commit: `13d9422 feat(ws): phase 0 coverage (98.5%) + phase 1 connection resilience`

- `HubConfig` struct with configurable `HeartbeatInterval`, `PongTimeout`, `WriteTimeout`, and `OnConnect`/`OnDisconnect` callbacks.
- `DefaultHubConfig()` and `NewHubWithConfig(config)` constructors. `NewHub()` delegates to `NewHubWithConfig(DefaultHubConfig())`.
- `readPump` and `writePump` use hub config values instead of hardcoded durations.
- `ReconnectingClient` with exponential backoff:
  - `ReconnectConfig`: URL, `InitialBackoff` (1 s), `MaxBackoff` (30 s), `BackoffMultiplier` (2.0), `MaxRetries` (0 = unlimited), `Dialer`, `Headers`, `OnConnect`/`OnDisconnect`/`OnReconnect`/`OnMessage` callbacks.
  - `Connect(ctx)`: blocking reconnect loop, returns on context cancel or max retries exceeded.
  - `Send(msg)`: thread-safe write to server, returns error if not connected.
  - `State()`: returns `StateDisconnected`, `StateConnecting`, or `StateConnected`.
  - `Close()`: cancels context, closes underlying connection.
- `ConnectionState` enum: `StateDisconnected`, `StateConnecting`, `StateConnected`.

### Phase 2: Authentication

Commit: `534bbe5 docs: flesh out Phase 2 auth task specs`
Commit: `9e48f0b feat(auth): Phase 2 — token-based authentication on WebSocket upgrade`

Token-based authentication at the HTTP upgrade handshake. No JWT library is imported; consumers provide their own validation logic via an interface.

- `Authenticator` interface: `Authenticate(r *http.Request) AuthResult`.
- `AuthResult` struct: `Valid bool`, `UserID string`, `Claims map[string]any`, `Error error`.
- `AuthenticatorFunc` adapter for plain function use.
- `APIKeyAuthenticator`: validates `Authorization: Bearer <key>` against a static key-to-userID map.
- `NewAPIKeyAuth(keys map[string]string)` constructor.
- `HubConfig.Authenticator`: optional; nil preserves backward-compatible behaviour (all connections accepted).
- `HubConfig.OnAuthFailure`: optional callback for rejected connections.
- `Client.UserID` and `Client.Claims` populated on successful authentication.
- Auth errors defined in `errors.go`: `ErrMissingAuthHeader`, `ErrMalformedAuthHeader`, `ErrInvalidAPIKey`.
- Unit tests: valid key, invalid key, missing header, malformed header, `AuthenticatorFunc` adapter, nil authenticator.
- Integration tests via httptest: authenticated connect, rejected connect (401), nil authenticator backward compat, `OnAuthFailure` callback, multiple clients with distinct user IDs.

### Phase 3: Redis Pub/Sub Bridge

Commit: `da3df00 feat(redis): add Redis pub/sub bridge for multi-instance Hub coordination`

Cross-instance coordination via Redis pub/sub.

- `RedisBridge` struct: wraps a `Hub` with a Redis client and listener goroutine.
- `RedisConfig`: `Addr`, `Password`, `DB`, `Prefix` (default `ws`).
- `NewRedisBridge(hub, cfg)`: validates connectivity with `PING` before returning.
- `Start(ctx)`: subscribes via `PSubscribe` to `{prefix}:broadcast` and `{prefix}:channel:*`, spawns listener goroutine.
- `Stop()`: cancels listener, closes pub/sub subscription and Redis client connection.
- `PublishBroadcast(msg)`: publishes to `{prefix}:broadcast`; all bridge instances deliver to their local hub clients.
- `PublishToChannel(channel, msg)`: publishes to `{prefix}:channel:{channel}`; all bridge instances deliver to local subscribers of that channel.
- Envelope pattern (`redisEnvelope`): wraps every message with a `sourceID` (16-byte cryptographically random hex) to prevent echo loops. The listener silently drops messages whose `sourceID` matches the local bridge.
- `SourceID()`: returns the instance identifier, useful for debugging.
- Redis integration tests use `skipIfNoRedis(t)` and unique time-based prefixes to avoid collisions between parallel runs.

---

## Known Limitations

### Load Balancer Affinity

The Redis bridge coordinates messages across hub instances but does not solve connection affinity. When a client connects to instance A and a message is published via instance B, the message reaches the client correctly through Redis. However, subscription state is local to each hub instance. A client subscribed on instance A is invisible to instance B's channel maps.

Consequence: `hub.ChannelSubscriberCount` and `hub.Stats` reflect only local state. There is no global subscriber registry. For systems that need to know whether any instance has a subscriber before publishing, either publish unconditionally (relying on Redis to route) or implement a shared registry in Redis.

Sticky sessions at the load balancer level (by client IP or cookie) eliminate the affinity problem entirely and are the recommended approach for most deployments.

### Origin Check

The WebSocket upgrader is configured with `CheckOrigin: func(*http.Request) bool { return true }`. This accepts connections from any origin, which is appropriate for local development and internal tooling. Production deployments behind a reverse proxy with strict origin control should override the upgrader or add origin validation in an `Authenticator` implementation.

### Broadcast Buffer

The hub's broadcast channel has a fixed capacity of 256. High-throughput broadcast workloads can saturate this buffer, causing `hub.Broadcast` to return an error. Callers should handle this error and consider whether dropping or queuing at the application level is appropriate.

### No Global Subscriber Registry

There is no mechanism to enumerate all connected clients across multiple hub instances. Redis holds no session state; it only relays messages. Applications requiring global presence information (e.g. "how many users are watching process X") must maintain their own counter, typically in Redis.

---

## Future Considerations

- **Sticky session documentation**: A deployment guide covering Nginx/Traefik IP-hash or cookie-based affinity for multi-instance setups.
- **Global subscriber registry**: Optional Redis-backed presence tracking to complement the bridge.
- **TLS for Redis**: `RedisConfig` currently supports `Addr`, `Password`, and `DB` only. Adding a `TLSConfig *tls.Config` field would support encrypted Redis connections without breaking the existing API.
- **Message acknowledgement**: The current model is fire-and-forget. A future phase could add client-side ack with server-side retry for guaranteed delivery on unreliable connections.
