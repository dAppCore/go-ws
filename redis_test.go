// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"crypto/tls"
	"strings"
	// Note: AX-6 — internal concurrency primitive; structural for go-ws hub state (RFC mandates concurrent connection map).
	"sync"
	"testing"
	"time"

	core "dappco.re/go"
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
		_ = client.Close()
	})
}

func redisBridgeListening(bridge *RedisBridge) bool {
	if bridge == nil {
		return false
	}

	bridge.mu.RLock()
	defer bridge.mu.RUnlock()

	return bridge.ctx != nil && bridge.pubsub != nil
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if testIsNil(bridge) {
		t.Fatalf("expected non-nil value")
	}
	if testIsEmpty(bridge.SourceID()) {
		t.Errorf("expected non-empty value")
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Stop()
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

}

func TestRedisBridge_NilHub(t *testing.T) {
	skipIfNoRedis(t)

	_, err := NewRedisBridge(nil, RedisConfig{
		Addr: redisAddr,
	})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestRedisBridge_EmptyAddr(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr: "",
	})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "redis address must not be empty") {
		t.Errorf("expected %v to contain %v", err.Error(), "redis address must not be empty")
	}

}

func TestRedisBridge_BadAddr(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr: "127.0.0.1:1", // Nothing listening here.
	})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "redis ping failed") {
		t.Errorf("expected %v to contain %v", err.Error(), "redis ping failed")
	}

}

func TestRedisBridge_InvalidPrefix_Ugly(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr:   redisAddr,
		Prefix: "bad prefix",
	})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "invalid redis prefix") {
		t.Errorf("expected %v to contain %v", err.Error(), "invalid redis prefix")
	}

}

func TestRedisBridge_NewRedisBridge_SourceIDFailure_Ugly(t *testing.T) {
	bridge, err := NewRedisBridge(NewHub(), RedisConfig{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !testIsNil(bridge) {
		t.Fatalf("expected nil bridge when validation fails")
	}
}

func TestRedisBridge_NewRedisBridge_StartFailure_Ugly(t *testing.T) {
	bridge, err := NewRedisBridge(NewHub(), RedisConfig{Addr: "", Prefix: "ws"})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !testIsNil(bridge) {
		t.Fatalf("expected nil bridge when Redis address is empty")
	}
}

func TestRedisBridge_DefaultPrefix(t *testing.T) {
	rc := skipIfNoRedis(t)
	cleanupRedis(t, rc, "ws")

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{
		Addr: redisAddr,
	})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual("ws", bridge.prefix) {
		t.Errorf("expected %v, got %v", "ws", bridge.prefix)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge.Stop)
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
	if !testEqual("redis.example:6380", options.Addr) {
		t.Errorf("expected %v, got %v", "redis.example:6380", options.Addr)
	}
	if !testEqual("secret", options.Password) {
		t.Errorf("expected %v, got %v", "secret", options.Password)
	}
	if !testEqual(4, options.DB) {
		t.Errorf("expected %v, got %v", 4, options.DB)
	}
	if !testSame(tlsConfig, options.TLSConfig) {
		t.Errorf("expected same reference")
	}

}

func TestRedisBridge_newRedisOptions_Good(t *testing.T) {
	options := newRedisOptions(RedisConfig{
		Addr: "redis.example:6379",
	})
	if !testEqual("redis.example:6379", options.Addr) {
		t.Errorf("expected %v, got %v", "redis.example:6379", options.Addr)
	}
	if !testEqual(redisConnectTimeout, options.DialTimeout) {
		t.Errorf("expected %v, got %v", redisConnectTimeout, options.DialTimeout)
	}
	if !testEqual(redisConnectTimeout, options.ReadTimeout) {
		t.Errorf("expected %v, got %v", redisConnectTimeout, options.ReadTimeout)
	}
	if !testEqual(redisConnectTimeout, options.WriteTimeout) {
		t.Errorf("expected %v, got %v", redisConnectTimeout, options.WriteTimeout)
	}
	if !testEqual(redisConnectTimeout, options.PoolTimeout) {
		t.Errorf("expected %v, got %v", redisConnectTimeout, options.PoolTimeout)
	}

}

func TestRedisBridge_validRedisForwardedMessage(t *testing.T) {
	t.Run("accepts messages without a process ID", func(t *testing.T) {
		if !(validRedisForwardedMessage(Message{Type: TypeEvent, Data: "hello"})) {
			t.Errorf("expected true")
		}

	})

	t.Run("rejects invalid process IDs on forwarded messages", func(t *testing.T) {
		if validRedisForwardedMessage(Message{Type: TypeProcessOutput, ProcessID: "bad process", Data: "line"}) {
			t.Errorf("expected false")
		}

	})

	t.Run("rejects invalid process IDs even on generic messages", func(t *testing.T) {
		if validRedisForwardedMessage(Message{Type: TypeEvent, ProcessID: "bad process", Data: "payload"}) {
			t.Errorf("expected false")
		}

	})
}

func TestRedisBridge_validRedisPrefix_Good(t *testing.T) {
	if !(validRedisPrefix("ws")) {
		t.Errorf("expected true")
	}
	if !(validRedisPrefix("my_app-1:prod")) {
		t.Errorf("expected true")
	}

}

func TestRedisBridge_validRedisPrefix_Bad(t *testing.T) {
	tests := []string{
		"",
		"bad prefix",
		strings.Repeat("a", maxChannelNameLen+1),
	}

	for _, prefix := range tests {
		if validRedisPrefix(prefix) {
			t.Errorf("expected false")
		}

	}
}

func TestRedisBridge_validRedisPrefix_Ugly(t *testing.T) {
	if validRedisPrefix(" ws ") {
		t.Errorf("expected false")
	}

}

func TestRedisBridge_Start_Bad(t *testing.T) {
	bridge := &RedisBridge{}

	err := bridge.Start(context.Background())
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "redis client is not available") {
		t.Errorf("expected %v to contain %v", err.Error(), "redis client is not available")
	}

}

