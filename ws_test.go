// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	core "dappco.re/go/core"
	"github.com/gorilla/websocket"
)

// wsURL converts an httptest server URL to a WebSocket URL.
func wsURL(server *httptest.Server) string {
	return "ws" + core.TrimPrefix(server.URL, "http")
}

func TestNewHub(t *testing.T) {
	t.Run("creates hub with initialised maps", func(t *testing.T) {
		hub := NewHub()
		if hub == nil {
			t.Fatal("expected non-nil")
		}
		if hub.clients == nil {
			t.Fatal("expected non-nil")
		}
		if hub.broadcast == nil {
			t.Fatal("expected non-nil")
		}
		if hub.register == nil {
			t.Fatal("expected non-nil")
		}
		if hub.unregister == nil {
			t.Fatal("expected non-nil")
		}
		if hub.channels == nil {
			t.Fatal("expected non-nil")
		}
	})
}

func TestHub_Run(t *testing.T) {
	t.Run("stops on context cancel", func(t *testing.T) {
		hub := NewHub()
		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			hub.Run(ctx)
			close(done)
		}()

		cancel()

		select {
		case <-done:
			// Good - hub stopped
		case <-time.After(time.Second):
			t.Fatal("hub should have stopped on context cancel")
		}
	})
}

func TestHub_Broadcast(t *testing.T) {
	t.Run("marshals message with timestamp", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		msg := Message{
			Type: TypeEvent,
			Data: "test data",
		}

		err := hub.Broadcast(msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("returns error when channel full", func(t *testing.T) {
		hub := NewHub()
		// Fill the broadcast channel
		for range 256 {
			hub.broadcast <- []byte("test")
		}

		err := hub.Broadcast(Message{Type: TypeEvent})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "broadcast channel full") {
			t.Fatalf("expected %v to contain %v", err.Error(), "broadcast channel full")
		}
	})
}

func TestHub_Stats(t *testing.T) {
	t.Run("returns empty stats for new hub", func(t *testing.T) {
		hub := NewHub()

		stats := hub.Stats()
		if 0 != stats.Clients {
			t.Fatalf("want %v, got %v", 0, stats.Clients)
		}
		if 0 != stats.Channels {
			t.Fatalf("want %v, got %v", 0, stats.Channels)
		}
		if 0 != stats.Subscribers {
			t.Fatalf("want %v, got %v", 0, stats.Subscribers)
		}
	})

	t.Run("tracks client and channel counts", func(t *testing.T) {
		hub := NewHub()

		// Manually add clients for testing
		hub.mu.Lock()
		client1 := &Client{subscriptions: make(map[string]bool)}
		client2 := &Client{subscriptions: make(map[string]bool)}
		hub.clients[client1] = true
		hub.clients[client2] = true
		hub.channels["test-channel"] = map[*Client]bool{
			client1: true,
			client2: true,
		}
		hub.channels["other-channel"] = map[*Client]bool{
			client1: true,
		}
		hub.mu.Unlock()

		stats := hub.Stats()
		if 2 != stats.Clients {
			t.Fatalf("want %v, got %v", 2, stats.Clients)
		}
		if 2 != stats.Channels {
			t.Fatalf("want %v, got %v", 2, stats.Channels)
		}
		if 3 != stats.Subscribers {
			t.Fatalf("want %v, got %v", 3, stats.Subscribers)
		}
	})
}

func TestHub_ClientCount(t *testing.T) {
	t.Run("returns zero for empty hub", func(t *testing.T) {
		hub := NewHub()
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}
	})

	t.Run("counts connected clients", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.clients[&Client{}] = true
		hub.clients[&Client{}] = true
		hub.mu.Unlock()
		if 2 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 2, hub.ClientCount())
		}
	})
}

func TestHub_ChannelCount(t *testing.T) {
	t.Run("returns zero for empty hub", func(t *testing.T) {
		hub := NewHub()
		if 0 != hub.ChannelCount() {
			t.Fatalf("want %v, got %v", 0, hub.ChannelCount())
		}
	})

	t.Run("counts active channels", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.channels["channel1"] = make(map[*Client]bool)
		hub.channels["channel2"] = make(map[*Client]bool)
		hub.mu.Unlock()
		if 2 != hub.ChannelCount() {
			t.Fatalf("want %v, got %v", 2, hub.ChannelCount())
		}
	})
}

func TestHub_ChannelSubscriberCount(t *testing.T) {
	t.Run("returns zero for non-existent channel", func(t *testing.T) {
		hub := NewHub()
		if 0 != hub.ChannelSubscriberCount("non-existent") {
			t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("non-existent"))
		}
	})

	t.Run("counts subscribers in channel", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.channels["test-channel"] = make(map[*Client]bool)
		hub.channels["test-channel"][&Client{}] = true
		hub.channels["test-channel"][&Client{}] = true
		hub.mu.Unlock()
		if 2 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 2, hub.ChannelSubscriberCount("test-channel"))
		}
	})
}

func TestHub_Subscribe(t *testing.T) {
	t.Run("adds client to channel", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()

		err := hub.Subscribe(client, "test-channel")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}
		if !client.subscriptions["test-channel"] {
			t.Fatal("expected true")
		}
	})

	t.Run("creates channel if not exists", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		err := hub.Subscribe(client, "new-channel")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		hub.mu.RLock()
		_, exists := hub.channels["new-channel"]
		hub.mu.RUnlock()
		if !exists {
			t.Fatal("expected true")
		}
	})
}

func TestHub_Unsubscribe(t *testing.T) {
	t.Run("removes client from channel", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		hub.Subscribe(client, "test-channel")
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}

		hub.Unsubscribe(client, "test-channel")
		if 0 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("test-channel"))
		}
		if client.subscriptions["test-channel"] {
			t.Fatal("expected false")
		}
	})

	t.Run("cleans up empty channels", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		hub.Subscribe(client, "temp-channel")
		hub.Unsubscribe(client, "temp-channel")

		hub.mu.RLock()
		_, exists := hub.channels["temp-channel"]
		hub.mu.RUnlock()
		if exists {
			t.Fatal("expected false")
		}
	})

	t.Run("handles non-existent channel gracefully", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		// Should not panic
		hub.Unsubscribe(client, "non-existent")
	})
}

