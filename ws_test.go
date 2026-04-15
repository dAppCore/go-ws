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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsURL converts an httptest server URL to a WebSocket URL.
func wsURL(server *httptest.Server) string {
	return "ws" + core.TrimPrefix(server.URL, "http")
}

func TestNewHub(t *testing.T) {
	t.Run("creates hub with initialised maps", func(t *testing.T) {
		hub := NewHub()

		require.NotNil(t, hub)
		assert.NotNil(t, hub.clients)
		assert.NotNil(t, hub.broadcast)
		assert.NotNil(t, hub.register)
		assert.NotNil(t, hub.unregister)
		assert.NotNil(t, hub.channels)
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
		require.NoError(t, err)
	})

	t.Run("returns error when channel full", func(t *testing.T) {
		hub := NewHub()
		// Fill the broadcast channel
		for range 256 {
			hub.broadcast <- []byte("test")
		}

		err := hub.Broadcast(Message{Type: TypeEvent})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "broadcast channel full")
	})
}

func TestHub_Stats(t *testing.T) {
	t.Run("returns empty stats for new hub", func(t *testing.T) {
		hub := NewHub()

		stats := hub.Stats()

		assert.Equal(t, 0, stats.Clients)
		assert.Equal(t, 0, stats.Channels)
		assert.Equal(t, 0, stats.Subscribers)
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

		assert.Equal(t, 2, stats.Clients)
		assert.Equal(t, 2, stats.Channels)
		assert.Equal(t, 3, stats.Subscribers)
	})
}

func TestHub_ClientCount(t *testing.T) {
	t.Run("returns zero for empty hub", func(t *testing.T) {
		hub := NewHub()
		assert.Equal(t, 0, hub.ClientCount())
	})

	t.Run("counts connected clients", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.clients[&Client{}] = true
		hub.clients[&Client{}] = true
		hub.mu.Unlock()

		assert.Equal(t, 2, hub.ClientCount())
	})
}

func TestHub_ChannelCount(t *testing.T) {
	t.Run("returns zero for empty hub", func(t *testing.T) {
		hub := NewHub()
		assert.Equal(t, 0, hub.ChannelCount())
	})

	t.Run("counts active channels", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.channels["channel1"] = make(map[*Client]bool)
		hub.channels["channel2"] = make(map[*Client]bool)
		hub.mu.Unlock()

		assert.Equal(t, 2, hub.ChannelCount())
	})
}