func TestRedisBridge_Start_InvalidPrefix_Bad(t *testing.T) {
	bridge := &RedisBridge{
		client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		prefix: "bad prefix",
	}
	defer testClose(t, bridge.client.Close)

	err := bridge.Start(context.Background())
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "invalid redis prefix") {
		t.Errorf("expected %v to contain %v", err.Error(), "invalid redis prefix")
	}

}

func TestRedisBridge_Start_ClosedClient_Bad(t *testing.T) {
	hub := NewHub()
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := client.Close(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	bridge := &RedisBridge{
		hub:    hub,
		client: client,
		prefix: "ws",
	}

	err := bridge.Start(context.Background())
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "redis subscribe failed") {
		t.Errorf("expected %v to contain %v", err.Error(), "redis subscribe failed")
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
	if !testEqual(1, hub.ClientCount()) {
		t.Fatalf("expected %v, got %v", 1, hub.ClientCount())
	}

	// Create two bridges on same Redis; bridge1 publishes and bridge2 receives.
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge1.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge1.Stop)

	// A second hub and bridge receive the cross-instance message.
	hub2, _, _ := startTestHub(t)
	client2 := &Client{
		hub:           hub2,
		send:          make(chan []byte, 256),
		subscriptions: make(map[string]bool),
	}
	hub2.register <- client2
	time.Sleep(50 * time.Millisecond)

	bridge2, err := NewRedisBridge(hub2, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge2.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge2.Stop)

	time.Sleep(100 * time.Millisecond)

	// Publish broadcast from bridge1.
	err = bridge1.PublishBroadcast(Message{Type: TypeEvent, Data: "cross-broadcast"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// bridge1's local hub should also receive the message.
	select {
	case msg := <-client.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeEvent, received.Type) {
			t.Errorf("expected %v, got %v", TypeEvent, received.Type)
		}
		if !testEqual("cross-broadcast", received.Data) {
			t.Errorf("expected %v, got %v", "cross-broadcast", received.Data)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("bridge1 client should have received the local broadcast")
	}

	// bridge2's hub should receive the message.
	select {
	case msg := <-client2.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeEvent, received.Type) {
			t.Errorf("expected %v, got %v", TypeEvent, received.Type)
		}
		if !testEqual("cross-broadcast", received.Data) {
			t.Errorf("expected %v, got %v", "cross-broadcast", received.Data)
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
	if err := hub.Subscribe(subClient, "process:abc"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge2.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge2.Stop)

	// Local hub bridge receives cross-instance channel messages.
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge1.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge1.Stop)

	time.Sleep(100 * time.Millisecond)

	// Publish to channel from bridge2.
	err = bridge2.PublishToChannel("process:abc", Message{
		Type:      TypeProcessOutput,
		ProcessID: "abc",
		Data:      "line of output",
	})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// subClient is subscribed to process:abc, so it should receive the message.
	select {
	case msg := <-subClient.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual(TypeProcessOutput, received.Type) {
			t.Errorf("expected %v, got %v", TypeProcessOutput, received.Type)
		}
		if !testEqual("line of output", received.Data) {
			t.Errorf("expected %v, got %v", "line of output", received.Data)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("subscribed client should have received the channel message")
	}

	// otherClient should not receive the channel message.
	select {
	case msg := <-otherClient.send:
		t.Fatalf("unsubscribed client should not receive channel message, got: %s", msg)
	case <-time.After(300 * time.Millisecond):
		// Good — no message delivered.
	}
}

func TestRedisBridge_PublishToChannel_Bad(t *testing.T) {
	bridge := &RedisBridge{prefix: "ws"}

	err := bridge.PublishToChannel("bad channel", Message{Type: TypeEvent})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "invalid channel name") {
		t.Errorf("expected %v to contain %v", err.Error(), "invalid channel name")
	}

	t.Run("rejects process channels with oversized IDs", func(t *testing.T) {
		err := bridge.PublishToChannel("process:"+strings.Repeat("a", maxProcessIDLen+1), Message{Type: TypeEvent})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
		}

	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()
		bridge := &RedisBridge{
			hub:    hub,
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
			ctx:    context.Background(),
			prefix: "ws",
		}
		defer testClose(t, bridge.client.Close)

		err := bridge.PublishToChannel("valid-channel", Message{
			Type:      TypeProcessOutput,
			ProcessID: "bad process",
			Data:      "payload",
		})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
		}

	})

}

