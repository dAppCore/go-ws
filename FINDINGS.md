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