func TestHub_ChannelSubscriberCount(t *testing.T) {
	t.Run("returns zero for non-existent channel", func(t *testing.T) {
		hub := NewHub()
		assert.Equal(t, 0, hub.ChannelSubscriberCount("non-existent"))
	})

	t.Run("counts subscribers in channel", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.channels["test-channel"] = make(map[*Client]bool)
		hub.channels["test-channel"][&Client{}] = true
		hub.channels["test-channel"][&Client{}] = true
		hub.mu.Unlock()

		assert.Equal(t, 2, hub.ChannelSubscriberCount("test-channel"))
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
		require.NoError(t, err)

		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))
		assert.True(t, client.subscriptions["test-channel"])
	})

	t.Run("creates channel if not exists", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		err := hub.Subscribe(client, "new-channel")
		require.NoError(t, err)

		hub.mu.RLock()
		_, exists := hub.channels["new-channel"]
		hub.mu.RUnlock()

		assert.True(t, exists)
	})

	t.Run("rejects invalid channel names", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		err := hub.Subscribe(client, "bad channel")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid channel name")
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
		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))

		hub.Unsubscribe(client, "test-channel")
		assert.Equal(t, 0, hub.ChannelSubscriberCount("test-channel"))
		assert.False(t, client.subscriptions["test-channel"])
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

		assert.False(t, exists, "empty channel should be removed")
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
		require.NoError(t, err)

		select {
		case msg := <-client.send:
			var received Message
			require.True(t, core.JSONUnmarshal(msg, &received).OK)
			assert.Equal(t, TypeEvent, received.Type)
			assert.Equal(t, "test-channel", received.Channel)
		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("returns nil for non-existent channel", func(t *testing.T) {
		hub := NewHub()

		err := hub.SendToChannel("non-existent", Message{Type: TypeEvent})
		assert.NoError(t, err, "should not error for non-existent channel")
	})

	t.Run("rejects invalid channel names", func(t *testing.T) {
		hub := NewHub()

		err := hub.SendToChannel("bad channel", Message{Type: TypeEvent})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid channel name")
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
		require.NoError(t, err)

		select {
		case msg := <-client.send:
			var received Message
			require.True(t, core.JSONUnmarshal(msg, &received).OK)
			assert.Equal(t, TypeProcessOutput, received.Type)
			assert.Equal(t, "proc-1", received.ProcessID)
			assert.Equal(t, "hello world", received.Data)
		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()

		err := hub.SendProcessOutput("bad process", "hello world")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid process ID")
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
		require.NoError(t, err)

		select {
		case msg := <-client.send:
			var received Message
			require.True(t, core.JSONUnmarshal(msg, &received).OK)
			assert.Equal(t, TypeProcessStatus, received.Type)
			assert.Equal(t, "proc-1", received.ProcessID)

			data, ok := received.Data.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "exited", data["status"])
			assert.Equal(t, float64(0), data["exitCode"])
		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()

		err := hub.SendProcessStatus("bad process", "exited", 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid process ID")
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
		require.NoError(t, err)

		select {
		case msg := <-client.send:
			var received Message
			require.True(t, core.JSONUnmarshal(msg, &received).OK)
			assert.Equal(t, TypeError, received.Type)
			assert.Equal(t, "something went wrong", received.Data)
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
		require.NoError(t, err)

		select {
		case msg := <-client.send:
			var received Message
			require.True(t, core.JSONUnmarshal(msg, &received).OK)
			assert.Equal(t, TypeEvent, received.Type)

			data, ok := received.Data.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "user_joined", data["event"])
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

		assert.Len(t, subs, 2)
		assert.Contains(t, subs, "channel1")
		assert.Contains(t, subs, "channel2")
	})
}

func TestClient_AllSubscriptions(t *testing.T) {
	t.Run("returns iterator over subscriptions", func(t *testing.T) {
		client := &Client{subscriptions: make(map[string]bool)}
		client.subscriptions["sub1"] = true
		client.subscriptions["sub2"] = true

		subs := slices.Collect(client.AllSubscriptions())
		assert.Len(t, subs, 2)
		assert.Contains(t, subs, "sub1")
		assert.Contains(t, subs, "sub2")
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
		assert.Len(t, clients, 2)
		assert.Contains(t, clients, client1)
		assert.Contains(t, clients, client2)
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
		assert.Len(t, channels, 2)
		assert.Contains(t, channels, "ch1")
		assert.Contains(t, channels, "ch2")
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
		require.True(t, r.OK)
		data := r.Value.([]byte)

		assert.Contains(t, string(data), `"type":"process_output"`)
		assert.Contains(t, string(data), `"channel":"process:1"`)
		assert.Contains(t, string(data), `"processId":"1"`)
		assert.Contains(t, string(data), `"data":"output line"`)
	})

	t.Run("unmarshals correctly", func(t *testing.T) {
		jsonStr := `{"type":"subscribe","data":"channel:test"}`

		var msg Message
		require.True(t, core.JSONUnmarshal([]byte(jsonStr), &msg).OK)

		assert.Equal(t, TypeSubscribe, msg.Type)
		assert.Equal(t, "channel:test", msg.Data)
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
		require.NoError(t, err)
		defer conn.Close()

		// Give time for registration
		time.Sleep(50 * time.Millisecond)

		assert.Equal(t, 1, hub.ClientCount())
	})

	t.Run("rejects cross-origin requests by default", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		header := http.Header{}
		header.Set("Origin", "https://evil.example")

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if conn != nil {
			conn.Close()
		}

		require.Error(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, 0, hub.ClientCount())
	})

	t.Run("allows custom origin policy", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		})
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		header := http.Header{}
		header.Set("Origin", "https://evil.example")

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		require.NoError(t, err)
		defer conn.Close()
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	})

	t.Run("handles subscribe message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)
		defer conn.Close()

		// Send subscribe message
		subscribeMsg := Message{
			Type: TypeSubscribe,
			Data: "test-channel",
		}
		err = conn.WriteJSON(subscribeMsg)
		require.NoError(t, err)

		// Give time for subscription
		time.Sleep(50 * time.Millisecond)

		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))
	})

	t.Run("rejects invalid subscribe channel names", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)
		defer conn.Close()

		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "bad channel"})
		require.NoError(t, err)

		var response Message
		conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		require.NoError(t, err)
		assert.Equal(t, TypeError, response.Type)
		assert.Contains(t, response.Data, "invalid channel name")
	})

	t.Run("handles unsubscribe message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)
		defer conn.Close()

		// Subscribe first
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))

		// Unsubscribe
		err = conn.WriteJSON(Message{Type: TypeUnsubscribe, Data: "test-channel"})
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 0, hub.ChannelSubscriberCount("test-channel"))
	})

	t.Run("responds to ping with pong", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)
		defer conn.Close()

		// Give time for registration
		time.Sleep(50 * time.Millisecond)

		// Send ping
		err = conn.WriteJSON(Message{Type: TypePing})
		require.NoError(t, err)

		// Read pong response
		var response Message
		conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		require.NoError(t, err)

		assert.Equal(t, TypePong, response.Type)
	})

	t.Run("broadcasts messages to clients", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)
		defer conn.Close()

		// Give time for registration
		time.Sleep(50 * time.Millisecond)

		// Broadcast a message
		err = hub.Broadcast(Message{
			Type: TypeEvent,
			Data: "broadcast test",
		})
		require.NoError(t, err)

		// Read the broadcast
		var response Message
		conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		require.NoError(t, err)

		assert.Equal(t, TypeEvent, response.Type)
		assert.Equal(t, "broadcast test", response.Data)
	})

	t.Run("unregisters client on connection close", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)

		// Wait for registration
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ClientCount())

		// Close connection
		conn.Close()

		// Wait for unregistration
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 0, hub.ClientCount())
	})

	t.Run("removes client from channels on disconnect", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)

		// Subscribe to channel
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))

		// Close connection
		conn.Close()
		time.Sleep(50 * time.Millisecond)

		// Channel should be cleaned up
		assert.Equal(t, 0, hub.ChannelSubscriberCount("test-channel"))
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

		assert.Equal(t, numClients, hub.ChannelSubscriberCount("shared-channel"))
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

		// All or most broadcasts should be received
		assert.GreaterOrEqual(t, received, numBroadcasts-10, "should receive most broadcasts")
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
		require.NoError(t, err)
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ClientCount())
	})
}

