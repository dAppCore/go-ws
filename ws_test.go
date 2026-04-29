// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"crypto/tls"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	// Note: AX-6 — internal concurrency primitive; structural for go-ws hub state (RFC mandates concurrent connection map).
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "dappco.re/go"
	coreerr "dappco.re/go/log"
	"github.com/gorilla/websocket"
)

// wsURL converts an httptest server URL to a WebSocket URL.
func wsURL(server *httptest.Server) string {
	return "ws" + core.TrimPrefix(server.URL, "http")
}

func originRequest(origin string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestNewHub(t *testing.T) {
	t.Run("creates hub with initialised maps", func(t *testing.T) {
		hub := NewHub()
		if testIsNil(hub) {
			t.Fatalf("expected non-nil value")
		}
		if testIsNil(hub.clients) {
			t.Errorf("expected non-nil value")
		}
		if testIsNil(hub.broadcast) {
			t.Errorf("expected non-nil value")
		}
		if testIsNil(hub.register) {
			t.Errorf("expected non-nil value")
		}
		if testIsNil(hub.unregister) {
			t.Errorf("expected non-nil value")
		}
		if testIsNil(hub.channels) {
			t.Errorf("expected non-nil value")
		}

	})
}

func TestWs_AllowedOrigins_Good(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		AllowedOrigins: []string{
			"https://app.example",
			"https://admin.example",
		},
	})
	if testIsNil(hub) {
		t.Fatalf("expected non-nil value")
	}
	if testIsNil(hub.config.CheckOrigin) {
		t.Fatalf("expected non-nil value")
	}
	if !(hub.config.CheckOrigin(originRequest("https://app.example"))) {
		t.Errorf("expected true")
	}
	if !(hub.config.CheckOrigin(originRequest("https://admin.example"))) {
		t.Errorf("expected true")
	}

}

func TestWs_AllowedOrigins_Bad(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		AllowedOrigins: []string{"https://app.example"},
	})
	if testIsNil(hub) {
		t.Fatalf("expected non-nil value")
	}
	if testIsNil(hub.config.CheckOrigin) {
		t.Fatalf("expected non-nil value")
	}
	if hub.config.CheckOrigin(originRequest("https://evil.example")) {
		t.Errorf("expected false")
	}
	if hub.config.CheckOrigin(originRequest("")) {
		t.Errorf("expected false")
	}

}

func TestWs_AllowedOrigins_Ugly(t *testing.T) {
	logs := core.NewBuffer()
	originalLogger := coreerr.Default()
	coreerr.SetDefault(coreerr.New(coreerr.Options{
		Level:  coreerr.LevelWarn,
		Output: logs,
	}))
	t.Cleanup(func() {
		coreerr.SetDefault(originalLogger)
	})

	hub := NewHub()
	if testIsNil(hub) {
		t.Fatalf("expected non-nil value")
	}
	if testIsNil(hub.config.CheckOrigin) {
		t.Fatalf("expected non-nil value")
	}
	if !testIsEmpty(hub.config.AllowedOrigins) {
		t.Errorf("expected empty value, got %v", hub.config.AllowedOrigins)
	}
	if !(hub.config.CheckOrigin(originRequest("https://evil.example"))) {
		t.Errorf("expected true")
	}
	if !testContains(logs.String(), "HubConfig.AllowedOrigins") {
		t.Errorf("expected %v to contain %v", logs.String(), "HubConfig.AllowedOrigins")
	}

}

func TestWs_validIdentifier_Good(t *testing.T) {
	tests := []struct {
		name  string
		value string
		max   int
	}{
		{name: "simple", value: "alpha", max: 10},
		{name: "safe token", value: "A-Z_0-9-.:", max: 20},
		{name: "exact max length", value: testRepeat("a", 8), max: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !(validIdentifier(tt.value, tt.max)) {
				t.Errorf("expected true")
			}

		})
	}
}

func TestWs_validIdentifier_Bad(t *testing.T) {
	tests := []struct {
		name  string
		value string
		max   int
	}{
		{name: "empty", value: "", max: 8},
		{name: "whitespace padded", value: " alpha", max: 8},
		{name: "embedded whitespace", value: "al pha", max: 8},
		{name: "too long", value: testRepeat("a", 9), max: 8},
		{name: "non-ascii", value: "grüße", max: 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if validIdentifier(tt.value, tt.max) {
				t.Errorf("expected false")
			}

		})
	}
}

func TestWs_validIdentifier_Ugly(t *testing.T) {
	if validIdentifier(testRepeat(" ", 4), 8) {
		t.Errorf("expected false")
	}
	if validIdentifier("line\nbreak", 16) {
		t.Errorf("expected false")
	}
	if validIdentifier("\tindent", 16) {
		t.Errorf("expected false")
	}

}

func TestWs_validateChannelTarget(t *testing.T) {
	t.Run("accepts regular channels", func(t *testing.T) {
		if err := testResultError(validateChannelTarget("test", "events:user-1")); err != nil {
			t.Errorf("expected no error, got %v", err)
		}

	})

	t.Run("accepts process channels with bounded IDs", func(t *testing.T) {
		if err := testResultError(validateChannelTarget("test", "process:proc-123")); err != nil {
			t.Errorf("expected no error, got %v", err)
		}

	})

	t.Run("rejects process channels with empty IDs", func(t *testing.T) {
		err := testResultError(validateChannelTarget("test", "process:"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
		}

	})

	t.Run("rejects process channels with oversized IDs", func(t *testing.T) {
		err := testResultError(validateChannelTarget("test", "process:"+testRepeat("a", maxProcessIDLen+1)))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
		}

	})
}

func TestWs_validProcessID_Good(t *testing.T) {
	tests := []string{
		"proc-123",
		"proc_123",
		"proc.123",
		testRepeat("a", maxProcessIDLen),
	}

	for _, processID := range tests {
		t.Run(processID, func(t *testing.T) {
			if !(validProcessID(processID)) {
				t.Errorf("expected true")
			}

		})
	}
}

func TestWs_validProcessID_Bad(t *testing.T) {
	tests := []string{
		"",
		"bad process",
		"proc:123",
		"grüße",
	}

	for _, processID := range tests {
		t.Run(processID, func(t *testing.T) {
			if validProcessID(processID) {
				t.Errorf("expected false")
			}

		})
	}
}

func TestWs_validProcessID_Ugly(t *testing.T) {
	if validProcessID(" proc-123 ") {
		t.Errorf("expected false")
	}
	if validProcessID(testRepeat("a", maxProcessIDLen+1)) {
		t.Errorf("expected false")
	}
	if validProcessID("line\nbreak") {
		t.Errorf("expected false")
	}

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

func TestWs_Run_NilClientEvents_Good(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		hub.Run(ctx)
		close(done)
	}()

	hub.register <- nil
	hub.unregister <- nil

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hub should stop after context cancel")
	}
}

func TestWs_Run_Ugly(t *testing.T) {
	testNotPanics(t, func() {
		var hub *Hub
		hub.Run(context.Background())
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

		err := testResultError(hub.Broadcast(msg))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

	})

	t.Run("returns error when channel full", func(t *testing.T) {
		hub := NewHub()
		// Fill the broadcast channel
		for range 256 {
			hub.broadcast <- []byte("test")
		}

		err := testResultError(hub.Broadcast(Message{Type: TypeEvent}))
		if err := err; err == nil {
			t.Errorf("expected error")
		}
		if !testContains(err.Error(), "broadcast channel full") {
			t.Errorf("expected %v to contain %v", err.Error(), "broadcast channel full")
		}

	})
}

func TestHub_Stats(t *testing.T) {
	t.Run("returns empty stats for new hub", func(t *testing.T) {
		hub := NewHub()

		stats := hub.Stats()
		if !testEqual(0, stats.Clients) {
			t.Errorf("expected %v, got %v", 0, stats.Clients)
		}
		if !testEqual(0, stats.Channels) {
			t.Errorf("expected %v, got %v", 0, stats.Channels)
		}
		if !testEqual(0, stats.Subscribers) {
			t.Errorf("expected %v, got %v",

				// Manually add clients for testing
				0, stats.Subscribers)
		}

	})

	t.Run("tracks client and channel counts", func(t *testing.T) {
		hub := NewHub()

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
		if !testEqual(2, stats.Clients) {
			t.Errorf("expected %v, got %v", 2, stats.Clients)
		}
		if !testEqual(2, stats.Channels) {
			t.Errorf("expected %v, got %v", 2, stats.Channels)
		}
		if !testEqual(3, stats.Subscribers) {
			t.Errorf("expected %v, got %v", 3, stats.Subscribers)
		}

	})
}

func TestHub_ClientCount(t *testing.T) {
	t.Run("returns zero for empty hub", func(t *testing.T) {
		hub := NewHub()
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
		}

	})

	t.Run("counts connected clients", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.clients[&Client{}] = true
		hub.clients[&Client{}] = true
		hub.mu.Unlock()
		if !testEqual(2, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 2, hub.ClientCount())
		}

	})
}

func TestHub_ChannelCount(t *testing.T) {
	t.Run("returns zero for empty hub", func(t *testing.T) {
		hub := NewHub()
		if !testEqual(0, hub.ChannelCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
		}

	})

	t.Run("counts active channels", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.channels["channel1"] = make(map[*Client]bool)
		hub.channels["channel2"] = make(map[*Client]bool)
		hub.mu.Unlock()
		if !testEqual(2, hub.ChannelCount()) {
			t.Errorf("expected %v, got %v", 2, hub.ChannelCount())
		}

	})
}