func TestHub_SendToChannel(t *testing.T) {
	t.Run("sends to channel subscribers", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		hub.Subscribe(client, "test-channel")

		err := hub.SendToChannel("test-channel", Message{
			Type: TypeEvent,
			Data: "test",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !core.JSONUnmarshal(msg, &received).OK {
				t.Fatal("expected true")
			}
			if TypeEvent != received.Type {
				t.Fatalf("want %v, got %v", TypeEvent, received.Type)
			}
			if "test-channel" != received.Channel {
				t.Fatalf("want %v, got %v", "test-channel", received.Channel)
			}
		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("returns nil for non-existent channel", func(t *testing.T) {
		hub := NewHub()

		err := hub.SendToChannel("non-existent", Message{Type: TypeEvent})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestHub_SendProcessOutput(t *testing.T) {
	t.Run("sends output to process channel", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		hub.Subscribe(client, "process:proc-1")

		err := hub.SendProcessOutput("proc-1", "hello world")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !core.JSONUnmarshal(msg, &received).OK {
				t.Fatal("expected true")
			}
			if TypeProcessOutput != received.Type {
				t.Fatalf("want %v, got %v", TypeProcessOutput, received.Type)
			}
			if "proc-1" != received.ProcessID {
				t.Fatalf("want %v, got %v", "proc-1", received.ProcessID)
			}
			if "hello world" != received.Data {
				t.Fatalf("want %v, got %v", "hello world", received.Data)
			}
		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})
}

func TestHub_SendProcessStatus(t *testing.T) {
	t.Run("sends status to process channel", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		hub.Subscribe(client, "process:proc-1")

		err := hub.SendProcessStatus("proc-1", "exited", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !core.JSONUnmarshal(msg, &received).OK {
				t.Fatal("expected true")
			}
			if TypeProcessStatus != received.Type {
				t.Fatalf("want %v, got %v", TypeProcessStatus, received.Type)
			}
			if "proc-1" != received.ProcessID {
				t.Fatalf("want %v, got %v", "proc-1", received.ProcessID)
			}

			data, ok := received.Data.(map[string]any)
			if !ok {
				t.Fatal("expected true")
			}
			if "exited" != data["status"] {
				t.Fatalf("want %v, got %v", "exited", data["status"])
			}
			if float64(0) != data["exitCode"] {
				t.Fatalf("want %v, got %v", float64(0), data["exitCode"])
			}
		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})
}

func TestHub_SendError(t *testing.T) {
	t.Run("broadcasts error message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		hub.register <- client
		// Give time for registration
		time.Sleep(10 * time.Millisecond)

		err := hub.SendError("something went wrong")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !core.JSONUnmarshal(msg, &received).OK {
				t.Fatal("expected true")
			}
			if TypeError != received.Type {
				t.Fatalf("want %v, got %v", TypeError, received.Type)
			}
			if "something went wrong" != received.Data {
				t.Fatalf("want %v, got %v", "something went wrong", received.Data)
			}
		case <-time.After(time.Second):
			t.Fatal("expected error message on client send channel")
		}
	})
}

func TestHub_SendEvent(t *testing.T) {
	t.Run("broadcasts event message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		hub.register <- client
		time.Sleep(10 * time.Millisecond)

		err := hub.SendEvent("user_joined", map[string]string{"user": "alice"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !core.JSONUnmarshal(msg, &received).OK {
				t.Fatal("expected true")
			}
			if TypeEvent != received.Type {
				t.Fatalf("want %v, got %v", TypeEvent, received.Type)
			}

			data, ok := received.Data.(map[string]any)
			if !ok {
				t.Fatal("expected true")
			}
			if "user_joined" != data["event"] {
				t.Fatalf("want %v, got %v", "user_joined", data["event"])
			}
		case <-time.After(time.Second):
			t.Fatal("expected event message on client send channel")
		}
	})
}

func TestClient_Subscriptions(t *testing.T) {
	t.Run("returns copy of subscriptions", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		hub.Subscribe(client, "channel1")
		hub.Subscribe(client, "channel2")

		subs := client.Subscriptions()
		if len(subs) != 2 {
			t.Fatalf("want len %v, got %v", 2, len(subs))
		}
		if !slices.Contains(subs, "channel1") {
			t.Fatalf("expected %v to contain %v", subs, "channel1")
		}
		if !slices.Contains(subs, "channel2") {
			t.Fatalf("expected %v to contain %v", subs, "channel2")
		}
	})
}

func TestClient_AllSubscriptions(t *testing.T) {
	t.Run("returns iterator over subscriptions", func(t *testing.T) {
		client := &Client{subscriptions: make(map[string]bool)}
		client.subscriptions["sub1"] = true
		client.subscriptions["sub2"] = true

		subs := slices.Collect(client.AllSubscriptions())
		if len(subs) != 2 {
			t.Fatalf("want len %v, got %v", 2, len(subs))
		}
		if !slices.Contains(subs, "sub1") {
			t.Fatalf("expected %v to contain %v", subs, "sub1")
		}
		if !slices.Contains(subs, "sub2") {
			t.Fatalf("expected %v to contain %v", subs, "sub2")
		}
	})
}

func TestHub_AllClients(t *testing.T) {
	t.Run("returns iterator over all clients", func(t *testing.T) {
		hub := NewHub()
		client1 := &Client{subscriptions: make(map[string]bool)}
		client2 := &Client{subscriptions: make(map[string]bool)}

		hub.mu.Lock()
		hub.clients[client1] = true
		hub.clients[client2] = true
		hub.mu.Unlock()

		clients := slices.Collect(hub.AllClients())
		if len(clients) != 2 {
			t.Fatalf("want len %v, got %v", 2, len(clients))
		}
		if !slices.Contains(clients, client1) {
			t.Fatalf("expected %v to contain %v", clients, client1)
		}
		if !slices.Contains(clients, client2) {
			t.Fatalf("expected %v to contain %v", clients, client2)
		}
	})
}

func TestHub_AllChannels(t *testing.T) {
	t.Run("returns iterator over all active channels", func(t *testing.T) {
		hub := NewHub()
		hub.mu.Lock()
		hub.channels["ch1"] = make(map[*Client]bool)
		hub.channels["ch2"] = make(map[*Client]bool)
		hub.mu.Unlock()

		channels := slices.Collect(hub.AllChannels())
		if len(channels) != 2 {
			t.Fatalf("want len %v, got %v", 2, len(channels))
		}
		if !slices.Contains(channels, "ch1") {
			t.Fatalf("expected %v to contain %v", channels, "ch1")
		}
		if !slices.Contains(channels, "ch2") {
			t.Fatalf("expected %v to contain %v", channels, "ch2")
		}
	})
}

func TestMessage_JSON(t *testing.T) {
	t.Run("marshals correctly", func(t *testing.T) {
		msg := Message{
			Type:      TypeProcessOutput,
			Channel:   "process:1",
			ProcessID: "1",
			Data:      "output line",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		}

		r := core.JSONMarshal(msg)
		if !r.OK {
			t.Fatal("expected true")
		}
		data := r.Value.([]byte)
		if !strings.Contains(string(data), `"type":"process_output"`) {
			t.Fatalf("expected %v to contain %v", string(data), `"type":"process_output"`)
		}
		if !strings.Contains(string(data), `"channel":"process:1"`) {
			t.Fatalf("expected %v to contain %v", string(data), `"channel":"process:1"`)
		}
		if !strings.Contains(string(data), `"processId":"1"`) {
			t.Fatalf("expected %v to contain %v", string(data), `"processId":"1"`)
		}
		if !strings.Contains(string(data), `"data":"output line"`) {
			t.Fatalf("expected %v to contain %v", string(data), `"data":"output line"`)
		}
	})

	t.Run("unmarshals correctly", func(t *testing.T) {
		jsonStr := `{"type":"subscribe","data":"channel:test"}`

		var msg Message
		if !core.JSONUnmarshal([]byte(jsonStr), &msg).OK {
			t.Fatal("expected true")
		}
		if TypeSubscribe != msg.Type {
			t.Fatalf("want %v, got %v", TypeSubscribe, msg.Type)
		}
		if "channel:test" != msg.Data {
			t.Fatalf("want %v, got %v", "channel:test", msg.Data)
		}
	})
}

func TestHub_WebSocketHandler(t *testing.T) {
	t.Run("upgrades connection and registers client", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		// Give time for registration
		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 1, hub.ClientCount())
		}
	})

	t.Run("handles subscribe message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		// Send subscribe message
		subscribeMsg := Message{
			Type: TypeSubscribe,
			Data: "test-channel",
		}
		err = conn.WriteJSON(subscribeMsg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Give time for subscription
		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}
	})

	t.Run("handles unsubscribe message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		// Subscribe first
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}

		// Unsubscribe
		err = conn.WriteJSON(Message{Type: TypeUnsubscribe, Data: "test-channel"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if 0 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("test-channel"))
		}
	})

	t.Run("responds to ping with pong", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		// Give time for registration
		time.Sleep(50 * time.Millisecond)

		// Send ping
		err = conn.WriteJSON(Message{Type: TypePing})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Read pong response
		var response Message
		conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if TypePong != response.Type {
			t.Fatalf("want %v, got %v", TypePong, response.Type)
		}
	})

	t.Run("broadcasts messages to clients", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		// Give time for registration
		time.Sleep(50 * time.Millisecond)

		// Broadcast a message
		err = hub.Broadcast(Message{
			Type: TypeEvent,
			Data: "broadcast test",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Read the broadcast
		var response Message
		conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if TypeEvent != response.Type {
			t.Fatalf("want %v, got %v", TypeEvent, response.Type)
		}
		if "broadcast test" != response.Data {
			t.Fatalf("want %v, got %v", "broadcast test", response.Data)
		}
	})

	t.Run("unregisters client on connection close", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Wait for registration
		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 1, hub.ClientCount())
		}

		// Close connection
		conn.Close()

		// Wait for unregistration
		time.Sleep(50 * time.Millisecond)
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}
	})

	t.Run("removes client from channels on disconnect", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Subscribe to channel
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}

		// Close connection
		conn.Close()
		time.Sleep(50 * time.Millisecond)
		if

		// Channel should be cleaned up
		0 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("test-channel"))
		}
	})
}

