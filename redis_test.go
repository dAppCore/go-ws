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

	core "dappco.re/go/core"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	require.NotNil(t, bridge)
	assert.NotEmpty(t, bridge.SourceID(), "bridge should have a unique source ID")

	// Start the bridge.
	err = bridge.Start(context.Background())
	require.NoError(t, err)

	// Stop the bridge.
	err = bridge.Stop()
	require.NoError(t, err)
}

func TestRedisBridge_NilHub(t *testing.T) {
	skipIfNoRedis(t)

	_, err := NewRedisBridge(nil, RedisConfig{
		Addr: redisAddr,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub must not be nil")
}

func TestRedisBridge_EmptyAddr(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis address must not be empty")
}

func TestRedisBridge_BadAddr(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr: "127.0.0.1:1", // Nothing listening here.
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis ping failed")
}

func TestRedisBridge_InvalidPrefix_Ugly(t *testing.T) {
	hub := NewHub()

	_, err := NewRedisBridge(hub, RedisConfig{
		Addr:   redisAddr,
		Prefix: "bad prefix",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid redis prefix")
}

func TestRedisBridge_NewRedisBridge_SourceIDFailure_Ugly(t *testing.T) {
	t.Skip("missing seam: crypto/rand.Read failure is fatal and cannot be simulated safely in a unit test")
}

func TestRedisBridge_NewRedisBridge_StartFailure_Ugly(t *testing.T) {
	t.Skip("missing seam: NewRedisBridge calls Start directly, so a post-construction Start failure cannot be injected without a test seam")
}

func TestRedisBridge_DefaultPrefix(t *testing.T) {
	rc := skipIfNoRedis(t)
	cleanupRedis(t, rc, "ws")

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{
		Addr: redisAddr,
	})
	require.NoError(t, err)
	assert.Equal(t, "ws", bridge.prefix)

	err = bridge.Start(context.Background())
	require.NoError(t, err)
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

	assert.Equal(t, "redis.example:6380", options.Addr)
	assert.Equal(t, "secret", options.Password)
	assert.Equal(t, 4, options.DB)
	assert.Same(t, tlsConfig, options.TLSConfig)
}

func TestRedisBridge_newRedisOptions_Good(t *testing.T) {
	options := newRedisOptions(RedisConfig{
		Addr: "redis.example:6379",
	})

	assert.Equal(t, "redis.example:6379", options.Addr)
	assert.Equal(t, redisConnectTimeout, options.DialTimeout)
	assert.Equal(t, redisConnectTimeout, options.ReadTimeout)
	assert.Equal(t, redisConnectTimeout, options.WriteTimeout)
	assert.Equal(t, redisConnectTimeout, options.PoolTimeout)
}

func TestRedisBridge_validRedisForwardedMessage(t *testing.T) {
	t.Run("accepts messages without a process ID", func(t *testing.T) {
		assert.True(t, validRedisForwardedMessage(Message{
			Type: TypeEvent,
			Data: "hello",
		}))
	})

	t.Run("rejects invalid process IDs on forwarded messages", func(t *testing.T) {
		assert.False(t, validRedisForwardedMessage(Message{
			Type:      TypeProcessOutput,
			ProcessID: "bad process",
			Data:      "line",
		}))
	})

	t.Run("rejects invalid process IDs even on generic messages", func(t *testing.T) {
		assert.False(t, validRedisForwardedMessage(Message{
			Type:      TypeEvent,
			ProcessID: "bad process",
			Data:      "payload",
		}))
	})
}

func TestRedisBridge_validRedisPrefix_Good(t *testing.T) {
	assert.True(t, validRedisPrefix("ws"))
	assert.True(t, validRedisPrefix("my_app-1:prod"))
}

func TestRedisBridge_validRedisPrefix_Bad(t *testing.T) {
	tests := []string{
		"",
		"bad prefix",
		strings.Repeat("a", maxChannelNameLen+1),
	}

	for _, prefix := range tests {
		assert.False(t, validRedisPrefix(prefix))
	}
}

func TestRedisBridge_validRedisPrefix_Ugly(t *testing.T) {
	assert.False(t, validRedisPrefix("  ws  "))
}

func TestRedisBridge_Start_Bad(t *testing.T) {
	bridge := &RedisBridge{}

	err := bridge.Start(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis client is not available")
}

func TestRedisBridge_Start_InvalidPrefix_Bad(t *testing.T) {
	bridge := &RedisBridge{
		client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		prefix: "bad prefix",
	}
	defer bridge.client.Close()

	err := bridge.Start(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid redis prefix")
}

func TestRedisBridge_Start_ClosedClient_Bad(t *testing.T) {
	hub := NewHub()
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	require.NoError(t, client.Close())

	bridge := &RedisBridge{
		hub:    hub,
		client: client,
		prefix: "ws",
	}

	err := bridge.Start(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis subscribe failed")
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
	require.Equal(t, 1, hub.ClientCount())

	// Create two bridges on same Redis — bridge1 publishes, bridge2 receives.
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)
	err = bridge1.Start(context.Background())
	require.NoError(t, err)
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
	require.NoError(t, err)
	err = bridge2.Start(context.Background())
	require.NoError(t, err)
	defer bridge2.Stop()

	// Allow subscriptions to propagate.
	time.Sleep(100 * time.Millisecond)

	// Publish broadcast from bridge1.
	err = bridge1.PublishBroadcast(Message{Type: TypeEvent, Data: "cross-broadcast"})
	require.NoError(t, err)

	// bridge1's local hub should also receive the message.
	select {
	case msg := <-client.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, TypeEvent, received.Type)
		assert.Equal(t, "cross-broadcast", received.Data)
	case <-time.After(3 * time.Second):
		t.Fatal("bridge1 client should have received the local broadcast")
	}

	// bridge2's hub should receive the message (client2 gets it).
	select {
	case msg := <-client2.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, TypeEvent, received.Type)
		assert.Equal(t, "cross-broadcast", received.Data)
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
	require.NoError(t, err)
	err = bridge2.Start(context.Background())
	require.NoError(t, err)
	defer bridge2.Stop()

	// Local hub bridge (the receiver).
	bridge1, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)
	err = bridge1.Start(context.Background())
	require.NoError(t, err)
	defer bridge1.Stop()

	time.Sleep(100 * time.Millisecond)

	// Publish to channel from bridge2.
	err = bridge2.PublishToChannel("process:abc", Message{
		Type:      TypeProcessOutput,
		ProcessID: "abc",
		Data:      "line of output",
	})
	require.NoError(t, err)

	// subClient (subscribed to process:abc) should receive the message.
	select {
	case msg := <-subClient.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, TypeProcessOutput, received.Type)
		assert.Equal(t, "line of output", received.Data)
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

func TestRedisBridge_PublishToChannel_Bad(t *testing.T) {
	bridge := &RedisBridge{prefix: "ws"}

	err := bridge.PublishToChannel("bad channel", Message{Type: TypeEvent})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid channel name")

	t.Run("rejects process channels with oversized IDs", func(t *testing.T) {
		err := bridge.PublishToChannel("process:"+strings.Repeat("a", maxProcessIDLen+1), Message{Type: TypeEvent})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid process ID")
	})

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()
		bridge := &RedisBridge{
			hub:    hub,
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
			ctx:    context.Background(),
			prefix: "ws",
		}
		defer bridge.client.Close()

		err := bridge.PublishToChannel("valid-channel", Message{
			Type:      TypeProcessOutput,
			ProcessID: "bad process",
			Data:      "payload",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid process ID")
	})
}

func TestRedisBridge_PublishToChannel_Ugly_NilHub(t *testing.T) {
	bridge := &RedisBridge{prefix: "ws"}

	err := bridge.PublishToChannel("valid-channel", Message{Type: TypeEvent})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub must not be nil")
}

func TestRedisBridge_PublishToChannel_HubMarshalError_Bad(t *testing.T) {
	hub := NewHub()
	bridge := &RedisBridge{
		hub:    hub,
		prefix: "ws",
	}

	err := bridge.PublishToChannel("valid-channel", Message{Type: TypeEvent, Data: make(chan int)})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal message")
}

func TestRedisBridge_PublishToChannel_Ugly(t *testing.T) {
	var bridge *RedisBridge

	err := bridge.PublishToChannel("valid-channel", Message{Type: TypeEvent})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bridge must not be nil")
}

func TestRedisBridge_PublishBroadcast_Bad(t *testing.T) {
	var bridge *RedisBridge

	err := bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "noop"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bridge must not be nil")

	t.Run("rejects invalid process IDs", func(t *testing.T) {
		hub := NewHub()
		bridge := &RedisBridge{
			hub:    hub,
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
			ctx:    context.Background(),
			prefix: "ws",
		}
		defer bridge.client.Close()

		err := bridge.PublishBroadcast(Message{
			Type:      TypeProcessStatus,
			ProcessID: "bad process",
			Data:      "payload",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid process ID")
	})
}

func TestRedisBridge_PublishBroadcast_Ugly(t *testing.T) {
	bridge := &RedisBridge{
		prefix: "ws",
	}

	err := bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "noop"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub must not be nil")
}

func TestRedisBridge_SourceID_Good(t *testing.T) {
	bridge := &RedisBridge{sourceID: "source-123"}

	assert.Equal(t, "source-123", bridge.SourceID())
}

func TestRedisBridge_SourceID_Bad(t *testing.T) {
	var bridge *RedisBridge

	assert.Empty(t, bridge.SourceID())
}

func TestRedisBridge_SourceID_Ugly(t *testing.T) {
	bridge := &RedisBridge{}

	assert.Empty(t, bridge.SourceID())
}

func TestRedisBridge_Start_Good(t *testing.T) {
	t.Run("starts and stops", func(t *testing.T) {
		rc := skipIfNoRedis(t)
		prefix := testPrefix(t)
		cleanupRedis(t, rc, prefix)

		hub, _, _ := startTestHub(t)

		bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
		require.NoError(t, err)

		err = bridge.Start(nil)
		require.NoError(t, err)
		require.NotNil(t, bridge.ctx)
		require.NotNil(t, bridge.cancel)
		require.NotNil(t, bridge.pubsub)

		require.NoError(t, bridge.Stop())
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
		require.NoError(t, err)
		defer bridge.Stop()

		ctx1, cancel1 := context.WithCancel(context.Background())
		require.NoError(t, bridge.Start(ctx1))

		ctx2, cancel2 := context.WithCancel(context.Background())
		require.NoError(t, bridge.Start(ctx2))

		cancel1()

		env := redisEnvelope{
			SourceID: "external-source",
			Message: Message{
				Type: TypeEvent,
				Data: "listener-restart",
			},
		}
		raw := mustMarshal(env)
		require.NotNil(t, raw)
		require.NoError(t, rc.Publish(context.Background(), prefix+":broadcast", raw).Err())

		select {
		case msg := <-client.send:
			var received Message
			require.True(t, core.JSONUnmarshal(msg, &received).OK)
			assert.Equal(t, "listener-restart", received.Data)
		case <-time.After(3 * time.Second):
			t.Fatal("bridge should keep listening after being restarted with a new context")
		}

		cancel2()
	})
}

func TestRedisBridge_Start_NilReceiver_Bad(t *testing.T) {
	var bridge *RedisBridge

	err := bridge.Start(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bridge must not be nil")
}

func TestRedisBridge_Start_Ugly(t *testing.T) {
	bridge := &RedisBridge{}

	err := bridge.Start(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis client is not available")
}

func TestRedisBridge_Stop_Ugly(t *testing.T) {
	assert.NoError(t, (*RedisBridge)(nil).Stop())
}

func TestRedisBridge_Stop_ZeroValue_Good(t *testing.T) {
	bridge := &RedisBridge{}

	assert.NoError(t, bridge.Stop())
}

func TestRedisBridge_Stop_Good(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	require.NoError(t, bridge.Stop())
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
	require.NoError(t, err)
	err = bridge.Start(context.Background())
	require.NoError(t, err)
	defer bridge.Stop()

	err = rc.Publish(context.Background(), prefix+":broadcast", []byte("not-json")).Err()
	require.NoError(t, err)

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
	require.NoError(t, err)

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
	require.NotNil(t, broadcast)
	require.NoError(t, rc.Publish(context.Background(), prefix+":broadcast", broadcast).Err())

	channelMsg := mustMarshal(redisEnvelope{
		SourceID: "external-channel",
		Message: Message{
			Type:    TypeEvent,
			Channel: "target",
			Data:    "channel",
		},
	})
	require.NotNil(t, channelMsg)
	require.NoError(t, rc.Publish(context.Background(), prefix+":channel:target", channelMsg).Err())

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, pubsub.Close())

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
	assert.False(t, ok)
}

func TestRedisBridge_DecodeRedisEnvelope_Good(t *testing.T) {
	payload := core.Sprintf(`{"sourceId":"%s","message":{"type":"event","timestamp":"2024-01-01T00:00:00Z"}}`, "source-123")

	env, ok := decodeRedisEnvelope(payload)
	require.True(t, ok)
	assert.Equal(t, "source-123", env.SourceID)
	assert.Equal(t, TypeEvent, env.Message.Type)
}

func TestRedisBridge_publish_Good(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)
	defer bridge.Stop()

	err = bridge.publish(prefix+":broadcast", Message{Type: TypeEvent, Data: "publish-ok"})
	require.NoError(t, err)
}

func TestRedisBridge_publish_Bad(t *testing.T) {
	bridge := &RedisBridge{
		client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		ctx:    context.Background(),
	}
	defer bridge.client.Close()

	err := bridge.publish("ws:broadcast", Message{Type: TypeEvent, Data: make(chan int)})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal redis envelope")
}

func TestRedisBridge_publish_InvalidProcessID_Bad(t *testing.T) {
	bridge := &RedisBridge{
		client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		ctx:    context.Background(),
	}
	defer bridge.client.Close()

	err := bridge.publish("ws:broadcast", Message{
		Type:      TypeProcessOutput,
		ProcessID: "bad process",
		Data:      "payload",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid process ID")
}

func TestRedisBridge_publish_Ugly(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var bridge *RedisBridge

		err := bridge.publish("ws:broadcast", Message{Type: TypeEvent})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "bridge must not be nil")
	})

	t.Run("missing context", func(t *testing.T) {
		bridge := &RedisBridge{
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
		}
		defer bridge.client.Close()

		err := bridge.publish("ws:broadcast", Message{Type: TypeEvent, Data: "payload"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "bridge has not been started")
	})

	t.Run("missing client", func(t *testing.T) {
		bridge := &RedisBridge{ctx: context.Background()}

		err := bridge.publish("ws:broadcast", Message{Type: TypeEvent, Data: "payload"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "redis client is not available")
	})

	t.Run("invalid prefix", func(t *testing.T) {
		bridge := &RedisBridge{
			client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}),
			ctx:    context.Background(),
			prefix: "bad prefix",
		}
		defer bridge.client.Close()

		err := bridge.publish("bad prefix:broadcast", Message{Type: TypeEvent, Data: "payload"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid redis prefix")
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
	require.NoError(t, err)
	defer bridge.Stop()

	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "self-echo"})
	require.NoError(t, err)

	select {
	case msg := <-client.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, "self-echo", received.Data)
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
	require.NoError(t, err)
	err = bridgeA.Start(context.Background())
	require.NoError(t, err)
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
	require.NoError(t, err)
	err = bridgeB.Start(context.Background())
	require.NoError(t, err)
	defer bridgeB.Stop()

	// Allow subscriptions to settle.
	time.Sleep(1 * time.Second)

	// Publish from A, verify B receives.
	err = bridgeA.PublishBroadcast(Message{Type: TypeEvent, Data: "from-A"})
	require.NoError(t, err)

	select {
	case msg := <-clientA.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, "from-A", received.Data)
	case <-time.After(3 * time.Second):
		t.Fatal("hub A should receive its local broadcast")
	}

	select {
	case msg := <-clientB.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, "from-A", received.Data)
	case <-time.After(3 * time.Second):
		t.Fatal("hub B should receive broadcast from hub A")
	}

	// Publish from B, verify A receives.
	err = bridgeB.PublishBroadcast(Message{Type: TypeEvent, Data: "from-B"})
	require.NoError(t, err)

	select {
	case msg := <-clientB.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, "from-B", received.Data)
	case <-time.After(3 * time.Second):
		t.Fatal("hub B should receive its local broadcast")
	}

	select {
	case msg := <-clientA.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, "from-B", received.Data)
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
	require.NoError(t, err)
	err = bridge.Start(context.Background())
	require.NoError(t, err)
	defer bridge.Stop()

	time.Sleep(100 * time.Millisecond)

	// Publish from this bridge — the local hub should receive the message once,
	// and loop prevention should stop a second echoed copy from Redis.
	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "echo-test"})
	require.NoError(t, err)

	select {
	case msg := <-client.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, "echo-test", received.Data)
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
	require.NoError(t, err)
	err = bridgeRecv.Start(context.Background())
	require.NoError(t, err)
	defer bridgeRecv.Stop()

	// Sender hub.
	hubSend, _, _ := startTestHub(t)
	bridgeSend, err := NewRedisBridge(hubSend, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)
	err = bridgeSend.Start(context.Background())
	require.NoError(t, err)
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
	assert.Equal(t, numPublishes, received)
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
	require.NoError(t, err)
	err = bridge.Start(context.Background())
	require.NoError(t, err)

	// Stop should not panic or hang.
	done := make(chan error, 1)
	go func() {
		done <- bridge.Stop()
	}()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() should not hang")
	}

	// Publishing after stop should fail gracefully (context cancelled).
	err = bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "after-stop"})
	assert.Error(t, err, "publishing after stop should error")
}