func TestHub_ChannelSubscriberCount(t *testing.T) {
	t.Run("returns zero for non-existent channel", func(t *testing.T) {
		hub := NewHub()
		if !testEqual(0, hub.ChannelSubscriberCount("non-existent")) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("non-existent"))
		}

	})

	t.Run("counts subscribers in channel", func(t *testing.T) {
		hub := NewHub()

		hub.mu.Lock()
		hub.channels["test-channel"] = make(map[*Client]bool)
		hub.channels["test-channel"][&Client{}] = true
		hub.channels["test-channel"][&Client{}] = true
		hub.mu.Unlock()
		if !testEqual(2, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 2, hub.ChannelSubscriberCount("test-channel"))
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

		err := testResultError(hub.Subscribe(client, "test-channel"))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}
		if !(client.subscriptions["test-channel"]) {
			t.Errorf("expected true")
		}

	})

	t.Run("creates channel if not exists", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		err := testResultError(hub.Subscribe(client, "new-channel"))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		hub.mu.RLock()
		_, exists := hub.channels["new-channel"]
		hub.mu.RUnlock()
		if !(exists) {
			t.Errorf("expected true")
		}

	})

	t.Run("rejects invalid channel names", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		err := testResultError(hub.Subscribe(client, "bad channel"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid channel name") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid channel name")
		}

	})

	t.Run("rejects process channels with oversized IDs", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		err := testResultError(hub.Subscribe(client, "process:"+testRepeat("a", maxProcessIDLen+1)))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
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

		_ = hub.Subscribe(client, "test-channel")
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}

		hub.Unsubscribe(client, "test-channel")
		if !testEqual(0, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("test-channel"))
		}
		if client.subscriptions["test-channel"] {
			t.Errorf("expected false")
		}

	})

	t.Run("cleans up empty channels", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			subscriptions: make(map[string]bool),
		}

		_ = hub.Subscribe(client, "temp-channel")
		hub.Unsubscribe(client, "temp-channel")

		hub.mu.RLock()
		_, exists := hub.channels["temp-channel"]
		hub.mu.RUnlock()
		if exists {
			t.Errorf("expected false")
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
		_ = hub.Subscribe(client, "test-channel")

		err := testResultError(hub.SendToChannel("test-channel", Message{Type: TypeEvent,
			Data: "test",
		}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if !testEqual(TypeEvent, received.Type) {
				t.Errorf("expected %v, got %v", TypeEvent, received.Type)
			}
			if !testEqual("test-channel", received.Channel) {
				t.Errorf("expected %v, got %v", "test-channel", received.Channel)
			}

		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("returns nil for non-existent channel", func(t *testing.T) {
		hub := NewHub()

		err := testResultError(hub.SendToChannel("non-existent", Message{Type: TypeEvent}))
		if err := err; err != nil {
			t.Errorf("expected no error, got %v", err)
		}

	})

	t.Run("rejects invalid channel names", func(t *testing.T) {
		hub := NewHub()

		err := testResultError(hub.SendToChannel("bad channel", Message{Type: TypeEvent}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid channel name") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid channel name")
		}

	})

	t.Run("rejects process channels with empty IDs", func(t *testing.T) {
		hub := NewHub()

		err := testResultError(hub.SendToChannel("process:", Message{Type: TypeEvent}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
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
		_ = hub.Subscribe(client, "process:proc-1")

		err := testResultError(hub.SendProcessOutput("proc-1", "hello world"))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if !testEqual(TypeProcessOutput, received.Type) {
				t.Errorf("expected %v, got %v", TypeProcessOutput, received.Type)
			}
			if !testEqual("proc-1", received.ProcessID) {
				t.Errorf("expected %v, got %v", "proc-1", received.ProcessID)
			}
			if !testEqual("hello world", received.Data) {
				t.Errorf("expected %v, got %v", "hello world", received.Data)
			}

		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()

		err := testResultError(hub.SendProcessOutput("bad process", "hello world"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
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
		_ = hub.Subscribe(client, "process:proc-1")

		err := testResultError(hub.SendProcessStatus("proc-1", "exited", 0))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if !testEqual(TypeProcessStatus, received.Type) {
				t.Errorf("expected %v, got %v", TypeProcessStatus, received.Type)
			}
			if !testEqual("proc-1", received.ProcessID) {
				t.Errorf("expected %v, got %v", "proc-1", received.ProcessID)
			}

			data, ok := received.Data.(map[string]any)
			if !(ok) {
				t.Fatalf("expected true")
			}
			if !testEqual("exited", data["status"]) {
				t.Errorf("expected %v, got %v", "exited", data["status"])
			}
			if !testEqual(float64(0), data["exitCode"]) {
				t.Errorf("expected %v, got %v", float64(0), data["exitCode"])
			}

		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()

		err := testResultError(hub.SendProcessStatus("bad process", "exited", 1))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
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

		err := testResultError(hub.SendError("something went wrong"))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if !testEqual(TypeError, received.Type) {
				t.Errorf("expected %v, got %v", TypeError, received.Type)
			}
			if !testEqual("something went wrong", received.Data) {
				t.Errorf("expected %v, got %v", "something went wrong", received.Data)
			}

		case <-time.After(time.Second):
			t.Fatal("expected error message on client send channel")
		}
	})
}

func TestHub_Broadcast_AssignsTimestampAndValidatesProcessID(t *testing.T) {
	t.Run("assigns a fresh timestamp", func(t *testing.T) {
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

		before := time.Now()
		err := testResultError(hub.Broadcast(Message{Type: TypeEvent,
			ProcessID: "proc-1",
			Data:      "hello",
			Timestamp: time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC),
		}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if received.Timestamp.Before(before) {
				t.Errorf("expected false")
			}

		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()

		err := testResultError(hub.Broadcast(Message{Type: TypeEvent,
			ProcessID: "bad process",
		}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
		}

	})
}

func TestHub_SendToChannel_AssignsTimestampAndValidatesProcessID(t *testing.T) {
	t.Run("assigns a fresh timestamp", func(t *testing.T) {
		hub := NewHub()
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}

		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		if err := testResultError(hub.Subscribe(client, "events")); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		before := time.Now()
		err := testResultError(hub.SendToChannel("events", Message{Type: TypeEvent,
			ProcessID: "proc-1",
			Data:      "hello",
			Timestamp: time.Date(2024, time.February, 3, 4, 5, 6, 0, time.UTC),
		}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if received.Timestamp.Before(before) {
				t.Errorf("expected false")
			}
			if !testEqual("events", received.Channel) {
				t.Errorf("expected %v, got %v", "events", received.Channel)
			}

		case <-time.After(time.Second):
			t.Fatal("expected message on client send channel")
		}
	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()

		err := testResultError(hub.SendToChannel("events", Message{Type: TypeEvent,
			ProcessID: "bad process",
		}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
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

		err := testResultError(hub.SendEvent("user_joined", map[string]string{"user": "alice"}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if !testEqual(TypeEvent, received.Type) {
				t.Errorf("expected %v, got %v", TypeEvent, received.Type)
			}

			data, ok := received.Data.(map[string]any)
			if !(ok) {
				t.Fatalf("expected true")
			}
			if !testEqual("user_joined", data["event"]) {
				t.Errorf("expected %v, got %v", "user_joined", data["event"])
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

		_ = hub.Subscribe(client, "channel1")
		_ = hub.Subscribe(client, "channel2")

		subs := client.Subscriptions()
		if gotLen := len(subs); gotLen != 2 {
			t.Errorf("expected length %v, got %v", 2, gotLen)
		}
		if !testContains(subs, "channel1") {
			t.Errorf("expected %v to contain %v", subs, "channel1")
		}
		if !testContains(subs, "channel2") {
			t.Errorf("expected %v to contain %v", subs, "channel2")
		}

	})
}

func TestClient_Subscriptions_Ugly(t *testing.T) {
	var client *Client
	if !testIsNil(client.Subscriptions()) {
		t.Errorf("expected nil, got %T", client.Subscriptions())
	}

}

func TestClient_AllSubscriptions(t *testing.T) {
	t.Run("returns iterator over subscriptions", func(t *testing.T) {
		client := &Client{subscriptions: make(map[string]bool)}
		client.subscriptions["sub1"] = true
		client.subscriptions["sub2"] = true

		subs := slices.Collect(client.AllSubscriptions())
		if gotLen := len(subs); gotLen != 2 {
			t.Errorf("expected length %v, got %v", 2, gotLen)
		}
		if !testContains(subs, "sub1") {
			t.Errorf("expected %v to contain %v", subs, "sub1")
		}
		if !testContains(subs, "sub2") {
			t.Errorf("expected %v to contain %v", subs, "sub2")
		}

	})
}

func TestClient_AllSubscriptions_Ugly(t *testing.T) {
	var client *Client
	testNotPanics(t, func() {
		if !testIsEmpty(slices.Collect(client.AllSubscriptions())) {
			t.Errorf("expected empty value, got %v", slices.Collect(client.AllSubscriptions()))
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
		if gotLen := len(clients); gotLen != 2 {
			t.Errorf("expected length %v, got %v", 2, gotLen)
		}
		if !testContains(clients, client1) {
			t.Errorf("expected %v to contain %v", clients, client1)
		}
		if !testContains(clients, client2) {
			t.Errorf("expected %v to contain %v", clients, client2)
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
		if gotLen := len(channels); gotLen != 2 {
			t.Errorf("expected length %v, got %v", 2, gotLen)
		}
		if !testContains(channels, "ch1") {
			t.Errorf("expected %v to contain %v", channels, "ch1")
		}
		if !testContains(channels, "ch2") {
			t.Errorf("expected %v to contain %v", channels, "ch2")
		}

	})
}

func TestWssortedHubClientsCovers(t *testing.T) {
	hub := NewHub()
	clients := []*Client{
		{UserID: "bravo"},
		nil,
		{UserID: "alpha"},
	}

	hub.mu.Lock()
	for _, client := range clients {
		hub.clients[client] = true
	}
	hub.mu.Unlock()

	ordered := slices.Collect(hub.AllClients())
	if gotLen := len(ordered); gotLen != 3 {
		t.Fatalf("expected length %v, got %v", 3, gotLen)
	}
	if !testIsNil(ordered[0]) {
		t.Errorf("expected nil, got %T", ordered[0])
	}
	if !testEqual("alpha", ordered[1].UserID) {
		t.Errorf("expected %v, got %v", "alpha", ordered[1].UserID)
	}
	if !testEqual("bravo", ordered[2].UserID) {
		t.Errorf("expected %v, got %v", "bravo", ordered[2].UserID)
	}
	if !testEqual("", clientSortKey(&Client{})) {
		t.Errorf("expected %v, got %v", "", clientSortKey(&Client{}))
	}

}

func TestWs_sortedHubClients_Bad(t *testing.T) {
	hub := NewHub()
	if !testIsEmpty(sortedHubClients(hub)) {
		t.Errorf("expected empty value, got %v", sortedHubClients(hub))
	}

}

func TestWs_sortedHubClients_Ugly(t *testing.T) {
	if !testIsNil(sortedHubClients(nil)) {
		t.Errorf("expected nil, got %T", sortedHubClients(nil))
	}

}

func TestWs_sortedHubClients_Good_SameUserID(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		time.Sleep(50 * time.Millisecond)
	}))
	defer serverA.Close()

	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		time.Sleep(50 * time.Millisecond)
	}))
	defer serverB.Close()

	left, _, err := websocket.DefaultDialer.Dial(wsURL(serverA), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, left.Close)
	right, _, err := websocket.DefaultDialer.Dial(wsURL(serverB), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, right.Close)

	hub := NewHub()
	leftClient := &Client{UserID: "shared", conn: left}
	rightClient := &Client{UserID: "shared", conn: right}

	hub.mu.Lock()
	hub.clients[leftClient] = true
	hub.clients[rightClient] = true
	hub.mu.Unlock()

	ordered := sortedHubClients(hub)
	if gotLen := len(ordered); gotLen != 2 {
		t.Fatalf("expected length %v, got %v", 2, gotLen)
	}
	if !testEqual("shared", ordered[0].UserID) {
		t.Errorf("expected %v, got %v", "shared", ordered[0].UserID)
	}
	if !testEqual("shared", ordered[1].UserID) {
		t.Errorf("expected %v, got %v", "shared", ordered[1].UserID)
	}
	if testEqual(clientSortKey(ordered[0]), clientSortKey(ordered[1])) {
		t.Errorf("expected values to differ: %v", clientSortKey(ordered[1]))
	}

}

func TestWs_sortedClientSubscriptions_Good(t *testing.T) {
	client := &Client{
		subscriptions: map[string]bool{
			"zeta":  true,
			"alpha": true,
			"mu":    true,
		},
	}
	if !testEqual([]string{"alpha", "mu", "zeta"}, sortedClientSubscriptions(client)) {
		t.Errorf("expected %v, got %v", []string{"alpha", "mu", "zeta"}, sortedClientSubscriptions(client))
	}

}

func TestWs_sortedClientSubscriptions_Bad(t *testing.T) {
	client := &Client{subscriptions: map[string]bool{}}
	if !testIsEmpty(sortedClientSubscriptions(client)) {
		t.Errorf("expected empty value, got %v", sortedClientSubscriptions(client))
	}

}

func TestWs_sortedClientSubscriptions_Ugly(t *testing.T) {
	if !testIsNil(sortedClientSubscriptions(nil)) {
		t.Errorf("expected nil, got %T", sortedClientSubscriptions(nil))
	}

}

func TestWs_sortedHubChannels_Good(t *testing.T) {
	hub := NewHub()
	hub.channels["zeta"] = map[*Client]bool{}
	hub.channels["alpha"] = map[*Client]bool{}
	hub.channels["mu"] = map[*Client]bool{}
	if !testEqual([]string{"alpha", "mu", "zeta"}, sortedHubChannels(hub)) {
		t.Errorf("expected %v, got %v", []string{"alpha", "mu", "zeta"}, sortedHubChannels(hub))
	}

}

func TestWs_sortedHubChannels_Bad(t *testing.T) {
	hub := NewHub()
	if !testIsEmpty(sortedHubChannels(hub)) {
		t.Errorf("expected empty value, got %v", sortedHubChannels(hub))
	}

}

func TestWs_sortedHubChannels_Ugly(t *testing.T) {
	if !testIsNil(sortedHubChannels(nil)) {
		t.Errorf("expected nil, got %T", sortedHubChannels(nil))
	}

}

func TestWs_clientSortKey_Good(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	client := &Client{conn: conn}
	if testIsEmpty(clientSortKey(client)) {
		t.Errorf("expected non-empty value")
	}

}

func TestWs_clientSortKey_Bad(t *testing.T) {
	if !testEqual("", clientSortKey(nil)) {
		t.Errorf("expected %v, got %v", "", clientSortKey(nil))
	}

}

func TestWs_clientSortKey_Ugly(t *testing.T) {
	if !testEqual("", clientSortKey(&Client{})) {
		t.Errorf("expected %v, got %v", "", clientSortKey(&Client{}))
	}

}

func TestWs_subscribeLocked_Good(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{MaxSubscriptionsPerClient: 1})
	client := &Client{}
	if err := testResultError(hub.subscribeLocked(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !(client.subscriptions["alpha"]) {
		t.Errorf("expected true")
	}
	if !testEqual(1, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("alpha"))
	}

}

func TestWs_subscribeLocked_Bad(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{MaxSubscriptionsPerClient: 1})
	client := &Client{subscriptions: map[string]bool{"alpha": true}}
	if err := testResultError(hub.subscribeLocked(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual(1, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("alpha"))
	}

}

func TestWs_subscribeLocked_Ugly(t *testing.T) {
	hub := NewHub()
	if err := testResultError(hub.subscribeLocked(nil, "alpha")); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

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
		if !(r.OK) {
			t.Fatalf("expected true")
		}

		data := r.Value.([]byte)
		if !testContains(string(data), `"type":"process_output"`) {
			t.Errorf("expected %v to contain %v", string(data), `"type":"process_output"`)
		}
		if !testContains(string(data), `"channel":"process:1"`) {
			t.Errorf("expected %v to contain %v", string(data), `"channel":"process:1"`)
		}
		if !testContains(string(data), `"processId":"1"`) {
			t.Errorf("expected %v to contain %v", string(data), `"processId":"1"`)
		}
		if !testContains(string(data), `"data":"output line"`) {
			t.Errorf("expected %v to contain %v", string(data), `"data":"output line"`)
		}

	})

	t.Run("unmarshals correctly", func(t *testing.T) {
		jsonStr := `{"type":"subscribe","data":"channel:test"}`

		var msg Message
		if !(core.JSONUnmarshal([]byte(jsonStr), &msg).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeSubscribe, msg.Type) {
			t.Errorf("expected %v, got %v", TypeSubscribe, msg.Type)
		}
		if !testEqual("channel:test", msg.Data) {
			t.Errorf("expected %v, got %v", "channel:test", msg.Data)
		}

	})
}

func TestHub_WebSocketHandler(t *testing.T) {
	t.Run("upgrades connection and registers client", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Give time for registration
				err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 1, hub.ClientCount())
		}

	})

	t.Run("drops registration when the hub is shutting down", func(t *testing.T) {
		hub := NewHub()
		hub.running = true
		close(hub.done)

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if conn != nil {
			defer testClose(t, conn.Close)
		}
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(20 * time.Millisecond)
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
		}

	})

	t.Run("allows cross-origin requests with NewHub dev default", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		header := http.Header{}
		header.Set("Origin", "https://evil.example")

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		if testIsNil(resp) {
			t.Fatalf("expected non-nil value")
		}
		if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
			t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
		}

	})

	t.Run("allows same-host cross-scheme requests with NewHub dev default", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		header := http.Header{}
		header.Set("Origin", "https://"+core.TrimPrefix(server.URL, "http://"))

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		if testIsNil(resp) {
			t.Fatalf("expected non-nil value")
		}
		if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
			t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
		}

	})

	t.Run("allows custom origin policy", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		})
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		header := http.Header{}
		header.Set("Origin", "https://evil.example")

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		if testIsNil(resp) {
			t.Fatalf("expected non-nil value")
		}
		if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
			t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
		}

	})

	t.Run("rejects origin before authenticating", func(t *testing.T) {
		var authCalled atomic.Bool

		hub := NewHubWithConfig(HubConfig{
			Authenticator: AuthenticatorFunc(func(r *http.Request) AuthResult {
				authCalled.Store(true)
				return AuthResult{Valid: true, UserID: "user-1"}
			}),
			CheckOrigin: func(r *http.Request) bool {
				return false
			},
		})
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		header := http.Header{}
		header.Set("Origin", "https://evil.example")

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if conn != nil {
			_ = conn.Close()
		}
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if testIsNil(resp) {
			t.Fatalf("expected non-nil value")
		}
		if !testEqual(http.StatusForbidden, resp.StatusCode) {
			t.Errorf("expected %v, got %v", http.StatusForbidden, resp.StatusCode)
		}
		if authCalled.Load() {
			t.Errorf("expected false")
		}
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
		}

	})

	t.Run("treats panicking origin checks as forbidden", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			CheckOrigin: func(r *http.Request) bool {
				panic("boom")
			},
		})
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		header := http.Header{}
		header.Set("Origin", "https://evil.example")

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if conn != nil {
			_ = conn.Close()
		}
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if testIsNil(resp) {
			t.Fatalf("expected non-nil value")
		}
		if !testEqual(http.StatusForbidden, resp.StatusCode) {
			t.Errorf("expected %v, got %v", http.StatusForbidden, resp.StatusCode)
		}
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
		}

	})

	t.Run("handles subscribe message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Send subscribe message
				err)
		}

		defer testClose(t, conn.Close)

		subscribeMsg := Message{
			Type: TypeSubscribe,
			Data: "test-channel",
		}
		err = conn.WriteJSON(subscribeMsg)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Give time for subscription
				err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
		}

	})

	t.Run("rejects invalid subscribe channel names", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "bad channel"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		var response Message
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !testEqual(TypeError, response.Type) {
			t.Errorf("expected %v, got %v", TypeError, response.Type)
		}
		if !testContains(response.Data, "invalid channel name") {
			t.Errorf("expected %v to contain %v", response.Data, "invalid channel name")
		}

	})

	t.Run("handles unsubscribe message", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Subscribe first
				err)
		}

		defer testClose(t, conn.Close)

		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v",

				// Unsubscribe
				1, hub.ChannelSubscriberCount("test-channel"))
		}

		err = conn.WriteJSON(Message{Type: TypeUnsubscribe, Data: "test-channel"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(0, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("test-channel"))
		}

	})

	t.Run("responds to ping with pong", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Give time for registration
				err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Send ping
		err = conn.WriteJSON(Message{Type: TypePing})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Read pong response
				err)
		}

		var response Message
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !testEqual(TypePong, response.Type) {
			t.Errorf("expected %v, got %v", TypePong, response.Type)
		}

	})

	t.Run("broadcasts messages to clients", func(t *testing.T) {
		hub := NewHub()
		ctx := t.Context()
		go hub.Run(ctx)
		if !testEventually(func() bool {
			return hub.isRunning()
		}, time.Second, 10*time.Millisecond) {
			t.Fatalf("condition was not met before timeout")
		}

		server := httptest.NewServer(hub.Handler())
		defer server.Close()

		wsURL := "ws" + core.TrimPrefix(server.URL, "http")

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Give time for registration
				err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Broadcast a message
		err = testResultError(hub.Broadcast(Message{Type: TypeEvent,
			Data: "broadcast test",
		}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Read the broadcast
				err)
		}

		var response Message
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		err = conn.ReadJSON(&response)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !testEqual(TypeEvent, response.Type) {
			t.Errorf("expected %v, got %v", TypeEvent, response.Type)
		}
		if !testEqual("broadcast test", response.Data) {
			t.Errorf("expected %v, got %v", "broadcast test", response.Data)
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Wait for registration
				err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ClientCount()) {
			t.Errorf("expected %v, got %v",

				// Close connection
				1, hub.ClientCount(

				// Wait for unregistration
				))
		}

		_ = conn.Close()

		time.Sleep(50 * time.Millisecond)
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Subscribe to channel
				err)
		}

		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v",

				// Close connection
				1, hub.ChannelSubscriberCount("test-channel"))
		}

		_ = conn.Close()
		time.Sleep(50 * time.Millisecond)
		if !testEqual(

			// Channel should be cleaned up
			0, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("test-channel"))
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

				_ = hub.Subscribe(client, "shared-channel")
				_ = hub.Subscribe(client, "shared-channel") // Double subscribe should be safe
			}(i)
		}

		wg.Wait()
		if !testEqual(numClients, hub.ChannelSubscriberCount("shared-channel")) {
			t.Errorf("expected %v, got %v", numClients, hub.ChannelSubscriberCount("shared-channel"))
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
		// All or most broadcasts should be received.
		if received < numBroadcasts-10 {
			t.Errorf("expected %v to be greater than or equal to %v", received, numBroadcasts-10)
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 1, hub.ClientCount())
		}

	})
}

func TestMustMarshal(t *testing.T) {
	t.Run("marshals valid data", func(t *testing.T) {
		data := mustMarshal(Message{Type: TypePong})
		if !testContains(string(data), "pong") {
			t.Errorf("expected %v to contain %v", string(data), "pong")
		}

	})

	t.Run("handles unmarshalable data without panic", func(t *testing.T) {
		// Create a channel which cannot be marshaled
		// This should not panic, even if it returns nil
		ch := make(chan int)
		testNotPanics(t, func() {
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
		if !testEqual(2, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 2, hub.ClientCount())
		}

		_ = hub.Subscribe(client1, "shutdown-channel")
		if !testEqual(1, hub.ChannelCount()) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelCount())
		}
		if !testEqual(1, hub.ChannelSubscriberCount(

			// Cancel context to trigger shutdown
			"shutdown-channel")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount(

				// Send channels should be closed
				"shutdown-channel"))
		}

		cancel()
		time.Sleep(50 * time.Millisecond)

		_, ok1 := <-client1.send
		if ok1 {
			t.Errorf("expected false")
		}

		_, ok2 := <-client2.send
		if ok2 {
			t.Errorf("expected false")
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
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
		}
		if !testEqual(0, hub.ChannelCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
		}
		if !testEqual(0, hub.ChannelSubscriberCount("shutdown-channel")) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("shutdown-channel"))
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
		if !testEqual(1, hub.ClientCount()) {
			t.Errorf("expected %v, got %v",

				// Fill the client's send buffer
				1, hub.ClientCount())
		}

		slowClient.send <- []byte("blocking")

		// Broadcast should trigger the overflow path
		err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "overflow"}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Wait for the unregister goroutine to fire
				err)
		}

		time.Sleep(100 * time.Millisecond)
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
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
		if !testEqual(1, hub.ClientCount()) {
			t.Errorf("expected %v, got %v",

				// Simulate a concurrent close before the hub attempts delivery.
				1, hub.ClientCount())
		}

		client.closeSend()

		err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "closed-channel"}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(100 * time.Millisecond)
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
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
		_ = hub.Subscribe(client, "test-channel")

		// Fill the client buffer
		client.send <- []byte("blocking")

		// SendToChannel should not block; it skips the full client
		err := testResultError(hub.SendToChannel("test-channel", Message{Type: TypeEvent, Data: "overflow"}))
		if err := err; err != nil {
			t.Errorf("expected no error, got %v", err)
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
		_ = hub.Subscribe(client, "test-channel")

		client.closeSend()

		err := testResultError(hub.SendToChannel("test-channel", Message{Type: TypeEvent, Data: "closed-channel"}))
		if err := err; err != nil {
			t.Errorf("expected no error, got %v", err)
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

		err := testResultError(hub.Broadcast(msg))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "failed to marshal message") {
			t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal message")
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

		err := testResultError(hub.SendToChannel("any-channel", msg))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "failed to marshal message") {
			t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal message")
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// The handler should have returned an error response
				err)
		}

		defer testClose(t, resp.Body.Close)
		if !testEqual(http.StatusBadRequest, resp.StatusCode) {
			t.Errorf("expected %v, got %v", http.StatusBadRequest, resp.StatusCode)
		}
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
		}

	})
}

func TestWs_Handler_Bad(t *testing.T) {
	var hub *Hub

	handler := hub.Handler()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)

	handler(recorder, req)
	if !testEqual(http.StatusServiceUnavailable, recorder.Code) {
		t.Errorf("expected %v, got %v", http.StatusServiceUnavailable, recorder.Code)
	}
	if !testContains(recorder.Body.String(), "Hub is not configured") {
		t.Errorf("expected %v to contain %v", recorder.Body.String(), "Hub is not configured")
	}

}