func TestRedisBridge_PublishToChannel_Ugly_NilHub(t *testing.T) {
	bridge := &RedisBridge{prefix: "ws"}

	err := bridge.PublishToChannel("valid-channel", Message{Type: TypeEvent})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestRedisBridge_PublishToChannel_HubMarshalError_Bad(t *testing.T) {
	hub := NewHub()
	bridge := &RedisBridge{
		hub:    hub,
		prefix: "ws",
	}

	err := bridge.PublishToChannel("valid-channel", Message{Type: TypeEvent, Data: make(chan int)})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "failed to marshal message") {
		t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal message")
	}

}

func TestRedisBridge_PublishToChannel_Ugly(t *testing.T) {
	var bridge *RedisBridge

	err := bridge.PublishToChannel("valid-channel", Message{Type: TypeEvent})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "bridge must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "bridge must not be nil")
	}

}

func TestRedisBridge_PublishBroadcast_Bad(t *testing.T) {
	var bridge *RedisBridge

	err := bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "noop"})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "bridge must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "bridge must not be nil")
	}

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()
		bridge := &RedisBridge{
			hub:    hub,
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
			ctx:    context.Background(),
			prefix: "ws",
		}
		defer testClose(t, bridge.client.Close)

		err := bridge.PublishBroadcast(Message{
			Type:      TypeProcessStatus,
			ProcessID: "bad process",
			Data:      "payload",
		})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid process ID") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
		}

	})

	t.Run("preserves local and redis failures", func(t *testing.T) {
		hub := NewHub()
		bridge := &RedisBridge{
			hub:    hub,
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
			ctx:    context.Background(),
			prefix: "ws",
		}
		defer testClose(t, bridge.client.Close)

		err := bridge.PublishBroadcast(Message{Type: TypeEvent, Data: make(chan int)})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "local:") {
			t.Errorf("expected %v to contain %v", err.Error(), "local:")
		}
		if !testContains(err.Error(), "redis:") {
			t.Errorf("expected %v to contain %v", err.Error(), "redis:")
		}
		if !testContains(err.Error(), "failed to marshal message") {
			t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal message")
		}
		if !testContains(err.Error(), "failed to marshal redis envelope") {
			t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal redis envelope")
		}

	})
}

