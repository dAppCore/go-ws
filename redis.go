// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	// AX-6-exception: Redis TLS transport config
	"crypto/tls"
	// Note: AX-6 — internal concurrency primitive; structural for go-ws hub state (RFC mandates concurrent connection map).
	"sync"
	"time"

	core "dappco.re/go"
	coreerr "dappco.re/go/log"
	"github.com/redis/go-redis/v9"
)

const (
	redisConnectTimeout   = 5 * time.Second
	redisPublishTimeout   = 5 * time.Second
	maxRedisEnvelopeBytes = defaultMaxMessageBytes
)

// RedisConfig configures the Redis connection and channel namespace used by a
// RedisBridge.
//
//	result := ws.NewRedisBridge(hub, ws.RedisConfig{Addr: "localhost:6379"})
//	bridge := result.Value.(*ws.RedisBridge)
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

func validRedisPublishMessage(msg Message) bool {
	if msg.ProcessID != "" && !validProcessID(msg.ProcessID) {
		return false
	}

	return true
}

func validRedisPrefix(prefix string) bool {
	return validIdentifier(prefix, maxChannelNameLen)
}

// RedisBridge mirrors hub broadcasts and channel messages through Redis pub/sub.
//
//	result := ws.NewRedisBridge(hub, ws.RedisConfig{Addr: "localhost:6379"})
//	bridge := result.Value.(*ws.RedisBridge)
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