func TestHub_Handler_AuthSnapshotAndUserID_Good(t *testing.T) {
	claims := map[string]any{
		"role": "admin",
	}
	authCalled := make(chan struct{}, 1)

	hub := NewHubWithConfig(HubConfig{
		Authenticator: AuthenticatorFunc(func(r *http.Request) AuthResult {
			select {
			case authCalled <- struct{}{}:
			default:
			}
			return AuthResult{
				Valid:  true,
				UserID: "  user-123  ",
				Claims: claims,
			}
		}),
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if testIsNil(resp) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	defer testClose(t, conn.Close)

	select {
	case <-authCalled:
	case <-time.After(time.Second):
		t.Fatal("authenticator should have been called")
	}

	claims["role"] = "user"
	if !testEventually(func() bool {
		return hub.ClientCount() == 1
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

	hub.mu.RLock()
	var client *Client
	for c := range hub.clients {
		client = c
		break
	}
	hub.mu.RUnlock()
	if testIsNil(client) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual("user-123", client.UserID) {
		t.Errorf("expected %v, got %v", "user-123", client.UserID)
	}
	if testIsNil(client.Claims) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual("admin", client.Claims["role"]) {
		t.Errorf("expected %v, got %v", "admin", client.Claims["role"])
	}

}

func TestHub_Handler_RejectsEmptyUserID_Bad(t *testing.T) {
	authFailure := make(chan AuthResult, 1)

	hub := NewHubWithConfig(HubConfig{
		Authenticator: AuthenticatorFunc(func(r *http.Request) AuthResult {
			return AuthResult{
				Valid:  true,
				UserID: " ",
				Claims: map[string]any{"role": "admin"},
			}
		}),
		OnAuthFailure: func(r *http.Request, result AuthResult) {
			select {
			case authFailure <- result:
			default:
			}
		},
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if conn != nil {
		_ = conn.Close()
	}
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if testIsNil(resp) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}

	select {
	case result := <-authFailure:
		if result.Valid {
			t.Errorf("expected false")
		}
		if result.Authenticated {
			t.Errorf("expected false")
		}
		if !(core.Is(result.Error, ErrMissingUserID)) {
			t.Errorf("expected true")
		}

	case <-time.After(time.Second):
		t.Fatal("expected OnAuthFailure callback to run")
	}
}

func TestHub_Handler_AuthenticatorPanic_Ugly(t *testing.T) {
	authFailure := make(chan AuthResult, 1)

	hub := NewHubWithConfig(HubConfig{
		Authenticator: AuthenticatorFunc(func(r *http.Request) AuthResult {
			panic("boom")
		}),
		OnAuthFailure: func(r *http.Request, result AuthResult) {
			select {
			case authFailure <- result:
			default:
			}
		},
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if conn != nil {
		_ = conn.Close()
	}
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if testIsNil(resp) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}

	select {
	case result := <-authFailure:
		if result.Valid {
			t.Errorf("expected false")
		}
		if result.Authenticated {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "authenticator panicked") {
			t.Errorf("expected %v to contain %v", result.Error.Error(), "authenticator panicked")
		}

	case <-time.After(time.Second):
		t.Fatal("expected OnAuthFailure callback to run")
	}
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ClientCount()) {
			t.Errorf("expected %v, got %v",

				// Get the client from the hub
				1, hub.ClientCount())
		}

		hub.mu.RLock()
		var client *Client
		for c := range hub.clients {
			client = c
			break
		}
		hub.mu.RUnlock()
		if testIsNil(client) {
			t.Fatalf("expected non-nil value")

			// Close via Client.Close()
		}

		err = testResultError(client.Close()) // conn.Close may return an error if already closing, that is acceptable
		_ = err

		time.Sleep(50 * time.Millisecond)
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v",

				// Connection should be closed — writing should fail
				0, hub.ClientCount())
		}

		_ = conn.Close() // ensure clean up
	})
}

func TestClient_Close_NilAndDetached_Ugly(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		var client *Client
		if err := testResultError(client.Close()); err != nil {
			t.Errorf("expected no error, got %v", err)
		}

	})

	t.Run("detached client with nil conn", func(t *testing.T) {
		client := &Client{}
		if err := testResultError(client.Close()); err != nil {
			t.Errorf("expected no error, got %v", err)
		}

	})

	t.Run("hub with nil conn", func(t *testing.T) {
		hub := NewHub()
		client := &Client{hub: hub}
		if err := testResultError(client.Close()); err != nil {
			t.Errorf("expected no error, got %v", err)
		}

	})
}

func TestClient_closeSend_Nil_Ugly(t *testing.T) {
	var client *Client
	testNotPanics(t, func() {
		client.closeSend()
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Send malformed JSON — should be ignored without disconnecting
		err = conn.WriteMessage(websocket.TextMessage, []byte("this is not json"))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Send a valid subscribe after the bad message — client should still be alive
				err)
		}

		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Send subscribe with numeric data instead of string
		err = conn.WriteJSON(map[string]any{
			"type": "subscribe",
			"data": 12345,
		})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(

			// No channels should have been created
			0, hub.ChannelCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
		}

	})
}

func TestClient_readPump_Ugly(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var client *Client
		testNotPanics(t, func() {
			client.readPump()
		})

	})

	t.Run("missing hub", func(t *testing.T) {
		client := &Client{}
		testNotPanics(t, func() {
			client.readPump()
		})

	})
}

func TestClient_writePump_Ugly(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var client *Client
		testNotPanics(t, func() {
			client.writePump()
		})

	})

	t.Run("missing connection", func(t *testing.T) {
		client := &Client{
			hub: &Hub{},
		}
		testNotPanics(t, func() {
			client.writePump()
		})

	})
}

func TestReadPumpSubscribeWithChannelFieldCovers(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	wsURL := "ws" + core.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	time.Sleep(50 * time.Millisecond)

	err = conn.WriteJSON(Message{
		Type:    TypeSubscribe,
		Channel: "field-channel",
	})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ChannelSubscriberCount("field-channel")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("field-channel"))
	}

}

func TestWs_messageTargetChannel_Good(t *testing.T) {
	t.Run("prefers the channel field", func(t *testing.T) {
		if !testEqual("field-channel", messageTargetChannel(Message{Channel: "field-channel", Data: "data-channel"})) {
			t.Errorf("expected %v, got %v", "field-channel", messageTargetChannel(Message{Channel: "field-channel", Data: "data-channel"}))
		}

	})

	t.Run("falls back to string data", func(t *testing.T) {
		if !testEqual("data-channel", messageTargetChannel(Message{Data: "data-channel"})) {
			t.Errorf("expected %v, got %v", "data-channel", messageTargetChannel(Message{Data: "data-channel"}))
		}

	})
}

func TestWs_messageTargetChannel_Bad(t *testing.T) {
	if !testIsEmpty(messageTargetChannel(Message{Data: []string{"data-channel"}})) {
		t.Errorf("expected empty value, got %v", messageTargetChannel(Message{Data: []string{"data-channel"}}))
	}

}

func TestWs_messageTargetChannel_Ugly(t *testing.T) {
	if !testIsEmpty(messageTargetChannel(Message{})) {
		t.Errorf("expected empty value, got %v", messageTargetChannel(Message{}))
	}

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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Subscribe first with valid data
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "test-channel"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v",

				// Send unsubscribe with non-string data — should be ignored
				1, hub.ChannelSubscriberCount("test-channel"))
		}

		err = conn.WriteJSON(map[string]any{
			"type": "unsubscribe",
			"data": []string{"test-channel"},
		})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(

			// Channel should still have the subscriber
			1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Send a message with an unknown type
		err = conn.WriteJSON(Message{Type: "unknown_type", Data: "anything"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// Client should still be connected
				err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 1, hub.ClientCount())
		}

	})
}

func TestReadPumpReadLimitEdges(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	wsURL := "ws" + core.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)
	if !testEventually(func() bool {
		return hub.ClientCount() == 1
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

	largePayload := testRepeat("A", defaultMaxMessageBytes+1)
	err = conn.WriteMessage(websocket.TextMessage, []byte(largePayload))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEventually(func() bool {
		return hub.ClientCount() == 0
	}, 2*time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

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
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, _, readErr := conn.ReadMessage()
		if err := readErr; err == nil {
			t.Errorf("expected error")
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Get the client
		hub.mu.RLock()
		var client *Client
		for c := range hub.clients {
			client = c
			break
		}
		hub.mu.RUnlock()
		if testIsNil(client) {
			t.Fatalf("expected non-nil value")

			// Queue multiple messages rapidly through the hub so writePump can
			// batch them into a single websocket frame when possible.
		}
		if err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "batch-1"})); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "batch-2"})); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "batch-3"})); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// Read frames until we have observed all three payloads or time out.
		deadline := time.Now().Add(time.Second)
		seen := map[string]bool{}
		for len(seen) < 3 {
			_ = conn.SetReadDeadline(deadline)
			_, data, readErr := conn.ReadMessage()
			if err := readErr; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			content := string(data)
			for _, token := range []string{"batch-1", "batch-2", "batch-3"} {
				if core.Contains(content, token) {
					seen[token] = true
				}
			}
		}
	})
}

func TestWritePumpHeartbeatCovers(t *testing.T) {
	pingSeen := make(chan struct{}, 1)
	serverErr := make(chan error, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			serverErr <- err
			return
		}

		defer testClose(t, conn.Close)

		conn.SetPingHandler(func(string) error {
			select {
			case pingSeen <- struct{}{}:
			default:
			}
			return nil
		})

		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		<-readDone
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	hub := NewHubWithConfig(HubConfig{
		HeartbeatInterval: 10 * time.Millisecond,
		WriteTimeout:      time.Second,
	})
	client := &Client{
		hub:           hub,
		conn:          conn,
		send:          make(chan []byte, 1),
		subscriptions: make(map[string]bool),
	}

	done := make(chan struct{})
	go func() {
		client.writePump()
		close(done)
	}()

	select {
	case <-pingSeen:
	case err := <-serverErr:
		t.Fatalf("expected no server error, got %v", err)
	case <-time.After(time.Second):
		t.Fatal("expected heartbeat ping")
	}

	close(client.send)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writePump should exit after the send channel is closed")
	}
}

func TestWs_readPump_PongTimeout_Good(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		HeartbeatInterval: 10 * time.Millisecond,
		PongTimeout:       30 * time.Millisecond,
		WriteTimeout:      time.Second,
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	wsURL := "ws" + core.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	// Ignore server pings so the read deadline expires.
	conn.SetPingHandler(func(string) error {
		return nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	if !testEventually(func() bool {
		return hub.ClientCount() == 1
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}
	if !testEventually(func() bool {
		return hub.ClientCount() == 0
	}, 2*time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client connection should close after pong timeout")
	}
}

func TestWritePumpNextWriterErrorRejects(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	hub := NewHubWithConfig(HubConfig{
		HeartbeatInterval: time.Second,
		WriteTimeout:      time.Second,
	})
	client := &Client{
		hub:           hub,
		conn:          conn,
		send:          make(chan []byte, 1),
		subscriptions: make(map[string]bool),
	}
	client.send <- []byte("payload")
	if err := conn.Close(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	done := make(chan struct{})
	go func() {
		client.writePump()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writePump should exit when NextWriter fails")
	}
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
			if err := err; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			defer testClose(t, conn.Close)
			conns[i] = conn
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(3, hub.ClientCount()) {
			t.Errorf("expected %v, got %v",

				// Subscribe all to "shared"
				3, hub.ClientCount())
		}

		for _, conn := range conns {
			err := conn.WriteJSON(Message{Type: TypeSubscribe, Data: "shared"})
			if err := err; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

		}
		time.Sleep(50 * time.Millisecond)
		if !testEqual(3, hub.ChannelSubscriberCount("shared")) {
			t.Errorf("expected %v, got %v",

				// Send to channel
				3, hub.ChannelSubscriberCount("shared"))
		}

		err := testResultError(hub.SendToChannel("shared", Message{Type: TypeEvent, Data: "hello all"}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v",

				// All three clients should receive the message
				err)
		}

		for _, conn := range conns {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			var received Message
			err := conn.ReadJSON(&received)
			if err := err; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if !testEqual(TypeEvent, received.Type) {
				t.Errorf("expected %v, got %v", TypeEvent, received.Type)
			}
			if !testEqual("hello all", received.Data) {
				t.Errorf("expected %v, got %v", "hello all", received.Data)
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
				_ = hub.Subscribe(clients[idx], "race-channel")
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
					_ = hub.Subscribe(clients[idx], "another-channel")
				}
			}(i)
		}
		wg.Wait()
		if !testEqual(

			// Verify: half should remain on race-channel, half should be on another-channel
			numClients/2, hub.ChannelSubscriberCount("race-channel")) {
			t.Errorf("expected %v, got %v", numClients/2, hub.ChannelSubscriberCount("race-channel"))
		}
		if !testEqual(numClients/2, hub.ChannelSubscriberCount("another-channel")) {
			t.Errorf("expected %v, got %v", numClients/2, hub.ChannelSubscriberCount("another-channel"))
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Subscribe to process channel
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "process:build-42"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		// Send lines one at a time with a small delay to avoid batching
		lines := []string{"Compiling...", "Linking...", "Done."}
		for _, line := range lines {
			err = testResultError(hub.SendProcessOutput("build-42", line))
			if err := err; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			time.Sleep(10 * time.Millisecond) // Allow writePump to flush each individually
		}

		// Read all three — messages may arrive individually or batched
		// with newline separators. ReadMessage gives raw frames.
		var received []Message
		for len(received) < 3 {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, data, readErr := conn.ReadMessage()
			if err := readErr; err != nil {
				t.Fatalf("expected no error, got %v",

					// A single frame may contain multiple newline-separated JSON objects
					err)
			}

			parts := core.Split(core.Trim(string(data)), "\n")
			for _, part := range parts {
				part = core.Trim(part)
				if part == "" {
					continue
				}
				var msg Message
				if !(core.JSONUnmarshal([]byte(part), &msg).OK) {
					t.Fatalf("expected true")
				}

				received = append(received, msg)
			}
		}
		if gotLen := len(received); gotLen != 3 {
			t.Fatalf("expected length %v, got %v", 3, gotLen)
		}

		for i, expected := range lines {
			if !testEqual(TypeProcessOutput, received[i].Type) {
				t.Errorf("expected %v, got %v", TypeProcessOutput, received[i].Type)
			}
			if !testEqual("build-42", received[i].ProcessID) {
				t.Errorf("expected %v, got %v", "build-42", received[i].ProcessID)
			}
			if !testEqual(expected, received[i].Data) {
				t.Errorf("expected %v, got %v", expected, received[i].Data)
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)

		// Subscribe
		err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "process:job-7"})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		// Send status
		err = testResultError(hub.SendProcessStatus("job-7", "exited", 1))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var received Message
		err = conn.ReadJSON(&received)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !testEqual(TypeProcessStatus, received.Type) {
			t.Errorf("expected %v, got %v", TypeProcessStatus, received.Type)
		}
		if !testEqual("job-7", received.ProcessID) {
			t.Errorf("expected %v, got %v", "job-7", received.ProcessID)
		}

		data, ok := received.Data.(map[string]any)
		if !(ok) {
			t.Fatalf("expected true")
		}
		if !testEqual("exited", data["status"]) {
			t.Errorf("expected %v, got %v", "exited", data["status"])

			// --- Benchmarks ---
		}
		if !testEqual(float64(1), data["exitCode"]) {
			t.Errorf("expected %v, got %v", float64(1), data["exitCode"])
		}

	})
}

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
		_ = hub.Subscribe(client, "bench-channel")
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
		if !testEqual(5*time.Second, hub.config.HeartbeatInterval) {
			t.Errorf("expected %v, got %v", 5*time.Second, hub.config.HeartbeatInterval)
		}
		if !testEqual(10*time.Second, hub.config.PongTimeout) {
			t.Errorf("expected %v, got %v", 10*time.Second, hub.config.PongTimeout)
		}
		if !testEqual(3*time.Second, hub.config.WriteTimeout) {
			t.Errorf("expected %v, got %v", 3*time.Second, hub.config.WriteTimeout)
		}

	})

	t.Run("applies defaults for zero values", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{})
		if !testEqual(DefaultHeartbeatInterval, hub.config.HeartbeatInterval) {
			t.Errorf("expected %v, got %v", DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
		}
		if !testEqual(DefaultPongTimeout, hub.config.PongTimeout) {
			t.Errorf("expected %v, got %v", DefaultPongTimeout, hub.config.PongTimeout)
		}
		if !testEqual(DefaultWriteTimeout, hub.config.WriteTimeout) {
			t.Errorf("expected %v, got %v", DefaultWriteTimeout, hub.config.WriteTimeout)
		}

	})

	t.Run("applies defaults for negative values", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			HeartbeatInterval: -1,
			PongTimeout:       -1,
			WriteTimeout:      -1,
		})
		if !testEqual(DefaultHeartbeatInterval, hub.config.HeartbeatInterval) {
			t.Errorf("expected %v, got %v", DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
		}
		if !testEqual(DefaultPongTimeout, hub.config.PongTimeout) {
			t.Errorf("expected %v, got %v", DefaultPongTimeout, hub.config.PongTimeout)
		}
		if !testEqual(DefaultWriteTimeout, hub.config.WriteTimeout) {
			t.Errorf("expected %v, got %v", DefaultWriteTimeout, hub.config.WriteTimeout)
		}

	})

	t.Run("expands pong timeout when it does not exceed heartbeat interval", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			HeartbeatInterval: 20 * time.Second,
			PongTimeout:       10 * time.Second,
		})
		if !testEqual(20*time.Second, hub.config.HeartbeatInterval) {
			t.Errorf("expected %v, got %v", 20*time.Second, hub.config.HeartbeatInterval)
		}
		if !testEqual(40*time.Second, hub.config.PongTimeout) {
			t.Errorf("expected %v, got %v", 40*time.Second, hub.config.PongTimeout)
		}

	})
}

func TestDefaultHubConfig(t *testing.T) {
	t.Run("returns sensible defaults", func(t *testing.T) {
		config := DefaultHubConfig()
		if !testEqual(30*time.Second, config.HeartbeatInterval) {
			t.Errorf("expected %v, got %v", 30*time.Second, config.HeartbeatInterval)
		}
		if !testEqual(60*time.Second, config.PongTimeout) {
			t.Errorf("expected %v, got %v", 60*time.Second, config.PongTimeout)
		}
		if !testEqual(10*time.Second, config.WriteTimeout) {
			t.Errorf("expected %v, got %v", 10*time.Second, config.WriteTimeout)
		}
		if !testIsNil(config.OnConnect) {
			t.Errorf("expected nil, got %T", config.OnConnect)
		}
		if !testIsNil(config.OnDisconnect) {
			t.Errorf("expected nil, got %T", config.OnDisconnect)
		}
		if !testIsNil(config.ChannelAuthoriser) {
			t.Errorf("expected nil, got %T", config.ChannelAuthoriser)
		}
		if !testIsEmpty(config.AllowedOrigins) {
			t.Errorf("expected empty value, got %v", config.AllowedOrigins)
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		select {
		case c := <-connectCalled:
			if testIsNil(c) {
				t.Errorf("expected non-nil value")
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		// Close the connection to trigger disconnect
		_ = conn.Close()

		select {
		case c := <-disconnectCalled:
			if testIsNil(c) {
				t.Errorf("expected non-nil value")
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
				return role == "admin" || core.HasPrefix(channel, "public:")
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

		err := testResultError(hub.Subscribe(client, "public:news"))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		err = testResultError(hub.Subscribe(client, "private:ops"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "subscription unauthorised") {
			t.Errorf("expected %v to contain %v", err.Error(), "subscription unauthorised")
		}
		if !testEqual(1, hub.ChannelSubscriberCount("public:news")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("public:news"))
		}
		if !testEqual(0, hub.ChannelSubscriberCount("private:ops")) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("private:ops"))
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

		err := testResultError(hub.Subscribe(client, "private:ops"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "subscription unauthorised") {
			t.Errorf("expected %v to contain %v", err.Error(), "subscription unauthorised")
		}
		if !testIsEmpty(client.subscriptions) {
			t.Errorf("expected empty value, got %v", client.subscriptions)
		}
		if !testEqual(0, hub.ChannelCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
		}

	})
}

func TestHub_ChannelAuthoriser_Panic_Ugly(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		ChannelAuthoriser: func(client *Client, channel string) bool {
			panic("boom")
		},
	})

	client := &Client{
		hub:           hub,
		subscriptions: make(map[string]bool),
	}

	err := testResultError(hub.Subscribe(client, "panic-channel"))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "subscription unauthorised") {
		t.Errorf("expected %v to contain %v", err.Error(), "subscription unauthorised")
	}
	if !testEqual(0, hub.ChannelCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
	}
	if !testIsEmpty(client.subscriptions) {
		t.Errorf("expected empty value, got %v", client.subscriptions)
	}

}

func TestHub_MaxSubscriptionsPerClient(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		MaxSubscriptionsPerClient: 1,
	})

	client := &Client{
		hub:           hub,
		subscriptions: make(map[string]bool),
	}
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err := testResultError(hub.Subscribe(client, "beta"))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(err, ErrSubscriptionLimitExceeded)) {
		t.Errorf("expected true")
	}
	if !testEqual(1, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("alpha"))
	}
	if !testEqual(0, hub.ChannelSubscriberCount("beta")) {
		t.Errorf("expected %v, got %v", 0, hub.

			// Use a very short heartbeat to test it actually fires
			ChannelSubscriberCount("beta"))
	}

}