func TestRedisBridge_PublishBroadcast_Ugly(t *testing.T) {
	bridge := &RedisBridge{
		prefix: "ws",
	}

	err := bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "noop"})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "hub must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "hub must not be nil")
	}

}

func TestRedisBridge_SourceID_Good(t *testing.T) {
	bridge := &RedisBridge{sourceID: "source-123"}
	if !testEqual("source-123", bridge.SourceID()) {
		t.Errorf("expected %v, got %v", "source-123", bridge.SourceID())
	}

}

func TestRedisBridge_SourceID_Bad(t *testing.T) {
	var bridge *RedisBridge
	if !testIsEmpty(bridge.SourceID()) {
		t.Errorf("expected empty value, got %v", bridge.SourceID())
	}

}

func TestRedisBridge_SourceID_Ugly(t *testing.T) {
	bridge := &RedisBridge{}
	if !testIsEmpty(bridge.SourceID()) {
		t.Errorf("expected empty value, got %v", bridge.SourceID())
	}

}

func TestRedisBridge_Start_Good(t *testing.T) {
	t.Run("starts and stops", func(t *testing.T) {
		rc := skipIfNoRedis(t)
		prefix := testPrefix(t)
		cleanupRedis(t, rc, prefix)

		hub, _, _ := startTestHub(t)

		bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		err = bridge.Start(context.TODO())
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if testIsNil(bridge.ctx) {
			t.Fatalf("expected non-nil value")
		}
		if testIsNil(bridge.cancel) {
			t.Fatalf("expected non-nil value")
		}
		if testIsNil(bridge.pubsub) {
			t.Fatalf("expected non-nil value")
		}
		if err := bridge.Stop(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

	})

	t.Run("replaces an existing listener when restarted", func(t *testing.T) {
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		defer testClose(t, bridge.Stop)

		ctx1, cancel1 := context.WithCancel(context.Background())
		if err := bridge.Start(ctx1); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		ctx2, cancel2 := context.WithCancel(context.Background())
		if err := bridge.Start(ctx2); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		cancel1()

		env := redisEnvelope{
			SourceID: "external-source",
			Message: Message{
				Type: TypeEvent,
				Data: "listener-restart",
			},
		}
		raw := mustMarshal(env)
		if testIsNil(raw) {
			t.Fatalf("expected non-nil value")
		}
		if err := rc.Publish(context.Background(), prefix+":broadcast", raw).Err(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		select {
		case msg := <-client.send:
			var received Message
			if !(core.JSONUnmarshal(msg, &received).OK) {
				t.Fatalf("expected true")
			}
			if !testEqual("listener-restart", received.Data) {
				t.Errorf("expected %v, got %v", "listener-restart", received.Data)
			}

		case <-time.After(3 * time.Second):
			t.Fatal("bridge should keep listening after being restarted with a new context")
		}

		cancel2()
	})
}

func TestRedisBridge_Start_NilReceiver_Bad(t *testing.T) {
	var bridge *RedisBridge

	err := bridge.Start(context.Background())
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "bridge must not be nil") {
		t.Errorf("expected %v to contain %v", err.Error(), "bridge must not be nil")
	}

}

func TestRedisBridge_Start_Ugly(t *testing.T) {
	bridge := &RedisBridge{}

	err := bridge.Start(context.Background())
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "redis client is not available") {
		t.Errorf("expected %v to contain %v", err.Error(), "redis client is not available")
	}

}

