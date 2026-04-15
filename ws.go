// SPDX-Licence-Identifier: EUPL-1.2

// Package ws provides WebSocket support for real-time streaming.
//
// The ws package enables live process output, events, and bidirectional communication
// between the Go backend and web frontends. It implements a hub pattern for managing
// WebSocket connections and channel-based subscriptions.
//
// # Getting Started
//
//	hub := ws.NewHub()
//	go hub.Run(ctx)
//
//	// Register HTTP handler
//	http.HandleFunc("/ws", hub.Handler())
//
// # Authentication
//
// The hub supports optional token-based authentication on upgrade. Supply an
// Authenticator via HubConfig to gate connections:
//
//	auth := ws.NewAPIKeyAuth(map[string]string{"secret-key": "user-1"})
//	hub := ws.NewHubWithConfig(ws.HubConfig{Authenticator: auth})
//	go hub.Run(ctx)
//
// When no Authenticator is set (nil), all connections are accepted — preserving
// backward compatibility.
//
// # Message Types
//
// The package defines several message types for different purposes:
//   - TypeProcessOutput: Real-time process output streaming
//   - TypeProcessStatus: Process status updates (running, exited, etc.)
//   - TypeEvent: Generic events
//   - TypeError: Error messages
//   - TypePing/TypePong: Keep-alive messages
//   - TypeSubscribe/TypeUnsubscribe: Channel subscription management
//
// # Channel Subscriptions
//
// Clients can subscribe to specific channels to receive targeted messages:
//
//	// Client sends: {"type": "subscribe", "data": "process:proc-1"}
//	// Server broadcasts only to subscribers of "process:proc-1"
//
// # Integration with Core
//
// The Hub can receive process events via Core.ACTION and forward them to WebSocket clients:
//
//	core.RegisterAction(func(c *framework.Core, msg framework.Message) error {
//	    switch m := msg.(type) {
//	    case process.ActionProcessOutput:
//	        hub.SendProcessOutput(m.ID, m.Line)
//	    case process.ActionProcessExited:
//	        hub.SendProcessStatus(m.ID, "exited", m.ExitCode)
//	    }
//	    return nil
//	})
package ws

import (
	"bytes"
	"context"
	"iter"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	core "dappco.re/go/core"
	coreerr "dappco.re/go/core/log"
	"github.com/gorilla/websocket"
)

// Default timing values for heartbeat and pong timeout.
const (
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultPongTimeout       = 60 * time.Second
	DefaultWriteTimeout      = 10 * time.Second
	maxChannelNameLen        = 256
	maxProcessIDLen          = 128
)

// ConnectionState represents the current state of a reconnecting client.
type ConnectionState int

const (
	// StateDisconnected indicates the client is not connected.
	StateDisconnected ConnectionState = iota
	// StateConnecting indicates the client is attempting to connect.
	StateConnecting
	// StateConnected indicates the client has an active connection.
	StateConnected
)

// HubConfig holds configuration for the Hub and its managed connections.
type HubConfig struct {
	// HeartbeatInterval is the interval between server-side ping messages.
	// Defaults to 30 seconds.
	HeartbeatInterval time.Duration

	// PongTimeout is how long the server waits for a pong before
	// considering the connection dead. Must be greater than HeartbeatInterval.
	// Defaults to 60 seconds.
	PongTimeout time.Duration

	// WriteTimeout is the deadline for write operations.
	// Defaults to 10 seconds.
	WriteTimeout time.Duration

	// OnConnect is called when a client connects to the hub.
	OnConnect func(client *Client)

	// OnDisconnect is called when a client disconnects from the hub.
	OnDisconnect func(client *Client)

	// Authenticator validates incoming WebSocket connections during the
	// HTTP upgrade handshake. When nil, all connections are accepted
	// (backward compatible). When set, connections that fail authentication
	// receive an HTTP 401 response and are not upgraded.
	Authenticator Authenticator

	// ChannelAuthoriser optionally decides whether a connected client may
	// subscribe to a named channel. When nil, all subscriptions are allowed.
	ChannelAuthoriser ChannelAuthoriser

	// CheckOrigin optionally validates the Origin header during the WebSocket
	// upgrade. When nil, gorilla/websocket's safe default origin policy is used.
	CheckOrigin func(r *http.Request) bool

	// OnAuthFailure is called when a connection is rejected by the
	// Authenticator. Useful for logging or metrics. Optional.
	OnAuthFailure func(r *http.Request, result AuthResult)
}