func TestMustMarshal(t *testing.T) {
	t.Run("marshals valid data", func(t *testing.T) {
		data := mustMarshal(Message{Type: TypePong})
		assert.Contains(t, string(data), "pong")
	})

	t.Run("handles unmarshalable data without panic", func(t *testing.T) {
		// Create a channel which cannot be marshaled
		// This should not panic, even if it returns nil
		ch := make(chan int)
		assert.NotPanics(t, func() {
			_ = mustMarshal(ch)
		})
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

		assert.Equal(t, 2, hub.ClientCount())
		hub.Subscribe(client1, "shutdown-channel")
		assert.Equal(t, 1, hub.ChannelCount())
		assert.Equal(t, 1, hub.ChannelSubscriberCount("shutdown-channel"))

		// Cancel context to trigger shutdown
		cancel()
		time.Sleep(50 * time.Millisecond)

		// Send channels should be closed
		_, ok1 := <-client1.send
		assert.False(t, ok1, "client1 send channel should be closed")
		_, ok2 := <-client2.send
		assert.False(t, ok2, "client2 send channel should be closed")

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
		assert.Equal(t, 0, hub.ClientCount())
		assert.Equal(t, 0, hub.ChannelCount())
		assert.Equal(t, 0, hub.ChannelSubscriberCount("shutdown-channel"))
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
		assert.Equal(t, 1, hub.ClientCount())

		// Fill the client's send buffer
		slowClient.send <- []byte("blocking")

		// Broadcast should trigger the overflow path
		err := hub.Broadcast(Message{Type: TypeEvent, Data: "overflow"})
		require.NoError(t, err)

		// Wait for the unregister goroutine to fire
		time.Sleep(100 * time.Millisecond)

		assert.Equal(t, 0, hub.ClientCount(), "slow client should be unregistered")
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
		assert.Equal(t, 1, hub.ClientCount())

		// Simulate a concurrent close before the hub attempts delivery.
		client.closeSend()

		err := hub.Broadcast(Message{Type: TypeEvent, Data: "closed-channel"})
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, 0, hub.ClientCount(), "client with closed send channel should be unregistered")
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
		assert.NoError(t, err)
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
		assert.NoError(t, err)
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
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to marshal message")
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
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to marshal message")
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
		require.NoError(t, err)
		defer resp.Body.Close()

		// The handler should have returned an error response
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, 0, hub.ClientCount())
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
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ClientCount())

		// Get the client from the hub
		hub.mu.RLock()
		var client *Client
		for c := range hub.clients {
			client = c
			break
		}
		hub.mu.RUnlock()
		require.NotNil(t, client)

		// Close via Client.Close()
		err = client.Close()
		// conn.Close may return an error if already closing, that is acceptable
		_ = err

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 0, hub.ClientCount())

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
		require.NoError(t, err)
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Send malformed JSON — should be ignored without disconnecting
		err = conn.WriteMessage(websocket.TextMessage, []byte("this is not json"))
		require.NoError(t, err)

		// Send a valid subscribe after the bad message — client should still be alive
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))
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
		require.NoError(t, err)
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Send subscribe with numeric data instead of string
		err = conn.WriteJSON(map[string]any{
			"type": "subscribe",
			"data": 12345,
		})
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)

		// No channels should have been created
		assert.Equal(t, 0, hub.ChannelCount())
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
		require.NoError(t, err)
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Subscribe first with valid data
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))

		// Send unsubscribe with non-string data — should be ignored
		err = conn.WriteJSON(map[string]any{
			"type": "unsubscribe",
			"data": []string{"test-channel"},
		})
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)

		// Channel should still have the subscriber
		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))
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
		require.NoError(t, err)
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Send a message with an unknown type
		err = conn.WriteJSON(Message{Type: "unknown_type", Data: "anything"})
		require.NoError(t, err)

		// Client should still be connected
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ClientCount())
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
		require.NoError(t, err)
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
		assert.Error(t, readErr, "reading should fail after close")
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
		require.NoError(t, err)
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
		require.NotNil(t, client)

		// Queue multiple messages rapidly through the hub so writePump can
		// batch them into a single websocket frame when possible.
		require.NoError(t, hub.Broadcast(Message{Type: TypeEvent, Data: "batch-1"}))
		require.NoError(t, hub.Broadcast(Message{Type: TypeEvent, Data: "batch-2"}))
		require.NoError(t, hub.Broadcast(Message{Type: TypeEvent, Data: "batch-3"}))

		// Read frames until we have observed all three payloads or time out.
		deadline := time.Now().Add(time.Second)
		seen := map[string]bool{}
		for len(seen) < 3 {
			conn.SetReadDeadline(deadline)
			_, data, readErr := conn.ReadMessage()
			require.NoError(t, readErr)

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
			require.NoError(t, err)
			defer conn.Close()
			conns[i] = conn
		}

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 3, hub.ClientCount())

		// Subscribe all to "shared"
		for _, conn := range conns {
			err := conn.WriteJSON(Message{Type: TypeSubscribe, Data: "shared"})
			require.NoError(t, err)
		}
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 3, hub.ChannelSubscriberCount("shared"))

		// Send to channel
		err := hub.SendToChannel("shared", Message{Type: TypeEvent, Data: "hello all"})
		require.NoError(t, err)

		// All three clients should receive the message
		for i, conn := range conns {
			conn.SetReadDeadline(time.Now().Add(time.Second))
			var received Message
			err := conn.ReadJSON(&received)
			require.NoError(t, err, "client %d should receive message", i)
			assert.Equal(t, TypeEvent, received.Type)
			assert.Equal(t, "hello all", received.Data)
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

		// Verify: half should remain on race-channel, half should be on another-channel
		assert.Equal(t, numClients/2, hub.ChannelSubscriberCount("race-channel"))
		assert.Equal(t, numClients/2, hub.ChannelSubscriberCount("another-channel"))
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
		require.NoError(t, err)
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Subscribe to process channel
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "process:build-42"})
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond)

		// Send lines one at a time with a small delay to avoid batching
		lines := []string{"Compiling...", "Linking...", "Done."}
		for _, line := range lines {
			err = hub.SendProcessOutput("build-42", line)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond) // Allow writePump to flush each individually
		}

		// Read all three — messages may arrive individually or batched
		// with newline separators. ReadMessage gives raw frames.
		var received []Message
		for len(received) < 3 {
			conn.SetReadDeadline(time.Now().Add(time.Second))
			_, data, readErr := conn.ReadMessage()
			require.NoError(t, readErr)

			// A single frame may contain multiple newline-separated JSON objects
			parts := strings.SplitSeq(core.Trim(string(data)), "\n")
			for part := range parts {
				part = core.Trim(part)
				if part == "" {
					continue
				}
				var msg Message
				require.True(t, core.JSONUnmarshal([]byte(part), &msg).OK)
				received = append(received, msg)
			}
		}

		require.Len(t, received, 3)
		for i, expected := range lines {
			assert.Equal(t, TypeProcessOutput, received[i].Type)
			assert.Equal(t, "build-42", received[i].ProcessID)
			assert.Equal(t, expected, received[i].Data)
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
		require.NoError(t, err)
		defer conn.Close()

		time.Sleep(50 * time.Millisecond)

		// Subscribe
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "process:job-7"})
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond)

		// Send status
		err = hub.SendProcessStatus("job-7", "exited", 1)
		require.NoError(t, err)

		conn.SetReadDeadline(time.Now().Add(time.Second))
		var received Message
		err = conn.ReadJSON(&received)
		require.NoError(t, err)
		assert.Equal(t, TypeProcessStatus, received.Type)
		assert.Equal(t, "job-7", received.ProcessID)

		data, ok := received.Data.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "exited", data["status"])
		assert.Equal(t, float64(1), data["exitCode"])
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

		assert.Equal(t, 5*time.Second, hub.config.HeartbeatInterval)
		assert.Equal(t, 10*time.Second, hub.config.PongTimeout)
		assert.Equal(t, 3*time.Second, hub.config.WriteTimeout)
	})

	t.Run("applies defaults for zero values", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{})

		assert.Equal(t, DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
		assert.Equal(t, DefaultPongTimeout, hub.config.PongTimeout)
		assert.Equal(t, DefaultWriteTimeout, hub.config.WriteTimeout)
	})

	t.Run("applies defaults for negative values", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			HeartbeatInterval: -1,
			PongTimeout:       -1,
			WriteTimeout:      -1,
		})

		assert.Equal(t, DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
		assert.Equal(t, DefaultPongTimeout, hub.config.PongTimeout)
		assert.Equal(t, DefaultWriteTimeout, hub.config.WriteTimeout)
	})

	t.Run("expands pong timeout when it does not exceed heartbeat interval", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			HeartbeatInterval: 20 * time.Second,
			PongTimeout:       10 * time.Second,
		})

		assert.Equal(t, 20*time.Second, hub.config.HeartbeatInterval)
		assert.Equal(t, 40*time.Second, hub.config.PongTimeout)
	})
}