func TestHub_CustomHeartbeat(t *testing.T) {
	t.Run("uses custom heartbeat interval for server pings", func(t *testing.T) {

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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

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
		go func() {
			_ = rc.Connect(clientCtx)
		}()

		// Wait for connect
		select {
		case <-connectCalled:
			// Good
		case <-time.After(time.Second):
			t.Fatal("OnConnect should have been called")
		}
		if !testEqual(StateConnected, rc.State()) {
			t.Errorf("expected %v, got %v",

				// Wait for client to register
				StateConnected, rc.State())
		}

		time.Sleep(50 * time.Millisecond)

		// Broadcast a message
		err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "hello"}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-msgReceived:
			if !testEqual(TypeEvent, msg.Type) {
				t.Errorf("expected %v, got %v", TypeEvent, msg.Type)
			}
			if !testEqual("hello", msg.Data) {
				t.Errorf("expected %v, got %v", "hello", msg.Data)
			}

		case <-time.After(time.Second):
			t.Fatal("should have received the broadcast message")
		}

		clientCancel()
		time.Sleep(50 * time.Millisecond)
	})
}

func TestReconnectingClient_ContextCancel_WhileConnected(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	wsURL := "ws" + core.TrimPrefix(server.URL, "http")

	connectCalled := make(chan struct{}, 1)
	rc := NewReconnectingClient(ReconnectConfig{
		URL: wsURL,
		OnConnect: func() {
			select {
			case connectCalled <- struct{}{}:
			default:
			}
		},
	})

	clientCtx, clientCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- testResultError(rc.Connect(clientCtx))
	}()

	select {
	case <-connectCalled:
	case <-time.After(time.Second):
		t.Fatal("OnConnect should have been called")
	}

	clientCancel()

	select {
	case err := <-done:
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testEqual(context.Canceled, err) {
			t.Errorf("expected %v, got %v", context.Canceled, err)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("Connect should return after context cancel while connected")
	}
}

func TestReconnectingClient_ReadLimit(t *testing.T) {
	largePayload := testRepeat("A", defaultMaxMessageBytes+1)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		time.Sleep(50 * time.Millisecond)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(largePayload)); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, clientConn.Close)

	rc := &ReconnectingClient{conn: clientConn}
	done := make(chan error, 1)
	go func() {
		done <- rc.readLoop()
	}()

	select {
	case readErr := <-done:
		if err := readErr; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(readErr.Error(), "read limit") {
			t.Errorf("expected %v to contain %v", readErr.Error(), "read limit")
		}

	case <-time.After(2 * time.Second):
		t.Fatal("read loop should stop after exceeding the read limit")
	}
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
	go func() {
		_ = rc.Connect(clientCtx)
	}()

	time.Sleep(50 * time.Millisecond)

	err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "raw-bytes"}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case data := <-rawReceived:
		if !testContains(string(data), "raw-bytes") {
			t.Errorf("expected %v to contain %v", string(data), "raw-bytes")
		}

		var received Message
		if !(core.JSONUnmarshal(data, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeEvent, received.Type) {
			t.Errorf("expected %v, got %v", TypeEvent, received.Type)
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
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
		go func() {
			_ = rc.Connect(clientCtx)
		}()

		// Wait for initial connection
		select {
		case <-connectCalled:
		case <-time.After(time.Second):
			t.Fatal("initial connection should have succeeded")
		}
		if !testEqual(StateConnected, rc.State()) {
			t.Errorf("expected %v, got %v",

				// Shut down the server to simulate disconnect
				StateConnected, rc.State())
		}

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
				t.Errorf("expected %v to be greater than %v", attempt, 0)
			}

		case <-time.After(3 * time.Second):
			t.Fatal("OnReconnect should have been called")
		}
		if !testEqual(StateConnected, rc.State()) {
			t.Errorf("expected %v, got %v", StateConnected, rc.State())
		}

		clientCancel()
	})
}

func TestReconnectingClient_ReconnectBackoffAfterDisconnect(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	var acceptedMu sync.Mutex
	acceptedAt := make([]time.Time, 0, 2)
	releaseSecond := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		acceptedMu.Lock()
		acceptedAt = append(acceptedAt, time.Now())
		connectionCount := len(acceptedAt)
		acceptedMu.Unlock()

		if connectionCount == 1 {
			time.Sleep(20 * time.Millisecond)
			_ = conn.Close()
			return
		}

		<-releaseSecond
		_ = conn.Close()
	}))
	defer server.Close()

	rc := NewReconnectingClient(ReconnectConfig{
		URL:            wsURL(server),
		InitialBackoff: 150 * time.Millisecond,
		MaxBackoff:     150 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- testResultError(rc.Connect(ctx))
	}()
	if !testEventually(func() bool {
		acceptedMu.Lock()
		defer acceptedMu.Unlock()
		return len(acceptedAt) >= 2
	}, 3*time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

	acceptedMu.Lock()
	firstAccepted := acceptedAt[0]
	secondAccepted := acceptedAt[1]
	acceptedMu.Unlock()
	if secondAccepted.Sub(firstAccepted) < 150*time.Millisecond {
		t.Errorf("expected %v to be greater than or equal to %v", secondAccepted.Sub(firstAccepted), 150*time.Millisecond)
	}

	close(releaseSecond)
	cancel()

	select {
	case err := <-done:
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testErrorIs(err, context.Canceled) {
			t.Errorf("expected %v to match %v", err, context.Canceled)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("Connect should return after cancellation")
	}
}

func TestReconnectingClient_MaxRetries(t *testing.T) {
	t.Run("stops after max retries exceeded", func(t *testing.T) {
		// Use a URL that will never connect
		rc := NewReconnectingClient(ReconnectConfig{
			URL:                  "ws://127.0.0.1:1", // Should refuse connection
			InitialBackoff:       10 * time.Millisecond,
			MaxBackoff:           50 * time.Millisecond,
			MaxReconnectAttempts: 3,
		})

		errCh := make(chan error, 1)
		go func() {
			errCh <- testResultError(rc.Connect(context.Background()))
		}()

		select {
		case err := <-errCh:
			if err := err; err == nil {
				t.Fatalf("expected error")
			}
			if !testContains(err.Error(), "max retries (3) exceeded") {
				t.Errorf("expected %v to contain %v", err.Error(), "max retries (3) exceeded")
			}

		case <-time.After(5 * time.Second):
			t.Fatal("should have stopped after max retries")
		}
		if !testEqual(StateDisconnected, rc.State()) {
			t.Errorf("expected %v, got %v", StateDisconnected, rc.State())
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
		go func() {
			_ = rc.Connect(clientCtx)
		}()

		<-connected
		time.Sleep(50 * time.Millisecond)

		// Send a subscribe message
		err := testResultError(rc.Send(Message{Type: TypeSubscribe,
			Data: "test-channel",
		}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		if !testEqual(1, hub.ChannelSubscriberCount("test-channel")) {
			t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("test-channel"))
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
		go func() {
			_ = rc.Connect(clientCtx)
		}()

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
				errCh <- testResultError(rc.Send(Message{Type: TypeSubscribe,
					Data: core.Sprintf("concurrent-%d", idx),
				}))
			}(i)
		}

		wg.Wait()
		close(errCh)

		for err := range errCh {
			if err := err; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

		}

		time.Sleep(100 * time.Millisecond)
		if hub.ChannelCount() < 1 {
			t.Errorf("expected %v to be greater than or equal to %v", hub.ChannelCount(), 1)
		}

	})

	t.Run("returns error when not connected", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})

		err := testResultError(rc.Send(Message{Type: TypePing}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "not connected") {
			t.Errorf("expected %v to contain %v", err.Error(), "not connected")
		}

	})

	t.Run("returns error for unmarshalable message", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})
		// Force a conn to be set so we get past the nil check
		// to hit the marshal error first
		err := testResultError(rc.Send(Message{Type: TypeEvent, Data: make(chan int)}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "failed to marshal message") {
			t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal message")
		}

	})
}

func TestWs_ReconnectingClient_Send_ContextCanceled_Good(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rc := &ReconnectingClient{
		conn:   conn,
		ctx:    ctx,
		config: ReconnectConfig{URL: wsURL(server)},
	}

	err = testResultError(rc.Send(Message{Type: TypeEvent, Data: "payload"}))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testErrorIs(err, context.Canceled) {
		t.Errorf("expected %v to match %v", err, context.Canceled)
	}

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
			done <- testResultError(rc.Connect(clientCtx))
		}()

		<-connected

		err := testResultError(rc.Close())
		if err := err; err != nil {
			t.Errorf("expected no error, got %v",

				// Good — Connect returned
				err)
		}

		select {
		case <-done:

		case <-time.After(time.Second):
			t.Fatal("Connect should have returned after Close")
		}
	})

	t.Run("close when not connected is safe", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})

		err := testResultError(rc.Close())
		if err := err; err != nil {
			t.Errorf("expected no error, got %v", err)
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
		if !testEqual(

			// attempt 1: 100ms
			100*time.Millisecond, rc.calculateBackoff(1)) {
			t.Errorf("expected %v, got %v", 100*

				// attempt 2: 200ms
				time.Millisecond, rc.calculateBackoff(1))
		}
		if !testEqual(200*time.Millisecond, rc.calculateBackoff(

			// attempt 3: 400ms
			2)) {
			t.Errorf("expected %v, got %v", 200*time.Millisecond, rc.calculateBackoff(

				// attempt 4: 800ms
				2))
		}
		if !testEqual(400*time.Millisecond, rc.calculateBackoff(3)) {
			t.Errorf("expected %v, got %v",

				// attempt 5: capped at 1s
				400*time.Millisecond, rc.calculateBackoff(3))
		}
		if !testEqual(800*time.Millisecond,

			// attempt 10: still capped at 1s
			rc.calculateBackoff(4)) {
			t.Errorf("expected %v, got %v", 800*time.Millisecond, rc.calculateBackoff(4))
		}
		if !testEqual(1*time.Second, rc.calculateBackoff(5)) {
			t.Errorf("expected %v, got %v", 1*time.Second, rc.calculateBackoff(5))
		}
		if !testEqual(1*time.Second, rc.calculateBackoff(10)) {
			t.Errorf("expected %v, got %v", 1*time.Second, rc.calculateBackoff(10))
		}

	})

	t.Run("caps an oversized initial backoff", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL:            "ws://localhost:1",
			InitialBackoff: 5 * time.Second,
			MaxBackoff:     1 * time.Second,
		})
		if !testEqual(1*time.Second, rc.config.InitialBackoff) {
			t.Errorf("expected %v, got %v", 1*time.Second, rc.config.InitialBackoff)
		}
		if !testEqual(1*time.Second, rc.calculateBackoff(1)) {
			t.Errorf("expected %v, got %v", 1*time.Second, rc.calculateBackoff(1))
		}

	})

	t.Run("rejects shrinking multipliers", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL:               "ws://localhost:1",
			InitialBackoff:    100 * time.Millisecond,
			MaxBackoff:        1 * time.Second,
			BackoffMultiplier: 0.5,
		})
		if !testEqual(2.0, rc.config.BackoffMultiplier) {
			t.Errorf("expected %v, got %v", 2.0, rc.config.BackoffMultiplier)
		}
		if !testEqual(100*time.Millisecond, rc.calculateBackoff(1)) {
			t.Errorf("expected %v, got %v", 100*time.Millisecond, rc.calculateBackoff(1))
		}
		if !testEqual(200*time.Millisecond, rc.calculateBackoff(2)) {
			t.Errorf("expected %v, got %v", 200*time.Millisecond, rc.calculateBackoff(2))
		}

	})
}

func TestWs_calculateBackoff_Good(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:               "ws://localhost:1",
		InitialBackoff:    250 * time.Millisecond,
		MaxBackoff:        2 * time.Second,
		BackoffMultiplier: 2.0,
	})
	if !testEqual(250*time.Millisecond, rc.calculateBackoff(1)) {
		t.Errorf("expected %v, got %v", 250*time.Millisecond, rc.calculateBackoff(1))
	}
	if !testEqual(500*time.Millisecond, rc.calculateBackoff(2)) {
		t.Errorf("expected %v, got %v", 500*time.Millisecond, rc.calculateBackoff(2))
	}
	if !testEqual(time.Second, rc.calculateBackoff(3)) {
		t.Errorf("expected %v, got %v", time.Second, rc.calculateBackoff(3))
	}

}

func TestWs_calculateBackoff_Bad(t *testing.T) {
	rc := &ReconnectingClient{
		config: ReconnectConfig{},
	}
	if !testEqual(1*time.Second, rc.calculateBackoff(0)) {
		t.Errorf("expected %v, got %v", 1*time.Second, rc.calculateBackoff(0))
	}
	if !testEqual(2*time.Second, rc.calculateBackoff(2)) {
		t.Errorf("expected %v, got %v", 2*time.Second, rc.calculateBackoff(2))
	}

	t.Run("returns the ceiling when the initial backoff already matches max", func(t *testing.T) {
		rc := &ReconnectingClient{
			config: ReconnectConfig{
				InitialBackoff:    1 * time.Second,
				MaxBackoff:        1 * time.Second,
				BackoffMultiplier: 2,
			},
		}
		if !testEqual(1*time.Second, rc.calculateBackoff(2)) {
			t.Errorf("expected %v, got %v", 1*time.Second, rc.calculateBackoff(2))
		}

	})
}

func TestWs_calculateBackoff_Ugly(t *testing.T) {
	rc := &ReconnectingClient{
		config: ReconnectConfig{
			InitialBackoff: 5 * time.Second,
			MaxBackoff:     1 * time.Second,
		},
	}
	if !testEqual(1*time.Second, rc.calculateBackoff(1)) {
		t.Errorf("expected %v, got %v", 1*time.Second, rc.calculateBackoff(1))
	}

}

func TestWs_waitForReconnectBackoff_Good(t *testing.T) {
	if !(waitForReconnectBackoff(context.Background(), nil, 0)) {
		t.Errorf("expected true")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !(waitForReconnectBackoff(ctx, nil, 10*time.Millisecond)) {
		t.Errorf("expected true")
	}

}

func TestWs_waitForReconnectBackoff_Bad(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForReconnectBackoff(ctx, nil, 10*time.Millisecond) {
		t.Errorf("expected false")
	}

}

func TestWs_waitForReconnectBackoff_Ugly(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if waitForReconnectBackoff(context.Background(), done, 10*time.Millisecond) {
		t.Errorf("expected false")
	}

}

func TestWs_stopTimer_Good(t *testing.T) {
	timer := time.NewTimer(time.Second)
	stopTimer(timer)

	select {
	case <-timer.C:
		t.Fatal("stopTimer should drain the timer channel before it can fire")
	default:
	}
}

func TestWs_stopTimer_Bad(t *testing.T) {
	timer := time.NewTimer(10 * time.Millisecond)
	<-timer.C
	testNotPanics(t, func() {
		stopTimer(timer)
	})

	t.Run("drains a fired timer", func(t *testing.T) {
		timer := time.NewTimer(10 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		testNotPanics(t, func() {
			stopTimer(timer)
		})

		select {
		case <-timer.C:
			t.Fatal("stopTimer should drain the fired timer channel")
		default:
		}
	})
}

func TestWs_stopTimer_Ugly(t *testing.T) {
	testNotPanics(t, func() {
		stopTimer(nil)
	})

}

func TestWs_closeRequested_Good(t *testing.T) {
	rc := &ReconnectingClient{done: make(chan struct{})}
	close(rc.done)
	if !(rc.closeRequested()) {
		t.Errorf("expected true")
	}

}

func TestWs_closeRequested_Bad(t *testing.T) {
	rc := &ReconnectingClient{done: make(chan struct{})}
	if rc.closeRequested() {
		t.Errorf("expected false")
	}

}

func TestWs_closeRequested_Ugly(t *testing.T) {
	var rc *ReconnectingClient
	if rc.closeRequested() {
		t.Errorf("expected false")
	}

}

func TestWs_NewReconnectingClient_InfMultiplier_Ugly(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:               "ws://localhost:1",
		BackoffMultiplier: math.Inf(1),
	})
	if !testEqual(2.0, rc.config.BackoffMultiplier) {
		t.Errorf("expected %v, got %v", 2.0, rc.config.BackoffMultiplier)
	}

}

func TestWs_calculateBackoff_InvalidMultiplier_Ugly(t *testing.T) {
	rc := &ReconnectingClient{
		config: ReconnectConfig{
			InitialBackoff:    100 * time.Millisecond,
			MaxBackoff:        1 * time.Second,
			BackoffMultiplier: math.Inf(1),
		},
	}
	if !testEqual(200*time.Millisecond, rc.calculateBackoff(2)) {
		t.Errorf("expected %v, got %v", 200*time.Millisecond, rc.calculateBackoff(2))
	}

}