func TestHub_Concurrency(t *testing.T) {
	t.Run("handles concurrent subscriptions", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		var wg sync.WaitGroup
		numClients := 100

		for i := range numClients {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				client := &Client{
					hub:           hub,
					send:          make(chan []byte, 256),
					subscriptions: make(map[string]bool),
				}

				hub.mu.Lock()
				hub.clients[client] = true
				hub.mu.Unlock()

				hub.Subscribe(client, "shared-channel")
				hub.Subscribe(client, "shared-channel") // Double subscribe should be safe
			}(i)
		}

		wg.Wait()
		if numClients != hub.ChannelSubscriberCount("shared-channel") {
			t.Fatalf("want %v, got %v", numClients, hub.ChannelSubscriberCount("shared-channel"))
		}
	})

	t.Run("handles concurrent broadcasts", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 1000),
			subscriptions: make(map[string]bool),
		}

		hub.register <- client
		time.Sleep(10 * time.Millisecond)

		var wg sync.WaitGroup
		numBroadcasts := 100

		for i := range numBroadcasts {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				_ = hub.Broadcast(Message{
					Type: TypeEvent,
					Data: id,
				})
			}(i)
		}

		wg.Wait()

		// Give time for broadcasts to be delivered
		time.Sleep(100 * time.Millisecond)

		// Count received messages
		received := 0
		timeout := time.After(100 * time.Millisecond)
	loop:
		for {
			select {
			case <-client.send:
				received++
			case <-timeout:
				break loop
			}
		}
		if

		// All or most broadcasts should be received
		received < numBroadcasts-10 {
			t.Fatalf("expected %v >= %v", received, numBroadcasts-10)
		}
	})
}

func TestHub_HandleWebSocket(t *testing.T) {
	t.Run("alias works same as Handler", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		// Test with HandleWebSocket directly
		server := httptest.NewServer(http.HandlerFunc(hub.HandleWebSocket))
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 1, hub.ClientCount())
		}
	})
}

func TestMustMarshal(t *testing.T) {
	t.Run("marshals valid data", func(t *testing.T) {
		data := mustMarshal(Message{Type: TypePong})
		if !strings.Contains(string(data), "pong") {
			t.Fatalf("expected %v to contain %v", string(data), "pong")
		}
	})

	t.Run("handles unmarshalable data without panic", func(t *testing.T) {
		// Create a channel which cannot be marshaled
		// This should not panic, even if it returns nil
		ch := make(chan int)
		func() {
			_ = mustMarshal(ch)
		}()
	})
}

// --- Phase 0: Additional coverage tests ---

func TestHub_Run_ShutdownClosesClients(t *testing.T) {
	t.Run("closes all client send channels on shutdown", func(t *testing.T) {
		disconnectCalled := make(chan *Client, 2)
		hub := NewHubWithConfig(HubConfig{
			OnDisconnect: func(client *Client) {
				disconnectCalled <- client
			},
		})
		ctx, cancel := context.WithCancel(context.Background())
		go hub.Run(ctx)

		// Register clients via the hub's Run loop
		client1 := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}
		client2 := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		hub.register <- client1
		hub.register <- client2
		time.Sleep(20 * time.Millisecond)
		if 2 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 2, hub.ClientCount())
		}
		hub.Subscribe(client1, "shutdown-channel")
		if 1 != hub.ChannelCount() {
			t.Fatalf("want %v, got %v", 1, hub.ChannelCount())
		}
		if 1 != hub.ChannelSubscriberCount("shutdown-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("shutdown-channel"))
		}

		// Cancel context to trigger shutdown
		cancel()
		time.Sleep(50 * time.Millisecond)

		// Send channels should be closed
		_, ok1 := <-client1.send
		if ok1 {
			t.Fatal("expected false")
		}
		_, ok2 := <-client2.send
		if ok2 {
			t.Fatal("expected false")
		}

		select {
		case <-disconnectCalled:
		case <-time.After(time.Second):
			t.Fatal("expected disconnect callback for client1 or client2")
		}
		select {
		case <-disconnectCalled:
		case <-time.After(time.Second):
			t.Fatal("expected disconnect callback for both clients")
		}
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}
		if 0 != hub.ChannelCount() {
			t.Fatalf("want %v, got %v", 0, hub.ChannelCount())
		}
		if 0 != hub.ChannelSubscriberCount("shutdown-channel") {
			t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("shutdown-channel"))
		}
	})
}

func TestHub_Run_BroadcastToClientWithFullBuffer(t *testing.T) {
	t.Run("unregisters client with full send buffer during broadcast", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		// Create a client with a tiny buffer that will overflow
		slowClient := &Client{
			hub:           hub,
			send:          make(chan []byte, 1), // Very small buffer
			subscriptions: make(map[string]bool),
		}

		hub.register <- slowClient
		time.Sleep(20 * time.Millisecond)
		if 1 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 1, hub.ClientCount())
		}

		// Fill the client's send buffer
		slowClient.send <- []byte("blocking")

		// Broadcast should trigger the overflow path
		err := hub.Broadcast(Message{Type: TypeEvent, Data: "overflow"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Wait for the unregister goroutine to fire
		time.Sleep(100 * time.Millisecond)
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}
	})
}

func TestHub_Run_BroadcastWithClosedSendChannel(t *testing.T) {
	t.Run("unregisters client whose send channel has already been closed", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 1),
			subscriptions: make(map[string]bool),
		}

		hub.register <- client
		time.Sleep(20 * time.Millisecond)
		if 1 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 1, hub.ClientCount())
		}

		// Simulate a concurrent close before the hub attempts delivery.
		client.closeSend()

		err := hub.Broadcast(Message{Type: TypeEvent, Data: "closed-channel"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		time.Sleep(100 * time.Millisecond)
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}
	})
}

func TestHub_SendToChannel_ClientBufferFull(t *testing.T) {
	t.Run("skips client with full send buffer", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 1), // Tiny buffer
			subscriptions: make(map[string]bool),
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		hub.Subscribe(client, "test-channel")

		// Fill the client buffer
		client.send <- []byte("blocking")

		// SendToChannel should not block; it skips the full client
		err := hub.SendToChannel("test-channel", Message{Type: TypeEvent, Data: "overflow"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestHub_SendToChannel_ClosedSendChannel(t *testing.T) {
	t.Run("skips client whose send channel has already been closed", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 1),
			subscriptions: make(map[string]bool),
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		hub.Subscribe(client, "test-channel")

		client.closeSend()

		err := hub.SendToChannel("test-channel", Message{Type: TypeEvent, Data: "closed-channel"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestHub_Broadcast_MarshalError(t *testing.T) {
	t.Run("returns error for unmarshalable message", func(t *testing.T) {
		hub := NewHub()

		// Channels cannot be marshalled to JSON
		msg := Message{
			Type: TypeEvent,
			Data: make(chan int),
		}

		err := hub.Broadcast(msg)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to marshal message") {
			t.Fatalf("expected %v to contain %v", err.Error(), "failed to marshal message")
		}
	})
}

func TestHub_SendToChannel_MarshalError(t *testing.T) {
	t.Run("returns error for unmarshalable message", func(t *testing.T) {
		hub := NewHub()

		msg := Message{
			Type: TypeEvent,
			Data: make(chan int),
		}

		err := hub.SendToChannel("any-channel", msg)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to marshal message") {
			t.Fatalf("expected %v to contain %v", err.Error(), "failed to marshal message")
		}
	})
}

func TestHub_Handler_UpgradeError(t *testing.T) {
	t.Run("returns silently on non-websocket request", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		// Make a plain HTTP request (not a WebSocket upgrade)
		resp, err := http.Get(server.URL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if

		// The handler should have returned an error response
		http.StatusBadRequest != resp.StatusCode {
			t.Fatalf("want %v, got %v", http.StatusBadRequest, resp.StatusCode)
		}
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}
	})
}

func TestClient_Close(t *testing.T) {
	t.Run("unregisters and closes connection", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 1, hub.ClientCount())
		}

		// Get the client from the hub
		hub.mu.RLock()
		var client *Client
		for c := range hub.clients {
			client = c
			break
		}
		hub.mu.RUnlock()
		if client == nil {
			t.Fatal("expected non-nil")

			// Close via Client.Close()
		}

		err = client.Close()
		// conn.Close may return an error if already closing, that is acceptable
		_ = err

		time.Sleep(50 * time.Millisecond)
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}

		// Connection should be closed — writing should fail
		_ = conn.Close() // ensure clean up
	})
}

func TestReadPump_MalformedJSON(t *testing.T) {
	t.Run("ignores malformed JSON messages", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Send malformed JSON — should be ignored without disconnecting
		err = conn.WriteMessage(websocket.TextMessage, []byte("this is not json"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Send a valid subscribe after the bad message — client should still be alive
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}
	})
}