func TestDefaultHubConfig(t *testing.T) {
	t.Run("returns sensible defaults", func(t *testing.T) {
		config := DefaultHubConfig()

		assert.Equal(t, 30*time.Second, config.HeartbeatInterval)
		assert.Equal(t, 60*time.Second, config.PongTimeout)
		assert.Equal(t, 10*time.Second, config.WriteTimeout)
		assert.Nil(t, config.OnConnect)
		assert.Nil(t, config.OnDisconnect)
		assert.Nil(t, config.ChannelAuthoriser)
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
		require.NoError(t, err)
		defer conn.Close()

		select {
		case c := <-connectCalled:
			assert.NotNil(t, c)
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
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)

		// Close the connection to trigger disconnect
		conn.Close()

		select {
		case c := <-disconnectCalled:
			assert.NotNil(t, c)
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
		require.NoError(t, err)

		err = hub.Subscribe(client, "private:ops")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "subscription unauthorised")

		assert.Equal(t, 1, hub.ChannelSubscriberCount("public:news"))
		assert.Equal(t, 0, hub.ChannelSubscriberCount("private:ops"))
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
		require.Error(t, err)
		assert.Contains(t, err.Error(), "subscription unauthorised")
		assert.Empty(t, client.subscriptions)
		assert.Equal(t, 0, hub.ChannelCount())
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
		require.NoError(t, err)
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

		assert.Equal(t, StateConnected, rc.State())

		// Wait for client to register
		time.Sleep(50 * time.Millisecond)

		// Broadcast a message
		err := hub.Broadcast(Message{Type: TypeEvent, Data: "hello"})
		require.NoError(t, err)

		select {
		case msg := <-msgReceived:
			assert.Equal(t, TypeEvent, msg.Type)
			assert.Equal(t, "hello", msg.Data)
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
	require.NoError(t, err)

	select {
	case data := <-rawReceived:
		assert.Contains(t, string(data), "raw-bytes")

		var received Message
		require.True(t, core.JSONUnmarshal(data, &received).OK)
		assert.Equal(t, TypeEvent, received.Type)
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
		require.NoError(t, err)

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
		assert.Equal(t, StateConnected, rc.State())

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
			assert.Greater(t, attempt, 0)
		case <-time.After(3 * time.Second):
			t.Fatal("OnReconnect should have been called")
		}

		assert.Equal(t, StateConnected, rc.State())
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
			require.Error(t, err)
			assert.Contains(t, err.Error(), "max retries (3) exceeded")
		case <-time.After(5 * time.Second):
			t.Fatal("should have stopped after max retries")
		}

		assert.Equal(t, StateDisconnected, rc.State())
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
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 1, hub.ChannelSubscriberCount("test-channel"))

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
			require.NoError(t, err)
		}

		time.Sleep(100 * time.Millisecond)
		assert.GreaterOrEqual(t, hub.ChannelCount(), 1)
	})

	t.Run("returns error when not connected", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})

		err := rc.Send(Message{Type: TypePing})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not connected")
	})

	t.Run("returns error for unmarshalable message", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})
		// Force a conn to be set so we get past the nil check
		// to hit the marshal error first
		err := rc.Send(Message{Type: TypeEvent, Data: make(chan int)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to marshal message")
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
		assert.NoError(t, err)

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
		assert.NoError(t, err)
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

		// attempt 1: 100ms
		assert.Equal(t, 100*time.Millisecond, rc.calculateBackoff(1))
		// attempt 2: 200ms
		assert.Equal(t, 200*time.Millisecond, rc.calculateBackoff(2))
		// attempt 3: 400ms
		assert.Equal(t, 400*time.Millisecond, rc.calculateBackoff(3))
		// attempt 4: 800ms
		assert.Equal(t, 800*time.Millisecond, rc.calculateBackoff(4))
		// attempt 5: capped at 1s
		assert.Equal(t, 1*time.Second, rc.calculateBackoff(5))
		// attempt 10: still capped at 1s
		assert.Equal(t, 1*time.Second, rc.calculateBackoff(10))
	})
}

