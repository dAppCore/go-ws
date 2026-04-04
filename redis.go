// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"sync"

	core "dappco.re/go/core"
	coreerr "dappco.re/go/core/log"
	"github.com/redis/go-redis/v9"
)

// RedisConfig configures the Redis pub/sub bridge.
type RedisConfig struct {
	// Addr is the Redis server address (e.g. "10.69.69.87:6379").
	Addr string

	// Password is the optional Redis authentication password.
	Password string

	// DB is the Redis database number (default 0).
	DB int

	// Prefix is the key prefix for Redis channels (default "ws").
	Prefix string

	// TLSConfig enables encrypted Redis connections when set.
	// It is passed directly to go-redis's connection options.
	TLSConfig *tls.Config
}

// redisEnvelope wraps a Message with a source identifier to prevent
// infinite echo loops between bridge instances.
type redisEnvelope struct {
	SourceID string  `json:"sourceId"`
	Message  Message `json:"message"`
}

// RedisBridge connects a Hub to Redis pub/sub for cross-instance messaging.
// Multiple Hub instances using the same Redis backend will coordinate
// broadcasts and channel messages transparently.
type RedisBridge struct {
	hub      *Hub
	client   *redis.Client
	pubsub   *redis.PubSub
	prefix   string
	sourceID string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewRedisBridge creates a Redis bridge for the given Hub.
// It establishes a connection to Redis and validates connectivity
// before returning. The bridge must be started with Start() to
// begin processing messages.
func NewRedisBridge(hub *Hub, cfg RedisConfig) (*RedisBridge, error) {
	if hub == nil {
		return nil, coreerr.E("NewRedisBridge", "hub must not be nil", nil)
	}
	if cfg.Addr == "" {
		return nil, coreerr.E("NewRedisBridge", "redis address must not be empty", nil)
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "ws"
	}

	client := redis.NewClient(newRedisOptions(cfg))

	// Verify connectivity.
	if err := client.Ping(context.Background()).Err(); err != nil {
		client.Close()
		return nil, coreerr.E("NewRedisBridge", "redis ping failed", err)
	}

	// Generate a unique source ID to prevent echo loops.
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		client.Close()
		return nil, coreerr.E("NewRedisBridge", "failed to generate source ID", err)
	}
	sourceID := hex.EncodeToString(idBytes)

	return &RedisBridge{
		hub:      hub,
		client:   client,
		prefix:   cfg.Prefix,
		sourceID: sourceID,
	}, nil
}

func newRedisOptions(cfg RedisConfig) *redis.Options {
	return &redis.Options{
		Addr:      cfg.Addr,
		Password:  cfg.Password,
		DB:        cfg.DB,
		TLSConfig: cfg.TLSConfig,
	}
}

// Start begins listening for Redis messages and forwarding them to
// the local Hub's clients. It subscribes to the broadcast channel
// and uses pattern-subscribe for all channel-targeted messages.
// The bridge runs until Stop() is called or the provided context
// is cancelled.
func (rb *RedisBridge) Start(ctx context.Context) error {
	rb.ctx, rb.cancel = context.WithCancel(ctx)

	broadcastChan := rb.prefix + ":broadcast"
	channelPattern := rb.prefix + ":channel:*"

	rb.pubsub = rb.client.PSubscribe(rb.ctx, broadcastChan, channelPattern)

	// Wait for the subscription confirmation.
	_, err := rb.pubsub.Receive(rb.ctx)
	if err != nil {
		rb.pubsub.Close()
		return coreerr.E("RedisBridge.Start", "redis subscribe failed", err)
	}

	rb.wg.Add(1)
	go rb.listen()

	return nil
}

// Stop cleanly shuts down the Redis bridge. It cancels the listener
// goroutine, closes the pub/sub subscription, and closes the Redis
// client connection.
func (rb *RedisBridge) Stop() error {
	if rb.cancel != nil {
		rb.cancel()
	}

	// Wait for the listener goroutine to exit.
	rb.wg.Wait()

	var firstErr error
	if rb.pubsub != nil {
		if err := rb.pubsub.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if rb.client != nil {
		if err := rb.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// PublishToChannel publishes a message to a specific channel via Redis.
// Other bridge instances subscribed to the same Redis will receive the
// message and deliver it to their local Hub clients on that channel.
func (rb *RedisBridge) PublishToChannel(channel string, msg Message) error {
	redisChan := rb.prefix + ":channel:" + channel
	return rb.publish(redisChan, msg)
}

// PublishBroadcast publishes a broadcast message via Redis. All bridge
// instances will receive it and deliver to all their local Hub clients.
func (rb *RedisBridge) PublishBroadcast(msg Message) error {
	redisChan := rb.prefix + ":broadcast"
	return rb.publish(redisChan, msg)
}

// publish serialises the envelope and publishes to the given Redis channel.
func (rb *RedisBridge) publish(redisChan string, msg Message) error {
	if rb.ctx == nil {
		return coreerr.E("RedisBridge.publish", "bridge has not been started", nil)
	}

	if rb.client == nil {
		return coreerr.E("RedisBridge.publish", "redis client is not available", nil)
	}

	env := redisEnvelope{
		SourceID: rb.sourceID,
		Message:  msg,
	}

	r := core.JSONMarshal(env)
	if !r.OK {
		return coreerr.E("RedisBridge.publish", "failed to marshal redis envelope", nil)
	}

	return rb.client.Publish(rb.ctx, redisChan, r.Value.([]byte)).Err()
}

// listen runs in a goroutine, reading messages from the Redis pub/sub
// channel and forwarding them to the local Hub. Messages originating
// from this bridge instance (matching sourceID) are silently dropped
// to prevent infinite loops.
func (rb *RedisBridge) listen() {
	defer rb.wg.Done()

	ch := rb.pubsub.Channel()
	broadcastChan := rb.prefix + ":broadcast"
	channelPrefix := rb.prefix + ":channel:"

	for {
		select {
		case <-rb.ctx.Done():
			return
		case redisMsg, ok := <-ch:
			if !ok {
				return
			}

			var env redisEnvelope
			if r := core.JSONUnmarshal([]byte(redisMsg.Payload), &env); !r.OK {
				// Skip malformed messages.
				continue
			}

			// Loop prevention: skip our own messages.
			if env.SourceID == rb.sourceID {
				continue
			}

			switch {
			case redisMsg.Channel == broadcastChan:
				// Deliver as a local broadcast.
				_ = rb.hub.Broadcast(env.Message)

			case core.HasPrefix(redisMsg.Channel, channelPrefix):
				// Extract the Hub channel name from the Redis channel.
				hubChannel := core.TrimPrefix(redisMsg.Channel, channelPrefix)
				_ = rb.hub.SendToChannel(hubChannel, env.Message)
			}
		}
	}
}

// SourceID returns the unique identifier for this bridge instance.
// Useful for testing and debugging.
func (rb *RedisBridge) SourceID() string {
	return rb.sourceID
}