// NewRedisBridge validates Redis connectivity and returns a bridge ready to be
// started with Start.
//
//	result := ws.NewRedisBridge(hub, ws.RedisConfig{Addr: "localhost:6379"})
func NewRedisBridge(hub *Hub, cfg RedisConfig) core.Result {
	if hub == nil {
		return core.Fail(coreerr.E("NewRedisBridge", "hub must not be nil", nil))
	}
	if cfg.Addr == "" {
		return core.Fail(coreerr.E("NewRedisBridge", "redis address must not be empty", nil))
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "ws"
	}
	if !validRedisPrefix(cfg.Prefix) {
		return core.Fail(coreerr.E("NewRedisBridge", "invalid redis prefix", nil))
	}

	client := redis.NewClient(newRedisOptions(cfg))

	// Verify connectivity.
	pingCtx, cancel := context.WithTimeout(context.Background(), redisConnectTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		logCloseError("NewRedisBridge.client", client)
		return core.Fail(coreerr.E("NewRedisBridge", "redis ping failed", err))
	}

	// Generate a unique source ID to prevent echo loops.
	sourceID := core.ID()

	bridge := &RedisBridge{
		hub:      hub,
		client:   client,
		prefix:   cfg.Prefix,
		sourceID: sourceID,
	}

	return core.Ok(bridge)
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

// Start subscribes the bridge to Redis pub/sub channels and launches the
// listener goroutine. Calling Start again replaces the active listener.
//
//	err := bridge.Start(ctx)
func (rb *RedisBridge) Start(ctx context.Context) core.Result {
	if rb == nil {
		return core.Fail(coreerr.E("RedisBridge.Start", "bridge must not be nil", nil))
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if r := rb.stopListener(); !r.OK {
		return r
	}

	rb.mu.RLock()
	client := rb.client
	prefix := rb.prefix
	rb.mu.RUnlock()
	if client == nil {
		return core.Fail(coreerr.E("RedisBridge.Start", "redis client is not available", nil))
	}
	if !validRedisPrefix(prefix) {
		return core.Fail(coreerr.E("RedisBridge.Start", "invalid redis prefix", nil))
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
		logCloseError("RedisBridge.Start.pubsub", pubsub)
		return core.Fail(coreerr.E("RedisBridge.Start", "redis subscribe failed", err))
	}

	rb.mu.Lock()
	rb.ctx = runCtx
	rb.cancel = cancel
	rb.pubsub = pubsub
	rb.mu.Unlock()

	rb.wg.Add(1)
	go rb.listen(runCtx, pubsub, prefix)

	return core.Ok(nil)
}

// Stop closes the Redis listener and client held by the bridge.
//
//	defer bridge.Stop()
func (rb *RedisBridge) Stop() core.Result {
	if rb == nil {
		return core.Ok(nil)
	}

	var firstErr error
	if r := rb.stopListener(); !r.OK {
		firstErr = r.Value.(error)
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

	if firstErr != nil {
		return core.Fail(firstErr)
	}
	return core.Ok(nil)
}

// PublishToChannel sends a message to local subscribers and publishes it to the
// Redis channel for the named hub channel.
//
//	err := bridge.PublishToChannel("notifications", ws.Message{Type: ws.TypeEvent, Data: "ready"})
func (rb *RedisBridge) PublishToChannel(channel string, msg Message) core.Result {
	if rb == nil {
		return core.Fail(coreerr.E("RedisBridge.PublishToChannel", "bridge must not be nil", nil))
	}

	if r := validateChannelTarget("RedisBridge.PublishToChannel", channel); !r.OK {
		return r
	}

	if rb.hub == nil {
		return core.Fail(coreerr.E("RedisBridge.PublishToChannel", "hub must not be nil", nil))
	}

	msg = stampServerMessage(msg)
	if !validRedisPublishMessage(msg) {
		return core.Fail(coreerr.E("RedisBridge.PublishToChannel", "invalid process ID", nil))
	}

	redisChan := rb.prefix + ":channel:" + channel
	if r := rb.hub.sendToChannelMessage(channel, msg, true); !r.OK {
		return r
	}

	return rb.publish(redisChan, msg)
}

// PublishBroadcast sends a message to local clients and publishes it to the
// Redis broadcast channel.
//
//	err := bridge.PublishBroadcast(ws.Message{Type: ws.TypeEvent, Data: "ready"})
func (rb *RedisBridge) PublishBroadcast(msg Message) core.Result {
	if rb == nil {
		return core.Fail(coreerr.E("RedisBridge.PublishBroadcast", "bridge must not be nil", nil))
	}
	if rb.hub == nil {
		return core.Fail(coreerr.E("RedisBridge.PublishBroadcast", "hub must not be nil", nil))
	}

	msg = stampServerMessage(msg)
	if !validRedisPublishMessage(msg) {
		return core.Fail(coreerr.E("RedisBridge.PublishBroadcast", "invalid process ID", nil))
	}

	local := rb.hub.broadcastMessage(msg, true)
	redisChan := rb.prefix + ":broadcast"
	redisResult := rb.publish(redisChan, msg)

	if !local.OK && !redisResult.OK {
		return core.Fail(coreerr.E("RedisBridge.PublishBroadcast", core.Sprintf("local: %v; redis: %v", local.Value, redisResult.Value), redisResult.Value.(error)))
	}
	if !redisResult.OK {
		return redisResult
	}

	return local
}

// publish serialises the envelope and publishes to the given Redis channel.
func (rb *RedisBridge) publish(redisChan string, msg Message) core.Result {
	if rb == nil {
		return core.Fail(coreerr.E("RedisBridge.publish", "bridge must not be nil", nil))
	}

	rb.mu.RLock()
	ctx := rb.ctx
	client := rb.client
	sourceID := rb.sourceID
	rb.mu.RUnlock()

	if ctx == nil {
		return core.Fail(coreerr.E("RedisBridge.publish", "bridge has not been started", nil))
	}

	if client == nil {
		return core.Fail(coreerr.E("RedisBridge.publish", "redis client is not available", nil))
	}

	if !validRedisPublishMessage(msg) {
		return core.Fail(coreerr.E("RedisBridge.publish", "invalid process ID", nil))
	}

	env := redisEnvelope{
		SourceID: sourceID,
		Message:  msg,
	}

	r := core.JSONMarshal(env)
	if !r.OK {
		return core.Fail(coreerr.E("RedisBridge.publish", "failed to marshal redis envelope", nil))
	}

	if !validRedisPrefix(rb.prefix) {
		return core.Fail(coreerr.E("RedisBridge.publish", "invalid redis prefix", nil))
	}

	publishCtx, cancel := context.WithTimeout(ctx, redisPublishTimeout)
	defer cancel()

	return core.ResultOf(nil, client.Publish(publishCtx, redisChan, r.Value.([]byte)).Err())
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
				if r := rb.hub.broadcastMessage(env.Message, true); !r.OK {
					coreerr.Warn("failed to forward redis broadcast", "op", "RedisBridge.listen", "err", r.Error())
				}

			case core.HasPrefix(redisMsg.Channel, channelPrefix):
				if rb.hub == nil {
					continue
				}
				// Extract the Hub channel name from the Redis channel.
				hubChannel := core.TrimPrefix(redisMsg.Channel, channelPrefix)
				if r := validateChannelTarget("RedisBridge.listen", hubChannel); !r.OK {
					continue
				}
				if r := rb.hub.sendToChannelMessage(hubChannel, env.Message, true); !r.OK {
					coreerr.Warn("failed to forward redis channel message", "op", "RedisBridge.listen", "err", r.Error())
				}
			}
		}
	}
}

func (rb *RedisBridge) stopListener() core.Result {
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

	return core.ResultOf(nil, err)
}

// SourceID returns the bridge instance ID used to suppress self-echoed Redis
// messages.
//
//	sourceID := bridge.SourceID()
func (rb *RedisBridge) SourceID() string {
	if rb == nil {
		return ""
	}

	return rb.sourceID
}