func TestRedisBridge_Stop_Ugly(t *testing.T) {
	if err := (*RedisBridge)(nil).Stop(); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestRedisBridge_Stop_ZeroValue_Good(t *testing.T) {
	bridge := &RedisBridge{}
	if err := bridge.Stop(); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestRedisBridge_Stop_Good(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := bridge.Stop(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

}

func TestRedisBridge_MalformedInboundPayload_Ugly(t *testing.T) {
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge.Stop)

	err = rc.Publish(context.Background(), prefix+":broadcast", []byte("not-json")).Err()
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-client.send:
		t.Fatalf("malformed inbound payload should not be forwarded, got: %s", msg)
	case <-time.After(300 * time.Millisecond):
		// Good - listener skipped the malformed payload.
	}
}

func TestRedisBridge_listen_NilHubAndClosedChannel_Good(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	pubsub := rc.PSubscribe(context.Background(), prefix+":broadcast", prefix+":channel:*")
	receiveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pubsub.Receive(receiveCtx)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	bridge := &RedisBridge{
		sourceID: "listener-source",
	}

	bridge.wg.Add(1)
	done := make(chan struct{})
	go func() {
		bridge.listen(context.Background(), pubsub, prefix)
		close(done)
	}()

	broadcast := mustMarshal(redisEnvelope{
		SourceID: "external-broadcast",
		Message: Message{
			Type: TypeEvent,
			Data: "broadcast",
		},
	})
	if testIsNil(broadcast) {
		t.Fatalf("expected non-nil value")
	}
	if err := rc.Publish(context.Background(), prefix+":broadcast", broadcast).Err(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	channelMsg := mustMarshal(redisEnvelope{
		SourceID: "external-channel",
		Message: Message{
			Type:    TypeEvent,
			Channel: "target",
			Data:    "channel",
		},
	})
	if testIsNil(channelMsg) {
		t.Fatalf("expected non-nil value")
	}
	if err := rc.Publish(context.Background(), prefix+":channel:target", channelMsg).Err(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if err := pubsub.Close(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("listener should stop when the pubsub channel closes")
	}

	bridge.wg.Wait()
}

func TestRedisBridge_DecodeRedisEnvelope_SizeLimit(t *testing.T) {
	largePayload := strings.Repeat("A", maxRedisEnvelopeBytes+1)

	_, ok := decodeRedisEnvelope(largePayload)
	if ok {
		t.Errorf("expected false")
	}

}

func TestRedisBridge_DecodeRedisEnvelope_Good(t *testing.T) {
	payload := core.Sprintf(`{"sourceId":"%s","message":{"type":"event","timestamp":"2024-01-01T00:00:00Z"}}`, "source-123")

	env, ok := decodeRedisEnvelope(payload)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if !testEqual("source-123", env.SourceID) {
		t.Errorf("expected %v, got %v", "source-123", env.SourceID)
	}
	if !testEqual(TypeEvent, env.Message.Type) {
		t.Errorf("expected %v, got %v", TypeEvent, env.Message.Type)
	}

}

func TestRedisBridge_publish_Good(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge.Stop)

	err = bridge.publish(prefix+":broadcast", Message{Type: TypeEvent, Data: "publish-ok"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

}

func TestRedisBridge_publish_Bad(t *testing.T) {
	bridge := &RedisBridge{
		client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		ctx:    context.Background(),
	}
	defer testClose(t, bridge.client.Close)

	err := bridge.publish("ws:broadcast", Message{Type: TypeEvent, Data: make(chan int)})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "failed to marshal redis envelope") {
		t.Errorf("expected %v to contain %v", err.Error(), "failed to marshal redis envelope")
	}

}

func TestRedisBridge_publish_InvalidProcessID_Bad(t *testing.T) {
	bridge := &RedisBridge{
		client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		ctx:    context.Background(),
	}
	defer testClose(t, bridge.client.Close)

	err := bridge.publish("ws:broadcast", Message{
		Type:      TypeProcessOutput,
		ProcessID: "bad process",
		Data:      "payload",
	})
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(err.Error(), "invalid process ID") {
		t.Errorf("expected %v to contain %v", err.Error(), "invalid process ID")
	}

}

func TestRedisBridge_publish_Ugly(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var bridge *RedisBridge

		err := bridge.publish("ws:broadcast", Message{Type: TypeEvent})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "bridge must not be nil") {
			t.Errorf("expected %v to contain %v", err.Error(), "bridge must not be nil")
		}

	})

	t.Run("missing context", func(t *testing.T) {
		bridge := &RedisBridge{
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		}
		defer testClose(t, bridge.client.Close)

		err := bridge.publish("ws:broadcast", Message{Type: TypeEvent, Data: "payload"})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "bridge has not been started") {
			t.Errorf("expected %v to contain %v", err.Error(), "bridge has not been started")
		}

	})

	t.Run("missing client", func(t *testing.T) {
		bridge := &RedisBridge{ctx: context.Background()}

		err := bridge.publish("ws:broadcast", Message{Type: TypeEvent, Data: "payload"})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "redis client is not available") {
			t.Errorf("expected %v to contain %v", err.Error(), "redis client is not available")
		}

	})

	t.Run("invalid prefix", func(t *testing.T) {
		bridge := &RedisBridge{
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
			ctx:    context.Background(),
			prefix: "bad prefix",
		}
		defer testClose(t, bridge.client.Close)

		err := bridge.publish("bad prefix:broadcast", Message{Type: TypeEvent, Data: "payload"})
		if err := err; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(err.Error(), "invalid redis prefix") {
			t.Errorf("expected %v to contain %v", err.Error(), "invalid redis prefix")
		}

	})
}

