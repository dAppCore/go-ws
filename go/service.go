// SPDX-License-Identifier: EUPL-1.2

// Service registration for the ws package. Exposes the *Hub surface
// as a Core service with action handlers so consumers can wire
// websocket broadcast/send/stats operations through the same plumbing
// as every other core service.
//
// Usage example: `c, _ := core.New(core.WithName("ws", ws.NewService(ws.HubConfig{})))`

package ws

import (
	"context"
	"time"

	core "dappco.re/go"
)

// Service is the registerable handle for the ws package — embeds
// *core.ServiceRuntime[HubConfig] for typed options access and holds
// a live *Hub ready for direct method calls or action use.
//
// Usage example: `svc := core.MustServiceFor[*ws.Service](c, "ws"); svc.Hub.Broadcast(msg)`
type Service struct {
	*core.ServiceRuntime[HubConfig]
	// Hub is the live *Hub the service was constructed with.
	// Usage example: `svc.Hub.Broadcast(ws.Message{Type: ws.MessageTypeEvent})`
	Hub           *Hub
	registrations core.Once
	cancel        context.CancelFunc
}

// NewService returns a factory that constructs the websocket hub and
// produces a *Service ready for c.Service() registration. Use through
// core.WithName so the framework wires lifecycle (OnStartup runs the
// hub event loop in the background, OnShutdown stops it).
//
// Usage example: `c, _ := core.New(core.WithName("ws", ws.NewService(ws.HubConfig{})))`
func NewService(config HubConfig) func(*core.Core) core.Result {
	return func(c *core.Core) core.Result {
		return core.Ok(&Service{
			ServiceRuntime: core.NewServiceRuntime(c, config),
			Hub:            NewHubWithConfig(config),
		})
	}
}

// OnStartup registers the ws action handlers on the attached Core and
// starts the hub event loop in a background goroutine. Implements
// core.Startable. Idempotent via core.Once.
//
// Usage example: `r := svc.OnStartup(ctx)`
func (s *Service) OnStartup(ctx context.Context) core.Result {
	if s == nil {
		return core.Ok(nil)
	}
	s.registrations.Do(func() {
		c := s.Core()
		if c == nil {
			return
		}
		c.Action("ws.broadcast", s.handleBroadcast)
		c.Action("ws.send_channel", s.handleSendChannel)
		c.Action("ws.send_event", s.handleSendEvent)
		c.Action("ws.send_error", s.handleSendError)
		c.Action("ws.send_process_output", s.handleSendProcessOutput)
		c.Action("ws.send_process_status", s.handleSendProcessStatus)
		c.Action("ws.client_count", s.handleClientCount)
		c.Action("ws.channel_count", s.handleChannelCount)
		c.Action("ws.subscriber_count", s.handleSubscriberCount)
		c.Action("ws.stats", s.handleStats)

		hubCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		go s.Hub.Run(hubCtx)
	})
	return core.Ok(nil)
}

// OnShutdown stops the hub event loop. Implements core.Stoppable.
//
// Usage example: `r := svc.OnShutdown(ctx)`
func (s *Service) OnShutdown(context.Context) core.Result {
	if s == nil {
		return core.Ok(nil)
	}
	if s.cancel != nil {
		s.cancel()
	}
	return core.Ok(nil)
}

// messageFromOpts builds a Message from opts.type + opts.channel +
// opts.process_id + opts.data. Caller may omit fields that aren't
// relevant to the message type.
//
// Usage example: `msgR := messageFromOpts(opts)`
func messageFromOpts(opts core.Options) Message {
	return Message{
		Type:      MessageType(opts.String("type")),
		Channel:   opts.String("channel"),
		ProcessID: opts.String("process_id"),
		Data:      opts.Get("data").Value,
		Timestamp: time.Now(),
	}
}

// handleBroadcast — `ws.broadcast` action handler. Reads opts.type +
// opts.channel + opts.process_id + opts.data and broadcasts the
// resulting Message to every connected client.
//
//	r := c.Action("ws.broadcast").Run(ctx, core.NewOptions(
//	    core.Option{Key: "type", Value: "event"},
//	    core.Option{Key: "data", Value: map[string]any{"event": "ready"}},
//	))
func (s *Service) handleBroadcast(_ core.Context, opts core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.broadcast", "service not initialised", nil))
	}
	return s.Hub.Broadcast(messageFromOpts(opts))
}

// handleSendChannel — `ws.send_channel` action handler. Reads
// opts.channel and the message-shape opts (type/process_id/data) and
// sends the Message to subscribers of the channel.
//
//	r := c.Action("ws.send_channel").Run(ctx, core.NewOptions(
//	    core.Option{Key: "channel", Value: "alerts"},
//	    core.Option{Key: "type", Value: "event"},
//	))
func (s *Service) handleSendChannel(_ core.Context, opts core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.send_channel", "service not initialised", nil))
	}
	channel := opts.String("channel")
	if channel == "" {
		return core.Fail(core.E("ws.send_channel", "channel is required", nil))
	}
	return s.Hub.SendToChannel(channel, messageFromOpts(opts))
}