func TestWs_calculateBackoff_Overflow_Ugly(t *testing.T) {
	rc := &ReconnectingClient{
		config: ReconnectConfig{
			InitialBackoff:    time.Duration(1 << 62),
			MaxBackoff:        time.Duration(1<<63 - 1),
			BackoffMultiplier: 10,
		},
	}
	if !testEqual(rc.config.MaxBackoff, rc.calculateBackoff(2)) {
		t.Errorf("expected %v, got %v", rc.config.MaxBackoff, rc.calculateBackoff(2))
	}

}

func TestWs_Connect_DoneClosed_Good(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL: "ws://127.0.0.1:1",
	})
	close(rc.done)

	err := testResultError(rc.Connect(context.Background()))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

}

func TestWs_Connect_NilContext_Good(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:                  "ws://127.0.0.1:1",
		InitialBackoff:       10 * time.Millisecond,
		MaxBackoff:           20 * time.Millisecond,
		MaxReconnectAttempts: 1,
	})

	done := make(chan error, 1)
	go func() {
		done <- testResultError(rc.Connect(context.TODO()))
	}()

	select {
	case err := <-done:
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "max retries (1) exceeded") {
			t.Errorf("expected %v to contain %v", err.Error(), "max retries (1) exceeded")
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Connect should return when the retry limit is reached")
	}
}

func TestReconnectingClient_MaxReconnectAttempts_Precedence_Good(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:                  "ws://127.0.0.1:1",
		InitialBackoff:       10 * time.Millisecond,
		MaxBackoff:           20 * time.Millisecond,
		MaxRetries:           99,
		MaxReconnectAttempts: 1,
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- testResultError(rc.Connect(context.Background()))
	}()

	select {
	case err := <-errCh:
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "max retries (1) exceeded") {
			t.Errorf("expected %v to contain %v", err.Error(), "max retries (1) exceeded")
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Connect should have stopped after MaxReconnectAttempts")
	}
}

func TestReconnectingClient_MaxReconnectAttempts_ZeroMeansUnlimited_Good(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:                  "ws://127.0.0.1:1",
		MaxReconnectAttempts: 0,
	})
	if !testEqual(0, rc.maxReconnectAttempts()) {
		t.Errorf("expected %v, got %v", 0, rc.maxReconnectAttempts())
	}

}

func TestReconnectingClient_MaxRetries_Compatibility_Good(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:        "ws://127.0.0.1:1",
		MaxRetries: 3,
	})
	if !testEqual(3, rc.maxReconnectAttempts()) {
		t.Errorf("expected %v, got %v", 3, rc.maxReconnectAttempts())
	}

}

func TestReconnectingClient_MaxReconnectAttempts_Negative_Ugly(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:                  "ws://localhost:1",
		MaxRetries:           -1,
		MaxReconnectAttempts: -5,
	})
	if !testEqual(0, rc.maxReconnectAttempts()) {
		t.Errorf("expected %v, got %v", 0, rc.maxReconnectAttempts())
	}

}

func TestDispatchReconnectMessageStringAndUnsupportedCovers(t *testing.T) {
	stringCalled := false
	dispatchReconnectMessage(func(s string) {
		stringCalled = true
		if !testContains(s, "payload") {
			t.Errorf("expected %v to contain %v", s, "payload")
		}

	}, []byte("payload"))
	if !(stringCalled) {
		t.Errorf("expected true")
	}
	testNotPanics(t, func() {
		dispatchReconnectMessage(123, []byte("ignored"))
	})

}

func TestReconnectingClient_Defaults(t *testing.T) {
	t.Run("applies defaults for zero config values", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://localhost:1",
		})
		if !testEqual(1*time.Second, rc.config.InitialBackoff) {
			t.Errorf("expected %v, got %v", 1*time.Second, rc.config.InitialBackoff)
		}
		if !testEqual(30*time.Second, rc.config.MaxBackoff) {
			t.Errorf("expected %v, got %v", 30*time.Second, rc.config.MaxBackoff)
		}
		if !testEqual(2.0, rc.config.BackoffMultiplier) {
			t.Errorf("expected %v, got %v", 2.0, rc.config.BackoffMultiplier)
		}
		if testIsNil(rc.config.Dialer) {
			t.Errorf("expected non-nil value")
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
			done <- testResultError(rc.Connect(ctx))
		}()

		// Allow first dial attempt to fail
		time.Sleep(200 * time.Millisecond)

		// Cancel during backoff
		cancel()

		select {
		case err := <-done:
			if err := err; err == nil {
				t.Fatalf("expected error")
			}
			if !testEqual(context.Canceled, err) {
				t.Errorf("expected %v, got %v", context.Canceled, err)
			}

		case <-time.After(2 * time.Second):
			t.Fatal("Connect should have returned after context cancel")
		}
	})
}

func TestConnectionState(t *testing.T) {
	t.Run("state constants are distinct", func(t *testing.T) {
		if testEqual(StateDisconnected, StateConnecting) {
			t.Errorf("expected values to differ: %v", StateConnecting)
		}
		if testEqual(StateConnecting, StateConnected) {
			t.Errorf("expected values to differ: %v", StateConnected)
		}
		if testEqual(StateDisconnected, StateConnected) {
			t.Errorf("expected values to differ: %v", StateConnected)
		}

	})
}

func TestReconnectingClient_State_Ugly(t *testing.T) {
	var rc *ReconnectingClient
	if !testEqual(StateDisconnected, rc.State()) {
		t.Errorf("expected %v, got %v",

			// ---------------------------------------------------------------------------
			// Hub.Run lifecycle — register, broadcast delivery, unregister via channels
			// ---------------------------------------------------------------------------
			StateDisconnected, rc.State())
	}

}

func TestHubRunRegisterClientCovers(t *testing.T) {
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
	if !testEqual(1, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 1, hub.ClientCount())
	}

}

func TestHubRunBroadcastDeliveryCovers(t *testing.T) {
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

	err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "lifecycle-test"}))
	if err := err; err != nil {
		t.Fatalf(

			// Hub.Run loop delivers the broadcast to the client's send channel
			"expected no error, got %v", err)
	}

	select {
	case msg := <-client.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeEvent, received.Type) {
			t.Errorf("expected %v, got %v", TypeEvent, received.Type)
		}
		if !testEqual("lifecycle-test", received.Data) {
			t.Errorf("expected %v, got %v", "lifecycle-test", received.Data)
		}

	case <-time.After(time.Second):
		t.Fatal("broadcast should be delivered via hub loop")
	}
}

func TestHubRunUnregisterClientCovers(t *testing.T) {
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
	if !testEqual(1, hub.ClientCount()) {
		t.Errorf(

			// Subscribe so we can verify channel cleanup
			"expected %v, got %v", 1, hub.ClientCount())
	}

	_ = hub.Subscribe(client, "lifecycle-chan")
	if !testEqual(1, hub.ChannelSubscriberCount("lifecycle-chan")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("lifecycle-chan"))
	}

	hub.unregister <- client
	time.Sleep(20 * time.Millisecond)
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}
	if !testEqual(0, hub.ChannelSubscriberCount("lifecycle-chan")) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("lifecycle-chan"))
	}

}

func TestHubRunUnregisterIgnoresDuplicateRejects(t *testing.T) {
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

func TestSubscribeMultipleChannelsCovers(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	_ = hub.Subscribe(client, "alpha")
	_ = hub.Subscribe(client, "beta")
	_ = hub.Subscribe(client, "gamma")
	if !testEqual(3, hub.ChannelCount()) {
		t.Errorf("expected %v, got %v", 3, hub.ChannelCount())
	}

	subs := client.Subscriptions()
	if gotLen := len(subs); gotLen != 3 {
		t.Errorf("expected length %v, got %v", 3, gotLen)
	}
	if !testContains(subs, "alpha") {
		t.Errorf("expected %v to contain %v", subs, "alpha")
	}
	if !testContains(subs, "beta") {
		t.Errorf("expected %v to contain %v", subs, "beta")
	}
	if !testContains(subs, "gamma") {
		t.Errorf("expected %v to contain %v", subs, "gamma")
	}

}

func TestSubscribeIdempotentDoubleSubscribeCovers(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}

	_ = hub.Subscribe(client, "dupl")
	_ = hub.Subscribe(client, "dupl")
	if !testEqual(

		// Still only one subscriber entry in the channel map
		1, hub.ChannelSubscriberCount("dupl")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("dupl"))
	}

}

func TestUnsubscribePartialLeaveCovers(t *testing.T) {
	hub := NewHub()
	client1 := &Client{hub: hub, send: make(chan []byte, 256), subscriptions: make(map[string]bool)}
	client2 := &Client{hub: hub, send: make(chan []byte, 256), subscriptions: make(map[string]bool)}

	_ = hub.Subscribe(client1, "shared")
	_ = hub.Subscribe(client2, "shared")
	if !testEqual(2, hub.ChannelSubscriberCount("shared")) {
		t.Errorf("expected %v, got %v", 2, hub.ChannelSubscriberCount("shared"))
	}

	hub.Unsubscribe(client1, "shared")
	if !testEqual(1, hub.ChannelSubscriberCount("shared")) {
		t.Errorf(

			// Channel still exists because client2 is subscribed
			"expected %v, got %v", 1, hub.ChannelSubscriberCount("shared"))
	}

	hub.mu.RLock()
	_, exists := hub.channels["shared"]
	hub.mu.RUnlock()
	if !(exists) {
		t.Errorf("expected true")
	}

}

// ---------------------------------------------------------------------------
// SendToChannel — multiple subscribers
// ---------------------------------------------------------------------------

func TestSendToChannelMultipleSubscribersCovers(t *testing.T) {
	hub := NewHub()
	clients := make([]*Client, 5)
	for i := range clients {
		clients[i] = &Client{
			hub:           hub,
			send:          make(chan []byte, 256),
			subscriptions: make(map[string]bool),
		}
		_ = hub.Subscribe(clients[i], "multi")
	}

	err := testResultError(hub.SendToChannel("multi", Message{Type: TypeEvent, Data: "fanout"}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	for i, c := range clients {
		select {
		case msg := <-c.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if !testEqual("multi", received.Channel) {
				t.Errorf("expected %v, got %v", "multi", received.Channel)
			}

		case <-time.After(time.Second):
			t.Fatalf("client %d should have received the message", i)
		}
	}
}

// ---------------------------------------------------------------------------
// SendProcessOutput / SendProcessStatus — edge cases
// ---------------------------------------------------------------------------

func TestSendProcessOutputNoSubscribersCovers(t *testing.T) {
	hub := NewHub()
	err := testResultError(hub.SendProcessOutput("orphan-proc", "some output"))
	if err := err; err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestSendProcessStatusNonZeroExitCovers(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	_ = hub.Subscribe(client, "process:fail-1")

	err := testResultError(hub.SendProcessStatus("fail-1", "exited", 137))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-client.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeProcessStatus, received.Type) {
			t.Errorf("expected %v, got %v", TypeProcessStatus, received.Type)
		}
		if !testEqual("fail-1", received.ProcessID) {
			t.Errorf("expected %v, got %v", "fail-1", received.ProcessID)
		}

		data := received.Data.(map[string]any)
		if !testEqual("exited", data["status"]) {
			t.Errorf("expected %v, got %v", "exited", data["status"])
		}
		if !testEqual(float64(137), data["exitCode"]) {
			t.Errorf("expected %v, got %v", float64(137),

				// ---------------------------------------------------------------------------
				// readPump — ping with timestamp verification
				// ---------------------------------------------------------------------------
				data["exitCode"])
		}

	case <-time.After(time.Second):
		t.Fatal("expected process status message")
	}
}

func TestReadPumpPingTimestampCovers(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)
	time.Sleep(50 * time.Millisecond)

	err = conn.WriteJSON(Message{Type: TypePing})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var pong Message
	err = conn.ReadJSON(&pong)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual(TypePong, pong.Type) {
		t.Errorf("expected %v, got %v", TypePong, pong.Type)

		// ---------------------------------------------------------------------------
		// writePump — batch sending with multiple messages
		// ---------------------------------------------------------------------------
	}
	if pong.Timestamp.IsZero() {
		t.Errorf("expected false")
	}

}

func TestWritePumpBatchMultipleMessagesCovers(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)
	time.Sleep(50 * time.Millisecond)

	// Rapidly send multiple broadcasts so they queue up
	numMessages := 10
	for i := range numMessages {
		err := testResultError(hub.Broadcast(Message{Type: TypeEvent,
			Data: core.Sprintf("batch-%d", i),
		}))
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

	}

	time.Sleep(100 * time.Millisecond)

	// Read all messages — batched with newline separators
	received := 0
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for received < numMessages {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		parts := core.Split(string(raw), "\n")
		for _, part := range parts {
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
	if !testEqual(numMessages, received) {
		t.Errorf("expected %v, got %v", numMessages, received)
	}

}

// ---------------------------------------------------------------------------
// Integration — unsubscribe stops delivery
// ---------------------------------------------------------------------------

func TestIntegrationUnsubscribeStopsDeliveryCovers(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)
	time.Sleep(50 * time.Millisecond)

	// Subscribe
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "temp:feed"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Verify we receive messages on the channel
	err = testResultError(hub.SendToChannel("temp:feed", Message{Type: TypeEvent, Data: "before-unsub"}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var msg1 Message
	err = conn.ReadJSON(&msg1)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual(

		// Unsubscribe
		"before-unsub", msg1.Data) {
		t.Errorf("expected %v, got %v", "before-unsub", msg1.Data)
	}

	err = conn.WriteJSON(Message{Type: TypeUnsubscribe, Data: "temp:feed"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Send another message -- client should NOT receive it
	err = testResultError(hub.SendToChannel("temp:feed", Message{Type: TypeEvent, Data: "after-unsub"}))
	if err := err; err != nil {
		t.Fatalf(

			// Try to read -- should timeout (no message delivered)
			"expected no error, got %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var msg2 Message
	err = conn.ReadJSON(&msg2)
	if err := err; err == nil {
		t.Errorf("expected error")
	}

}

// ---------------------------------------------------------------------------
// Integration — broadcast reaches all clients (no channel subscription)
// ---------------------------------------------------------------------------

func TestIntegrationBroadcastReachesAllClientsCovers(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	numClients := 3
	conns := make([]*websocket.Conn, numClients)
	for i := range numClients {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		conns[i] = conn
	}

	time.Sleep(100 * time.Millisecond)
	if !testEqual(numClients, hub.ClientCount()) {
		t.Errorf(

			// Broadcast -- no channel subscription needed
			"expected %v, got %v", numClients, hub.ClientCount())
	}

	err := testResultError(hub.Broadcast(Message{Type: TypeError, Data: "global-alert"}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	for _, conn := range conns {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var received Message
		err := conn.ReadJSON(&received)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !testEqual(TypeError, received.Type) {
			t.Errorf("expected %v, got %v", TypeError, received.Type)
		}
		if !testEqual(

			// ---------------------------------------------------------------------------
			// Integration — disconnect cleans up all subscriptions
			// ---------------------------------------------------------------------------
			"global-alert", received.Data) {
			t.Errorf("expected %v, got %v", "global-alert", received.Data)
		}

	}
}

func TestIntegrationDisconnectCleansUpEverythingCovers(t *testing.T) {
	hub := NewHub()
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf(

			// Subscribe to multiple channels
			"expected no error, got %v", err)
	}

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "ch-a"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "ch-b"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 1, hub.ClientCount())
	}
	if !testEqual(1, hub.ChannelSubscriberCount("ch-a")) {
		t.Errorf("expected %v, got %v",

			// Disconnect
			1, hub.ChannelSubscriberCount("ch-a"))
	}
	if !testEqual(1, hub.ChannelSubscriberCount("ch-b")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("ch-b"))
	}

	_ = conn.Close()
	time.Sleep(100 * time.Millisecond)
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}
	if !testEqual(0, hub.ChannelSubscriberCount("ch-a")) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("ch-a"))
	}
	if !testEqual(0, hub.ChannelSubscriberCount("ch-b")) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("ch-b"))
	}
	if !testEqual(0, hub.ChannelCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
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
			return role == "admin" || core.HasPrefix(channel, "public:")
		},
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	time.Sleep(50 * time.Millisecond)

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "private:ops"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var response Message
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual(TypeError, response.Type) {
		t.Errorf("expected %v, got %v", TypeError, response.Type)
	}
	if !testContains(response.Data.(string), "subscription unauthorised") {
		t.Errorf("expected %v to contain %v", response.Data.(string), "subscription unauthorised")
	}
	if !testEqual(0, hub.ChannelSubscriberCount("private:ops")) {
		t.Errorf("expected %v, got %v", 0, hub.

			// ---------------------------------------------------------------------------
			// Concurrent broadcast + subscribe via hub loop (race test)
			// ---------------------------------------------------------------------------
			ChannelSubscriberCount("private:ops"))
	}

	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "public:news"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ChannelSubscriberCount("public:news")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("public:news"))
	}

}

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
	if !testEqual(50, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 50, hub.ClientCount())
	}

}

func TestHub_Handler_RejectsWhenNotRunning(t *testing.T) {
	// Handler should not block or register clients when the hub loop is not running.
	hub := NewHub()

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		if err := err; err == nil {
			t.Errorf("expected error")
		}
		if !testEqual(0, hub.ClientCount()) {
			t.Errorf("expected %v, got %v", 0, hub.ClientCount())
		}

		return
	}

	defer testClose(t, conn.Close)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, readErr := conn.ReadMessage()
	if err := readErr; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 1, hub.ClientCount())
	}

	_ = conn.Close()
	time.Sleep(50 * time.Millisecond)
	if gotLen := len(ctxErr); gotLen != 1 {
		t.Fatalf("expected length %v, got %v", 1, gotLen)
	}

}

func TestHub_OnConnect_CallbackCanReenterHub(t *testing.T) {
	connected := make(chan struct{}, 1)
	subscribeErr := make(chan error, 1)

	hub := NewHubWithConfig(HubConfig{
		OnConnect: func(client *Client) {
			connected <- struct{}{}
			subscribeErr <- testResultError(client.hub.Subscribe(client, "callback-channel"))
		},
	})
	ctx := t.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, conn.Close)

	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("OnConnect callback did not run")
	}

	select {
	case err := <-subscribeErr:
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

	case <-time.After(time.Second):
		t.Fatal("re-entrant subscription from OnConnect timed out")
	}
	if !testEventually(func() bool {
		return hub.ChannelSubscriberCount("callback-channel") == 1
	}, time.Second, 10*time.Millisecond) {
		t.Errorf("condition was not met before timeout")
	}

}

func TestWsnilHubErrorCovers(t *testing.T) {
	err := testResultError(nilHubResult("Broadcast"))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}
	if !testContains(err.Error(), "Broadcast") {
		t.Errorf("expected %v to contain %v", err.Error(), "Broadcast")
	}

}

