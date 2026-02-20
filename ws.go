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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local development
	},
}

// Default timing values for heartbeat and pong timeout.
const (
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultPongTimeout       = 60 * time.Second
	DefaultWriteTimeout      = 10 * time.Second
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
}

// Hub manages WebSocket connections and message broadcasting.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	channels   map[string]map[*Client]bool
	config     HubConfig
	mu         sync.RWMutex
}

// NewHub creates a new WebSocket hub with default configuration.
func NewHub() *Hub {
	return NewHubWithConfig(DefaultHubConfig())
}

// NewHubWithConfig creates a new WebSocket hub with the given configuration.
func NewHubWithConfig(config HubConfig) *Hub {
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if config.PongTimeout <= 0 {
		config.PongTimeout = DefaultPongTimeout
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
	}
}

// Run starts the hub's main loop. It should be called in a goroutine.
// The loop exits when the context is canceled.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			// Close all client connections on shutdown
			h.mu.Lock()
			for client := range h.clients {
				close(client.send)
				delete(h.clients, client)
			}
			h.mu.Unlock()
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			if h.config.OnConnect != nil {
				h.config.OnConnect(client)
			}
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				// Remove from all channels
				for channel := range client.subscriptions {
					if clients, ok := h.channels[channel]; ok {
						delete(clients, client)
						// Clean up empty channels
						if len(clients) == 0 {
							delete(h.channels, channel)
						}
					}
				}
				h.mu.Unlock()
				if h.config.OnDisconnect != nil {
					h.config.OnDisconnect(client)
				}
			} else {
				h.mu.Unlock()
			}
		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// Client buffer full, will be cleaned up
					go func(c *Client) {
						h.unregister <- c
					}(client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Subscribe adds a client to a channel.
func (h *Hub) Subscribe(client *Client, channel string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.channels[channel]; !ok {
		h.channels[channel] = make(map[*Client]bool)
	}
	h.channels[channel][client] = true

	client.mu.Lock()
	client.subscriptions[channel] = true
	client.mu.Unlock()
}

// Unsubscribe removes a client from a channel.
func (h *Hub) Unsubscribe(client *Client, channel string) {
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
	delete(client.subscriptions, channel)
	client.mu.Unlock()
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msg Message) error {
	msg.Timestamp = time.Now()
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	select {
	case h.broadcast <- data:
	default:
		return fmt.Errorf("broadcast channel full")
	}
	return nil
}

// SendToChannel sends a message to all clients subscribed to a channel.
func (h *Hub) SendToChannel(channel string, msg Message) error {
	msg.Timestamp = time.Now()
	msg.Channel = channel
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	h.mu.RLock()
	clients, ok := h.channels[channel]
	if !ok {
		h.mu.RUnlock()
		return nil // No subscribers, not an error
	}

	// Copy client references under lock to avoid races during iteration
	targets := make([]*Client, 0, len(clients))
	for client := range clients {
		targets = append(targets, client)
	}
	h.mu.RUnlock()

	for _, client := range targets {
		select {
		case client.send <- data:
		default:
			// Client buffer full, skip
		}
	}
	return nil
}

// SendProcessOutput sends process output to subscribers of the process channel.
func (h *Hub) SendProcessOutput(processID string, output string) error {
	return h.SendToChannel("process:"+processID, Message{
		Type:      TypeProcessOutput,
		ProcessID: processID,
		Data:      output,
	})
}

// SendProcessStatus sends a process status update to subscribers.
func (h *Hub) SendProcessStatus(processID string, status string, exitCode int) error {
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

// HubStats contains hub statistics.
type HubStats struct {
	Clients  int `json:"clients"`
	Channels int `json:"channels"`
}

// Stats returns current hub statistics.
func (h *Hub) Stats() HubStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return HubStats{
		Clients:  len(h.clients),
		Channels: len(h.channels),
	}
}

// HandleWebSocket is an alias for Handler for clearer API.
func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	h.Handler()(w, r)
}

// Handler returns an HTTP handler for WebSocket connections.
func (h *Hub) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		h.register <- client

		go client.writePump()
		go client.readPump()
	}
}

// readPump handles incoming messages from the client.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
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
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case TypeSubscribe:
			if channel, ok := msg.Data.(string); ok {
				c.hub.Subscribe(c, channel)
			}
		case TypeUnsubscribe:
			if channel, ok := msg.Data.(string); ok {
				c.hub.Unsubscribe(c, channel)
			}
		case TypePing:
			c.send <- mustMarshal(Message{Type: TypePong, Timestamp: time.Now()})
		}
	}
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
			for i := 0; i < n; i++ {
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
	data, _ := json.Marshal(v)
	return data
}

// Subscriptions returns a copy of the client's current subscriptions.
func (c *Client) Subscriptions() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]string, 0, len(c.subscriptions))
	for channel := range c.subscriptions {
		result = append(result, channel)
	}
	return result
}

// Close closes the client connection.
func (c *Client) Close() error {
	c.hub.unregister <- c
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
	// Zero means unlimited retries.
	MaxRetries int

	// OnConnect is called when the client successfully connects.
	OnConnect func()

	// OnDisconnect is called when the client loses its connection.
	OnDisconnect func()

	// OnReconnect is called when the client successfully reconnects
	// after a disconnection. The attempt count is passed in.
	OnReconnect func(attempt int)

	// OnMessage is called when a message is received from the server.
	OnMessage func(msg Message)

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
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewReconnectingClient creates a new reconnecting WebSocket client.
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
			if rc.config.MaxRetries > 0 && attempt > rc.config.MaxRetries {
				rc.setState(StateDisconnected)
				return fmt.Errorf("max retries (%d) exceeded: %w", rc.config.MaxRetries, err)
			}
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
				rc.config.OnReconnect(attempt)
			}
		} else {
			if rc.config.OnConnect != nil {
				rc.config.OnConnect()
			}
		}

		// Reset attempt counter after a successful connection
		attempt = 0
		wasConnected = true

		// Run the read loop — blocks until connection drops
		rc.readLoop()

		// Connection lost
		rc.mu.Lock()
		rc.conn = nil
		rc.mu.Unlock()

		if rc.config.OnDisconnect != nil {
			rc.config.OnDisconnect()
		}
	}
}

// Send sends a message to the server. Returns an error if not connected.
func (rc *ReconnectingClient) Send(msg Message) error {
	msg.Timestamp = time.Now()
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	rc.mu.RLock()
	conn := rc.conn
	rc.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.conn.WriteMessage(websocket.TextMessage, data)
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
	rc.mu.RLock()
	conn := rc.conn
	rc.mu.RUnlock()
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
	for i := 1; i < attempt; i++ {
		backoff = time.Duration(float64(backoff) * rc.config.BackoffMultiplier)
		if backoff > rc.config.MaxBackoff {
			backoff = rc.config.MaxBackoff
			break
		}
	}
	return backoff
}

func (rc *ReconnectingClient) readLoop() {
	rc.mu.RLock()
	conn := rc.conn
	rc.mu.RUnlock()

	if conn == nil {
		return
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		if rc.config.OnMessage != nil {
			var msg Message
			if jsonErr := json.Unmarshal(data, &msg); jsonErr == nil {
				rc.config.OnMessage(msg)
			}
		}
	}
}
