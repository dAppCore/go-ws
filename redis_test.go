// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"crypto/tls"
	"strings"
	"sync"
	"testing"
	"time"

	core "dappco.re/go/core"
	"github.com/redis/go-redis/v9"
)

const redisAddr = "10.69.69.87:6379"

// skipIfNoRedis checks Redis availability and returns a connected client.
// Tests are skipped when Redis is unreachable.
func skipIfNoRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Redis not available:", err)
	}
	return client
}

// testPrefix returns a unique Redis key prefix per test to avoid
// collisions between parallel test runs.
func testPrefix(t *testing.T) string {
	t.Helper()
	return core.Sprintf("test_%d", time.Now().UnixNano())
}

// cleanupRedis removes all keys matching the given prefix pattern.
func cleanupRedis(t *testing.T, client *redis.Client, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		// PubSub channels are ephemeral — no keys to clean.
		// But flush any leftover data keys just in case.
		iter := client.Scan(ctx, 0, prefix+":*", 100).Iterator()
		for iter.Next(ctx) {
			client.Del(ctx, iter.Val())
		}
		client.Close()
	})
}

// startTestHub creates a Hub, starts it, and returns cleanup resources.
func startTestHub(t *testing.T) (*Hub, context.Context, context.CancelFunc) {
	t.Helper()
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	t.Cleanup(func() { cancel() })
	return hub, ctx, cancel
}

// ---------------------------------------------------------------------------
// Bridge lifecycle
// ---------------------------------------------------------------------------