func TestWsnilHubErrorRejects(t *testing.T) {
	err := testResultError(nilHubResult(""))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestWsnilHubErrorEdges(t *testing.T) {
	err := testResultError(nilHubResult(" \t\n"))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestWs_NewHubWithConfig_Good(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{})
	if testIsNil(hub) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual(DefaultHeartbeatInterval, hub.config.HeartbeatInterval) {
		t.Errorf("expected %v, got %v", DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
	}
	if !testEqual(DefaultPongTimeout, hub.config.PongTimeout) {
		t.Errorf("expected %v, got %v", DefaultPongTimeout, hub.config.PongTimeout)
	}
	if !testEqual(DefaultWriteTimeout, hub.config.WriteTimeout) {
		t.Errorf("expected %v, got %v", DefaultWriteTimeout, hub.config.WriteTimeout)
	}
	if !testEqual(DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient) {
		t.Errorf("expected %v, got %v", DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient)
	}

}

func TestWs_NewHubWithConfig_Bad(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		HeartbeatInterval:         5 * time.Second,
		PongTimeout:               4 * time.Second,
		WriteTimeout:              -1,
		MaxSubscriptionsPerClient: -1,
	})
	if testIsNil(hub) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual(5*time.Second, hub.config.HeartbeatInterval) {
		t.Errorf("expected %v, got %v", 5*time.Second, hub.config.HeartbeatInterval)
	}
	if !testEqual(10*time.Second, hub.config.PongTimeout) {
		t.Errorf("expected %v, got %v", 10*time.Second, hub.config.PongTimeout)
	}
	if !testEqual(DefaultWriteTimeout, hub.config.WriteTimeout) {
		t.Errorf("expected %v, got %v", DefaultWriteTimeout, hub.config.WriteTimeout)
	}
	if !testEqual(DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient) {
		t.Errorf("expected %v, got %v", DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient)
	}

}

func TestWs_NewHubWithConfig_Ugly(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		HeartbeatInterval:         -1,
		PongTimeout:               time.Nanosecond,
		WriteTimeout:              0,
		MaxSubscriptionsPerClient: 0,
	})
	if testIsNil(hub) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual(DefaultHeartbeatInterval, hub.config.HeartbeatInterval) {
		t.Errorf("expected %v, got %v", DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
	}
	if !testEqual(DefaultPongTimeout, hub.config.PongTimeout) {
		t.Errorf("expected %v, got %v", DefaultPongTimeout, hub.config.PongTimeout)
	}
	if !testEqual(DefaultWriteTimeout, hub.config.WriteTimeout) {
		t.Errorf("expected %v, got %v", DefaultWriteTimeout, hub.config.WriteTimeout)
	}
	if !testEqual(DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient) {
		t.Errorf("expected %v, got %v", DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient)
	}

}

func TestWs_Subscribe_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		subscriptions: make(map[string]bool),
	}

	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()

	err := testResultError(hub.Subscribe(client, "alpha"))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !(client.subscriptions["alpha"]) {
		t.Errorf("expected true")
	}
	if !testEqual(1, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("alpha"))
	}

}

func TestWs_Subscribe_RunningHubClosedDone_Bad(t *testing.T) {
	t.Run("nil hub", func(t *testing.T) {
		client := &Client{subscriptions: make(map[string]bool)}

		err := testResultError((*Hub)(nil).Subscribe(client, "alpha"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "hub must not be nil") {
			t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
		}

	})

	t.Run("invalid channel", func(t *testing.T) {
		hub := NewHub()
		client := &Client{subscriptions: make(map[string]bool)}

		err := testResultError(hub.Subscribe(client, "bad channel"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid channel name") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid channel name")
		}

	})

	t.Run("channel authoriser rejects", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{
			ChannelAuthoriser: func(client *Client, channel string) bool {
				return false
			},
		})
		client := &Client{hub: hub, subscriptions: make(map[string]bool)}

		err := testResultError(hub.Subscribe(client, "alpha"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "subscription unauthorised") {
			t.Errorf("expected %v to contain %v", err.Error(), "subscription unauthorised")
		}

	})

	t.Run("subscription limit exceeded", func(t *testing.T) {
		hub := NewHubWithConfig(HubConfig{MaxSubscriptionsPerClient: 1})
		client := &Client{hub: hub, subscriptions: make(map[string]bool)}
		if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		err := testResultError(hub.Subscribe(client, "beta"))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !(core.Is(err, ErrSubscriptionLimitExceeded)) {
			t.Errorf("expected true")
		}

	})
}

func TestWs_Subscribe_Ugly(t *testing.T) {
	hub := NewHub()
	if err := testResultError(hub.Subscribe(nil, "alpha")); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestWs_Subscribe_NilHub_Bad(t *testing.T) {
	client := &Client{subscriptions: make(map[string]bool)}

	err := testResultError((*Hub)(nil).Subscribe(client, "alpha"))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestWs_Subscribe_NilSubscriptions_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{hub: hub}
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual([]string{"alpha"}, client.Subscriptions()) {
		t.Errorf("expected %v, got %v", []string{"alpha"}, client.Subscriptions())
	}

}

func TestWs_Subscribe_HubStoppedBeforeReply_Bad(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	if !testEventually(func() bool {
		return hub.isRunning()
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

	client := &Client{hub: hub, subscriptions: make(map[string]bool)}
	client.mu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- testResultError(hub.Subscribe(client, "alpha"))
	}()

	time.Sleep(20 * time.Millisecond)
	hub.doneOnce.Do(func() { close(hub.done) })

	select {
	case err := <-done:
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "hub stopped before subscription completed") {
			t.Errorf("expected %v to contain %v", err.Error(), "hub stopped before subscription completed")
		}

	case <-time.After(time.Second):
		t.Fatal("Subscribe should return once the hub shuts down")
	}

	client.mu.Unlock()
}

func TestWs_Unsubscribe_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		subscriptions: make(map[string]bool),
	}

	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	hub.Unsubscribe(client, "alpha")
	if client.subscriptions["alpha"] {
		t.Errorf("expected false")
	}
	if !testEqual(0, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("alpha"))
	}

}

func TestWs_Unsubscribe_RunningHubClosedDone_Bad(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		subscriptions: make(map[string]bool),
	}
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	hub.Unsubscribe(client, "bad channel")
	if !(client.subscriptions["alpha"]) {
		t.Errorf("expected true")
	}
	if !testEqual(1, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("alpha"))
	}

}

func TestWs_Unsubscribe_Ugly(t *testing.T) {
	testNotPanics(t, func() {
		var hub *Hub
		hub.Unsubscribe(nil, "alpha")
		hub.Unsubscribe(&Client{}, "")
	})

}

func TestWs_Unsubscribe_NilHub_Ugly(t *testing.T) {
	testNotPanics(t, func() {
		(*Hub)(nil).Unsubscribe(&Client{subscriptions: make(map[string]bool)}, "alpha")
	})

}

func TestWs_Unsubscribe_HubStoppedBeforeReply_Bad(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	if !testEventually(func() bool {
		return hub.isRunning()
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

	client := &Client{hub: hub, subscriptions: make(map[string]bool)}
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	client.mu.Lock()
	done := make(chan struct{})
	go func() {
		hub.Unsubscribe(client, "alpha")
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	hub.doneOnce.Do(func() { close(hub.done) })

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Unsubscribe should return once the hub shuts down")
	}

	client.mu.Unlock()
}

func TestWs_dispatchReconnectMessage_Good(t *testing.T) {
	var seen []Message

	dispatchReconnectMessage(func(msg Message) {
		seen = append(seen, msg)
	}, []byte("{\"type\":\"event\",\"data\":\"alpha\"}\n{\"type\":\"error\",\"data\":\"beta\"}"))
	if gotLen := len(seen); gotLen != 2 {
		t.Fatalf("expected length %v, got %v", 2, gotLen)
	}
	if !testEqual(TypeEvent, seen[0].Type) {
		t.Errorf("expected %v, got %v", TypeEvent, seen[0].Type)
	}
	if !testEqual("alpha", seen[0].Data) {
		t.Errorf("expected %v, got %v", "alpha", seen[0].Data)
	}
	if !testEqual(TypeError, seen[1].Type) {
		t.Errorf("expected %v, got %v", TypeError, seen[1].Type)
	}
	if !testEqual("beta", seen[1].Data) {
		t.Errorf("expected %v, got %v", "beta", seen[1].Data)
	}

}

func TestWs_dispatchReconnectMessage_Bad(t *testing.T) {
	called := 0

	dispatchReconnectMessage(func(msg Message) {
		called++
	}, []byte("{not-json}\n{\"type\":\"event\",\"data\":\"ok\"}"))
	if !testEqual(1, called) {
		t.Errorf("expected %v, got %v", 1, called)
	}

}

func TestWs_dispatchReconnectMessage_Ugly(t *testing.T) {
	testNotPanics(t, func() {
		dispatchReconnectMessage(nil, []byte("ignored"))
		dispatchReconnectMessage(123, []byte("ignored"))
		dispatchReconnectMessage(func(msg Message) {
			panic("boom")
		}, []byte("{\"type\":\"event\"}"))
	})

}

func TestReconnectingClient_Send_Good(t *testing.T) {
	msgSeen := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)

		_, data, err := conn.ReadMessage()
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		msgSeen <- data
	}))
	defer server.Close()

	rc := NewReconnectingClient(ReconnectConfig{
		URL: wsURL(server),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- testResultError(rc.Connect(ctx))
	}()
	if !testEventually(func() bool {
		return rc.State() == StateConnected
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}
	if err := testResultError(rc.Send(Message{Type: TypeEvent, Data: "payload"})); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case data := <-msgSeen:
		if !testContains(string(data), "\"type\":\"event\"") {
			t.Errorf("expected %v to contain %v", string(data), "\"type\":\"event\"")
		}
		if !testContains(string(data), "\"data\":\"payload\"") {
			t.Errorf("expected %v to contain %v", string(data), "\"data\":\"payload\"")
		}

	case <-time.After(time.Second):
		t.Fatal("server should have received the sent message")
	}
	if err := testResultError(rc.Close()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case err := <-done:
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testEqual(context.Canceled, err) {
			t.Errorf("expected %v, got %v", context.Canceled, err)
		}

	case <-time.After(time.Second):
		t.Fatal("Connect should stop after Close cancels the context")
	}
}

func TestReconnectingClient_Send_Bad(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var rc *ReconnectingClient

		err := testResultError(rc.Send(Message{Type: TypeEvent}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "client must not be nil") {
			t.Errorf("expected %v to contain %v", err.Error(), "client must not be nil")
		}

	})

	t.Run("not connected", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{URL: "ws://127.0.0.1:1"})

		err := testResultError(rc.Send(Message{Type: TypeEvent}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "not connected") {
			t.Errorf("expected %v to contain %v", err.Error(), "not connected")
		}

	})

	t.Run("marshal failure", func(t *testing.T) {
		rc := NewReconnectingClient(ReconnectConfig{
			URL: "ws://127.0.0.1:1",
			OnError: func(err error) {
				if !testContains(err.Error(), "failed to marshal message") {
					t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal message")
				}

			},
		})

		err := testResultError(rc.Send(Message{Type: TypeEvent, Data: make(chan int)}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "failed to marshal message") {
			t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal message")
		}

	})

	t.Run("context canceled", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err := err; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			defer testClose(t, conn.Close)
		}))
		defer server.Close()

		clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, clientConn.Close)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		rc := &ReconnectingClient{
			conn:   clientConn,
			ctx:    ctx,
			state:  StateConnected,
			config: ReconnectConfig{URL: "ws://127.0.0.1:1"},
		}

		err = testResultError(rc.Send(Message{Type: TypeEvent, Data: "payload"}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testEqual(context.Canceled, err) {
			t.Errorf("expected %v, got %v", context.Canceled, err)
		}

	})

	t.Run("write failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err := err; err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			defer testClose(t, conn.Close)
		}))
		defer server.Close()

		clientConn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		rc := &ReconnectingClient{
			conn:   clientConn,
			state:  StateConnected,
			done:   make(chan struct{}),
			config: ReconnectConfig{URL: wsURL(server)},
		}
		if err := clientConn.Close(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		err = testResultError(rc.Send(Message{Type: TypeEvent, Data: "payload"}))
		if err := err; err == nil {
			t.Fatalf("expected error")
		}

	})
}

func TestReconnectingClient_Close_Ugly(t *testing.T) {
	var rc *ReconnectingClient
	if err := testResultError(rc.Close()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestReconnectingClient_Connect_Ugly(t *testing.T) {
	var rc *ReconnectingClient

	err := testResultError(rc.Connect(context.Background()))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "client must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "client must not be nil")
	}

}

func TestReconnectingClient_Connect_OnError_Good(t *testing.T) {
	errs := make(chan error, 4)

	rc := NewReconnectingClient(ReconnectConfig{
		URL:                  "ws://127.0.0.1:1",
		InitialBackoff:       10 * time.Millisecond,
		MaxBackoff:           20 * time.Millisecond,
		MaxReconnectAttempts: 1,
		OnError: func(err error) {
			select {
			case errs <- err:
			default:
			}
		},
	})

	done := make(chan error, 1)
	go func() {
		done <- testResultError(rc.Connect(context.Background()))
	}()

	select {
	case err := <-done:
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "max retries (1) exceeded") {
			t.Errorf("expected %v to contain %v", err.Error(), "max retries (1) exceeded")
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Connect should stop after max retries")
	}
	if !testEventually(func() bool {
		return len(errs) >= 2
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

	first := <-errs
	second := <-errs
	if err := first; err == nil {
		t.Fatalf("expected error")
	}
	if err := second; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(second.Error(), "max retries (1) exceeded") {
		t.Errorf("expected %v to contain %v", second.Error(), "max retries (1) exceeded")
	}

}

func TestReconnectingClient_Send_Ugly(t *testing.T) {
	rc := NewReconnectingClient(ReconnectConfig{URL: "ws://127.0.0.1:1"})
	rc.setState(StateConnected)

	err := testResultError(rc.Send(Message{Type: TypeEvent}))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "not connected") {
		t.Errorf("expected %v to contain %v", err.Error(), "not connected")
	}

}

func TestReconnectingClient_readLoop_Ugly(t *testing.T) {
	rc := &ReconnectingClient{}
	if err := testResultError(rc.readLoop()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestWs_sameOriginCheck_Good(t *testing.T) {
	tests := []struct {
		name string
		req  func() *http.Request
		want bool
	}{
		{
			name: "no origin header is allowed",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
			},
			want: true,
		},
		{
			name: "matches host and scheme",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "http://example.com")
				return r
			},
			want: true,
		},
		{
			name: "matches https on explicit port",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "https://example.com:443/ws", nil)
				r.TLS = &tls.ConnectionState{}
				r.Header.Set("Origin", "https://example.com")
				return r
			},
			want: true,
		},
		{
			name: "uses request URL host when Host is empty",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.org:8080/ws", nil)
				r.Host = ""
				r.URL.Host = "example.org:8080"
				r.Header.Set("Origin", "http://example.org:8080")
				return r
			},
			want: true,
		},
		{
			name: "treats whitespace origin as absent",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "   ")
				return r
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !testEqual(tt.want, sameOriginCheck(tt.req())) {
				t.Errorf("expected %v, got %v", tt.want, sameOriginCheck(tt.req()))
			}

		})
	}
}

func TestWs_sameOriginCheck_Bad(t *testing.T) {
	tests := []struct {
		name string
		req  func() *http.Request
	}{
		{
			name: "scheme mismatch",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "https://example.com")
				return r
			},
		},
		{
			name: "host mismatch",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "http://evil.example")
				return r
			},
		},
		{
			name: "port mismatch",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com:8080/ws", nil)
				r.Header.Set("Origin", "http://example.com:9090")
				return r
			},
		},
		{
			name: "malformed origin",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "://broken")
				return r
			},
		},
		{
			name: "invalid origin host",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "http://example.com:bad")
				return r
			},
		},
		{
			name: "invalid origin port after parse",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "http://[2001:db8::1]:bad")
				return r
			},
		},
		{
			name: "origin host requires brackets for ipv6",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "http://2001:db8::1")
				return r
			},
		},
		{
			name: "missing origin host",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Header.Set("Origin", "http://")
				return r
			},
		},
		{
			name: "invalid request host",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Host = "example.com:bad"
				r.Header.Set("Origin", "http://example.com")
				return r
			},
		},
		{
			name: "request host requires brackets for ipv6",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
				r.Host = "2001:db8::1"
				r.Header.Set("Origin", "http://example.com")
				return r
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if sameOriginCheck(tt.req()) {
				t.Errorf("expected false")
			}

		})
	}
}

func TestWs_sameOriginCheck_Ugly(t *testing.T) {
	if sameOriginCheck(nil) {
		t.Errorf("expected false")
	}

	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	r.Host = ""
	r.URL.Host = ""
	r.Header.Set("Origin", "http://example.com")
	if sameOriginCheck(r) {
		t.Errorf("expected false")
	}

}

func TestWs_sameOriginCheck_Ugly_NilURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	r.URL = nil
	r.Host = ""
	r.Header.Set("Origin", "http://example.com")
	if sameOriginCheck(r) {
		t.Errorf("expected false")
	}

}

func TestWs_sameOriginCheck_Ugly_MissingSeam(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	r.Host = "["
	r.Header.Set("Origin", "http://example.com")
	if sameOriginCheck(r) {
		t.Errorf("expected false")
	}
}

func TestWs_safeOriginCheck_Good(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)

	called := false
	if !(safeOriginCheck(func(req *http.Request) bool {
		called = true
		if !testSame(r, req) {
			t.Errorf("expected same reference")
		}
		return true
	}, r)) {
		t.Errorf("expected true")
	}
	if !(called) {
		t.Errorf("expected true")
	}

}

func TestWs_safeOriginCheck_Bad(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	if safeOriginCheck(func(*http.Request) bool {
		return false
	}, r) {
		t.Errorf("expected false")
	}

}

func TestWs_safeOriginCheck_Ugly(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)

	var check func(*http.Request) bool
	if safeOriginCheck(check, r) {
		t.Errorf("expected false")
	}

}

func TestWs_safeAuthenticate_Good(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := safeAuthenticate(AuthenticatorFunc(func(*http.Request) AuthResult {
		return AuthResult{Authenticated: true, UserID: "user-123"}
	}), r)
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("user-123", result.UserID) {
		t.Errorf("expected %v, got %v", "user-123", result.UserID)
	}

}