func TestReadPump_SubscribeWithNonStringData(t *testing.T) {
	t.Run("ignores subscribe with non-string data", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Send subscribe with numeric data instead of string
		err = conn.WriteJSON(map[string]any{
			"type": "subscribe",
			"data": 12345,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if

		// No channels should have been created
		0 != hub.ChannelCount() {
			t.Fatalf("want %v, got %v", 0, hub.ChannelCount())
		}
	})
}

func TestReadPump_UnsubscribeWithNonStringData(t *testing.T) {
	t.Run("ignores unsubscribe with non-string data", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Subscribe first with valid data
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}

		// Send unsubscribe with non-string data — should be ignored
		err = conn.WriteJSON(map[string]any{
			"type": "unsubscribe",
			"data": []string{"test-channel"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if

		// Channel should still have the subscriber
		1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}
	})
}

func TestReadPump_UnknownMessageType(t *testing.T) {
	t.Run("ignores unknown message types without disconnecting", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Send a message with an unknown type
		err = conn.WriteJSON(Message{Type: "unknown_type", Data: "anything"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Client should still be connected
		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 1, hub.ClientCount())
		}
	})
}

func TestWritePump_SendsCloseOnChannelClose(t *testing.T) {
	t.Run("sends close message when send channel is closed", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Unregister the client (which closes its send channel)
		hub.mu.RLock()
		var client *Client
		for c := range hub.clients {
			client = c
			break
		}
		hub.mu.RUnlock()

		hub.unregister <- client
		time.Sleep(50 * time.Millisecond)

		// The client should receive a close message and the connection should end
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, _, readErr := conn.ReadMessage()
		if readErr == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestWritePump_BatchesMessages(t *testing.T) {
	t.Run("batches multiple queued messages into a single write", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Get the client
		hub.mu.RLock()
		var client *Client
		for c := range hub.clients {
			client = c
			break
		}
		hub.mu.RUnlock()
		if client == nil {
			t.Fatal("expected non-nil")

			// Queue multiple messages rapidly through the hub so writePump can
			// batch them into a single websocket frame when possible.
		}
		if hub.Broadcast(Message{Type: TypeEvent, Data: "batch-1"}) != nil {
			t.Fatalf("unexpected error: %v", hub.Broadcast(Message{Type: TypeEvent, Data: "batch-1"}))
		}
		if hub.Broadcast(Message{Type: TypeEvent, Data: "batch-2"}) != nil {
			t.Fatalf("unexpected error: %v", hub.Broadcast(Message{Type: TypeEvent, Data: "batch-2"}))
		}
		if hub.Broadcast(Message{Type: TypeEvent, Data: "batch-3"}) != nil {
			t.Fatalf("unexpected error: %v", hub.Broadcast(Message{Type: TypeEvent, Data: "batch-3"}))
		}

		// Read frames until we have observed all three payloads or time out.
		deadline := time.Now().Add(time.Second)
		seen := map[string]bool{}
		for len(seen) < 3 {
			conn.SetReadDeadline(deadline)
			_, data, readErr := conn.ReadMessage()
			if readErr != nil {
				t.Fatalf("unexpected error: %v", readErr)
			}

			content := string(data)
			for _, token := range []string{"batch-1", "batch-2", "batch-3"} {
				if strings.Contains(content, token) {
					seen[token] = true
				}
			}
		}
	})
}

func TestHub_MultipleClientsOnChannel(t *testing.T) {
	t.Run("delivers channel messages to all subscribers", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		// Connect three clients and subscribe them all to the same channel
		conns := make([]*websocket.Conn, 3)
		for i := range conns {
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer conn.Close()
			conns[i] = conn
		}

		time.Sleep(50 * time.Millisecond)
		if 3 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 3, hub.ClientCount())
		}

		// Subscribe all to "shared"
		for _, conn := range conns {
			err := conn.WriteJSON(Message{Type: TypeSubscribe, Data: "shared"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		time.Sleep(50 * time.Millisecond)
		if 3 != hub.ChannelSubscriberCount("shared") {
			t.Fatalf("want %v, got %v", 3, hub.ChannelSubscriberCount("shared"))
		}

		// Send to channel
		err := hub.SendToChannel("shared", Message{Type: TypeEvent, Data: "hello all"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// All three clients should receive the message
		for _, conn := range conns {
			conn.SetReadDeadline(time.Now().Add(time.Second))
			var received Message
			err := conn.ReadJSON(&received)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if TypeEvent != received.Type {
				t.Fatalf("want %v, got %v", TypeEvent, received.Type)
			}
			if "hello all" != received.Data {
				t.Fatalf("want %v, got %v", "hello all", received.Data)
			}
		}
	})
}

func TestHub_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	t.Run("handles concurrent subscribe and unsubscribe safely", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		var wg sync.WaitGroup
		numClients := 50

		clients := make([]*Client, numClients)
		for i := range numClients {
			clients[i] = &Client{
				hub:           hub,
				send:          make(chan []byte, 256),
				subscriptions: make(map[string]bool),
			}
			hub.mu.Lock()
			hub.clients[clients[i]] = true
			hub.mu.Unlock()
		}

		// Half subscribe, half unsubscribe concurrently
		for i := range numClients {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				hub.Subscribe(clients[idx], "race-channel")
			}(i)
		}
		wg.Wait()

		// Now concurrently unsubscribe half and subscribe the other half to a new channel
		for i := range numClients {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				if idx%2 == 0 {
					hub.Unsubscribe(clients[idx], "race-channel")
				} else {
					hub.Subscribe(clients[idx], "another-channel")
				}
			}(i)
		}
		wg.Wait()
		if

		// Verify: half should remain on race-channel, half should be on another-channel
		numClients/2 != hub.ChannelSubscriberCount("race-channel") {
			t.Fatalf("want %v, got %v", numClients/2, hub.ChannelSubscriberCount("race-channel"))
		}
		if numClients/2 != hub.ChannelSubscriberCount("another-channel") {
			t.Fatalf("want %v, got %v", numClients/2, hub.ChannelSubscriberCount("another-channel"))
		}
	})
}

func TestHub_ProcessOutputEndToEnd(t *testing.T) {
	t.Run("end-to-end process output via WebSocket", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Subscribe to process channel
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "process:build-42"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		time.Sleep(50 * time.Millisecond)

		// Send lines one at a time with a small delay to avoid batching
		lines := []string{"Compiling...", "Linking...", "Done."}
		for _, line := range lines {
			err = hub.SendProcessOutput("build-42", line)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			time.Sleep(10 * time.Millisecond) // Allow writePump to flush each individually
		}

		// Read all three — messages may arrive individually or batched
		// with newline separators. ReadMessage gives raw frames.
		var received []Message
		for len(received) < 3 {
			conn.SetReadDeadline(time.Now().Add(time.Second))
			_, data, readErr := conn.ReadMessage()
			if readErr != nil {
				t.Fatalf("unexpected error: %v", readErr)
			}

			// A single frame may contain multiple newline-separated JSON objects
			parts := strings.SplitSeq(core.Trim(string(data)), "\n")
			for part := range parts {
				part = core.Trim(part)
				if part == "" {
					continue
				}
				var msg Message
				if !core.JSONUnmarshal([]byte(part), &msg).OK {
					t.Fatal("expected true")
				}
				received = append(received, msg)
			}
		}
		if len(received) != 3 {
			t.Fatalf("want len %v, got %v", 3, len(received))
		}
		for i, expected := range lines {
			if TypeProcessOutput != received[i].Type {
				t.Fatalf("want %v, got %v", TypeProcessOutput, received[i].Type)
			}
			if "build-42" != received[i].ProcessID {
				t.Fatalf("want %v, got %v", "build-42", received[i].ProcessID)
			}
			if expected != received[i].Data {
				t.Fatalf("want %v, got %v", expected, received[i].Data)
			}
		}
	})
}

func TestHub_ProcessStatusEndToEnd(t *testing.T) {
	t.Run("end-to-end process status via WebSocket", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Subscribe
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "process:job-7"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		time.Sleep(50 * time.Millisecond)

		// Send status
		err = hub.SendProcessStatus("job-7", "exited", 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		conn.SetReadDeadline(time.Now().Add(time.Second))
		var received Message
		err = conn.ReadJSON(&received)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if TypeProcessStatus != received.Type {
			t.Fatalf("want %v, got %v", TypeProcessStatus, received.Type)
		}
		if "job-7" != received.ProcessID {
			t.Fatalf("want %v, got %v", "job-7", received.ProcessID)
		}

		data, ok := received.Data.(map[string]any)
		if !ok {
			t.Fatal("expected true")
		}
		if "exited" != data["status"] {
			t.Fatalf("want %v, got %v", "exited", data["status"])
		}
		if float64(1) != data["exitCode"] {
			t.Fatalf("want %v, got %v", float64(1), data["exitCode"])
		}
	})
}

