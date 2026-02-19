# TODO.md -- go-ws

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