func TestWs_safeAuthenticate_Bad(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := safeAuthenticate(AuthenticatorFunc(func(*http.Request) AuthResult {
		return AuthResult{Valid: false, Error: core.NewError("denied")}
	}), r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if err := result.Error; err == nil || err.Error() != "denied" {
		t.Errorf("expected error %q, got %v", "denied", err)
	}

}

func TestWs_safeAuthenticate_Ugly(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := safeAuthenticate(AuthenticatorFunc(func(*http.Request) AuthResult {
		panic("boom")
	}), r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if result.Authenticated {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "authenticator panicked") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "authenticator panicked")
	}

}

func TestWs_splitHostAndPort_Good(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		scheme string
		wantH  string
		wantP  string
	}{
		{name: "host and port", host: "example.com:8080", scheme: "http", wantH: "example.com", wantP: "8080"},
		{name: "bare host uses http default port", host: "example.com", scheme: "http", wantH: "example.com", wantP: "80"},
		{name: "ipv6 host uses wss default port", host: "[2001:db8::1]", scheme: "wss", wantH: "2001:db8::1", wantP: "443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, ok := splitHostAndPort(tt.host, tt.scheme)
			if !(ok) {
				t.Fatalf("expected true")
			}
			if !testEqual(tt.wantH, host) {
				t.Errorf("expected %v, got %v", tt.wantH, host)
			}
			if !testEqual(tt.wantP, port) {
				t.Errorf("expected %v, got %v", tt.wantP, port)
			}

		})
	}
}

func TestWs_splitHostAndPort_Bad(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{name: "empty host", host: ""},
		{name: "bare colon", host: ":"},
		{name: "unbracketed ipv6 with port", host: "2001:db8::1:443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := splitHostAndPort(tt.host, "http")
			if ok {
				t.Errorf("expected false")
			}

		})
	}
}

func TestWs_splitHostAndPort_Ugly(t *testing.T) {
	host, port, ok := splitHostAndPort(" [::1] ", "https")
	if !(ok) {
		t.Fatalf("expected true")
	}
	if !testEqual("::1", host) {
		t.Errorf("expected %v, got %v", "::1", host)
	}
	if !testEqual("443", port) {
		t.Errorf("expected %v, got %v", "443", port)
	}

	host, port, ok = splitHostAndPort("example.com", "  ")
	if !(ok) {
		t.Fatalf("expected true")
	}
	if !testEqual("example.com", host) {
		t.Errorf("expected %v, got %v", "example.com", host)
	}
	if !testEqual("80", port) {
		t.Errorf("expected %v, got %v", "80", port)
	}

}

func TestWs_splitHostAndPort_Ugly_EmptyBrackets(t *testing.T) {
	_, _, ok := splitHostAndPort("[]", "https")
	if ok {
		t.Errorf("expected false")
	}

}

func TestWsNilHubReceiversEdges(t *testing.T) {
	var hub *Hub
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}
	if !testEqual(0, hub.ChannelCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
	}
	if !testEqual(0, hub.ChannelSubscriberCount("notifications")) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("notifications"))
	}
	if !testIsEmpty(slices.Collect(hub.AllClients())) {
		t.Errorf("expected empty value, got %v", slices.Collect(hub.AllClients()))
	}
	if !testIsEmpty(slices.Collect(hub.AllChannels())) {
		t.Errorf("expected empty value, got %v", slices.Collect(hub.AllChannels()))
	}
	if !testEqual(HubStats{}, hub.Stats()) {
		t.Errorf("expected %v, got %v", HubStats{}, hub.Stats())
	}
	if hub.isRunning() {
		t.Errorf("expected false")
	}

}

func TestWs_defaultPortForScheme_Good(t *testing.T) {
	if !testEqual("443", defaultPortForScheme("https")) {
		t.Errorf("expected %v, got %v", "443", defaultPortForScheme("https"))
	}
	if !testEqual("443", defaultPortForScheme("wss")) {
		t.Errorf("expected %v, got %v", "443", defaultPortForScheme("wss"))
	}

}

func TestWs_defaultPortForScheme_Bad(t *testing.T) {
	if !testEqual("80", defaultPortForScheme("http")) {
		t.Errorf("expected %v, got %v", "80", defaultPortForScheme("http"))
	}
	if !testEqual("80", defaultPortForScheme("ws")) {
		t.Errorf("expected %v, got %v", "80", defaultPortForScheme("ws"))
	}

}

func TestWs_defaultPortForScheme_Ugly(t *testing.T) {
	if !testEqual("443", defaultPortForScheme(" HTTPS ")) {
		t.Errorf("expected %v, got %v", "443", defaultPortForScheme(" HTTPS "))
	}
	if !testEqual("80", defaultPortForScheme("")) {
		t.Errorf("expected %v, got %v", "80", defaultPortForScheme(""))
	}

}

func TestWsClientCloseCovers(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		subscriptions: map[string]bool{"alpha": true},
		send:          make(chan []byte, 1),
	}

	hub.mu.Lock()
	hub.clients[client] = true
	hub.channels["alpha"] = map[*Client]bool{client: true}
	hub.mu.Unlock()
	if err := testResultError(client.Close()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}
	if !testEqual(0, hub.ChannelCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
	}
	if client.subscriptions["alpha"] {
		t.Errorf("expected false")
	}

}

func TestWsClientCloseRejects(t *testing.T) {
	hub := NewHub()
	var called bool
	hub.config.OnDisconnect = func(*Client) {
		called = true
	}

	client := &Client{
		hub:           hub,
		subscriptions: map[string]bool{"alpha": true},
	}

	hub.mu.Lock()
	hub.clients[client] = true
	hub.channels["alpha"] = map[*Client]bool{client: true}
	hub.mu.Unlock()
	if err := testResultError(client.Close()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !(called) {
		t.Errorf("expected true")
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}
	if !testEqual(0, hub.ChannelCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
	}

}

func TestWsClientCloseEdges(t *testing.T) {
	var client *Client
	if err := testResultError(client.Close()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	client = &Client{}
	if err := testResultError(client.Close()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestWs_Broadcast_Good(t *testing.T) {
	hub := NewHub()
	err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "broadcast"}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case raw := <-hub.broadcast:
		var received Message
		if !(core.JSONUnmarshal(raw, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeEvent, received.Type) {
			t.Errorf("expected %v, got %v", TypeEvent, received.Type)
		}
		if !testEqual("broadcast", received.Data) {
			t.Errorf("expected %v, got %v", "broadcast", received.Data)
		}
		if received.Timestamp.IsZero() {
			t.Errorf("expected false")
		}

	case <-time.After(time.Second):
		t.Fatal("broadcast should be queued")
	}
}

func TestWs_Broadcast_Bad(t *testing.T) {
	var hub *Hub

	err := testResultError(hub.Broadcast(Message{Type: TypeEvent}))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestWs_SendToChannel_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 1),
		subscriptions: make(map[string]bool),
	}
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err := testResultError(hub.SendToChannel("alpha", Message{Type: TypeEvent, Data: "payload"}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case raw := <-client.send:
		var received Message
		if !(core.JSONUnmarshal(raw, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("alpha", received.Channel) {
			t.Errorf("expected %v, got %v", "alpha", received.Channel)
		}
		if !testEqual(TypeEvent, received.Type) {
			t.Errorf("expected %v, got %v", TypeEvent, received.Type)
		}
		if !testEqual("payload", received.Data) {
			t.Errorf("expected %v, got %v", "payload", received.Data)
		}
		if received.Timestamp.IsZero() {
			t.Errorf("expected false")
		}

	case <-time.After(time.Second):
		t.Fatal("channel message should be queued")
	}
}

func TestWs_sendToChannelMessage_PreserveTimestamp_Good(t *testing.T) {
	hub := NewHub()
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 1),
		subscriptions: make(map[string]bool),
	}
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	timestamp := time.Date(2026, time.March, 19, 12, 0, 0, 0, time.UTC)
	err := testResultError(hub.sendToChannelMessage("alpha", Message{Type: TypeEvent,
		Data:      "payload",
		Timestamp: timestamp,
	}, true))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case raw := <-client.send:
		var received Message
		if !(core.JSONUnmarshal(raw, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(timestamp, received.Timestamp) {
			t.Errorf("expected %v, got %v", timestamp, received.Timestamp)
		}
		if !testEqual("alpha", received.Channel) {
			t.Errorf("expected %v, got %v", "alpha", received.Channel)
		}

	case <-time.After(time.Second):
		t.Fatal("channel message should be queued")
	}
}

func TestWs_broadcastMessage_PreserveTimestamp_Good(t *testing.T) {
	hub := NewHub()

	timestamp := time.Date(2026, time.March, 19, 13, 0, 0, 0, time.UTC)
	err := testResultError(hub.broadcastMessage(Message{Type: TypeEvent,
		Data:      "payload",
		Timestamp: timestamp,
	}, true))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case raw := <-hub.broadcast:
		var received Message
		if !(core.JSONUnmarshal(raw, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(timestamp, received.Timestamp) {
			t.Errorf("expected %v, got %v", timestamp, received.Timestamp)
		}

	case <-time.After(time.Second):
		t.Fatal("broadcast should be queued")
	}
}

func TestWs_SendToChannel_Bad(t *testing.T) {
	var hub *Hub

	err := testResultError(hub.SendToChannel("alpha", Message{Type: TypeEvent}))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestWsEnqueueUnregisterCovers(t *testing.T) {
	hub := &Hub{
		unregister: make(chan *Client, 1),
		done:       make(chan struct{}),
	}
	client := &Client{}

	hub.enqueueUnregister(client)

	select {
	case got := <-hub.unregister:
		if !testSame(client, got) {
			t.Errorf("expected same reference")
		}

	case <-time.After(time.Second):
		t.Fatal("expected client to be queued for unregister")
	}
}

func TestWsEnqueueUnregisterEdges(t *testing.T) {
	testNotPanics(t, func() {
		var hub *Hub
		hub.enqueueUnregister(nil)
	})

	// Missing seam: the closed-done branch in enqueueUnregister is
	// racey to assert without an injectable send primitive.
	t.Skip("missing seam: enqueueUnregister closed-done branch is not directly testable")
}

func TestWsHandleSubscribeRequestCovers(t *testing.T) {
	hub := NewHub()
	client := &Client{hub: hub, subscriptions: make(map[string]bool)}

	err := testResultError(hub.handleSubscribeRequest(subscriptionRequest{
		client:  client,
		channel: "alpha",
	}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !(client.subscriptions["alpha"]) {
		t.Errorf("expected true")
	}
	if !testEqual(1, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 1, hub.ChannelSubscriberCount("alpha"))
	}

}

func TestWsHandleSubscribeRequestEdges(t *testing.T) {
	hub := NewHub()

	err := testResultError(hub.handleSubscribeRequest(subscriptionRequest{}))
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual(0, hub.ChannelCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelCount())
	}

}

func TestWsHandleUnsubscribeRequestCovers(t *testing.T) {
	hub := NewHub()
	client := &Client{hub: hub, subscriptions: make(map[string]bool)}
	if err := testResultError(hub.Subscribe(client, "alpha")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	hub.handleUnsubscribeRequest(subscriptionRequest{
		client:  client,
		channel: "alpha",
	})
	if client.subscriptions["alpha"] {
		t.Errorf("expected false")
	}
	if !testEqual(0, hub.ChannelSubscriberCount("alpha")) {
		t.Errorf("expected %v, got %v", 0, hub.ChannelSubscriberCount("alpha"))
	}

}

func TestWsHandleUnsubscribeRequestEdges(t *testing.T) {
	hub := NewHub()
	testNotPanics(t, func() {
		hub.handleUnsubscribeRequest(subscriptionRequest{})
	})

}

func TestWs_Subscribe_Bad(t *testing.T) {
	hub := NewHub()
	client := &Client{hub: hub, subscriptions: make(map[string]bool)}
	hub.running = true
	close(hub.done)

	err := testResultError(hub.Subscribe(client, "alpha"))
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub is not running") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub is not running")
	}

}

func TestWs_Unsubscribe_Bad(t *testing.T) {
	hub := NewHub()
	client := &Client{hub: hub, subscriptions: make(map[string]bool)}
	hub.running = true
	close(hub.done)
	testNotPanics(t, func() {
		hub.Unsubscribe(client, "alpha")
	})

}

func TestWs_ClientClose_Good_ConnOnly(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, conn.Close)
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	client := &Client{conn: conn}
	if err := testResultError(client.Close()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("after-close")); err == nil {
		t.Fatalf("expected error")
	}

}

func TestWs_marshalClientMessage_Good(t *testing.T) {
	timestamp := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	data := marshalClientMessage(Message{
		Type:      TypeProcessStatus,
		Channel:   "alpha",
		ProcessID: "proc-1",
		Data:      map[string]any{"state": "done"},
		Timestamp: timestamp,
	})
	if testIsNil(data) {
		t.Fatalf("expected non-nil value")
	}

	var wire struct {
		Type      MessageType    `json:"type"`
		Channel   string         `json:"channel"`
		ProcessID string         `json:"processId"`
		Data      map[string]any `json:"data"`
		Timestamp time.Time      `json:"timestamp"`
	}
	if !(core.JSONUnmarshal(data, &wire).OK) {
		t.Fatalf("expected true")
	}
	if !testEqual(TypeProcessStatus, wire.Type) {
		t.Errorf("expected %v, got %v", TypeProcessStatus, wire.Type)
	}
	if !testEqual("alpha", wire.Channel) {
		t.Errorf("expected %v, got %v", "alpha", wire.Channel)
	}
	if !testEqual("proc-1", wire.ProcessID) {
		t.Errorf("expected %v, got %v", "proc-1", wire.ProcessID)
	}
	if !testEqual("done", wire.Data["state"]) {
		t.Errorf("expected %v, got %v", "done", wire.Data["state"])
	}
	if !testEqual(timestamp, wire.Timestamp) {
		t.Errorf("expected %v, got %v", timestamp, wire.Timestamp)
	}

}

func TestWs_marshalClientMessage_Bad(t *testing.T) {
	data := marshalClientMessage(Message{
		Type: TypeEvent,
		Data: make(chan int),
	})
	if !testIsNil(data) {
		t.Errorf("expected nil, got %T", data)
	}

}

func TestWs_dispatchReconnectMessage_Good_BlankFrames(t *testing.T) {
	seen := make([]Message, 0, 2)

	dispatchReconnectMessage(func(msg Message) {
		seen = append(seen, msg)
	}, []byte("\n{\"type\":\"event\",\"data\":\"alpha\"}\n\n{\"type\":\"error\",\"data\":\"beta\"}\n"))
	if gotLen := len(seen); gotLen != 2 {
		t.Fatalf("expected length %v, got %v", 2, gotLen)
	}
	if !testEqual(TypeEvent, seen[0].Type) {
		t.Errorf("expected %v, got %v", TypeEvent, seen[0].Type)
	}
	if !testEqual("alpha", seen[0].Data) {
		t.Errorf("expected %v, got %v", "alpha", seen[0].Data)
	}
	if !testEqual(TypeError, seen[1].Type) {
		t.Errorf("expected %v, got %v", TypeError, seen[1].Type)
	}
	if !testEqual("beta", seen[1].Data) {
		t.Errorf("expected %v, got %v", "beta", seen[1].Data)
	}

}

func TestWs_dispatchReconnectMessage_Ugly_NilCallbacks(t *testing.T) {
	testNotPanics(t, func() {
		var raw func([]byte)
		var msgFn func(Message)
		var stringFn func(string)
		dispatchReconnectMessage(raw, []byte("payload"))
		dispatchReconnectMessage(msgFn, []byte("{\"type\":\"event\"}"))
		dispatchReconnectMessage(stringFn, []byte("payload"))
	})

}

// --- v0.9.0 public symbol triplets ---

func TestWs_DefaultHubConfig_Good(t *T) {
	cfg := DefaultHubConfig()
	AssertEqual(t, DefaultHeartbeatInterval, cfg.HeartbeatInterval)
	AssertEqual(t, DefaultPongTimeout, cfg.PongTimeout)
	AssertEqual(t, DefaultWriteTimeout, cfg.WriteTimeout)
	AssertEqual(t, DefaultMaxSubscriptionsPerClient, cfg.MaxSubscriptionsPerClient)
}

func TestWs_DefaultHubConfig_Bad(t *T) {
	cfg := DefaultHubConfig()
	AssertNil(t, cfg.Authenticator)
	AssertNil(t, cfg.ChannelAuthoriser)
	AssertNil(t, cfg.CheckOrigin)
}

func TestWs_DefaultHubConfig_Ugly(t *T) {
	cfg := DefaultHubConfig()
	cfg.AllowedOrigins = append(cfg.AllowedOrigins, "https://app.example")
	again := DefaultHubConfig()
	AssertEmpty(t, again.AllowedOrigins)
}

func TestWs_NewHub_Good(t *T) {
	hub := NewHub()
	AssertNotNil(t, hub)
	AssertNotNil(t, hub.clients)
	AssertNotNil(t, hub.broadcast)
	AssertNotNil(t, hub.channels)
}

func TestWs_NewHub_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
	AssertEqual(t, DefaultPongTimeout, hub.config.PongTimeout)
	AssertEqual(t, DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient)
}

func TestWs_NewHub_Ugly(t *T) {
	hub := NewHub()
	req := NewHTTPTestRequest("GET", "http://evil.example/ws", nil)
	req.Header.Set("Origin", "https://evil.example")
	AssertTrue(t, hub.config.CheckOrigin(req))
}

func TestWs_Hub_Run_Good(t *T) {
	hub := NewHub()
	ctx, cancel := WithCancel(Background())
	go hub.Run(ctx)
	RequireTrue(t, complianceEventually(func() bool { return hub.isRunning() }))
	cancel()
	AssertTrue(t, complianceEventually(func() bool { return !hub.isRunning() }))
}

func TestWs_Hub_Run_Bad(t *T) {
	var hub *Hub
	AssertNotPanics(t, func() {
		hub.Run(Background())
	})
	AssertFalse(t, hub.isRunning())
}

func TestWs_Hub_Run_Ugly(t *T) {
	hub := NewHub()
	ctx, cancel := WithCancel(Background())
	cancel()
	hub.Run(ctx)
	AssertFalse(t, hub.isRunning())
}

func TestWs_Hub_Subscribe_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	err := testResultError(hub.Subscribe(client, "agent.dispatch"))
	AssertNoError(t, err)
	AssertEqual(t, 1, hub.ChannelSubscriberCount("agent.dispatch"))
}

func TestWs_Hub_Subscribe_Bad(t *T) {
	hub := NewHub()
	client := complianceClient()
	err := testResultError(hub.Subscribe(client, " agent.dispatch"))
	AssertError(t, err, "invalid channel")
	AssertEmpty(t, client.Subscriptions())
}

func TestWs_Hub_Subscribe_Ugly(t *T) {
	var hub *Hub
	client := complianceClient()
	err := testResultError(hub.Subscribe(client, "agent.dispatch"))
	AssertError(t, err, "hub must not be nil")
	AssertEmpty(t, client.Subscriptions())
}

func TestWs_Hub_Unsubscribe_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	RequireNoError(t, hub.Subscribe(client, "agent.dispatch"))
	hub.Unsubscribe(client, "agent.dispatch")
	AssertEqual(t, 0, hub.ChannelSubscriberCount("agent.dispatch"))
}