// --- Benchmarks ---

func BenchmarkBroadcast(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	// Register 100 clients
	for range 100 {
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}
		hub.register <- client
	}
	time.Sleep(50 * time.Millisecond)

	msg := Message{Type: TypeEvent, Data: "benchmark"}

	b.ResetTimer()
	for range b.N {
		_ = hub.Broadcast(msg)
	}
}

func BenchmarkSendToChannel(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	// Register 50 clients, all subscribed to the same channel
	for range 50 {
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}
		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		hub.Subscribe(client, "bench-channel")
	}

	msg := Message{Type: TypeEvent, Data: "benchmark"}

	b.ResetTimer()
	for range b.N {
		_ = hub.SendToChannel("bench-channel", msg)
	}
}

// --- Phase 1: Connection resilience tests ---

func TestNewHubWithConfig(t *testing.T) {
	t.Run("uses custom heartbeat and pong timeout", func(t *testing.T) {
		config := HubConfig{
			HeartbeatInterval: 5 * time.Second,
			PongTimeout:       10 * time.Second,
			WriteTimeout:      3 * time.Second,
		}
		hub := NewHubWithConfig(config)
		if 5*time.Second != hub.config.HeartbeatInterval {
			t.Fatalf("want %v, got %v", 5*time.Second, hub.config.HeartbeatInterval)
		}
		if 10*time.Second != hub.config.PongTimeout {
			t.Fatalf("want %v, got %v", 10*time.Second, hub.config.PongTimeout)
		}
		if 3*time.Second != hub.config.WriteTimeout {
			t.Fatalf("want %v, got %v", 3*time.Second, hub.config.WriteTimeout)
		}
	})

	t.Run("applies defaults for zero values", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{})
		if DefaultHeartbeatInterval != hub.config.HeartbeatInterval {
			t.Fatalf("want %v, got %v", DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
		}
		if DefaultPongTimeout != hub.config.PongTimeout {
			t.Fatalf("want %v, got %v", DefaultPongTimeout, hub.config.PongTimeout)
		}
		if DefaultWriteTimeout != hub.config.WriteTimeout {
			t.Fatalf("want %v, got %v", DefaultWriteTimeout, hub.config.WriteTimeout)
		}
	})

	t.Run("applies defaults for negative values", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			HeartbeatInterval: -1,
			PongTimeout:       -1,
			WriteTimeout:      -1,
		})
		if DefaultHeartbeatInterval != hub.config.HeartbeatInterval {
			t.Fatalf("want %v, got %v", DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
		}
		if DefaultPongTimeout != hub.config.PongTimeout {
			t.Fatalf("want %v, got %v", DefaultPongTimeout, hub.config.PongTimeout)
		}
		if DefaultWriteTimeout != hub.config.WriteTimeout {
			t.Fatalf("want %v, got %v", DefaultWriteTimeout, hub.config.WriteTimeout)
		}
	})

	t.Run("expands pong timeout when it does not exceed heartbeat interval", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			HeartbeatInterval: 20 * time.Second,
			PongTimeout:       10 * time.Second,
		})
		if 20*time.Second != hub.config.HeartbeatInterval {
			t.Fatalf("want %v, got %v", 20*time.Second, hub.config.HeartbeatInterval)
		}
		if 40*time.Second != hub.config.PongTimeout {
			t.Fatalf("want %v, got %v", 40*time.Second, hub.config.PongTimeout)
		}
	})
}

func TestDefaultHubConfig(t *testing.T) {
	t.Run("returns sensible defaults", func(t *testing.T) {
		config := DefaultHubConfig()
		if 30*time.Second != config.HeartbeatInterval {
			t.Fatalf("want %v, got %v", 30*time.Second, config.HeartbeatInterval)
		}
		if 60*time.Second != config.PongTimeout {
			t.Fatalf("want %v, got %v", 60*time.Second, config.PongTimeout)
		}
		if 10*time.Second != config.WriteTimeout {
			t.Fatalf("want %v, got %v", 10*time.Second, config.WriteTimeout)
		}
		if config.OnConnect != nil {
			t.Fatal("expected nil")
		}
		if config.OnDisconnect != nil {
			t.Fatal("expected nil")
		}
		if config.ChannelAuthoriser != nil {
			t.Fatal("expected nil")
		}
	})
}

func TestHub_ConnectionCallbacks(t *testing.T) {
	t.Run("calls OnConnect when client connects", func(t *testing.T) {
		connectCalled := make(chan *Client, 1)

		hub := NewHubWithConfig(HubConfig{
			OnConnect: func(client *Client) {
				connectCalled <- client
			},
		})
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		select {
		case c := <-connectCalled:
			if c == nil {
				t.Fatal("expected non-nil")
			}
		case <-time.After(time.Second):
			t.Fatal("OnConnect callback should have been called")
		}
	})

	t.Run("calls OnDisconnect when client disconnects", func(t *testing.T) {
		disconnectCalled := make(chan *Client, 1)

		hub := NewHubWithConfig(HubConfig{
			OnDisconnect: func(client *Client) {
				disconnectCalled <- client
			},
		})
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		// Close the connection to trigger disconnect
		conn.Close()

		select {
		case c := <-disconnectCalled:
			if c == nil {
				t.Fatal("expected non-nil")
			}
		case <-time.After(time.Second):
			t.Fatal("OnDisconnect callback should have been called")
		}
	})

	t.Run("does not call OnDisconnect for unknown client", func(t *testing.T) {
		disconnectCalled := make(chan struct{}, 1)

		hub := NewHubWithConfig(HubConfig{
			OnDisconnect: func(client *Client) {
				disconnectCalled <- struct{}{}
			},
		})
		ctx := t.Context()
		go hub.Run(ctx)

		// Send an unregister for a client that was never registered
		unknownClient := &Client{
			hub:           hub,
			send:          make(chan []byte, 1),
			subscriptions: make(map[string]bool),
		}
		hub.unregister <- unknownClient

		select {
		case <-disconnectCalled:
			t.Fatal("OnDisconnect should not be called for unknown client")
		case <-time.After(100 * time.Millisecond):
			// Good — callback was not called
		}
	})
}

func TestHub_ChannelAuthoriser(t *testing.T) {
	t.Run("rejects unauthorised subscriptions", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			ChannelAuthoriser: func(client *Client, channel string) bool {
				role, _ := client.Claims["role"].(string)
				return role == "admin" || strings.HasPrefix(channel, "public:")
			},
		})

		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 1),
			subscriptions: make(map[string]bool),
			Claims:        map[string]any{"role": "viewer"},
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()

		err := hub.Subscribe(client, "public:news")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = hub.Subscribe(client, "private:ops")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "subscription unauthorised") {
			t.Fatalf("expected %v to contain %v", err.Error(), "subscription unauthorised")
		}
		if 1 != hub.ChannelSubscriberCount("public:news") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("public:news"))
		}
		if 0 != hub.ChannelSubscriberCount("private:ops") {
			t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("private:ops"))
		}
	})
}

func TestHub_Subscribe_ReturnsError(t *testing.T) {
	t.Run("propagates authoriser failures", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			ChannelAuthoriser: func(client *Client, channel string) bool {
				return channel != "private:ops"
			},
		})

		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		err := hub.Subscribe(client, "private:ops")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "subscription unauthorised") {
			t.Fatalf("expected %v to contain %v", err.Error(), "subscription unauthorised")
		}
		if len(client.subscriptions) != 0 {
			t.Fatalf("expected empty, got %v", client.subscriptions)
		}
		if 0 != hub.ChannelCount() {
			t.Fatalf("want %v, got %v", 0, hub.ChannelCount())
		}
	})
}

func TestHub_CustomHeartbeat(t *testing.T) {
	t.Run("uses custom heartbeat interval for server pings", func(t *testing.T) {
		// Use a very short heartbeat to test it actually fires
		hub := NewHubWithConfig(HubConfig{
			HeartbeatInterval: 100 * time.Millisecond,
			PongTimeout:       500 * time.Millisecond,
			WriteTimeout:      500 * time.Millisecond,
		})
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		pingReceived := make(chan struct{}, 1)
		dialer := websocket.Dialer{}
		conn, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()

		conn.SetPingHandler(func(appData string) error {
			select {
			case pingReceived <- struct{}{}:
			default:
			}
			// Respond with pong
			return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})

		// Start reading in background to process control frames
		go func() {
			for {
				_, _, err := conn.ReadMessage()
				if err != nil {
					return
				}
			}
		}()

		select {
		case <-pingReceived:
			// Server ping received within custom heartbeat interval
		case <-time.After(time.Second):
			t.Fatal("should have received server ping within custom heartbeat interval")
		}
	})
}

