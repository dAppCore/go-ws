// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"net/http/httptest"
	// Note: AX-6 — internal concurrency primitive; structural for go-ws hub state (RFC mandates concurrent connection map).
	"sync"
	"testing"

	core "dappco.re/go/core"
	"github.com/gorilla/websocket"
)

// BenchmarkBroadcast_100 measures broadcast throughput with 100 connected clients.
// Uses b.Loop() (Go 1.25+) and b.ReportAllocs() for accurate profiling.
func BenchmarkBroadcast_100(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	numClients := 100
	clients := make([]*Client, numClients)
	for i := range numClients {
		clients[i] = &Client{
			hub:           hub,
			send:          make(chan []byte, 4096),
			subscriptions: make(map[string]bool),
		}
		hub.register <- clients[i]
	}
	for hub.ClientCount() < numClients {
	}

	msg := Message{Type: TypeEvent, Data: "bench"}

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_ = hub.Broadcast(msg)
	}

	b.StopTimer()
	for _, c := range clients {
		for len(c.send) > 0 {
			<-c.send
		}
	}
}

// BenchmarkSendToChannel_50 measures channel-targeted delivery with 50 subscribers.
func BenchmarkSendToChannel_50(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	numSubscribers := 50
	for range numSubscribers {
		client := &Client{
			hub:           hub,
			send:          make(chan []byte, 4096),
			subscriptions: make(map[string]bool),
		}
		hub.mu.Lock()
		hub.clients[client] = true
		hub.mu.Unlock()
		_ = hub.Subscribe(client, "bench-channel")
	}

	msg := Message{Type: TypeEvent, Data: "bench-chan"}

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_ = hub.SendToChannel("bench-channel", msg)
	}
}

// BenchmarkBroadcast_Parallel measures concurrent broadcast throughput.
func BenchmarkBroadcast_Parallel(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	numClients := 100
	clients := make([]*Client, numClients)
	for i := range numClients {
		clients[i] = &Client{
			hub:           hub,
			send:          make(chan []byte, 8192),
			subscriptions: make(map[string]bool),
		}
		hub.register <- clients[i]
	}
	for hub.ClientCount() < numClients {
	}

	msg := Message{Type: TypeEvent, Data: "parallel-bench"}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = hub.Broadcast(msg)
		}
	})
}

// BenchmarkMarshalMessage measures the cost of JSON message serialisation.
func BenchmarkMarshalMessage(b *testing.B) {
	msg := Message{
		Type:      TypeProcessOutput,
		Channel:   "process:bench-1",
		ProcessID: "bench-1",
		Data:      "output line from the build process",
	}

	b.ReportAllocs()

	for b.Loop() {
		r := core.JSONMarshal(msg)
		_ = r
	}
}

// BenchmarkWebSocketEndToEnd measures a full round-trip through a real
// WebSocket connection: server broadcasts, client receives.
func BenchmarkWebSocketEndToEnd(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	defer server.Close()

	url := "ws" + server.URL[4:] // http -> ws
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		b.Fatalf("dial failed: %v", err)
	}
	defer testClose(b, conn.Close)

	for hub.ClientCount() < 1 {
	}

	msg := Message{Type: TypeEvent, Data: "e2e-bench"}

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if err := hub.Broadcast(msg); err != nil {
			b.Fatalf("broadcast: %v", err)
		}
		_, _, err := conn.ReadMessage()
		if err != nil {
			b.Fatalf("read: %v", err)
		}
	}
}

// BenchmarkSubscribeUnsubscribe measures subscribe/unsubscribe cycle throughput.
func BenchmarkSubscribeUnsubscribe(b *testing.B) {
	hub := NewHub()

	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()

	b.ReportAllocs()

	for b.Loop() {
		_ = hub.Subscribe(client, "bench-sub")
		hub.Unsubscribe(client, "bench-sub")
	}
}

// BenchmarkSendToChannel_Parallel measures concurrent channel sends.
func BenchmarkSendToChannel_Parallel(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	numSubscribers := 50
	clients := make([]*Client, numSubscribers)
	for i := range numSubscribers {
		clients[i] = &Client{
			hub:           hub,
			send:          make(chan []byte, 8192),
			subscriptions: make(map[string]bool),
		}
		hub.mu.Lock()
		hub.clients[clients[i]] = true
		hub.mu.Unlock()
		_ = hub.Subscribe(clients[i], "parallel-chan")
	}

	msg := Message{Type: TypeEvent, Data: "p-bench"}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = hub.SendToChannel("parallel-chan", msg)
		}
	})
}

// BenchmarkMultiChannelFanout measures broadcasting to multiple channels
// with different subscriber counts.
func BenchmarkMultiChannelFanout(b *testing.B) {
	hub := NewHub()
	ctx := b.Context()
	go hub.Run(ctx)

	numChannels := 10
	subsPerChannel := 10
	channels := make([]string, numChannels)

	for ch := range numChannels {
		channels[ch] = core.Sprintf("fanout-%d", ch)
		for range subsPerChannel {
			client := &Client{
				hub:           hub,
				send:          make(chan []byte, 4096),
				subscriptions: make(map[string]bool),
			}
			hub.mu.Lock()
			hub.clients[client] = true
			hub.mu.Unlock()
			_ = hub.Subscribe(client, channels[ch])
		}
	}

	msg := Message{Type: TypeEvent, Data: "fanout"}

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		for _, ch := range channels {
			_ = hub.SendToChannel(ch, msg)
		}
	}
}

// BenchmarkConcurrentSubscribers measures the cost of subscribing many
// clients concurrently to the same channel.
func BenchmarkConcurrentSubscribers(b *testing.B) {
	hub := NewHub()

	b.ReportAllocs()

	for b.Loop() {
		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() {
				client := &Client{
					hub:           hub,
					send:          make(chan []byte, 1),
					subscriptions: make(map[string]bool),
				}
				_ = hub.Subscribe(client, "conc-sub-bench")
			})
		}
		wg.Wait()

		// Reset for next iteration
		hub.mu.Lock()
		hub.channels = make(map[string]map[*Client]bool)
		hub.mu.Unlock()
	}
}
