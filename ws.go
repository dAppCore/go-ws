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
//	// Client sends: {"type": "subscribe", "channel": "process:proc-1"}
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
	"iter"
	"maps"
	"math"
	"net"
	// Note: AX-6 — HTTP request and response types define the WebSocket upgrade boundary.
	"net/http"
	"net/url"
	"slices"
	// Note: AX-6 — origin, host, and channel normalization is structural HTTP/WebSocket boundary validation.
	"strings"
	// Note: AX-6 — internal concurrency primitive; structural for go-ws hub state (RFC mandates concurrent connection map).
	"sync"
	"time"

	core "dappco.re/go/core"
	coreerr "dappco.re/go/core/log"
	"github.com/gorilla/websocket"
)

// Default timing values for heartbeat and pong timeout.
const (
	DefaultHeartbeatInterval         = 30 * time.Second
	DefaultPongTimeout               = 60 * time.Second
	DefaultWriteTimeout              = 10 * time.Second
	DefaultMaxSubscriptionsPerClient = 1024
	defaultMaxMessageBytes           = 64 * 1024
	maxChannelNameLen                = 256
	maxProcessIDLen                  = 128
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

// HubConfig configures the hub.
// ws.NewHubWithConfig(ws.HubConfig{HeartbeatInterval: 30 * time.Second})
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

	// MaxSubscriptionsPerClient limits the number of active subscriptions a
	// single client may hold. Zero or negative values use the default limit.
	MaxSubscriptionsPerClient int

	// AllowedOrigins lists exact Origin header values accepted during the
	// WebSocket upgrade. When empty and CheckOrigin is nil, all origins are
	// allowed for development compatibility only; configure this in production.
	AllowedOrigins []string

	// CheckOrigin optionally overrides the Origin header policy during the
	// WebSocket upgrade. When nil, NewHubWithConfig derives one from
	// AllowedOrigins.
	//
	//	hub := ws.NewHubWithConfig(ws.HubConfig{
	//	    AllowedOrigins: []string{"https://app.example"},
	//	})
	CheckOrigin func(r *http.Request) bool

	// OnAuthFailure is called when a connection is rejected by the
	// Authenticator. Useful for logging or metrics. Optional.
	OnAuthFailure func(r *http.Request, result AuthResult)
}