func TestReconnectingClient_Connect(t *testing.T) {
	t.Run("connects to server and receives messages", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		connectCalled := make(chan struct{}, 1)
		msgReceived := make(chan Message, 1)

		rc := NewReconnectingClient(ReconnectConfig{
			URL: wsURL,
			OnConnect: func() {
				select {
				case connectCalled <- struct{}{}:
				default:
				}
			},
			OnMessage: func(msg Message) {
				select {
				case msgReceived <- msg:
				default:
				}
			},
		})

		// Run Connect in background
		clientCtx, clientCancel := context.WithCancel(context.Background())
		defer clientCancel()
		go rc.Connect(clientCtx)

		// Wait for connect
		select {
		case <-connectCalled:
			// Good
		case <-time.After(time.Second):
			t.Fatal("OnConnect should have been called")
		}
		if StateConnected != rc.State() {
			t.Fatalf("want %v, got %v", StateConnected, rc.State())
		}

		// Wait for client to register
		time.Sleep(50 * time.Millisecond)

		// Broadcast a message
		err := hub.Broadcast(Message{Type: TypeEvent, Data: "hello"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case msg := <-msgReceived:
			if TypeEvent != msg.Type {
				t.Fatalf("want %v, got %v", TypeEvent, msg.Type)
			}
			if "hello" != msg.Data {
				t.Fatalf("want %v, got %v", "hello", msg.Data)
			}
		case <-time.After(time.Second):
			t.Fatal("should have received the broadcast message")
		}

		clientCancel()
		time.Sleep(50 * time.Millisecond)
	})
}

func TestReconnectingClient_OnMessageRawBytes(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	wsURL := "ws" + core.TrimPrefix(server.URL, "http")

	rawReceived := make(chan []byte, 1)

	rc := NewReconnectingClient(ReconnectConfig{
		URL: wsURL,
		OnMessage: func(msg []byte) {
			copied := append([]byte(nil), msg...)
			select {
			case rawReceived <- copied:
			default:
			}
		},
	})

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()
	go rc.Connect(clientCtx)

	time.Sleep(50 * time.Millisecond)

	err := hub.Broadcast(Message{Type: TypeEvent, Data: "raw-bytes"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case data := <-rawReceived:
		if !strings.Contains(string(data), "raw-bytes") {
			t.Fatalf("expected %v to contain %v", string(data), "raw-bytes")
		}

		var received Message
		if !core.JSONUnmarshal(data, &received).OK {
			t.Fatal("expected true")
		}
		if TypeEvent != received.Type {
			t.Fatalf("want %v, got %v", TypeEvent, received.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("raw byte callback should have been invoked")
	}
}

func TestReconnectingClient_Reconnect(t *testing.T) {
	t.Run("reconnects after server restart", func(t *testing.T) {
		hub := NewHub()
		ctx, cancel := context.WithCancel(context.Background())
		go hub.Run(ctx)

		// Use a net.Listener so we control the port
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		server := &httptest.Server{
			Listener: listener,
			Config:   &http.Server{Handler: hub.Handler()},
		}
		server.Start()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")
		addr := listener.Addr().String()

		reconnectCalled := make(chan int, 5)
		disconnectCalled := make(chan struct{}, 5)
		connectCalled := make(chan struct{}, 5)

		rc := NewReconnectingClient(ReconnectConfig{
			URL:            wsURL,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     200 * time.Millisecond,
			OnConnect: func() {
				select {
				case connectCalled <- struct{}{}:
				default:
				}
			},
			OnDisconnect: func() {
				select {
				case disconnectCalled <- struct{}{}:
				default:
				}
			},
			OnReconnect: func(attempt int) {
				select {
				case reconnectCalled <- attempt:
				default:
				}
			},
		})

		clientCtx, clientCancel := context.WithCancel(context.Background())
		defer clientCancel()
		go rc.Connect(clientCtx)

		// Wait for initial connection
		select {
		case <-connectCalled:
		case <-time.After(time.Second):
			t.Fatal("initial connection should have succeeded")
		}
		if StateConnected != rc.State() {
			t.Fatalf("want %v, got %v", StateConnected, rc.State())
		}

		// Shut down the server to simulate disconnect
		cancel()
		server.Close()

		// Wait for disconnect callback
		select {
		case <-disconnectCalled:
			// Good
		case <-time.After(time.Second):
			t.Fatal("OnDisconnect should have been called")
		}

		// Start a new server on the same address for reconnection
		hub2 := NewHub()
		ctx2 := t.Context()
		go hub2.Run(ctx2)

		listener2, err := net.Listen("tcp", addr)
		if err != nil {
			t.Skipf("could not bind to same port: %v", err)
			return
		}

		newServer := &httptest.Server{
			Listener: listener2,
			Config:   &http.Server{Handler: hub2.Handler()},
		}
		newServer.Start()
		defer newServer.Close()

		// Wait for reconnection
		select {
		case attempt := <-reconnectCalled:
			if attempt <= 0 {
				t.Fatalf("expected %v > %v", attempt, 0)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("OnReconnect should have been called")
		}
		if StateConnected != rc.State() {
			t.Fatalf("want %v, got %v", StateConnected, rc.State())
		}
		clientCancel()
	})
}

func TestReconnectingClient_MaxRetries(t *testing.T) {
	t.Run("stops after max retries exceeded", func(t *testing.T) {
		// Use a URL that will never connect
		rc := NewReconnectingClient(ReconnectConfig{
			URL:            "ws://127.0.0.1:1", // Should refuse connection
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     50 * time.Millisecond,
			MaxRetries:     3,
		})

		errCh := make(chan error, 1)
		go func() {
			errCh <- rc.Connect(context.Background())
		}()

		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "max retries (3) exceeded") {
				t.Fatalf("expected %v to contain %v", err.Error(), "max retries (3) exceeded")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("should have stopped after max retries")
		}
		if StateDisconnected != rc.State() {
			t.Fatalf("want %v, got %v", StateDisconnected, rc.State())
		}
	})
}

func TestReconnectingClient_Send(t *testing.T) {
	t.Run("sends message to server", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		connected := make(chan struct{}, 1)

		rc := NewReconnectingClient(ReconnectConfig{
			URL: wsURL,
			OnConnect: func() {
				select {
				case connected <- struct{}{}:
				default:
				}
			},
		})

		clientCtx, clientCancel := context.WithCancel(context.Background())
		defer clientCancel()
		go rc.Connect(clientCtx)

		<-connected
		time.Sleep(50 * time.Millisecond)

		// Send a subscribe message
		err := rc.Send(Message{
			Type: TypeSubscribe,
			Data: "test-channel",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if 1 != hub.ChannelSubscriberCount("test-channel") {
			t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}

		clientCancel()
	})

	t.Run("serialises concurrent sends without panicking", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		connected := make(chan struct{}, 1)
		rc := NewReconnectingClient(ReconnectConfig{
			URL: wsURL,
			OnConnect: func() {
				select {
				case connected <- struct{}{}:
				default:
				}
			},
		})

		clientCtx, clientCancel := context.WithCancel(context.Background())
		defer clientCancel()
		go rc.Connect(clientCtx)

		select {
		case <-connected:
		case <-time.After(time.Second):
			t.Fatal("client should have connected")
		}
		time.Sleep(50 * time.Millisecond)

		const sends = 32
		errCh := make(chan error, sends)
		var wg sync.WaitGroup
		for i := range sends {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				errCh <- rc.Send(Message{
					Type: TypeSubscribe,
					Data: core.Sprintf("concurrent-%d", idx),
				})
			}(i)
		}

		wg.Wait()
		close(errCh)

		for err := range errCh {
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		time.Sleep(100 * time.Millisecond)
		if hub.ChannelCount() < 1 {
			t.Fatalf("expected %v >= %v", hub.ChannelCount(), 1)
		}
	})

	t.Run("returns error when not connected", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})

		err := rc.Send(Message{Type: TypePing})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "not connected") {
			t.Fatalf("expected %v to contain %v", err.Error(), "not connected")
		}
	})

	t.Run("returns error for unmarshalable message", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})
		// Force a conn to be set so we get past the nil check
		// to hit the marshal error first
		err := rc.Send(Message{Type: TypeEvent, Data: make(chan int)})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to marshal message") {
			t.Fatalf("expected %v to contain %v", err.Error(), "failed to marshal message")
		}
	})
}