func TestRedisBridge_SelfEchoSuppressed_Good(t *testing.T) {
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge.Stop)

	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "self-echo"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-client.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("self-echo", received.Data) {
			t.Errorf("expected %v, got %v", "self-echo", received.Data)
		}

	case <-time.After(time.Second):
		t.Fatal("client should receive the local broadcast")
	}

	select {
	case msg := <-client.send:
		t.Fatalf("bridge should not echo its own Redis message, got: %s", msg)
	case <-time.After(300 * time.Millisecond):
		// Good - the bridge skipped its own source ID.
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridgeA.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridgeA.Stop)

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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridgeB.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridgeB.Stop)

	if !testEventually(func() bool {
		return redisBridgeListening(bridgeA) && redisBridgeListening(bridgeB)
	}, 3*time.Second, 50*time.Millisecond) {
		t.Fatal("bridges did not start listening in time")
	}

	// Publish from A, verify B receives.
	err = bridgeA.PublishBroadcast(Message{Type: TypeEvent, Data: "from-A"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-clientA.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("from-A", received.Data) {
			t.Errorf("expected %v, got %v", "from-A", received.Data)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("hub A should receive its local broadcast")
	}

	select {
	case msg := <-clientB.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("from-A", received.Data) {
			t.Errorf("expected %v, got %v", "from-A", received.Data)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("hub B should receive broadcast from hub A")
	}

	// Publish from B, verify A receives.
	err = bridgeB.PublishBroadcast(Message{Type: TypeEvent, Data: "from-B"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-clientB.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("from-B", received.Data) {
			t.Errorf("expected %v, got %v", "from-B", received.Data)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("hub B should receive its local broadcast")
	}

	select {
	case msg := <-clientA.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("from-B", received.Data) {
			t.Errorf("expected %v, got %v", "from-B", received.Data)
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge.Stop)

	time.Sleep(100 * time.Millisecond)

	// Publish from this bridge — the local hub should receive the message once,
	// and loop prevention should stop a second echoed copy from Redis.
	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "echo-test"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-client.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("echo-test", received.Data) {
			t.Errorf("expected %v, got %v", "echo-test", received.Data)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("bridge should deliver the broadcast to its local hub")
	}

	select {
	case msg := <-client.send:
		t.Fatalf("bridge should not echo its own Redis message twice, got: %s", msg)
	case <-time.After(500 * time.Millisecond):
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridgeRecv.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridgeRecv.Stop)

	// Sender hub.
	hubSend, _, _ := startTestHub(t)
	bridgeSend, err := NewRedisBridge(hubSend, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridgeSend.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridgeSend.Stop)

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
	if !testEqual(numPublishes, received) {
		t.Errorf("expected %v, got %v", numPublishes, received)
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Stop should not panic or hang.
	done := make(chan error, 1)
	go func() {
		done <- bridge.Stop()
	}()

	select {
	case err := <-done:
		if err := err; err != nil {
			t.Errorf("expected no error, got %v", err)
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Stop() should not hang")
	}

	// Publishing after stop should fail gracefully (context cancelled).
	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "after-stop"})
	if err := err; err == nil {
		t.Errorf("expected error")
	}

}

func TestRedisBridge_StopWithoutStart(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// Stop without Start should not panic.
	testNotPanics(t, func() {
		_ = bridge.Stop()
	})

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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	err = bridge.Start(ctx)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Cancel the context so the listener exits gracefully.
	cancel()
	time.Sleep(200 * time.Millisecond)

	// Cleanup without hanging.
	err = bridge.Stop()
	if err := err; err != nil {
		t.Errorf("expected no error, got %v", err)
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

	if err := hub.Subscribe(clientA, "events:user:1"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := hub.Subscribe(clientB, "events:user:2"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Receiver bridge.
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge1.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge1.Stop)

	// Sender bridge.
	hub2, _, _ := startTestHub(t)
	bridge2, err := NewRedisBridge(hub2, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge2.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge2.Stop)

	time.Sleep(200 * time.Millisecond)

	// Publish to events:user:1 — only clientA should receive.
	err = bridge2.PublishToChannel("events:user:1", Message{Type: TypeEvent, Data: "for-user-1"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-clientA.send:
		var received Message
		if !(core.JSONUnmarshal(msg, &received).OK) {
			t.Fatalf("expected true")
		}
		if !testEqual("for-user-1", received.Data) {
			t.Errorf("expected %v, got %v", "for-user-1", received.Data)
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

func TestRedisBridge_InvalidInboundChannel_Ugly(t *testing.T) {
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge.Stop)

	env := redisEnvelope{
		SourceID: "external-source",
		Message: Message{
			Type: TypeEvent,
			Data: "should-be-dropped",
		},
	}
	raw := mustMarshal(env)
	if testIsNil(raw) {
		t.Fatalf("expected non-nil value")
	}

	err = rc.Publish(context.Background(), prefix+":channel:bad channel", raw).Err()
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-client.send:
		t.Fatalf("invalid inbound channel should not be forwarded, got: %s", msg)
	case <-time.After(300 * time.Millisecond):
		// Good - listener dropped the invalid channel name.
	}
}

func TestRedisBridge_listen_InvalidProcessID_Ugly(t *testing.T) {
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = bridge.Start(context.Background())
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer testClose(t, bridge.Stop)

	env := redisEnvelope{
		SourceID: "external-source",
		Message: Message{
			Type:      TypeProcessOutput,
			ProcessID: "bad process",
			Data:      "should-be-dropped",
		},
	}
	raw := mustMarshal(env)
	if testIsNil(raw) {
		t.Fatalf("expected non-nil value")
	}

	err = rc.Publish(context.Background(), prefix+":broadcast", raw).Err()
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	select {
	case msg := <-client.send:
		t.Fatalf("invalid process ID should not be forwarded, got: %s", msg)
	case <-time.After(300 * time.Millisecond):
		// Good - listener dropped the forwarded message before local delivery.
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	bridge2, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if testEqual(bridge1.SourceID(), bridge2.SourceID()) {
		t.Errorf("expected values to differ: %v", bridge2.SourceID())
	}

	_ = bridge1.Stop()
	_ = bridge2.Stop()
}