// DefaultHubConfig returns a HubConfig with sensible defaults.
func DefaultHubConfig() HubConfig {
	return HubConfig{
		HeartbeatInterval: DefaultHeartbeatInterval,
		PongTimeout:       DefaultPongTimeout,
		WriteTimeout:      DefaultWriteTimeout,
	}
}

// MessageType identifies the type of WebSocket message.
type MessageType string

const (
	// TypeProcessOutput indicates real-time process output.
	TypeProcessOutput MessageType = "process_output"
	// TypeProcessStatus indicates a process status change.
	TypeProcessStatus MessageType = "process_status"
	// TypeEvent indicates a generic event.
	TypeEvent MessageType = "event"
	// TypeError indicates an error message.
	TypeError MessageType = "error"
	// TypePing is a client-to-server keep-alive request.
	TypePing MessageType = "ping"
	// TypePong is the server response to ping.
	TypePong MessageType = "pong"
	// TypeSubscribe requests subscription to a channel.
	TypeSubscribe MessageType = "subscribe"
	// TypeUnsubscribe requests unsubscription from a channel.
	TypeUnsubscribe MessageType = "unsubscribe"
)

// Message is the standard WebSocket message format.
type Message struct {
	Type      MessageType `json:"type"`
	Channel   string      `json:"channel,omitempty"`
	ProcessID string      `json:"processId,omitempty"`
	Data      any         `json:"data,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// Client represents a connected WebSocket client.
type Client struct {
	hub           *Hub
	conn          *websocket.Conn
	send          chan []byte
	subscriptions map[string]bool
	mu            sync.RWMutex
	sendCloseOnce sync.Once

	// UserID is the authenticated user's identifier, set during the
	// upgrade handshake when an Authenticator is configured. Empty
	// when no Authenticator is set.
	UserID string

	// Claims holds arbitrary authentication metadata (e.g. roles,
	// scopes). Nil when no Authenticator is set.
	Claims map[string]any
}

// ChannelAuthoriser decides whether a client may subscribe to a named channel.
// Return true to allow the subscription or false to reject it.
type ChannelAuthoriser func(client *Client, channel string) bool

// Hub manages WebSocket connections and message broadcasting.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	channels   map[string]map[*Client]bool
	config     HubConfig
	done       chan struct{}
	doneOnce   sync.Once
	running    bool
	mu         sync.RWMutex
}

// ws.NewHub(); go hub.Run(ctx)
func NewHub() *Hub {
	return NewHubWithConfig(DefaultHubConfig())
}

// ws.NewHubWithConfig(ws.HubConfig{HeartbeatInterval: 30 * time.Second})
func NewHubWithConfig(config HubConfig) *Hub {
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if config.PongTimeout <= 0 {
		config.PongTimeout = DefaultPongTimeout
	}
	if config.PongTimeout <= config.HeartbeatInterval {
		config.PongTimeout = config.HeartbeatInterval * 2
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		channels:   make(map[string]map[*Client]bool),
		config:     config,
		done:       make(chan struct{}),
	}
}

func validChannelName(channel string) bool {
	return validIdentifier(channel, maxChannelNameLen)
}

func validProcessID(processID string) bool {
	return validIdentifier(processID, maxProcessIDLen)
}

func validIdentifier(value string, maxLen int) bool {
	if value == "" || len(value) > maxLen {
		return false
	}

	if strings.TrimSpace(value) != value {
		return false
	}

	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == ':':
		default:
			return false
		}
	}

	return true
}

// Run starts the hub's main loop. It should be called in a goroutine.
// The loop exits when the context is cancelled.
func (h *Hub) Run(ctx context.Context) {
	h.mu.Lock()
	h.running = true
	h.mu.Unlock()
	defer h.doneOnce.Do(func() { close(h.done) })
	defer func() {
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			// Close all client connections on shutdown.
			// This mirrors the unregister path so subscriptions and
			// disconnect callbacks are handled consistently.
			var disconnected []*Client
			h.mu.Lock()
			for client := range h.clients {
				disconnected = append(disconnected, client)
				h.removeClientLocked(client)
			}
			h.mu.Unlock()
			if h.config.OnDisconnect != nil {
				for _, client := range disconnected {
					safeClientCallback(func() {
						h.config.OnDisconnect(client)
					})
				}
			}
			return
		case client := <-h.register:
			if client == nil {
				continue
			}

			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			if h.config.OnConnect != nil {
				safeClientCallback(func() {
					h.config.OnConnect(client)
				})
			}
		case client := <-h.unregister:
			if client == nil {
				continue
			}

			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				h.removeClientLocked(client)

				h.mu.Unlock()
				if h.config.OnDisconnect != nil {
					safeClientCallback(func() {
						h.config.OnDisconnect(client)
					})
				}
			} else {
				h.mu.Unlock()
			}
		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				if !trySend(client.send, message) {
					// Client buffer full or already closed, will be cleaned up.
					h.enqueueUnregister(client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) enqueueUnregister(client *Client) {
	if h == nil || client == nil {
		return
	}

	go func() {
		select {
		case h.unregister <- client:
		case <-h.done:
		}
	}()
}

// removeClientLocked removes a client from the hub and all channel
// membership maps. The hub lock must be held by the caller.
func (h *Hub) removeClientLocked(client *Client) {
	delete(h.clients, client)
	client.closeSend()

	// Remove from all channels.
	client.mu.Lock()
	for channel := range client.subscriptions {
		if clients, ok := h.channels[channel]; ok {
			delete(clients, client)
			// Clean up empty channels.
			if len(clients) == 0 {
				delete(h.channels, channel)
			}
		}
		delete(client.subscriptions, channel)
	}
	client.mu.Unlock()
}

// Subscribe adds a client to a channel.
func (h *Hub) Subscribe(client *Client, channel string) error {
	if client == nil {
		return nil
	}
	if !validChannelName(channel) {
		return coreerr.E("Subscribe", "invalid channel name", nil)
	}

	if h != nil && h.config.ChannelAuthoriser != nil && !safeAuthoriserResult(func() bool {
		return h.config.ChannelAuthoriser(client, channel)
	}) {
		return coreerr.E("Subscribe", "subscription unauthorised", nil)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.channels[channel]; !ok {
		h.channels[channel] = make(map[*Client]bool)
	}
	h.channels[channel][client] = true

	client.mu.Lock()
	if client.subscriptions == nil {
		client.subscriptions = make(map[string]bool)
	}
	client.subscriptions[channel] = true
	client.mu.Unlock()

	return nil
}

// Unsubscribe removes a client from a channel.
func (h *Hub) Unsubscribe(client *Client, channel string) {
	if client == nil || channel == "" {
		return
	}
	if !validChannelName(channel) {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if clients, ok := h.channels[channel]; ok {
		delete(clients, client)
		// Clean up empty channels
		if len(clients) == 0 {
			delete(h.channels, channel)
		}
	}

	client.mu.Lock()
	if client.subscriptions != nil {
		delete(client.subscriptions, channel)
	}
	client.mu.Unlock()
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msg Message) error {
	msg.Timestamp = time.Now()
	r := core.JSONMarshal(msg)
	if !r.OK {
		return coreerr.E("Broadcast", "failed to marshal message", nil)
	}

	select {
	case h.broadcast <- r.Value.([]byte):
	default:
		return coreerr.E("Broadcast", "broadcast channel full", nil)
	}
	return nil
}

// SendToChannel sends a message to all clients subscribed to a channel.
func (h *Hub) SendToChannel(channel string, msg Message) error {
	if !validChannelName(channel) {
		return coreerr.E("SendToChannel", "invalid channel name", nil)
	}

	msg.Timestamp = time.Now()
	msg.Channel = channel
	r := core.JSONMarshal(msg)
	if !r.OK {
		return coreerr.E("SendToChannel", "failed to marshal message", nil)
	}
	data := r.Value.([]byte)

	h.mu.RLock()
	clients, ok := h.channels[channel]
	if !ok {
		h.mu.RUnlock()
		return nil // No subscribers, not an error
	}

	// Copy client references under lock to avoid races during iteration
	targets := slices.Collect(maps.Keys(clients))
	h.mu.RUnlock()

	for _, client := range targets {
		_ = trySend(client.send, data)
	}
	return nil
}

// SendProcessOutput sends process output to subscribers of the process channel.
func (h *Hub) SendProcessOutput(processID string, output string) error {
	if !validProcessID(processID) {
		return coreerr.E("SendProcessOutput", "invalid process ID", nil)
	}

	return h.SendToChannel("process:"+processID, Message{
		Type:      TypeProcessOutput,
		ProcessID: processID,
		Data:      output,
	})
}

// SendProcessStatus sends a process status update to subscribers.
func (h *Hub) SendProcessStatus(processID string, status string, exitCode int) error {
	if !validProcessID(processID) {
		return coreerr.E("SendProcessStatus", "invalid process ID", nil)
	}

	return h.SendToChannel("process:"+processID, Message{
		Type:      TypeProcessStatus,
		ProcessID: processID,
		Data: map[string]any{
			"status":   status,
			"exitCode": exitCode,
		},
	})
}

// SendError sends an error message to all connected clients.
func (h *Hub) SendError(errMsg string) error {
	return h.Broadcast(Message{
		Type: TypeError,
		Data: errMsg,
	})
}

// SendEvent sends a generic event to all connected clients.
func (h *Hub) SendEvent(eventType string, data any) error {
	return h.Broadcast(Message{
		Type: TypeEvent,
		Data: map[string]any{
			"event": eventType,
			"data":  data,
		},
	})
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ChannelCount returns the number of active channels.
func (h *Hub) ChannelCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.channels)
}

// ChannelSubscriberCount returns the number of subscribers for a channel.
func (h *Hub) ChannelSubscriberCount(channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if clients, ok := h.channels[channel]; ok {
		return len(clients)
	}
	return 0
}

// AllClients returns an iterator for all connected clients.
func (h *Hub) AllClients() iter.Seq[*Client] {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Values(slices.Collect(maps.Keys(h.clients)))
}

// AllChannels returns an iterator for all active channels.
func (h *Hub) AllChannels() iter.Seq[string] {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Values(slices.Collect(maps.Keys(h.channels)))
}

// HubStats contains hub statistics, including the total subscriber count.
type HubStats struct {
	Clients     int `json:"clients"`
	Channels    int `json:"channels"`
	Subscribers int `json:"subscribers"`
}

// Stats returns current hub statistics.
func (h *Hub) Stats() HubStats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	subscriberCount := 0
	for _, clients := range h.channels {
		subscriberCount += len(clients)
	}

	return HubStats{
		Clients:     len(h.clients),
		Channels:    len(h.channels),
		Subscribers: subscriberCount,
	}
}

// HandleWebSocket is an alias for Handler for clearer API.
func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	h.Handler()(w, r)
}

func safeAuthenticate(auth Authenticator, r *http.Request) (result AuthResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = AuthResult{
				Valid: false,
				Error: coreerr.E("Hub.Handler", "authenticator panicked", nil),
			}
		}
	}()

	return finalizeAuthResult(auth.Authenticate(r))
}

func safeClientCallback(call func()) {
	defer func() {
		_ = recover()
	}()
	call()
}

func safeAuthoriserResult(authorise func() bool) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	return authorise()
}

// Handler returns an HTTP handler for WebSocket connections.
func (h *Hub) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Authenticate if an Authenticator is configured.
		var authResult AuthResult
		if h.config.Authenticator != nil {
			authResult = safeAuthenticate(h.config.Authenticator, r)
			if !authResultAccepted(authResult) {
				if h.config.OnAuthFailure != nil {
					safeClientCallback(func() {
						h.config.OnAuthFailure(r, authResult)
					})
				}
				http.Error(w, "Unauthorised", http.StatusUnauthorized)
				return
			}
		}

		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     h.config.CheckOrigin,
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		client := &Client{
			hub:           h,
			conn:          conn,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		// Populate auth fields when authentication succeeded.
		if h.config.Authenticator != nil {
			client.UserID = authResult.UserID
			client.Claims = authResult.Claims
		}

		h.mu.RLock()
		isRunning := h.running
		h.mu.RUnlock()
		if !isRunning {
			conn.Close()
			return
		}

		select {
		case h.register <- client:
		case <-h.done:
			conn.Close()
			return
		}

		go client.writePump()
		go client.readPump()
	}
}

// readPump handles incoming messages from the client.
func (c *Client) readPump() {
	defer func() {
		if c.hub != nil {
			select {
			case c.hub.unregister <- c:
			case <-c.hub.done:
			}
		}
		if c.conn != nil {
			c.conn.Close()
		}
	}()

	pongTimeout := c.hub.config.PongTimeout
	c.conn.SetReadLimit(65536)
	c.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		var msg Message
		if r := core.JSONUnmarshal(message, &msg); !r.OK {
			continue
		}

		switch msg.Type {
		case TypeSubscribe:
			if channel := messageTargetChannel(msg); channel != "" {
				if err := c.hub.Subscribe(c, channel); err != nil {
					errMsg := mustMarshal(Message{
						Type:      TypeError,
						Data:      err.Error(),
						Timestamp: time.Now(),
					})
					if errMsg != nil {
						_ = trySend(c.send, errMsg)
					}
				}
			}
		case TypeUnsubscribe:
			if channel := messageTargetChannel(msg); channel != "" {
				c.hub.Unsubscribe(c, channel)
			}
		case TypePing:
			pongMessage := mustMarshal(Message{Type: TypePong, Timestamp: time.Now()})
			if pongMessage == nil {
				continue
			}

			_ = trySend(c.send, pongMessage)
		}
	}
}

// messageTargetChannel returns the subscription channel named in a client frame.
// The RFC uses the Channel field, while existing callers in this module have
// historically sent the target in Data, so both shapes are accepted.
func messageTargetChannel(msg Message) string {
	if msg.Channel != "" {
		return msg.Channel
	}

	if channel, ok := msg.Data.(string); ok {
		return channel
	}

	return ""
}

// writePump sends messages to the client.
func (c *Client) writePump() {
	heartbeat := c.hub.config.HeartbeatInterval
	writeTimeout := c.hub.config.WriteTimeout
	ticker := time.NewTicker(heartbeat)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Batch queued messages
			n := len(c.send)
			for range n {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func mustMarshal(v any) []byte {
	r := core.JSONMarshal(v)
	if !r.OK {
		return nil
	}
	return r.Value.([]byte)
}

func trySend(ch chan []byte, message []byte) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()

	select {
	case ch <- message:
		return true
	default:
		return false
	}
}

func (c *Client) closeSend() {
	if c == nil {
		return
	}

	c.sendCloseOnce.Do(func() {
		if c.send != nil {
			close(c.send)
		}
	})
}

// Subscriptions returns a copy of the client's current subscriptions.
func (c *Client) Subscriptions() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return slices.Collect(maps.Keys(c.subscriptions))
}

// AllSubscriptions returns an iterator for the client's current subscriptions.
func (c *Client) AllSubscriptions() iter.Seq[string] {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Values(slices.Collect(maps.Keys(c.subscriptions)))
}

// Close closes the client connection.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}

	if c.hub == nil {
		if c.conn == nil {
			return nil
		}
		return c.conn.Close()
	}

	c.hub.enqueueUnregister(c)
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// ReconnectConfig holds configuration for the reconnecting WebSocket client.
type ReconnectConfig struct {
	// URL is the WebSocket server URL to connect to.
	URL string

	// InitialBackoff is the delay before the first reconnection attempt.
	// Defaults to 1 second.
	InitialBackoff time.Duration

	// MaxBackoff is the maximum delay between reconnection attempts.
	// Defaults to 30 seconds.
	MaxBackoff time.Duration

	// BackoffMultiplier controls exponential growth of the backoff.
	// Defaults to 2.0.
	BackoffMultiplier float64

	// MaxRetries is the maximum number of consecutive reconnection attempts.
	// Deprecated: use MaxReconnectAttempts.
	// Zero means unlimited retries.
	MaxRetries int

	// MaxReconnectAttempts is the maximum number of consecutive reconnection attempts.
	// Zero means unlimited retries.
	// If both MaxReconnectAttempts and MaxRetries are set, MaxReconnectAttempts
	// takes precedence.
	MaxReconnectAttempts int

	// OnConnect is called when the client successfully connects.
	OnConnect func()

	// OnDisconnect is called when the client loses its connection.
	OnDisconnect func()

	// OnReconnect is called when the client successfully reconnects
	// after a disconnection. The attempt count is passed in.
	OnReconnect func(attempt int)

	// OnError is called when the client encounters a connection, read,
	// or send error.
	OnError func(err error)

	// OnMessage is called when a message is received from the server.
	// Supported callback shapes are:
	//   - func([]byte) for raw frame payloads
	//   - func(Message) for decoded JSON messages
	// Raw callbacks receive the frame bytes exactly as read. Message
	// callbacks receive each decoded JSON object in the frame.
	OnMessage any

	// Dialer is the WebSocket dialer to use. Defaults to websocket.DefaultDialer.
	Dialer *websocket.Dialer

	// Headers are additional HTTP headers to send during the handshake.
	Headers http.Header
}

// ReconnectingClient is a WebSocket client that automatically reconnects
// with exponential backoff when the connection drops.
type ReconnectingClient struct {
	config  ReconnectConfig
	conn    *websocket.Conn
	send    chan []byte
	state   ConnectionState
	mu      sync.RWMutex
	writeMu sync.Mutex
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

// ws.NewReconnectingClient(ws.ReconnectConfig{URL: "ws://localhost:8080/ws"})
func NewReconnectingClient(config ReconnectConfig) *ReconnectingClient {
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 1 * time.Second
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 30 * time.Second
	}
	if config.BackoffMultiplier <= 0 {
		config.BackoffMultiplier = 2.0
	}
	if config.Dialer == nil {
		config.Dialer = websocket.DefaultDialer
	}

	return &ReconnectingClient{
		config: config,
		send:   make(chan []byte, 256),
		state:  StateDisconnected,
		done:   make(chan struct{}),
	}
}

// Connect starts the reconnecting client. It blocks until the context is
// cancelled. The client will automatically reconnect on connection loss.
func (rc *ReconnectingClient) Connect(ctx context.Context) error {
	rc.ctx, rc.cancel = context.WithCancel(ctx)
	defer rc.cancel()

	attempt := 0
	wasConnected := false

	for {
		select {
		case <-rc.ctx.Done():
			rc.setState(StateDisconnected)
			return rc.ctx.Err()
		default:
		}

		rc.setState(StateConnecting)
		attempt++

		conn, _, err := rc.config.Dialer.DialContext(rc.ctx, rc.config.URL, rc.config.Headers)
		if err != nil {
			maxRetries := rc.maxReconnectAttempts()
			if maxRetries > 0 && attempt > maxRetries {
				rc.setState(StateDisconnected)
				wrapped := coreerr.E("ReconnectingClient.Connect", core.Sprintf("max retries (%d) exceeded", maxRetries), err)
				if rc.config.OnError != nil {
					safeReconnectCallback(func() {
						rc.config.OnError(wrapped)
					})
				}
				return wrapped
			}
			if rc.config.OnError != nil {
				safeReconnectCallback(func() {
					rc.config.OnError(err)
				})
			}
			rc.setState(StateDisconnected)
			backoff := rc.calculateBackoff(attempt)
			select {
			case <-rc.ctx.Done():
				rc.setState(StateDisconnected)
				return rc.ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		// Connected successfully
		rc.mu.Lock()
		rc.conn = conn
		rc.mu.Unlock()
		rc.setState(StateConnected)

		if wasConnected {
			if rc.config.OnReconnect != nil {
				safeReconnectCallback(func() {
					rc.config.OnReconnect(attempt)
				})
			}
		} else {
			if rc.config.OnConnect != nil {
				safeReconnectCallback(func() {
					rc.config.OnConnect()
				})
			}
		}

		// Reset attempt counter after a successful connection
		attempt = 0
		wasConnected = true

		// Run the read loop — blocks until connection drops
		readErr := rc.readLoop()

		// Connection lost
		rc.mu.Lock()
		rc.conn = nil
		rc.mu.Unlock()
		rc.setState(StateDisconnected)

		if readErr != nil && rc.ctx != nil && rc.ctx.Err() == nil && rc.config.OnError != nil {
			safeReconnectCallback(func() {
				rc.config.OnError(readErr)
			})
		}

		if rc.config.OnDisconnect != nil {
			safeReconnectCallback(func() {
				rc.config.OnDisconnect()
			})
		}
	}
}

func safeReconnectCallback(call func()) {
	defer func() {
		_ = recover()
	}()
	call()
}

// Send sends a message to the server. Returns an error if not connected.
func (rc *ReconnectingClient) Send(msg Message) error {
	msg.Timestamp = time.Now()
	r := core.JSONMarshal(msg)
	if !r.OK {
		err := coreerr.E("ReconnectingClient.Send", "failed to marshal message", nil)
		if rc.config.OnError != nil {
			safeReconnectCallback(func() {
				rc.config.OnError(err)
			})
		}
		return err
	}

	rc.mu.RLock()
	conn := rc.conn
	ctx := rc.ctx
	rc.mu.RUnlock()
	if conn == nil {
		return coreerr.E("ReconnectingClient.Send", "not connected", nil)
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	rc.writeMu.Lock()
	defer rc.writeMu.Unlock()

	rc.mu.RLock()
	if rc.conn == nil || rc.conn != conn {
		rc.mu.RUnlock()
		return coreerr.E("ReconnectingClient.Send", "not connected", nil)
	}
	if rc.ctx != nil && rc.ctx.Err() != nil {
		err := rc.ctx.Err()
		rc.mu.RUnlock()
		return err
	}
	rc.mu.RUnlock()

	if err := conn.WriteMessage(websocket.TextMessage, r.Value.([]byte)); err != nil {
		if rc.config.OnError != nil {
			safeReconnectCallback(func() {
				rc.config.OnError(err)
			})
		}
		_ = conn.Close()
		return err
	}

	return nil
}

// State returns the current connection state.
func (rc *ReconnectingClient) State() ConnectionState {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.state
}

// Close gracefully shuts down the reconnecting client.
func (rc *ReconnectingClient) Close() error {
	if rc.cancel != nil {
		rc.cancel()
	}

	rc.setState(StateDisconnected)

	rc.mu.Lock()
	conn := rc.conn
	rc.conn = nil
	rc.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (rc *ReconnectingClient) setState(state ConnectionState) {
	rc.mu.Lock()
	rc.state = state
	rc.mu.Unlock()
}

func (rc *ReconnectingClient) calculateBackoff(attempt int) time.Duration {
	backoff := rc.config.InitialBackoff
	for range attempt - 1 {
		backoff = time.Duration(float64(backoff) * rc.config.BackoffMultiplier)
		if backoff > rc.config.MaxBackoff {
			backoff = rc.config.MaxBackoff
			break
		}
	}
	return backoff
}

func (rc *ReconnectingClient) maxReconnectAttempts() int {
	maxRetries := rc.config.MaxReconnectAttempts
	if maxRetries == 0 {
		maxRetries = rc.config.MaxRetries
	}
	if maxRetries < 0 {
		return 0
	}
	return maxRetries
}

func (rc *ReconnectingClient) readLoop() error {
	rc.mu.RLock()
	conn := rc.conn
	rc.mu.RUnlock()

	if conn == nil {
		return nil
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		if rc.config.OnMessage != nil {
			dispatchReconnectMessage(rc.config.OnMessage, data)
		}
	}
}

func dispatchReconnectMessage(handler any, data []byte) {
	switch fn := handler.(type) {
	case nil:
		return
	case func([]byte):
		if fn == nil {
			return
		}
		safeReconnectCallback(func() {
			fn(data)
		})
	case func(Message):
		if fn == nil {
			return
		}
		frames := bytes.Split(data, []byte{'\n'})
		for _, frame := range frames {
			frame = bytes.TrimSpace(frame)
			if len(frame) == 0 {
				continue
			}

			var msg Message
			if r := core.JSONUnmarshal(frame, &msg); !r.OK {
				continue
			}

			safeReconnectCallback(func() {
				fn(msg)
			})
		}
	case func(string):
		if fn == nil {
			return
		}
		safeReconnectCallback(func() {
			fn(string(data))
		})
	default:
		return
	}
}