func TestReconnectingClient_Close(t *testing.T) {
	t.Run("stops reconnection loop", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		connected := make(chan struct{}, 1)

		rc := NewReconnectingClient(ReconnectConfig{
			URL: wsURL,
			OnConnect: func() {
				select {
				case connected <- struct{}{}:
				default:
				}
			},
		})

		clientCtx := t.Context()

		done := make(chan error, 1)
		go func() {
			done <- rc.Connect(clientCtx)
		}()

		<-connected

		err := rc.Close()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case <-done:
			// Good — Connect returned
		case <-time.After(time.Second):
			t.Fatal("Connect should have returned after Close")
		}
	})

	t.Run("close when not connected is safe", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})

		err := rc.Close()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestReconnectingClient_ExponentialBackoff(t *testing.T) {
	t.Run("calculates correct backoff values", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL:               "ws://localhost:1",
			InitialBackoff:    100 * time.Millisecond,
			MaxBackoff:        1 * time.Second,
			BackoffMultiplier: 2.0,
		})
		if

		// attempt 1: 100ms
		100*time.Millisecond != rc.calculateBackoff(1) {
			t.Fatalf("want %v, got %v", 100*time.Millisecond, rc.calculateBackoff(1))
		}
		if
		// attempt 2: 200ms
		200*time.Millisecond != rc.calculateBackoff(2) {
			t.Fatalf("want %v, got %v", 200*time.Millisecond, rc.calculateBackoff(2))
		}
		if
		// attempt 3: 400ms
		400*time.Millisecond != rc.calculateBackoff(3) {
			t.Fatalf("want %v, got %v", 400*time.Millisecond, rc.calculateBackoff(3))
		}
		if
		// attempt 4: 800ms
		800*time.Millisecond != rc.calculateBackoff(4) {
			t.Fatalf("want %v, got %v", 800*time.Millisecond, rc.calculateBackoff(4))
		}
		if
		// attempt 5: capped at 1s
		1*time.Second != rc.calculateBackoff(5) {
			t.Fatalf("want %v, got %v", 1*time.Second, rc.calculateBackoff(5))
		}
		if
		// attempt 10: still capped at 1s
		1*time.Second != rc.calculateBackoff(10) {
			t.Fatalf("want %v, got %v", 1*time.Second, rc.calculateBackoff(10))
		}
	})
}

func TestReconnectingClient_Defaults(t *testing.T) {
	t.Run("applies defaults for zero config values", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})
		if 1*time.Second != rc.config.InitialBackoff {
			t.Fatalf("want %v, got %v", 1*time.Second, rc.config.InitialBackoff)
		}
		if 30*time.Second != rc.config.MaxBackoff {
			t.Fatalf("want %v, got %v", 30*time.Second, rc.config.MaxBackoff)
		}
		if 2.0 != rc.config.BackoffMultiplier {
			t.Fatalf("want %v, got %v", 2.0, rc.config.BackoffMultiplier)
		}
		if rc.config.Dialer == nil {
			t.Fatal("expected non-nil")
		}
	})
}

func TestReconnectingClient_ContextCancel(t *testing.T) {
	t.Run("returns context error on cancel during backoff", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL:            "ws://127.0.0.1:1",
			InitialBackoff: 10 * time.Second, // Long backoff
		})

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() {
			done <- rc.Connect(ctx)
		}()

		// Allow first dial attempt to fail
		time.Sleep(200 * time.Millisecond)

		// Cancel during backoff
		cancel()

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if context.Canceled != err {
				t.Fatalf("want %v, got %v", context.Canceled, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Connect should have returned after context cancel")
		}
	})
}

func TestConnectionState(t *testing.T) {
	t.Run("state constants are distinct", func(t *testing.T) {
		if StateDisconnected == StateConnecting {
			t.Fatalf("did not expect %v", StateConnecting)
		}
		if StateConnecting == StateConnected {
			t.Fatalf("did not expect %v", StateConnected)
		}
		if StateDisconnected == StateConnected {
			t.Fatalf("did not expect %v", StateConnected)
		}
	})
}

// ---------------------------------------------------------------------------
// Hub.Run lifecycle — register, broadcast delivery, unregister via channels
// ---------------------------------------------------------------------------

func TestHubRun_RegisterClient_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	hub.register <- client
	time.Sleep(20 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}
}

func TestHubRun_BroadcastDelivery_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	hub.register <- client
	time.Sleep(20 * time.Millisecond)

	err := hub.Broadcast(Message{Type: TypeEvent, Data: "lifecycle-test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Hub.Run loop delivers the broadcast to the client's send channel
	select {
	case msg := <-client.send:
		var received Message
		if !core.JSONUnmarshal(msg, &received).OK {
			t.Fatal("expected true")
		}
		if TypeEvent != received.Type {
			t.Fatalf("want %v, got %v", TypeEvent, received.Type)
		}
		if "lifecycle-test" != received.Data {
			t.Fatalf("want %v, got %v", "lifecycle-test", received.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("broadcast should be delivered via hub loop")
	}
}

func TestHubRun_UnregisterClient_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	hub.register <- client
	time.Sleep(20 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}

	// Subscribe so we can verify channel cleanup
	hub.Subscribe(client, "lifecycle-chan")
	if 1 != hub.ChannelSubscriberCount("lifecycle-chan") {
		t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("lifecycle-chan"))
	}

	hub.unregister <- client
	time.Sleep(20 * time.Millisecond)
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
	if 0 != hub.ChannelSubscriberCount("lifecycle-chan") {
		t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("lifecycle-chan"))
	}
}

func TestHubRun_UnregisterIgnoresDuplicate_Bad(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	hub.register <- client
	time.Sleep(20 * time.Millisecond)

	hub.unregister <- client
	time.Sleep(20 * time.Millisecond)

	// Second unregister should not panic or block
	done := make(chan struct{})
	go func() {
		hub.unregister <- client
		close(done)
	}()

	select {
	case <-done:
		// Good -- no panic, no block
	case <-time.After(time.Second):
		t.Fatal("duplicate unregister should not block")
	}
}

// ---------------------------------------------------------------------------
// Subscribe / Unsubscribe — additional channel management tests
// ---------------------------------------------------------------------------

func TestSubscribe_MultipleChannels_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	hub.Subscribe(client, "alpha")
	hub.Subscribe(client, "beta")
	hub.Subscribe(client, "gamma")
	if 3 != hub.ChannelCount() {
		t.Fatalf("want %v, got %v", 3, hub.ChannelCount())
	}
	subs := client.Subscriptions()
	if len(subs) != 3 {
		t.Fatalf("want len %v, got %v", 3, len(subs))
	}
	if !slices.Contains(subs, "alpha") {
		t.Fatalf("expected %v to contain %v", subs, "alpha")
	}
	if !slices.Contains(subs, "beta") {
		t.Fatalf("expected %v to contain %v", subs, "beta")
	}
	if !slices.Contains(subs, "gamma") {
		t.Fatalf("expected %v to contain %v", subs, "gamma")
	}
}

func TestSubscribe_IdempotentDoubleSubscribe_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	hub.Subscribe(client, "dupl")
	hub.Subscribe(client, "dupl")
	if

	// Still only one subscriber entry in the channel map
	1 != hub.ChannelSubscriberCount("dupl") {
		t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("dupl"))
	}
}

func TestUnsubscribe_PartialLeave_Good(t *testing.T) {
	hub := NewHub()
	client1 := &Client{hub: hub, send: make(chan []byte, 256), subscriptions: make(map[string]bool)}
	client2 := &Client{hub: hub, send: make(chan []byte, 256), subscriptions: make(map[string]bool)}

	hub.Subscribe(client1, "shared")
	hub.Subscribe(client2, "shared")
	if 2 != hub.ChannelSubscriberCount("shared") {
		t.Fatalf("want %v, got %v", 2, hub.ChannelSubscriberCount("shared"))
	}

	hub.Unsubscribe(client1, "shared")
	if 1 != hub.ChannelSubscriberCount("shared") {
		t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("shared"))
	}

	// Channel still exists because client2 is subscribed
	hub.mu.RLock()
	_, exists := hub.channels["shared"]
	hub.mu.RUnlock()
	if !exists {
		t.Fatal("expected true")
	}
}

// ---------------------------------------------------------------------------
// SendToChannel — multiple subscribers
// ---------------------------------------------------------------------------