func TestRedisBridge_StopWithoutStart(t *testing.T) {
	rc := skipIfNoRedis(t)
	prefix := testPrefix(t)
	cleanupRedis(t, rc, prefix)

	hub, _, _ := startTestHub(t)

	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)

	// Stop without Start should not panic.
	assert.NotPanics(t, func() {
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
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	err = bridge.Start(ctx)
	require.NoError(t, err)

	// Cancel the context — the listener should exit gracefully.
	cancel()
	time.Sleep(200 * time.Millisecond)

	// Cleanup without hanging.
	err = bridge.Stop()
	assert.NoError(t, err)
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
	require.NoError(t, err)
	err = bridge1.Start(context.Background())
	require.NoError(t, err)
	defer bridge1.Stop()

	// Sender bridge.
	hub2, _, _ := startTestHub(t)
	bridge2, err := NewRedisBridge(hub2, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)
	err = bridge2.Start(context.Background())
	require.NoError(t, err)
	defer bridge2.Stop()

	time.Sleep(200 * time.Millisecond)

	// Publish to events:user:1 — only clientA should receive.
	err = bridge2.PublishToChannel("events:user:1", Message{Type: TypeEvent, Data: "for-user-1"})
	require.NoError(t, err)

	select {
	case msg := <-clientA.send:
		var received Message
		require.True(t, core.JSONUnmarshal(msg, &received).OK)
		assert.Equal(t, "for-user-1", received.Data)
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
	require.NoError(t, err)
	err = bridge.Start(context.Background())
	require.NoError(t, err)
	defer bridge.Stop()

	env := redisEnvelope{
		SourceID: "external-source",
		Message: Message{
			Type: TypeEvent,
			Data: "should-be-dropped",
		},
	}
	raw := mustMarshal(env)
	require.NotNil(t, raw)

	err = rc.Publish(context.Background(), prefix+":channel:bad channel", raw).Err()
	require.NoError(t, err)

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
	require.NoError(t, err)
	err = bridge.Start(context.Background())
	require.NoError(t, err)
	defer bridge.Stop()

	env := redisEnvelope{
		SourceID: "external-source",
		Message: Message{
			Type:      TypeProcessOutput,
			ProcessID: "bad process",
			Data:      "should-be-dropped",
		},
	}
	raw := mustMarshal(env)
	require.NotNil(t, raw)

	err = rc.Publish(context.Background(), prefix+":broadcast", raw).Err()
	require.NoError(t, err)

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
	require.NoError(t, err)

	bridge2, err := NewRedisBridge(hub, RedisConfig{Addr: redisAddr, Prefix: prefix})
	require.NoError(t, err)

	assert.NotEqual(t, bridge1.SourceID(), bridge2.SourceID(),
		"each bridge instance must have a unique source ID")

	_ = bridge1.Stop()
	_ = bridge2.Stop()
}