func TestRedisBridge_CreateAndLifecycle(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{
		Addr:   redisAddr,
		Prefix: prefix,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bridge == nil {
		t.Fatal("expected non-nil")
	}
	if len(bridge.SourceID()) == 0 {
		t.Fatal("expected non-empty")
	}

	// Start the bridge.
	err = bridge.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Stop the bridge.
	err = bridge.Stop()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedisBridge_NilHub(t *testing.T) {
	skipIfNoRedis(t)

	_, err := NewRedisBridge(nil, RedisConfig{
		Addr: redisAddr,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "hub must not be nil") {
		t.Fatalf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}
}

func TestRedisBridge_EmptyAddr(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr: "",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "redis address must not be empty") {
		t.Fatalf("expected %v to contain %v", err.Error(), "redis address must not be empty")
	}
}

func TestRedisBridge_BadAddr(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr: "127.0.0.1:1", // Nothing listening here.
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "redis ping failed") {
		t.Fatalf("expected %v to contain %v", err.Error(), "redis ping failed")
	}
}

func TestRedisBridge_DefaultPrefix(t *testing.T) {
	rc := skipIfNoRedis(t)
	cleanupRedis(t, rc, "ws")

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{
		Addr: redisAddr,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if "ws" != bridge.prefix {
		t.Fatalf("want %v, got %v", "ws", bridge.prefix)
	}

	err = bridge.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge.Stop()
}

func TestRedisBridge_TLSConfig(t *testing.T) {
	tlsConfig := &tls.Config{
		ServerName: "redis.local",
	}

	options := newRedisOptions(RedisConfig{
		Addr:      "redis.example:6380",
		Password:  "secret",
		DB:        4,
		TLSConfig: tlsConfig,
	})
	if "redis.example:6380" != options.Addr {
		t.Fatalf("want %v, got %v", "redis.example:6380", options.Addr)
	}
	if "secret" != options.Password {
		t.Fatalf("want %v, got %v", "secret", options.Password)
	}
	if 4 != options.DB {
		t.Fatalf("want %v, got %v", 4, options.DB)
	}
	if tlsConfig != options.TLSConfig {
		t.Fatalf("want same %v, got %v", tlsConfig, options.TLSConfig)
	}
}

// ---------------------------------------------------------------------------
// PublishBroadcast — messages reach local WebSocket clients
// ---------------------------------------------------------------------------

func TestRedisBridge_PublishBroadcast(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	// Register a local client.
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub.register <- client
	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}

	// Create two bridges on same Redis — bridge1 publishes, bridge2 receives.
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge1.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge1.Stop()

	// A second hub + bridge to receive the cross-instance message.
	hub2, _, _ := startTestHub(t)
	client2 := &Client{
		hub:           hub2,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub2.register <- client2
	time.Sleep(50 * time.Millisecond)

	bridge2, err := NewRedisBridge(hub2, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge2.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge2.Stop()

	// Allow subscriptions to propagate.
	time.Sleep(100 * time.Millisecond)

	// Publish broadcast from bridge1.
	err = bridge1.PublishBroadcast(Message{Type: TypeEvent, Data: "cross-broadcast"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// bridge2's hub should receive the message (client2 gets it).
	select {
	case msg := <-client2.send:
		var received Message
		if !core.JSONUnmarshal(msg, &received).OK {
			t.Fatal("expected true")
		}
		if TypeEvent != received.Type {
			t.Fatalf("want %v, got %v", TypeEvent, received.Type)
		}
		if "cross-broadcast" != received.Data {
			t.Fatalf("want %v, got %v", "cross-broadcast", received.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge2 client should have received the broadcast")
	}
}

// ---------------------------------------------------------------------------
// PublishToChannel — targeted channel delivery
// ---------------------------------------------------------------------------

func TestRedisBridge_PublishToChannel(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	// Create a client subscribed to a specific channel.
	subClient := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub.register <- subClient
	time.Sleep(50 * time.Millisecond)
	hub.Subscribe(subClient, "process:abc")

	// Create a client NOT subscribed to that channel.
	otherClient := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub.register <- otherClient
	time.Sleep(50 * time.Millisecond)

	// Second hub + bridge (the publisher).
	hub2, _, _ := startTestHub(t)
	bridge2, err := NewRedisBridge(hub2, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge2.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge2.Stop()

	// Local hub bridge (the receiver).
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge1.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge1.Stop()

	time.Sleep(100 * time.Millisecond)

	// Publish to channel from bridge2.
	err = bridge2.PublishToChannel("process:abc", Message{
		Type:      TypeProcessOutput,
		ProcessID: "abc",
		Data:      "line of output",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// subClient (subscribed to process:abc) should receive the message.
	select {
	case msg := <-subClient.send:
		var received Message
		if !core.JSONUnmarshal(msg, &received).OK {
			t.Fatal("expected true")
		}
		if TypeProcessOutput != received.Type {
			t.Fatalf("want %v, got %v", TypeProcessOutput, received.Type)
		}
		if "line of output" != received.Data {
			t.Fatalf("want %v, got %v", "line of output", received.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscribed client should have received the channel message")
	}

	// otherClient should NOT receive the message.
	select {
	case msg := <-otherClient.send:
		t.Fatalf("unsubscribed client should not receive channel message, got: %s", msg)
	case <-time.After(300 * time.Millisecond):
		// Good — no message delivered.
	}
}

// ---------------------------------------------------------------------------
// Cross-bridge messaging
// ---------------------------------------------------------------------------

func TestRedisBridge_CrossBridge(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	// Hub A with a client.
	hubA, _, _ := startTestHub(t)
	clientA := &Client{
		hub:           hubA,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hubA.register <- clientA
	time.Sleep(50 * time.Millisecond)

	bridgeA, err := NewRedisBridge(hubA, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridgeA.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridgeA.Stop()

	// Hub B with a client.
	hubB, _, _ := startTestHub(t)
	clientB := &Client{
		hub:           hubB,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hubB.register <- clientB
	time.Sleep(50 * time.Millisecond)

	bridgeB, err := NewRedisBridge(hubB, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridgeB.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridgeB.Stop()

	// Allow subscriptions to settle.
	time.Sleep(200 * time.Millisecond)

	// Publish from A, verify B receives.
	err = bridgeA.PublishBroadcast(Message{Type: TypeEvent, Data: "from-A"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case msg := <-clientB.send:
		var received Message
		if !core.JSONUnmarshal(msg, &received).OK {
			t.Fatal("expected true")
		}
		if "from-A" != received.Data {
			t.Fatalf("want %v, got %v", "from-A", received.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hub B should receive broadcast from hub A")
	}

	// Publish from B, verify A receives.
	err = bridgeB.PublishBroadcast(Message{Type: TypeEvent, Data: "from-B"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case msg := <-clientA.send:
		var received Message
		if !core.JSONUnmarshal(msg, &received).OK {
			t.Fatal("expected true")
		}
		if "from-B" != received.Data {
			t.Fatalf("want %v, got %v", "from-B", received.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hub A should receive broadcast from hub B")
	}
}

// ---------------------------------------------------------------------------
// Loop prevention
// ---------------------------------------------------------------------------

func TestRedisBridge_LoopPrevention(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)
	client := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub.register <- client
	time.Sleep(50 * time.Millisecond)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge.Stop()

	time.Sleep(100 * time.Millisecond)

	// Publish from this bridge — the same bridge should NOT deliver
	// the message back to its own hub.
	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "echo-test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case msg := <-client.send:
		t.Fatalf("bridge should not echo its own messages, got: %s", msg)
	case <-time.After(500 * time.Millisecond):
		// Good — no echo.
	}
}

// ---------------------------------------------------------------------------
// Concurrent publishes
// ---------------------------------------------------------------------------

func TestRedisBridge_ConcurrentPublishes(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	// Receiver hub.
	hubRecv, _, _ := startTestHub(t)
	recvClient := &Client{
		hub:           hubRecv,
		send:          make(chan []byte, 1024),
		subscriptions: make(map[string]bool),
	}
	hubRecv.register <- recvClient
	time.Sleep(50 * time.Millisecond)

	bridgeRecv, err := NewRedisBridge(hubRecv, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridgeRecv.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridgeRecv.Stop()

	// Sender hub.
	hubSend, _, _ := startTestHub(t)
	bridgeSend, err := NewRedisBridge(hubSend, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridgeSend.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridgeSend.Stop()

	time.Sleep(200 * time.Millisecond)

	// Fire 10 concurrent broadcasts.
	numPublishes := 10
	var wg sync.WaitGroup
	for i := range numPublishes {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = bridgeSend.PublishBroadcast(Message{
				Type: TypeEvent,
				Data: core.Sprintf("concurrent-%d", idx),
			})
		}(i)
	}
	wg.Wait()

	// Collect received messages.
	received := 0
	timeout := time.After(5 * time.Second)
	for received < numPublishes {
		select {
		case <-recvClient.send:
			received++
		case <-timeout:
			t.Fatalf("expected %d messages, received %d", numPublishes, received)
		}
	}
	if numPublishes != received {
		t.Fatalf("want %v, got %v", numPublishes, received)
	}
}

// ---------------------------------------------------------------------------
// Graceful shutdown
// ---------------------------------------------------------------------------

func TestRedisBridge_GracefulShutdown(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Stop should not panic or hang.
	done := make(chan error, 1)
	go func() {
		done <- bridge.Stop()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() should not hang")
	}

	// Publishing after stop should fail gracefully (context cancelled).
	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "after-stop"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRedisBridge_StopWithoutStart(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Stop without Start should not panic.
	func() {
		_ = bridge.Stop()
	}()
}

// ---------------------------------------------------------------------------
// Context cancellation stops the bridge
// ---------------------------------------------------------------------------

func TestRedisBridge_ContextCancellation(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	err = bridge.Start(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Cancel the context — the listener should exit gracefully.
	cancel()
	time.Sleep(200 * time.Millisecond)

	// Cleanup without hanging.
	err = bridge.Stop()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Channel message with pattern matching
// ---------------------------------------------------------------------------

func TestRedisBridge_ChannelPatternMatching(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	// Subscribe clients to different channels.
	clientA := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	clientB := &Client{
		hub:           hub,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub.register <- clientA
	hub.register <- clientB
	time.Sleep(50 * time.Millisecond)

	hub.Subscribe(clientA, "events:user:1")
	hub.Subscribe(clientB, "events:user:2")

	// Receiver bridge.
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge1.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge1.Stop()

	// Sender bridge.
	hub2, _, _ := startTestHub(t)
	bridge2, err := NewRedisBridge(hub2, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = bridge2.Start(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer bridge2.Stop()

	time.Sleep(200 * time.Millisecond)

	// Publish to events:user:1 — only clientA should receive.
	err = bridge2.PublishToChannel("events:user:1", Message{Type: TypeEvent, Data: "for-user-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case msg := <-clientA.send:
		var received Message
		if !core.JSONUnmarshal(msg, &received).OK {
			t.Fatal("expected true")
		}
		if "for-user-1" != received.Data {
			t.Fatalf("want %v, got %v", "for-user-1", received.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("clientA should receive the channel message")
	}

	// clientB should NOT receive it.
	select {
	case msg := <-clientB.send:
		t.Fatalf("clientB should not receive message for user:1, got: %s", msg)
	case <-time.After(300 * time.Millisecond):
		// Good.
	}
}

// ---------------------------------------------------------------------------
// Unique source IDs per bridge instance
// ---------------------------------------------------------------------------

func TestRedisBridge_UniqueSourceIDs(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bridge2, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bridge1.SourceID() == bridge2.SourceID() {
		t.Fatalf("did not expect %v", bridge2.SourceID())
	}

	_ = bridge1.Stop()
	_ = bridge2.Stop()
}