func TestSendToChannel_MultipleSubscribers_Good(t *testing.T) {
	hub := NewHub()
	clients := make([]*Client, 5)
	for i := range clients {
		clients[i] = &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}
		hub.Subscribe(clients[i], "multi")
	}

	err := hub.SendToChannel("multi", Message{Type: TypeEvent, Data: "fanout"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, c := range clients {
		select {
		case msg := <-c.send:
			var received Message
			if !core.JSONUnmarshal(msg, &received).OK {
				t.Fatal("expected true")
			}
			if "multi" != received.Channel {
				t.Fatalf("want %v, got %v", "multi", received.Channel)
			}
		case <-time.After(time.Second):
			t.Fatalf("client %d should have received the message", i)
		}
	}
}

// ---------------------------------------------------------------------------
// SendProcessOutput / SendProcessStatus — edge cases
// ---------------------------------------------------------------------------

func TestSendProcessOutput_NoSubscribers_Good(t *testing.T) {
	hub := NewHub()
	err := hub.SendProcessOutput("orphan-proc", "some output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendProcessStatus_NonZeroExit_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub.Subscribe(client, "process:fail-1")

	err := hub.SendProcessStatus("fail-1", "exited", 137)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case msg := <-client.send:
		var received Message
		if !core.JSONUnmarshal(msg, &received).OK {
			t.Fatal("expected true")
		}
		if TypeProcessStatus != received.Type {
			t.Fatalf("want %v, got %v", TypeProcessStatus, received.Type)
		}
		if "fail-1" != received.ProcessID {
			t.Fatalf("want %v, got %v", "fail-1", received.ProcessID)
		}
		data := received.Data.(map[string]any)
		if "exited" != data["status"] {
			t.Fatalf("want %v, got %v", "exited", data["status"])
		}
		if float64(137) != data["exitCode"] {
			t.Fatalf("want %v, got %v", float64(137), data["exitCode"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected process status message")
	}
}

// ---------------------------------------------------------------------------
// readPump — ping with timestamp verification
// ---------------------------------------------------------------------------

func TestReadPump_PingTimestamp_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	err = conn.WriteJSON(Message{Type: TypePing})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var pong Message
	err = conn.ReadJSON(&pong)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if TypePong != pong.Type {
		t.Fatalf("want %v, got %v", TypePong, pong.Type)
	}
	if pong.Timestamp.IsZero() {
		t.Fatal("expected false")
	}
}

// ---------------------------------------------------------------------------
// writePump — batch sending with multiple messages
// ---------------------------------------------------------------------------

func TestWritePump_BatchMultipleMessages_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	// Rapidly send multiple broadcasts so they queue up
	numMessages := 10
	for i := range numMessages {
		err := hub.Broadcast(Message{
			Type: TypeEvent,
			Data: core.Sprintf("batch-%d", i),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Read all messages — batched with newline separators
	received := 0
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for received < numMessages {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		parts := strings.SplitSeq(string(raw), "\n")
		for part := range parts {
			part = core.Trim(part)
			if part == "" {
				continue
			}
			var msg Message
			if core.JSONUnmarshal([]byte(part), &msg).OK {
				received++
			}
		}
	}
	if numMessages != received {
		t.Fatalf("want %v, got %v", numMessages, received)
	}
}

// ---------------------------------------------------------------------------
// Integration — unsubscribe stops delivery
// ---------------------------------------------------------------------------

func TestIntegration_UnsubscribeStopsDelivery_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	// Subscribe
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "temp:feed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify we receive messages on the channel
	err = hub.SendToChannel("temp:feed", Message{Type: TypeEvent, Data: "before-unsub"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var msg1 Message
	err = conn.ReadJSON(&msg1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if "before-unsub" != msg1.Data {
		t.Fatalf("want %v, got %v", "before-unsub", msg1.Data)
	}

	// Unsubscribe
	err = conn.WriteJSON(Message{Type: TypeUnsubscribe, Data: "temp:feed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Send another message -- client should NOT receive it
	err = hub.SendToChannel("temp:feed", Message{Type: TypeEvent, Data: "after-unsub"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Try to read -- should timeout (no message delivered)
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var msg2 Message
	err = conn.ReadJSON(&msg2)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Integration — broadcast reaches all clients (no channel subscription)
// ---------------------------------------------------------------------------

func TestIntegration_BroadcastReachesAllClients_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	numClients := 3
	conns := make([]*websocket.Conn, numClients)
	for i := range numClients {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer conn.Close()
		conns[i] = conn
	}

	time.Sleep(100 * time.Millisecond)
	if numClients != hub.ClientCount() {
		t.Fatalf("want %v, got %v", numClients, hub.ClientCount())
	}

	// Broadcast -- no channel subscription needed
	err := hub.Broadcast(Message{Type: TypeError, Data: "global-alert"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var received Message
		err := conn.ReadJSON(&received)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if TypeError != received.Type {
			t.Fatalf("want %v, got %v", TypeError, received.Type)
		}
		if "global-alert" != received.Data {
			t.Fatalf("want %v, got %v", "global-alert", received.Data)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration — disconnect cleans up all subscriptions
// ---------------------------------------------------------------------------

func TestIntegration_DisconnectCleansUpEverything_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Subscribe to multiple channels
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "ch-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "ch-b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}
	if 1 != hub.ChannelSubscriberCount("ch-a") {
		t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("ch-a"))
	}
	if 1 != hub.ChannelSubscriberCount("ch-b") {
		t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("ch-b"))
	}

	// Disconnect
	conn.Close()
	time.Sleep(100 * time.Millisecond)
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
	if 0 != hub.ChannelSubscriberCount("ch-a") {
		t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("ch-a"))
	}
	if 0 != hub.ChannelSubscriberCount("ch-b") {
		t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("ch-b"))
	}
	if 0 != hub.ChannelCount() {
		t.Fatalf("want %v, got %v", 0, hub.ChannelCount())
	}
}

func TestIntegration_ChannelAuthoriser_RejectsForbiddenSubscription_Good(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		Authenticator: AuthenticatorFunc(func(r *http.Request) AuthResult {
			return AuthResult{
				Valid:  true,
				UserID: "user-1",
				Claims: map[string]any{"role": "viewer"},
			}
		}),
		ChannelAuthoriser: func(client *Client, channel string) bool {
			role, _ := client.Claims["role"].(string)
			return role == "admin" || strings.HasPrefix(channel, "public:")
		},
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "private:ops"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var response Message
	if conn.ReadJSON(&response) != nil {
		t.Fatalf("unexpected error: %v", conn.ReadJSON(&response))
	}
	if TypeError != response.Type {
		t.Fatalf("want %v, got %v", TypeError, response.Type)
	}
	if !strings.Contains(response.Data.(string), "subscription unauthorised") {
		t.Fatalf("expected %v to contain %v", response.Data.(string), "subscription unauthorised")
	}
	if 0 != hub.ChannelSubscriberCount("private:ops") {
		t.Fatalf("want %v, got %v", 0, hub.ChannelSubscriberCount("private:ops"))
	}

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "public:news"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ChannelSubscriberCount("public:news") {
		t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("public:news"))
	}
}

// ---------------------------------------------------------------------------
// Concurrent broadcast + subscribe via hub loop (race test)
// ---------------------------------------------------------------------------

func TestConcurrentSubscribeAndBroadcast_Good(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			client := &Client{
				hub:           hub,
				send:          make(chan []byte, 256),
				subscriptions: make(map[string]bool),
			}
			hub.register <- client
		}(i)
		go func(id int) {
			defer wg.Done()
			_ = hub.Broadcast(Message{Type: TypeEvent, Data: id})
		}(i)
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)
	if 50 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 50, hub.ClientCount())
	}
}

func TestHub_Handler_RejectsWhenNotRunning(t *testing.T) {
	// Handler should not block or register clients when the hub loop is not running.
	hub := NewHub()

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if 0 != hub.ClientCount() {
			t.Fatalf("want %v, got %v", 0, hub.ClientCount())
		}
		return
	}

	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, readErr := conn.ReadMessage()
	if readErr == nil {
		t.Fatal("expected error, got nil")
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
}

func TestHub_OnConnect_CallbackPanic_DoesNotCrashHub(t *testing.T) {
	ctxErr := make(chan error, 1)

	hub := NewHubWithConfig(HubConfig{
		OnConnect: func(*Client) {
			panic("panic in onConnect")
		},
		OnDisconnect: func(client *Client) {
			select {
			case ctxErr <- nil:
			default:
			}
		},
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}

	conn.Close()
	time.Sleep(50 * time.Millisecond)
	if len(ctxErr) != 1 {
		t.Fatalf("want len %v, got %v", 1, len(ctxErr))
	}
}