func TestWs_Hub_Unsubscribe_Bad(t *T) {
	hub := NewHub()
	client := complianceClient()
	RequireNoError(t, hub.Subscribe(client, "agent.dispatch"))
	hub.Unsubscribe(client, " agent.dispatch")
	AssertEqual(t, 1, hub.ChannelSubscriberCount("agent.dispatch"))
}

func TestWs_Hub_Unsubscribe_Ugly(t *T) {
	var hub *Hub
	client := complianceClient()
	AssertNotPanics(t, func() {
		hub.Unsubscribe(client, "agent.dispatch")
	})
	AssertEmpty(t, client.Subscriptions())
}

func TestWs_Hub_Broadcast_Good(t *T) {
	hub := NewHub()
	err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "ready"}))
	AssertNoError(t, err)
	msg := complianceBroadcastMessage(t, hub)
	AssertEqual(t, TypeEvent, msg.Type)
	AssertFalse(t, msg.Timestamp.IsZero())
}

func TestWs_Hub_Broadcast_Bad(t *T) {
	hub := NewHub()
	err := testResultError(hub.Broadcast(Message{Type: TypeProcessOutput, ProcessID: "bad:id"}))
	AssertError(t, err, "invalid process ID")
	AssertEqual(t, 0, len(hub.broadcast))
}

func TestWs_Hub_Broadcast_Ugly(t *T) {
	var hub *Hub
	err := testResultError(hub.Broadcast(Message{Type: TypeEvent, Data: "ready"}))
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, hub)
}

func TestWs_Hub_SendToChannel_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	RequireNoError(t, hub.Subscribe(client, "agent.dispatch"))
	err := testResultError(hub.SendToChannel("agent.dispatch", Message{Type: TypeEvent, Data: "queued"}))
	AssertNoError(t, err)
	AssertEqual(t, "agent.dispatch", complianceClientMessage(t, client).Channel)
}

func TestWs_Hub_SendToChannel_Bad(t *T) {
	hub := NewHub()
	err := testResultError(hub.SendToChannel(" agent.dispatch", Message{Type: TypeEvent}))
	AssertError(t, err, "invalid channel")
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestWs_Hub_SendToChannel_Ugly(t *T) {
	hub := NewHub()
	err := testResultError(hub.SendToChannel("agent.dispatch", Message{Type: TypeEvent, Data: "nobody"}))
	AssertNoError(t, err)
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestWs_Hub_SendProcessOutput_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	RequireNoError(t, hub.Subscribe(client, "process:proc-1"))
	RequireNoError(t, hub.SendProcessOutput("proc-1", "line"))
	AssertEqual(t, TypeProcessOutput, complianceClientMessage(t, client).Type)
}

func TestWs_Hub_SendProcessOutput_Bad(t *T) {
	hub := NewHub()
	err := testResultError(hub.SendProcessOutput("bad:id", "line"))
	AssertError(t, err, "invalid process ID")
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestWs_Hub_SendProcessOutput_Ugly(t *T) {
	hub := NewHub()
	err := testResultError(hub.SendProcessOutput("proc-1", ""))
	AssertNoError(t, err)
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestWs_Hub_SendProcessStatus_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	RequireNoError(t, hub.Subscribe(client, "process:proc-1"))
	RequireNoError(t, hub.SendProcessStatus("proc-1", "running", 0))
	AssertEqual(t, TypeProcessStatus, complianceClientMessage(t, client).Type)
}

func TestWs_Hub_SendProcessStatus_Bad(t *T) {
	hub := NewHub()
	err := testResultError(hub.SendProcessStatus("bad:id", "failed", 1))
	AssertError(t, err, "invalid process ID")
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestWs_Hub_SendProcessStatus_Ugly(t *T) {
	hub := NewHub()
	err := testResultError(hub.SendProcessStatus("proc-1", "", -1))
	AssertNoError(t, err)
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestWs_Hub_SendError_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendError("server refused"))
	msg := complianceBroadcastMessage(t, hub)
	AssertEqual(t, TypeError, msg.Type)
	AssertEqual(t, "server refused", msg.Data)
}

func TestWs_Hub_SendError_Bad(t *T) {
	var hub *Hub
	err := testResultError(hub.SendError("server refused"))
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, hub)
}

func TestWs_Hub_SendError_Ugly(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendError(""))
	msg := complianceBroadcastMessage(t, hub)
	AssertEqual(t, "", msg.Data)
}

func TestWs_Hub_SendEvent_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendEvent("agent.ready", "payload"))
	msg := complianceBroadcastMessage(t, hub)
	AssertEqual(t, TypeEvent, msg.Type)
	AssertContains(t, msg.Data.(map[string]any), "event")
}

func TestWs_Hub_SendEvent_Bad(t *T) {
	var hub *Hub
	err := testResultError(hub.SendEvent("agent.ready", "payload"))
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, hub)
}

func TestWs_Hub_SendEvent_Ugly(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendEvent("", nil))
	msg := complianceBroadcastMessage(t, hub)
	AssertEqual(t, TypeEvent, msg.Type)
	AssertContains(t, msg.Data.(map[string]any), "data")
}

func TestWs_Hub_ClientCount_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	hub.clients[client] = true
	AssertEqual(t, 1, hub.ClientCount())
}

func TestWs_Hub_ClientCount_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, 0, hub.ClientCount())
	AssertNotNil(t, hub.clients)
}

func TestWs_Hub_ClientCount_Ugly(t *T) {
	var hub *Hub
	AssertEqual(t, 0, hub.ClientCount())
	AssertNil(t, hub)
}

func TestWs_Hub_ChannelCount_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.Subscribe(complianceClient(), "alpha"))
	RequireNoError(t, hub.Subscribe(complianceClient(), "beta"))
	AssertEqual(t, 2, hub.ChannelCount())
}

func TestWs_Hub_ChannelCount_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, 0, hub.ChannelCount())
	AssertNotNil(t, hub.channels)
}

func TestWs_Hub_ChannelCount_Ugly(t *T) {
	var hub *Hub
	AssertEqual(t, 0, hub.ChannelCount())
	AssertNil(t, hub)
}

func TestWs_Hub_ChannelSubscriberCount_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.Subscribe(complianceClient(), "alpha"))
	RequireNoError(t, hub.Subscribe(complianceClient(), "alpha"))
	AssertEqual(t, 2, hub.ChannelSubscriberCount("alpha"))
}

func TestWs_Hub_ChannelSubscriberCount_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, 0, hub.ChannelSubscriberCount("missing"))
	AssertNotNil(t, hub.channels)
}

func TestWs_Hub_ChannelSubscriberCount_Ugly(t *T) {
	var hub *Hub
	AssertEqual(t, 0, hub.ChannelSubscriberCount("alpha"))
	AssertNil(t, hub)
}

func TestWs_Hub_AllClients_Good(t *T) {
	hub := NewHub()
	hub.clients[&Client{UserID: "b"}] = true
	hub.clients[&Client{UserID: "a"}] = true
	var ids []string
	for client := range hub.AllClients() {
		ids = append(ids, client.UserID)
	}
	AssertEqual(t, []string{"a", "b"}, ids)
}

func TestWs_Hub_AllClients_Bad(t *T) {
	hub := NewHub()
	var clients []*Client
	for client := range hub.AllClients() {
		clients = append(clients, client)
	}
	AssertEmpty(t, clients)
}

func TestWs_Hub_AllClients_Ugly(t *T) {
	var hub *Hub
	var clients []*Client
	for client := range hub.AllClients() {
		clients = append(clients, client)
	}
	AssertEmpty(t, clients)
}

func TestWs_Hub_AllChannels_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.Subscribe(complianceClient(), "beta"))
	RequireNoError(t, hub.Subscribe(complianceClient(), "alpha"))
	var channels []string
	for channel := range hub.AllChannels() {
		channels = append(channels, channel)
	}
	AssertEqual(t, []string{"alpha", "beta"}, channels)
}

func TestWs_Hub_AllChannels_Bad(t *T) {
	hub := NewHub()
	var channels []string
	for channel := range hub.AllChannels() {
		channels = append(channels, channel)
	}
	AssertEmpty(t, channels)
}

func TestWs_Hub_AllChannels_Ugly(t *T) {
	var hub *Hub
	var channels []string
	for channel := range hub.AllChannels() {
		channels = append(channels, channel)
	}
	AssertEmpty(t, channels)
}

func TestWs_Hub_Stats_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	hub.clients[client] = true
	RequireNoError(t, hub.Subscribe(client, "alpha"))
	stats := hub.Stats()
	AssertEqual(t, 1, stats.Clients)
	AssertEqual(t, 1, stats.Channels)
	AssertEqual(t, 1, stats.Subscribers)
}

func TestWs_Hub_Stats_Bad(t *T) {
	hub := NewHub()
	stats := hub.Stats()
	AssertEqual(t, HubStats{}, stats)
	AssertEqual(t, 0, stats.Subscribers)
}

func TestWs_Hub_Stats_Ugly(t *T) {
	var hub *Hub
	stats := hub.Stats()
	AssertEqual(t, HubStats{}, stats)
	AssertNil(t, hub)
}

func TestWs_Hub_Handler_Good(t *T) {
	hub, _ := complianceStartHub(t)
	server := NewHTTPTestServer(hub.Handler())
	t.Cleanup(server.Close)
	resp := HTTPGet(server.URL)
	RequireTrue(t, resp.OK)
	AssertEqual(t, 400, resp.Value.(*Response).StatusCode)
	AssertNoError(t, resp.Value.(*Response).Body.Close())
}

func TestWs_Hub_Handler_Bad(t *T) {
	var hub *Hub
	handler := hub.Handler()
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "/ws", nil)
	handler(rec, req)
	AssertEqual(t, 503, rec.Code)
	AssertContains(t, rec.Body.String(), "Hub is not configured")
}

func TestWs_Hub_Handler_Ugly(t *T) {
	hub, _ := complianceStartHub(t)
	hub.config.CheckOrigin = func(*Request) bool { panic("origin panic") }
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "http://example.com/ws", nil)
	hub.Handler()(rec, req)
	AssertEqual(t, 403, rec.Code)
}

func TestWs_Hub_HandleWebSocket_Good(t *T) {
	hub, _ := complianceStartHub(t)
	server := NewHTTPTestServer(HandlerFunc(hub.HandleWebSocket))
	t.Cleanup(server.Close)
	resp := HTTPGet(server.URL)
	RequireTrue(t, resp.OK)
	AssertEqual(t, 400, resp.Value.(*Response).StatusCode)
	AssertNoError(t, resp.Value.(*Response).Body.Close())
}

func TestWs_Hub_HandleWebSocket_Bad(t *T) {
	var hub *Hub
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "/ws", nil)
	hub.HandleWebSocket(rec, req)
	AssertEqual(t, 503, rec.Code)
	AssertContains(t, rec.Body.String(), "Hub is not configured")
}

func TestWs_Hub_HandleWebSocket_Ugly(t *T) {
	hub, _ := complianceStartHub(t)
	hub.config.CheckOrigin = func(*Request) bool { return false }
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "http://example.com/ws", nil)
	hub.HandleWebSocket(rec, req)
	AssertEqual(t, 403, rec.Code)
}

func TestWs_Client_Subscriptions_Good(t *T) {
	client := complianceClient()
	client.subscriptions["beta"] = true
	client.subscriptions["alpha"] = true
	AssertEqual(t, []string{"alpha", "beta"}, client.Subscriptions())
}

func TestWs_Client_Subscriptions_Bad(t *T) {
	var client *Client
	subscriptions := client.Subscriptions()
	AssertNil(t, subscriptions)
	AssertEmpty(t, subscriptions)
}

func TestWs_Client_Subscriptions_Ugly(t *T) {
	client := complianceClient()
	client.subscriptions["alpha"] = true
	snapshot := client.Subscriptions()
	snapshot[0] = "mutated"
	AssertEqual(t, []string{"alpha"}, client.Subscriptions())
}

func TestWs_Client_AllSubscriptions_Good(t *T) {
	client := complianceClient()
	client.subscriptions["beta"] = true
	client.subscriptions["alpha"] = true
	var subscriptions []string
	for channel := range client.AllSubscriptions() {
		subscriptions = append(subscriptions, channel)
	}
	AssertEqual(t, []string{"alpha", "beta"}, subscriptions)
}

func TestWs_Client_AllSubscriptions_Bad(t *T) {
	client := complianceClient()
	var subscriptions []string
	for channel := range client.AllSubscriptions() {
		subscriptions = append(subscriptions, channel)
	}
	AssertEmpty(t, subscriptions)
}

func TestWs_Client_AllSubscriptions_Ugly(t *T) {
	var client *Client
	var subscriptions []string
	for channel := range client.AllSubscriptions() {
		subscriptions = append(subscriptions, channel)
	}
	AssertEmpty(t, subscriptions)
}

func TestWs_Client_Close_Good(t *T) {
	hub := NewHub()
	client := complianceClient()
	client.hub = hub
	hub.clients[client] = true
	RequireNoError(t, hub.Subscribe(client, "alpha"))
	AssertNoError(t, client.Close())
	AssertEqual(t, 0, hub.ClientCount())
	AssertEmpty(t, client.Subscriptions())
}

func TestWs_Client_Close_Bad(t *T) {
	var client *Client
	err := testResultError(client.Close())
	AssertNoError(t, err)
	AssertNil(t, client)
}

func TestWs_Client_Close_Ugly(t *T) {
	hub, _ := complianceStartHub(t)
	client := complianceClient()
	client.hub = hub
	hub.register <- client
	RequireTrue(t, complianceEventually(func() bool { return hub.ClientCount() == 1 }))
	AssertNoError(t, client.Close())
	AssertTrue(t, complianceEventually(func() bool { return hub.ClientCount() == 0 }))
}

func TestWs_NewReconnectingClient_Good(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{URL: "ws://example.invalid/ws"})
	AssertEqual(t, StateDisconnected, rc.State())
	AssertEqual(t, Second, rc.config.InitialBackoff)
	AssertEqual(t, 30*Second, rc.config.MaxBackoff)
	AssertNotNil(t, rc.config.Dialer)
}

func TestWs_NewReconnectingClient_Bad(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{InitialBackoff: 10 * Millisecond, MaxBackoff: 5 * Millisecond})
	AssertEqual(t, 5*Millisecond, rc.config.InitialBackoff)
	AssertEqual(t, 5*Millisecond, rc.config.MaxBackoff)
	AssertEqual(t, 2.0, rc.config.BackoffMultiplier)
}

func TestWs_NewReconnectingClient_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{BackoffMultiplier: -4, InitialBackoff: -1, MaxBackoff: -1})
	AssertEqual(t, Second, rc.config.InitialBackoff)
	AssertEqual(t, 30*Second, rc.config.MaxBackoff)
	AssertEqual(t, 2.0, rc.config.BackoffMultiplier)
}

func TestWs_ReconnectingClient_Connect_Good(t *T) {
	_, server := complianceStartWSServer(t, HubConfig{})
	rc := NewReconnectingClient(ReconnectConfig{URL: complianceWSURL(server)})
	done := make(chan error, 1)
	go func() { done <- rc.Connect(Background()) }()
	RequireTrue(t, complianceEventually(func() bool { return rc.State() == StateConnected }))
	AssertNoError(t, rc.Close())
	timeout, cancel := WithTimeout(Background(), Second)
	defer cancel()
	select {
	case err := <-done:
		if err != nil {
			AssertContains(t, err.Error(), "context canceled")
		}
	case <-timeout.Done():
		t.Fatal("timed out waiting for reconnecting client shutdown")
	}
}

func TestWs_ReconnectingClient_Connect_Bad(t *T) {
	var rc *ReconnectingClient
	err := testResultError(rc.Connect(Background()))
	AssertError(t, err, "client must not be nil")
	AssertNil(t, rc)
}

func TestWs_ReconnectingClient_Connect_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:                  "ws://127.0.0.1:1/ws",
		InitialBackoff:       Millisecond,
		MaxBackoff:           Millisecond,
		MaxReconnectAttempts: 1,
	})
	err := testResultError(rc.Connect(Background()))
	AssertError(t, err, "max retries")
	AssertEqual(t, StateDisconnected, rc.State())
}

func TestWs_ReconnectingClient_Send_Good(t *T) {
	_, server := complianceStartWSServer(t, HubConfig{})
	rc := NewReconnectingClient(ReconnectConfig{URL: complianceWSURL(server)})
	done := make(chan error, 1)
	go func() { done <- rc.Connect(Background()) }()
	RequireTrue(t, complianceEventually(func() bool { return rc.State() == StateConnected }))
	AssertNoError(t, rc.Send(Message{Type: TypePing}))
	AssertNoError(t, rc.Close())
	<-done
}

func TestWs_ReconnectingClient_Send_Bad(t *T) {
	var rc *ReconnectingClient
	err := testResultError(rc.Send(Message{Type: TypePing}))
	AssertError(t, err, "client must not be nil")
	AssertNil(t, rc)
}

func TestWs_ReconnectingClient_Send_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{URL: "ws://example.invalid/ws"})
	err := testResultError(rc.Send(Message{Type: TypePing}))
	AssertError(t, err, "not connected")
	AssertEqual(t, StateDisconnected, rc.State())
}

func TestWs_ReconnectingClient_State_Good(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	rc.setState(StateConnecting)
	AssertEqual(t, StateConnecting, rc.State())
}

func TestWs_ReconnectingClient_State_Bad(t *T) {
	var rc *ReconnectingClient
	state := rc.State()
	AssertEqual(t, StateDisconnected, state)
	AssertNil(t, rc)
}

func TestWs_ReconnectingClient_State_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	rc.setState(ConnectionState(99))
	AssertEqual(t, ConnectionState(99), rc.State())
}

func TestWs_ReconnectingClient_Close_Good(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	rc.setState(StateConnected)
	AssertNoError(t, rc.Close())
	AssertEqual(t, StateDisconnected, rc.State())
}

func TestWs_ReconnectingClient_Close_Bad(t *T) {
	var rc *ReconnectingClient
	err := testResultError(rc.Close())
	AssertNoError(t, err)
	AssertNil(t, rc)
}

func TestWs_ReconnectingClient_Close_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	AssertNoError(t, rc.Close())
	AssertNoError(t, rc.Close())
	AssertEqual(t, StateDisconnected, rc.State())
}