// handleSendEvent — `ws.send_event` action handler. Reads opts.event
// (string) + opts.data (any) and broadcasts an event-type Message.
//
//	r := c.Action("ws.send_event").Run(ctx, core.NewOptions(
//	    core.Option{Key: "event", Value: "ready"},
//	    core.Option{Key: "data", Value: 42},
//	))
func (s *Service) handleSendEvent(_ core.Context, opts core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.send_event", "service not initialised", nil))
	}
	event := opts.String("event")
	if event == "" {
		return core.Fail(core.E("ws.send_event", "event is required", nil))
	}
	return s.Hub.SendEvent(event, opts.Get("data").Value)
}

// handleSendError — `ws.send_error` action handler. Reads opts.message
// and broadcasts an error-type Message.
//
//	r := c.Action("ws.send_error").Run(ctx, core.NewOptions(
//	    core.Option{Key: "message", Value: "auth failed"},
//	))
func (s *Service) handleSendError(_ core.Context, opts core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.send_error", "service not initialised", nil))
	}
	message := opts.String("message")
	if message == "" {
		return core.Fail(core.E("ws.send_error", "message is required", nil))
	}
	return s.Hub.SendError(message)
}

// handleSendProcessOutput — `ws.send_process_output` action handler.
// Reads opts.process_id + opts.output and broadcasts a process-output
// Message.
//
//	r := c.Action("ws.send_process_output").Run(ctx, core.NewOptions(
//	    core.Option{Key: "process_id", Value: "proc-1"},
//	    core.Option{Key: "output", Value: "hello\n"},
//	))
func (s *Service) handleSendProcessOutput(_ core.Context, opts core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.send_process_output", "service not initialised", nil))
	}
	pid := opts.String("process_id")
	if pid == "" {
		return core.Fail(core.E("ws.send_process_output", "process_id is required", nil))
	}
	return s.Hub.SendProcessOutput(pid, opts.String("output"))
}

// handleSendProcessStatus — `ws.send_process_status` action handler.
// Reads opts.process_id + opts.status + opts.exit_code and broadcasts
// a process-status Message.
//
//	r := c.Action("ws.send_process_status").Run(ctx, core.NewOptions(
//	    core.Option{Key: "process_id", Value: "proc-1"},
//	    core.Option{Key: "status", Value: "exited"},
//	    core.Option{Key: "exit_code", Value: 0},
//	))
func (s *Service) handleSendProcessStatus(_ core.Context, opts core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.send_process_status", "service not initialised", nil))
	}
	pid := opts.String("process_id")
	if pid == "" {
		return core.Fail(core.E("ws.send_process_status", "process_id is required", nil))
	}
	return s.Hub.SendProcessStatus(pid, opts.String("status"), opts.Int("exit_code"))
}

// handleClientCount — `ws.client_count` action handler. Returns the
// number of currently-connected clients in r.Value.
//
//	r := c.Action("ws.client_count").Run(ctx, core.NewOptions())
//	count, _ := r.Value.(int)
func (s *Service) handleClientCount(_ core.Context, _ core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.client_count", "service not initialised", nil))
	}
	return core.Ok(s.Hub.ClientCount())
}

// handleChannelCount — `ws.channel_count` action handler. Returns the
// number of active channels in r.Value.
//
//	r := c.Action("ws.channel_count").Run(ctx, core.NewOptions())
//	count, _ := r.Value.(int)
func (s *Service) handleChannelCount(_ core.Context, _ core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.channel_count", "service not initialised", nil))
	}
	return core.Ok(s.Hub.ChannelCount())
}

// handleSubscriberCount — `ws.subscriber_count` action handler. Reads
// opts.channel and returns subscriber count for that channel in
// r.Value.
//
//	r := c.Action("ws.subscriber_count").Run(ctx, core.NewOptions(
//	    core.Option{Key: "channel", Value: "alerts"},
//	))
func (s *Service) handleSubscriberCount(_ core.Context, opts core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.subscriber_count", "service not initialised", nil))
	}
	channel := opts.String("channel")
	if channel == "" {
		return core.Fail(core.E("ws.subscriber_count", "channel is required", nil))
	}
	return core.Ok(s.Hub.ChannelSubscriberCount(channel))
}

// handleStats — `ws.stats` action handler. Returns the current HubStats
// snapshot in r.Value.
//
//	r := c.Action("ws.stats").Run(ctx, core.NewOptions())
//	stats, _ := r.Value.(HubStats)
func (s *Service) handleStats(_ core.Context, _ core.Options) core.Result {
	if s == nil || s.Hub == nil {
		return core.Fail(core.E("ws.stats", "service not initialised", nil))
	}
	return core.Ok(s.Hub.Stats())
}