func TestReconnectingClient_Defaults(t *testing.T) {
	t.Run("applies defaults for zero config values", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})

		assert.Equal(t, 1*time.Second, rc.config.InitialBackoff)
		assert.Equal(t, 30*time.Second, rc.config.MaxBackoff)
		assert.Equal(t, 2.0, rc.config.BackoffMultiplier)
		assert.NotNil(t, rc.config.Dialer)
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
			require.Error(t, err)
			assert.Equal(t, context.Canceled, err)
		case <-time.After(2 * time.Second):
			t.Fatal("Connect should have returned after context cancel")
		}
	})
}

func TestConnectionState(t *testing.T) {
	t.Run("state constants are distinct", func(t *testing.T) {
		assert.NotEqual(t, StateDisconnected, StateConnecting)
		assert.NotEqual(t, StateConnecting, StateConnected)
		assert.NotEqual(t, StateDisconnected, StateConnected)
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

	assert.Equal(t, 1, hub.ClientCount(), "client should be registered via hub loop")
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
	require.NoError(t, err)

	// Hub.Run loop delivers the broadcast to the client's send channel
	select {
	case msg := <-client.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, TypeEvent, received.Type)
		assert.Equal(t, "lifecycle-test", received.Data)
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
	assert.Equal(t, 1, hub.ClientCount())

	// Subscribe so we can verify channel cleanup
	hub.Subscribe(client, "lifecycle-chan")
	assert.Equal(t, 1, hub.ChannelSubscriberCount("lifecycle-chan"))

	hub.unregister <- client
	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, 0, hub.ClientCount())
	assert.Equal(t, 0, hub.ChannelSubscriberCount("lifecycle-chan"))
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

	assert.Equal(t, 3, hub.ChannelCount())
	subs := client.Subscriptions()
	assert.Len(t, subs, 3)
	assert.Contains(t, subs, "alpha")
	assert.Contains(t, subs, "beta")
	assert.Contains(t, subs, "gamma")
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

	// Still only one subscriber entry in the channel map
	assert.Equal(t, 1, hub.ChannelSubscriberCount("dupl"))
}

func TestUnsubscribe_PartialLeave_Good(t *testing.T) {
	hub := NewHub()
	client1 := &Client{hub: hub, send: make(chan []byte, 256), subscriptions: make(map[string]bool)}
	client2 := &Client{hub: hub, send: make(chan []byte, 256), subscriptions: make(map[string]bool)}

	hub.Subscribe(client1, "shared")
	hub.Subscribe(client2, "shared")
	assert.Equal(t, 2, hub.ChannelSubscriberCount("shared"))

	hub.Unsubscribe(client1, "shared")
	assert.Equal(t, 1, hub.ChannelSubscriberCount("shared"))

	// Channel still exists because client2 is subscribed
	hub.mu.RLock()
	_, exists := hub.channels["shared"]
	hub.mu.RUnlock()
	assert.True(t, exists, "channel should persist while subscribers remain")
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
	require.NoError(t, err)

	for i, c := range clients {
		select {
		case msg := <-c.send:
			var received Message
			require.True(t, core.JSONUnmarshal(msg, &received).OK)
			assert.Equal(t, "multi", received.Channel)
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
	assert.NoError(t, err, "sending to a process with no subscribers should not error")
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
	require.NoError(t, err)

	select {
	case msg := <-client.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, TypeProcessStatus, received.Type)
		assert.Equal(t, "fail-1", received.ProcessID)
		data := received.Data.(map[string]any)
		assert.Equal(t, "exited", data["status"])
		assert.Equal(t, float64(137), data["exitCode"])
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
	require.NoError(t, err)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	err = conn.WriteJSON(Message{Type: TypePing})
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var pong Message
	err = conn.ReadJSON(&pong)
	require.NoError(t, err)
	assert.Equal(t, TypePong, pong.Type)
	assert.False(t, pong.Timestamp.IsZero(), "pong should include a timestamp")
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
	require.NoError(t, err)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	// Rapidly send multiple broadcasts so they queue up
	numMessages := 10
	for i := range numMessages {
		err := hub.Broadcast(Message{
			Type: TypeEvent,
			Data: core.Sprintf("batch-%d", i),
		})
		require.NoError(t, err)
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

	assert.Equal(t, numMessages, received, "all batched messages should be received")
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
	require.NoError(t, err)
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	// Subscribe
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "temp:feed"})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Verify we receive messages on the channel
	err = hub.SendToChannel("temp:feed", Message{Type: TypeEvent, Data: "before-unsub"})
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var msg1 Message
	err = conn.ReadJSON(&msg1)
	require.NoError(t, err)
	assert.Equal(t, "before-unsub", msg1.Data)

	// Unsubscribe
	err = conn.WriteJSON(Message{Type: TypeUnsubscribe, Data: "temp:feed"})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Send another message -- client should NOT receive it
	err = hub.SendToChannel("temp:feed", Message{Type: TypeEvent, Data: "after-unsub"})
	require.NoError(t, err)

	// Try to read -- should timeout (no message delivered)
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var msg2 Message
	err = conn.ReadJSON(&msg2)
	assert.Error(t, err, "should not receive messages after unsubscribing")
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
		require.NoError(t, err)
		defer conn.Close()
		conns[i] = conn
	}

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, numClients, hub.ClientCount())

	// Broadcast -- no channel subscription needed
	err := hub.Broadcast(Message{Type: TypeError, Data: "global-alert"})
	require.NoError(t, err)

	for i, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var received Message
		err := conn.ReadJSON(&received)
		require.NoError(t, err, "client %d should receive broadcast", i)
		assert.Equal(t, TypeError, received.Type)
		assert.Equal(t, "global-alert", received.Data)
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
	require.NoError(t, err)

	// Subscribe to multiple channels
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "ch-a"})
	require.NoError(t, err)
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "ch-b"})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, hub.ClientCount())
	assert.Equal(t, 1, hub.ChannelSubscriberCount("ch-a"))
	assert.Equal(t, 1, hub.ChannelSubscriberCount("ch-b"))

	// Disconnect
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, 0, hub.ClientCount())
	assert.Equal(t, 0, hub.ChannelSubscriberCount("ch-a"))
	assert.Equal(t, 0, hub.ChannelSubscriberCount("ch-b"))
	assert.Equal(t, 0, hub.ChannelCount(), "empty channels should be cleaned up")
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
	require.NoError(t, err)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "private:ops"})
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var response Message
	require.NoError(t, conn.ReadJSON(&response))
	assert.Equal(t, TypeError, response.Type)
	assert.Contains(t, response.Data.(string), "subscription unauthorised")
	assert.Equal(t, 0, hub.ChannelSubscriberCount("private:ops"))

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "public:news"})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, hub.ChannelSubscriberCount("public:news"))
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

	assert.Equal(t, 50, hub.ClientCount())
}

func TestHub_Handler_RejectsWhenNotRunning(t *testing.T) {
	// Handler should not block or register clients when the hub loop is not running.
	hub := NewHub()

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		assert.Error(t, err)
		assert.Equal(t, 0, hub.ClientCount())
		return
	}

	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, readErr := conn.ReadMessage()
	require.Error(t, readErr)
	assert.Equal(t, 0, hub.ClientCount())
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
	require.NoError(t, err)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, hub.ClientCount())

	conn.Close()
	time.Sleep(50 * time.Millisecond)

	require.Len(t, ctxErr, 1)
}