// config := ws.DefaultHubConfig()
func DefaultHubConfig() HubConfig {
	return HubConfig{
		HeartbeatInterval:         DefaultHeartbeatInterval,
		PongTimeout:               DefaultPongTimeout,
		WriteTimeout:              DefaultWriteTimeout,
		MaxSubscriptionsPerClient: DefaultMaxSubscriptionsPerClient,
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
// msg := ws.Message{Type: ws.TypeEvent, Data: "hello"}
type Message struct {
	Type      MessageType `json:"type"`
	Channel   string      `json:"channel,omitempty"`
	ProcessID string      `json:"processId,omitempty"`
	Data      any         `json:"data,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// Client represents a connected WebSocket client.
// client := &ws.Client{UserID: "user-123"}
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
// hub := ws.NewHub()
type Hub struct {
	clients             map[*Client]bool
	broadcast           chan []byte
	register            chan *Client
	unregister          chan *Client
	subscribeRequests   chan subscriptionRequest
	unsubscribeRequests chan subscriptionRequest
	channels            map[string]map[*Client]bool
	config              HubConfig
	done                chan struct{}
	doneOnce            sync.Once
	running             bool
	mu                  sync.RWMutex
}

type subscriptionRequest struct {
	client  *Client
	channel string
	reply   chan error
}

// ws.NewHub(); go hub.Run(ctx)
func NewHub() *Hub {
	config := DefaultHubConfig()
	if config.CheckOrigin == nil && len(config.AllowedOrigins) == 0 {
		coreerr.Warn("websocket hub allows all origins; set HubConfig.AllowedOrigins in production")
	}
	return NewHubWithConfig(config)
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
	if config.MaxSubscriptionsPerClient <= 0 {
		config.MaxSubscriptionsPerClient = DefaultMaxSubscriptionsPerClient
	}
	if config.CheckOrigin == nil {
		config.CheckOrigin = allowedOriginsCheck(config.AllowedOrigins)
	}
	return &Hub{
		clients:             make(map[*Client]bool),
		broadcast:           make(chan []byte, 256),
		register:            make(chan *Client),
		unregister:          make(chan *Client),
		subscribeRequests:   make(chan subscriptionRequest),
		unsubscribeRequests: make(chan subscriptionRequest),
		channels:            make(map[string]map[*Client]bool),
		config:              config,
		done:                make(chan struct{}),
	}
}

func nilHubError(operation string) error {
	return coreerr.E(operation, "hub must not be nil", nil)
}

func stampServerMessage(msg Message) Message {
	// Server-emitted messages own the timestamp field.
	msg.Timestamp = time.Now()
	return msg
}

func stampServerMessageIfNeeded(msg Message) Message {
	if msg.Timestamp.IsZero() {
		return stampServerMessage(msg)
	}

	return msg
}

func validateMessageIdentifiers(operation string, msg Message) error {
	if msg.ProcessID != "" && !validProcessID(msg.ProcessID) {
		return coreerr.E(operation, "invalid process ID", nil)
	}

	return nil
}

func validateChannelTarget(operation string, channel string) error {
	if !validChannelName(channel) {
		return coreerr.E(operation, "invalid channel name", nil)
	}

	if processID, ok := processChannelID(channel); ok && !validProcessID(processID) {
		return coreerr.E(operation, "invalid process ID", nil)
	}

	return nil
}

func processChannelID(channel string) (string, bool) {
	if !strings.HasPrefix(channel, "process:") {
		return "", false
	}

	return strings.TrimPrefix(channel, "process:"), true
}

func validChannelName(channel string) bool {
	return validIdentifier(channel, maxChannelNameLen)
}

func validProcessID(processID string) bool {
	if !validIdentifier(processID, maxProcessIDLen) {
		return false
	}

	// Process IDs are embedded in `process:<id>` channel names, so the
	// identifier itself must not contain the separator token.
	return !strings.Contains(processID, ":")
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
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

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
					go safeClientCallback(func() {
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
				go safeClientCallback(func() {
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
					go safeClientCallback(func() {
						h.config.OnDisconnect(client)
					})
				}
			} else {
				h.mu.Unlock()
			}
		case request := <-h.subscribeRequests:
			err := h.handleSubscribeRequest(request)
			if request.reply != nil {
				request.reply <- err
			}
		case request := <-h.unsubscribeRequests:
			h.handleUnsubscribeRequest(request)
			if request.reply != nil {
				request.reply <- nil
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

func (h *Hub) handleSubscribeRequest(request subscriptionRequest) error {
	if request.client == nil {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	return h.subscribeLocked(request.client, request.channel)
}

func (h *Hub) handleUnsubscribeRequest(request subscriptionRequest) {
	if request.client == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.unsubscribeLocked(request.client, request.channel)
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
	if h == nil {
		return coreerr.E("Subscribe", "hub must not be nil", nil)
	}
	if err := validateChannelTarget("Subscribe", channel); err != nil {
		return err
	}

	if h != nil && h.config.ChannelAuthoriser != nil && !safeAuthoriserResult(func() bool {
		return h.config.ChannelAuthoriser(client, channel)
	}) {
		return coreerr.E("Subscribe", "subscription unauthorised", nil)
	}

	if h.isRunning() {
		request := subscriptionRequest{
			client:  client,
			channel: channel,
			reply:   make(chan error, 1),
		}

		select {
		case h.subscribeRequests <- request:
		case <-h.done:
			return coreerr.E("Subscribe", "hub is not running", nil)
		}

		select {
		case err := <-request.reply:
			return err
		case <-h.done:
			return coreerr.E("Subscribe", "hub stopped before subscription completed", nil)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	return h.subscribeLocked(client, channel)
}

func (h *Hub) subscribeLocked(client *Client, channel string) error {
	if client == nil {
		return nil
	}

	maxSubs := h.config.MaxSubscriptionsPerClient
	if maxSubs > 0 {
		client.mu.RLock()
		currentSubs := len(client.subscriptions)
		_, alreadySubscribed := client.subscriptions[channel]
		client.mu.RUnlock()

		if !alreadySubscribed && currentSubs >= maxSubs {
			return ErrSubscriptionLimitExceeded
		}
	}

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
	if h == nil {
		return
	}
	if validateChannelTarget("Unsubscribe", channel) != nil {
		return
	}

	if h.isRunning() {
		request := subscriptionRequest{
			client:  client,
			channel: channel,
			reply:   make(chan error, 1),
		}

		select {
		case h.unsubscribeRequests <- request:
		case <-h.done:
			return
		}

		select {
		case <-request.reply:
			return
		case <-h.done:
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.unsubscribeLocked(client, channel)
}

func (h *Hub) unsubscribeLocked(client *Client, channel string) {
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

func (h *Hub) isRunning() bool {
	if h == nil {
		return false
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.running
}

// hub.Broadcast(ws.Message{Type: ws.TypeEvent, Data: "hello everyone"})
func (h *Hub) Broadcast(msg Message) error {
	return h.broadcastMessage(msg, false)
}

func (h *Hub) broadcastMessage(msg Message, preserveTimestamp bool) error {
	if h == nil {
		return nilHubError("Broadcast")
	}
	if err := validateMessageIdentifiers("Broadcast", msg); err != nil {
		return err
	}

	if preserveTimestamp {
		msg = stampServerMessageIfNeeded(msg)
	} else {
		msg = stampServerMessage(msg)
	}
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

// hub.SendToChannel("notifications", ws.Message{Type: ws.TypeEvent, Data: "important update"})
func (h *Hub) SendToChannel(channel string, msg Message) error {
	return h.sendToChannelMessage(channel, msg, false)
}

func (h *Hub) sendToChannelMessage(channel string, msg Message, preserveTimestamp bool) error {
	if h == nil {
		return nilHubError("SendToChannel")
	}

	if err := validateChannelTarget("SendToChannel", channel); err != nil {
		return err
	}
	if err := validateMessageIdentifiers("SendToChannel", msg); err != nil {
		return err
	}

	if preserveTimestamp {
		msg = stampServerMessageIfNeeded(msg)
	} else {
		msg = stampServerMessage(msg)
	}
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
		if !trySend(client.send, data) {
			// Keep the channel membership maps clean if a client can no
			// longer accept outbound frames.
			h.enqueueUnregister(client)
		}
	}
	return nil
}

func sortedClientSubscriptions(client *Client) []string {
	if client == nil {
		return nil
	}

	subscriptions := slices.Collect(maps.Keys(client.subscriptions))
	slices.Sort(subscriptions)
	return subscriptions
}

func sortedHubChannels(h *Hub) []string {
	if h == nil {
		return nil
	}

	channels := slices.Collect(maps.Keys(h.channels))
	slices.Sort(channels)
	return channels
}

func sortedHubClients(h *Hub) []*Client {
	if h == nil {
		return nil
	}

	clients := slices.Collect(maps.Keys(h.clients))
	slices.SortStableFunc(clients, func(left *Client, right *Client) int {
		switch {
		case left == nil && right == nil:
			return 0
		case left == nil:
			return -1
		case right == nil:
			return 1
		}

		if compare := strings.Compare(left.UserID, right.UserID); compare != 0 {
			return compare
		}

		return strings.Compare(clientSortKey(left), clientSortKey(right))
	})
	return clients
}

func clientSortKey(client *Client) string {
	if client == nil || client.conn == nil || client.conn.RemoteAddr() == nil {
		return ""
	}

	return client.conn.RemoteAddr().String()
}

// hub.SendProcessOutput("proc-123", "line of output\n")
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

// hub.SendProcessStatus("proc-123", "exited", 0)
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

// hub.SendError("server error")
func (h *Hub) SendError(errMsg string) error {
	return h.Broadcast(Message{
		Type: TypeError,
		Data: errMsg,
	})
}

// hub.SendEvent("user-joined", map[string]any{"user": "alice"})
func (h *Hub) SendEvent(eventType string, data any) error {
	return h.Broadcast(Message{
		Type: TypeEvent,
		Data: map[string]any{
			"event": eventType,
			"data":  data,
		},
	})
}

// clientCount := hub.ClientCount()
func (h *Hub) ClientCount() int {
	if h == nil {
		return 0
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// channelCount := hub.ChannelCount()
func (h *Hub) ChannelCount() int {
	if h == nil {
		return 0
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.channels)
}

// subscriberCount := hub.ChannelSubscriberCount("notifications")
func (h *Hub) ChannelSubscriberCount(channel string) int {
	if h == nil {
		return 0
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	if clients, ok := h.channels[channel]; ok {
		return len(clients)
	}
	return 0
}

// for client := range hub.AllClients() { _ = client.UserID }
func (h *Hub) AllClients() iter.Seq[*Client] {
	if h == nil {
		return func(yield func(*Client) bool) {}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Values(sortedHubClients(h))
}

// for channel := range hub.AllChannels() { _ = channel }
func (h *Hub) AllChannels() iter.Seq[string] {
	if h == nil {
		return func(yield func(string) bool) {}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Values(sortedHubChannels(h))
}

// HubStats contains hub statistics, including the total subscriber count.
// stats := hub.Stats()
type HubStats struct {
	Clients     int `json:"clients"`
	Channels    int `json:"channels"`
	Subscribers int `json:"subscribers"`
}

// stats := hub.Stats()
func (h *Hub) Stats() HubStats {
	if h == nil {
		return HubStats{}
	}

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

// http.HandleFunc("/ws", hub.HandleWebSocket)
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

func safeOriginCheck(checkOrigin func(*http.Request) bool, r *http.Request) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	return checkOrigin(r)
}

func allowAllOriginsCheck(*http.Request) bool {
	return true
}

func allowedOriginsCheck(allowedOrigins []string) func(*http.Request) bool {
	allowedOrigins = slices.Clone(allowedOrigins)
	if len(allowedOrigins) == 0 {
		return allowAllOriginsCheck
	}

	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}

	return func(r *http.Request) bool {
		if r == nil {
			return false
		}

		_, ok := allowed[r.Header.Get("Origin")]
		return ok
	}
}

// sameOriginCheck allows requests without an Origin header and otherwise
// requires the Origin scheme and host to match the request target.
func sameOriginCheck(r *http.Request) bool {
	if r == nil {
		return false
	}

	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" {
		return false
	}

	requestHost := strings.TrimSpace(r.Host)
	if requestHost == "" && r.URL != nil {
		requestHost = strings.TrimSpace(r.URL.Host)
	}
	if requestHost == "" {
		return false
	}

	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	}

	if !strings.EqualFold(originURL.Scheme, requestScheme) {
		return false
	}

	originHost, originPort, ok := splitHostAndPort(originURL.Host, originURL.Scheme)
	if !ok {
		return false
	}

	requestHostName, requestPort, ok := splitHostAndPort(requestHost, requestScheme)
	if !ok {
		return false
	}

	return strings.EqualFold(originHost, requestHostName) && originPort == requestPort
}

func splitHostAndPort(host string, scheme string) (string, string, bool) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", false
	}

	if hostname, port, err := net.SplitHostPort(host); err == nil {
		if hostname == "" {
			return "", "", false
		}
		return hostname, port, true
	}

	if strings.HasPrefix(host, "[") {
		trimmed := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if trimmed == "" {
			return "", "", false
		}
		return trimmed, defaultPortForScheme(scheme), true
	}

	if strings.Contains(host, ":") {
		return "", "", false
	}

	return host, defaultPortForScheme(scheme), true
}

func defaultPortForScheme(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "https", "wss":
		return "443"
	default:
		return "80"
	}
}

// http.HandleFunc("/ws", hub.Handler())
func (h *Hub) Handler() http.HandlerFunc {
	if h == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Hub is not configured", http.StatusServiceUnavailable)
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if !h.isRunning() {
			http.Error(w, "Hub is not running", http.StatusServiceUnavailable)
			return
		}

		checkOrigin := h.config.CheckOrigin
		if checkOrigin == nil {
			checkOrigin = allowAllOriginsCheck
		}
		originAllowed := safeOriginCheck(checkOrigin, r)
		if !originAllowed {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		// Authenticate only after the origin policy has accepted the request.
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
			CheckOrigin:     func(*http.Request) bool { return originAllowed },
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
	if c == nil || c.hub == nil || c.conn == nil {
		return
	}

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
	c.conn.SetReadLimit(defaultMaxMessageBytes)
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
	if c == nil || c.hub == nil || c.conn == nil {
		return
	}

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
			closed := false
			defer func() {
				if !closed {
					_ = w.Close()
				}
			}()
			w.Write(message)

			// Batch queued messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				next, ok := <-c.send
				if !ok {
					return
				}
				w.Write([]byte{'\n'})
				w.Write(next)
			}

			closed = true
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

// subscriptions := client.Subscriptions()
func (c *Client) Subscriptions() []string {
	if c == nil {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return sortedClientSubscriptions(c)
}

// for channel := range client.AllSubscriptions() { _ = channel }
func (c *Client) AllSubscriptions() iter.Seq[string] {
	if c == nil {
		return func(yield func(string) bool) {}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Values(sortedClientSubscriptions(c))
}

// err := client.Close()
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

	if c.hub.isRunning() {
		c.hub.enqueueUnregister(c)
	} else {
		var disconnected bool
		c.hub.mu.Lock()
		if _, ok := c.hub.clients[c]; ok {
			c.hub.removeClientLocked(c)
			disconnected = true
		}
		c.hub.mu.Unlock()

		if disconnected && c.hub.config.OnDisconnect != nil {
			safeClientCallback(func() {
				c.hub.config.OnDisconnect(c)
			})
		}
	}

	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// client := ws.NewReconnectingClient(ws.ReconnectConfig{URL: "ws://localhost:8080/ws"})
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
	// Deprecated: use MaxReconnectAttempts. Retained for source compatibility.
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

// client := ws.NewReconnectingClient(ws.ReconnectConfig{URL: "ws://localhost:8080/ws"})
type ReconnectingClient struct {
	config   ReconnectConfig
	conn     *websocket.Conn
	send     chan []byte
	state    ConnectionState
	mu       sync.RWMutex
	writeMu  sync.Mutex
	done     chan struct{}
	doneOnce sync.Once
	ctx      context.Context
	cancel   context.CancelFunc
}

// ws.NewReconnectingClient(ws.ReconnectConfig{URL: "ws://localhost:8080/ws"})
func NewReconnectingClient(config ReconnectConfig) *ReconnectingClient {
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 1 * time.Second
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 30 * time.Second
	}
	if config.InitialBackoff > config.MaxBackoff {
		config.InitialBackoff = config.MaxBackoff
	}
	if !(config.BackoffMultiplier >= 1.0) || math.IsInf(config.BackoffMultiplier, 0) {
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

// err := client.Connect(ctx)
func (rc *ReconnectingClient) Connect(ctx context.Context) error {
	if rc == nil {
		return coreerr.E("ReconnectingClient.Connect", "client must not be nil", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	connectCtx, cancel := context.WithCancel(ctx)
	rc.mu.Lock()
	rc.ctx = connectCtx
	rc.cancel = cancel
	rc.mu.Unlock()
	defer func() {
		cancel()
		rc.mu.Lock()
		rc.ctx = nil
		rc.cancel = nil
		rc.mu.Unlock()
	}()

	attempt := 0
	wasConnected := false
	waitBeforeDial := false

	for {
		select {
		case <-connectCtx.Done():
			rc.setState(StateDisconnected)
			return connectCtx.Err()
		case <-rc.done:
			rc.setState(StateDisconnected)
			if err := connectCtx.Err(); err != nil {
				return err
			}
			return nil
		default:
		}

		if waitBeforeDial {
			backoff := rc.calculateBackoff(attempt)
			if !waitForReconnectBackoff(connectCtx, rc.done, backoff) {
				rc.setState(StateDisconnected)
				if err := connectCtx.Err(); err != nil {
					return err
				}
				return nil
			}
		}

		rc.setState(StateConnecting)
		attempt++

		conn, _, err := rc.config.Dialer.DialContext(connectCtx, rc.config.URL, rc.config.Headers)
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
			if !waitForReconnectBackoff(connectCtx, rc.done, backoff) {
				rc.setState(StateDisconnected)
				if err := connectCtx.Err(); err != nil {
					return err
				}
				return nil
			}
			continue
		}
		waitBeforeDial = false

		// Connected successfully
		rc.mu.Lock()
		rc.conn = conn
		rc.mu.Unlock()
		rc.setState(StateConnected)

		connDone := make(chan struct{})
		go func(activeConn *websocket.Conn, done <-chan struct{}) {
			select {
			case <-connectCtx.Done():
				if activeConn != nil {
					_ = activeConn.Close()
				}
			case <-rc.done:
				if activeConn != nil {
					_ = activeConn.Close()
				}
			case <-done:
			}
		}(conn, connDone)

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
		close(connDone)

		// Connection lost
		rc.mu.Lock()
		rc.conn = nil
		rc.mu.Unlock()
		rc.setState(StateDisconnected)

		if rc.closeRequested() {
			if rc.config.OnDisconnect != nil {
				safeReconnectCallback(func() {
					rc.config.OnDisconnect()
				})
			}
			if err := connectCtx.Err(); err != nil {
				return err
			}
			return nil
		}

		if readErr != nil && connectCtx.Err() == nil && rc.config.OnError != nil {
			safeReconnectCallback(func() {
				rc.config.OnError(readErr)
			})
		}

		if rc.config.OnDisconnect != nil {
			safeReconnectCallback(func() {
				rc.config.OnDisconnect()
			})
		}

		waitBeforeDial = true
	}
}

func safeReconnectCallback(call func()) {
	defer func() {
		_ = recover()
	}()
	call()
}

func marshalClientMessage(msg Message) []byte {
	type clientMessage struct {
		Type      MessageType `json:"type"`
		Channel   string      `json:"channel,omitempty"`
		ProcessID string      `json:"processId,omitempty"`
		Data      any         `json:"data,omitempty"`
		Timestamp *time.Time  `json:"timestamp,omitempty"`
	}

	wire := clientMessage{
		Type:      msg.Type,
		Channel:   msg.Channel,
		ProcessID: msg.ProcessID,
		Data:      msg.Data,
	}
	if !msg.Timestamp.IsZero() {
		wire.Timestamp = &msg.Timestamp
	}

	r := core.JSONMarshal(wire)
	if !r.OK {
		return nil
	}

	return r.Value.([]byte)
}

// err := client.Send(ws.Message{Type: ws.TypeSubscribe, Channel: "notifications"})
func (rc *ReconnectingClient) Send(msg Message) error {
	if rc == nil {
		return coreerr.E("ReconnectingClient.Send", "client must not be nil", nil)
	}

	data := marshalClientMessage(msg)
	if data == nil {
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

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
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

// state := client.State()
func (rc *ReconnectingClient) State() ConnectionState {
	if rc == nil {
		return StateDisconnected
	}

	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.state
}

// err := client.Close()
func (rc *ReconnectingClient) Close() error {
	if rc == nil {
		return nil
	}

	if rc.cancel != nil {
		rc.cancel()
	}

	rc.doneOnce.Do(func() {
		close(rc.done)
	})

	rc.setState(StateDisconnected)

	rc.mu.Lock()
	conn := rc.conn
	rc.conn = nil
	rc.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

func (rc *ReconnectingClient) closeRequested() bool {
	if rc == nil || rc.done == nil {
		return false
	}

	select {
	case <-rc.done:
		return true
	default:
		return false
	}
}

func (rc *ReconnectingClient) setState(state ConnectionState) {
	rc.mu.Lock()
	rc.state = state
	rc.mu.Unlock()
}

func (rc *ReconnectingClient) calculateBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return rc.clampedInitialBackoff()
	}

	backoff := rc.clampedInitialBackoff()
	maxBackoff := rc.clampedMaxBackoff()
	multiplier := rc.clampedBackoffMultiplier()
	for i := 1; i < attempt; i++ {
		if backoff >= maxBackoff {
			return maxBackoff
		}

		next := time.Duration(float64(backoff) * multiplier)
		if next <= 0 || next > maxBackoff {
			return maxBackoff
		}
		backoff = next
	}

	if backoff > maxBackoff {
		return maxBackoff
	}

	return backoff
}

func (rc *ReconnectingClient) clampedInitialBackoff() time.Duration {
	backoff := rc.config.InitialBackoff
	if backoff <= 0 {
		backoff = 1 * time.Second
	}
	maxBackoff := rc.clampedMaxBackoff()
	if backoff > maxBackoff {
		return maxBackoff
	}
	return backoff
}

func (rc *ReconnectingClient) clampedMaxBackoff() time.Duration {
	maxBackoff := rc.config.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}
	return maxBackoff
}

func (rc *ReconnectingClient) clampedBackoffMultiplier() float64 {
	multiplier := rc.config.BackoffMultiplier
	if !(multiplier >= 1.0) || math.IsInf(multiplier, 0) {
		multiplier = 2.0
	}
	return multiplier
}

func waitForReconnectBackoff(ctx context.Context, done <-chan struct{}, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}

	timer := time.NewTimer(delay)
	defer stopTimer(timer)

	select {
	case <-ctx.Done():
		return false
	case <-done:
		return false
	case <-timer.C:
		return true
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}

	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
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

	conn.SetReadLimit(defaultMaxMessageBytes)

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
		frames := strings.Split(string(data), "\n")
		for _, frame := range frames {
			frame = strings.TrimSpace(frame)
			if frame == "" {
				continue
			}

			var msg Message
			if r := core.JSONUnmarshal([]byte(frame), &msg); !r.OK {
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
