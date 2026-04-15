// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"sync"
	"time"

	core "dappco.re/go/core"
	coreerr "dappco.re/go/core/log"
	"github.com/redis/go-redis/v9"
)

const (
	redisConnectTimeout   = 5 * time.Second
	redisPublishTimeout   = 5 * time.Second
	maxRedisEnvelopeBytes = defaultMaxMessageBytes
)

// RedisConfig configures the Redis pub/sub bridge.
// bridge, _ := ws.NewRedisBridge(hub, ws.RedisConfig{Addr: "localhost:6379"})
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

func decodeRedisEnvelope(payload string) (redisEnvelope, bool) {
	if len(payload) == 0 || len(payload) > maxRedisEnvelopeBytes {
		return redisEnvelope{}, false
	}

	var env redisEnvelope
	if r := core.JSONUnmarshal([]byte(payload), &env); !r.OK {
		return redisEnvelope{}, false
	}

	return env, true
}

// validRedisForwardedMessage rejects forwarded envelopes that carry an invalid
// process identifier. Redis is an external trust boundary, so process IDs are
// re-validated before messages are delivered to the local hub.
func validRedisForwardedMessage(msg Message) bool {
	if msg.ProcessID != "" && !validProcessID(msg.ProcessID) {
		return false
	}

	return true
}

// RedisBridge connects a Hub to Redis pub/sub for cross-instance messaging.
// bridge, _ := ws.NewRedisBridge(hub, ws.RedisConfig{Addr: "localhost:6379"})
type RedisBridge struct {
	hub      *Hub
	client   *redis.Client
	pubsub   *redis.PubSub
	prefix   string
	sourceID string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.RWMutex
}

// ws.NewRedisBridge(hub, ws.RedisConfig{Addr: "localhost:6379"})
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
	if !validIdentifier(cfg.Prefix, maxChannelNameLen) {
		return nil, coreerr.E("NewRedisBridge", "invalid redis prefix", nil)
	}

	client := redis.NewClient(newRedisOptions(cfg))

	// Verify connectivity.
	pingCtx, cancel := context.WithTimeout(context.Background(), redisConnectTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
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

	bridge := &RedisBridge{
		hub:      hub,
		client:   client,
		prefix:   cfg.Prefix,
		sourceID: sourceID,
	}

	if err := bridge.Start(context.Background()); err != nil {
		client.Close()
		return nil, err
	}

	return bridge, nil
}

func newRedisOptions(cfg RedisConfig) *redis.Options {
	return &redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		TLSConfig:    cfg.TLSConfig,
		DialTimeout:  redisConnectTimeout,
		ReadTimeout:  redisConnectTimeout,
		WriteTimeout: redisConnectTimeout,
		PoolTimeout:  redisConnectTimeout,
	}
}

// Start begins listening for Redis messages and forwarding them to
// the local Hub's clients. If the bridge is already running, Start
// replaces the existing listener so callers can bind bridge lifetime
// to a specific context after construction.
func (rb *RedisBridge) Start(ctx context.Context) error {
	if rb == nil {
		return coreerr.E("RedisBridge.Start", "bridge must not be nil", nil)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := rb.stopListener(); err != nil {
		return err
	}

	rb.mu.RLock()
	client := rb.client
	prefix := rb.prefix
	rb.mu.RUnlock()
	if client == nil {
		return coreerr.E("RedisBridge.Start", "redis client is not available", nil)
	}
	if !validIdentifier(prefix, maxChannelNameLen) {
		return coreerr.E("RedisBridge.Start", "invalid redis prefix", nil)
	}

	runCtx, cancel := context.WithCancel(ctx)

	broadcastChan := prefix + ":broadcast"
	channelPattern := prefix + ":channel:*"

	pubsub := client.PSubscribe(runCtx, broadcastChan, channelPattern)

	// Wait for the subscription confirmation.
	receiveCtx, receiveCancel := context.WithTimeout(runCtx, redisConnectTimeout)
	defer receiveCancel()
	_, err := pubsub.Receive(receiveCtx)
	if err != nil {
		cancel()
		pubsub.Close()
		return coreerr.E("RedisBridge.Start", "redis subscribe failed", err)
	}

	rb.mu.Lock()
	rb.ctx = runCtx
	rb.cancel = cancel
	rb.pubsub = pubsub
	rb.mu.Unlock()

	rb.wg.Add(1)
	go rb.listen(runCtx, pubsub, prefix)

	return nil
}

// Stop cleanly shuts down the Redis bridge. It cancels the listener
// goroutine, closes the pub/sub subscription, and closes the Redis
// client connection.
func (rb *RedisBridge) Stop() error {
	if rb == nil {
		return nil
	}

	var firstErr error
	if err := rb.stopListener(); err != nil {
		firstErr = err
	}

	rb.mu.Lock()
	client := rb.client
	rb.client = nil
	rb.mu.Unlock()
	if client != nil {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// PublishToChannel publishes a message to a specific channel via Redis.
// Other bridge instances subscribed to the same Redis will receive the
// message and deliver it to their local Hub clients on that channel.
func (rb *RedisBridge) PublishToChannel(channel string, msg Message) error {
	if rb == nil {
		return coreerr.E("RedisBridge.PublishToChannel", "bridge must not be nil", nil)
	}

	if !validChannelName(channel) {
		return coreerr.E("RedisBridge.PublishToChannel", "invalid channel name", nil)
	}

	if rb.hub == nil {
		return coreerr.E("RedisBridge.PublishToChannel", "hub must not be nil", nil)
	}

	msg = stampServerMessage(msg)

	if err := rb.hub.SendToChannel(channel, msg); err != nil {
		return err
	}

	redisChan := rb.prefix + ":channel:" + channel
	return rb.publish(redisChan, msg)
}

// PublishBroadcast publishes a broadcast message via Redis. All bridge
// instances will receive it and deliver to all their local Hub clients.
func (rb *RedisBridge) PublishBroadcast(msg Message) error {
	if rb == nil {
		return coreerr.E("RedisBridge.PublishBroadcast", "bridge must not be nil", nil)
	}
	if rb.hub == nil {
		return coreerr.E("RedisBridge.PublishBroadcast", "hub must not be nil", nil)
	}

	msg = stampServerMessage(msg)

	redisChan := rb.prefix + ":broadcast"
	redisErr := rb.publish(redisChan, msg)
	localErr := rb.hub.Broadcast(msg)

	if redisErr != nil {
		return redisErr
	}

	return localErr
}

// publish serialises the envelope and publishes to the given Redis channel.
func (rb *RedisBridge) publish(redisChan string, msg Message) error {
	if rb == nil {
		return coreerr.E("RedisBridge.publish", "bridge must not be nil", nil)
	}

	rb.mu.RLock()
	ctx := rb.ctx
	client := rb.client
	sourceID := rb.sourceID
	rb.mu.RUnlock()

	if ctx == nil {
		return coreerr.E("RedisBridge.publish", "bridge has not been started", nil)
	}

	if client == nil {
		return coreerr.E("RedisBridge.publish", "redis client is not available", nil)
	}

	msg = stampServerMessage(msg)

	env := redisEnvelope{
		SourceID: sourceID,
		Message:  msg,
	}

	r := core.JSONMarshal(env)
	if !r.OK {
		return coreerr.E("RedisBridge.publish", "failed to marshal redis envelope", nil)
	}

	publishCtx, cancel := context.WithTimeout(ctx, redisPublishTimeout)
	defer cancel()

	return client.Publish(publishCtx, redisChan, r.Value.([]byte)).Err()
}

// listen runs in a goroutine, reading messages from the Redis pub/sub
// channel and forwarding them to the local Hub. Messages originating
// from this bridge instance (matching sourceID) are silently dropped
// to prevent infinite loops.
func (rb *RedisBridge) listen(ctx context.Context, pubsub *redis.PubSub, prefix string) {
	defer rb.wg.Done()

	ch := pubsub.Channel()
	broadcastChan := prefix + ":broadcast"
	channelPrefix := prefix + ":channel:"

	for {
		select {
		case <-ctx.Done():
			return
		case redisMsg, ok := <-ch:
			if !ok {
				return
			}

			env, ok := decodeRedisEnvelope(redisMsg.Payload)
			if !ok {
				// Skip malformed messages.
				continue
			}

			// Loop prevention: skip our own messages.
			if env.SourceID == rb.sourceID {
				continue
			}

			if !validRedisForwardedMessage(env.Message) {
				continue
			}

			switch {
			case redisMsg.Channel == broadcastChan:
				if rb.hub == nil {
					continue
				}
				// Deliver as a local broadcast.
				_ = rb.hub.Broadcast(env.Message)

			case core.HasPrefix(redisMsg.Channel, channelPrefix):
				if rb.hub == nil {
					continue
				}
				// Extract the Hub channel name from the Redis channel.
				hubChannel := core.TrimPrefix(redisMsg.Channel, channelPrefix)
				if !validChannelName(hubChannel) {
					continue
				}
				_ = rb.hub.SendToChannel(hubChannel, env.Message)
			}
		}
	}
}

func (rb *RedisBridge) stopListener() error {
	rb.mu.Lock()
	cancel := rb.cancel
	pubsub := rb.pubsub
	rb.cancel = nil
	rb.pubsub = nil
	rb.ctx = nil
	rb.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	var err error
	if pubsub != nil {
		err = pubsub.Close()
	}

	rb.wg.Wait()

	return err
}

// SourceID returns the unique identifier for this bridge instance.
// Useful for testing and debugging.
func (rb *RedisBridge) SourceID() string {
	if rb == nil {
		return ""
	}

	return rb.sourceID
}
