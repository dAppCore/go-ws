// SPDX-License-Identifier: EUPL-1.2

package ws

import (
	core "dappco.re/go"
)

// nilService returns a typed-nil *Service for exercising the
// "service not initialised" guard each handler carries.
func nilService() *Service {
	var s *Service
	return s
}

// --- messageFromOpts ---

func TestServiceHandlers_messageFromOpts_Good(t *core.T) {
	opts := core.NewOptions(
		core.Option{Key: "type", Value: "event"},
		core.Option{Key: "channel", Value: "alerts"},
		core.Option{Key: "process_id", Value: "proc-1"},
		core.Option{Key: "data", Value: map[string]any{"event": "ready"}},
	)
	msg := messageFromOpts(opts)
	core.AssertEqual(t, TypeEvent, msg.Type)
	core.AssertEqual(t, "alerts", msg.Channel)
	core.AssertEqual(t, "proc-1", msg.ProcessID)
	core.AssertNotNil(t, msg.Data)
	core.AssertTrue(t, !msg.Timestamp.IsZero())
}

func TestServiceHandlers_messageFromOpts_Bad(t *core.T) {
	// Missing fields default to their zero values rather than panicking.
	msg := messageFromOpts(core.NewOptions())
	core.AssertEqual(t, MessageType(""), msg.Type)
	core.AssertEqual(t, "", msg.Channel)
	core.AssertEqual(t, "", msg.ProcessID)
	core.AssertTrue(t, msg.Data == nil)
}

func TestServiceHandlers_messageFromOpts_Ugly(t *core.T) {
	// A type-only message still produces a usable, timestamped Message.
	msg := messageFromOpts(core.NewOptions(core.Option{Key: "type", Value: "error"}))
	core.AssertEqual(t, TypeError, msg.Type)
	core.AssertTrue(t, !msg.Timestamp.IsZero())
}

// --- handleBroadcast ---

func TestServiceHandlers_handleBroadcast_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleBroadcast(nil, core.NewOptions(
		core.Option{Key: "type", Value: "event"},
		core.Option{Key: "data", Value: "hello"},
	))
	core.AssertTrue(t, r.OK)
}

func TestServiceHandlers_handleBroadcast_Bad(t *core.T) {
	r := nilService().handleBroadcast(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleBroadcast_Ugly(t *core.T) {
	// Hub present but no clients: a broadcast to nobody still succeeds.
	svc := serviceForTest(t)
	r := svc.handleBroadcast(nil, core.NewOptions())
	core.AssertTrue(t, r.OK)
}

// --- handleSendChannel ---

func TestServiceHandlers_handleSendChannel_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendChannel(nil, core.NewOptions(
		core.Option{Key: "channel", Value: "alerts"},
		core.Option{Key: "type", Value: "event"},
	))
	core.AssertTrue(t, r.OK)
}

func TestServiceHandlers_handleSendChannel_Bad(t *core.T) {
	r := nilService().handleSendChannel(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleSendChannel_Ugly(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendChannel(nil, core.NewOptions(core.Option{Key: "type", Value: "event"}))
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "channel is required"))
}

// --- handleSendEvent ---

func TestServiceHandlers_handleSendEvent_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendEvent(nil, core.NewOptions(
		core.Option{Key: "event", Value: "ready"},
		core.Option{Key: "data", Value: 42},
	))
	core.AssertTrue(t, r.OK)
}

func TestServiceHandlers_handleSendEvent_Bad(t *core.T) {
	r := nilService().handleSendEvent(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleSendEvent_Ugly(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendEvent(nil, core.NewOptions(core.Option{Key: "data", Value: 1}))
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "event is required"))
}

// --- handleSendError ---

func TestServiceHandlers_handleSendError_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendError(nil, core.NewOptions(
		core.Option{Key: "message", Value: "auth failed"},
	))
	core.AssertTrue(t, r.OK)
}

func TestServiceHandlers_handleSendError_Bad(t *core.T) {
	r := nilService().handleSendError(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleSendError_Ugly(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendError(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "message is required"))
}

// --- handleSendProcessOutput ---

func TestServiceHandlers_handleSendProcessOutput_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendProcessOutput(nil, core.NewOptions(
		core.Option{Key: "process_id", Value: "proc-1"},
		core.Option{Key: "output", Value: "hello\n"},
	))
	core.AssertTrue(t, r.OK)
}

func TestServiceHandlers_handleSendProcessOutput_Bad(t *core.T) {
	r := nilService().handleSendProcessOutput(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleSendProcessOutput_Ugly(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendProcessOutput(nil, core.NewOptions(core.Option{Key: "output", Value: "x"}))
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "process_id is required"))
}

// --- handleSendProcessStatus ---

func TestServiceHandlers_handleSendProcessStatus_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendProcessStatus(nil, core.NewOptions(
		core.Option{Key: "process_id", Value: "proc-1"},
		core.Option{Key: "status", Value: "exited"},
		core.Option{Key: "exit_code", Value: 0},
	))
	core.AssertTrue(t, r.OK)
}

func TestServiceHandlers_handleSendProcessStatus_Bad(t *core.T) {
	r := nilService().handleSendProcessStatus(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleSendProcessStatus_Ugly(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSendProcessStatus(nil, core.NewOptions(core.Option{Key: "status", Value: "exited"}))
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "process_id is required"))
}

// --- handleClientCount ---

func TestServiceHandlers_handleClientCount_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleClientCount(nil, core.NewOptions())
	core.AssertTrue(t, r.OK)
	count, ok := r.Value.(int)
	core.AssertTrue(t, ok)
	core.AssertEqual(t, 0, count)
}

func TestServiceHandlers_handleClientCount_Bad(t *core.T) {
	r := nilService().handleClientCount(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleClientCount_Ugly(t *core.T) {
	// Repeated reads on a fresh hub stay at zero and keep succeeding.
	svc := serviceForTest(t)
	first := svc.handleClientCount(nil, core.NewOptions())
	second := svc.handleClientCount(nil, core.NewOptions())
	core.AssertTrue(t, first.OK)
	core.AssertTrue(t, second.OK)
	core.AssertEqual(t, first.Value.(int), second.Value.(int))
}

// --- handleChannelCount ---

func TestServiceHandlers_handleChannelCount_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleChannelCount(nil, core.NewOptions())
	core.AssertTrue(t, r.OK)
	count, ok := r.Value.(int)
	core.AssertTrue(t, ok)
	core.AssertEqual(t, 0, count)
}

func TestServiceHandlers_handleChannelCount_Bad(t *core.T) {
	r := nilService().handleChannelCount(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleChannelCount_Ugly(t *core.T) {
	svc := serviceForTest(t)
	first := svc.handleChannelCount(nil, core.NewOptions())
	second := svc.handleChannelCount(nil, core.NewOptions())
	core.AssertTrue(t, first.OK)
	core.AssertEqual(t, first.Value.(int), second.Value.(int))
}

// --- handleSubscriberCount ---

func TestServiceHandlers_handleSubscriberCount_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSubscriberCount(nil, core.NewOptions(
		core.Option{Key: "channel", Value: "alerts"},
	))
	core.AssertTrue(t, r.OK)
	count, ok := r.Value.(int)
	core.AssertTrue(t, ok)
	core.AssertEqual(t, 0, count)
}

func TestServiceHandlers_handleSubscriberCount_Bad(t *core.T) {
	r := nilService().handleSubscriberCount(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleSubscriberCount_Ugly(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleSubscriberCount(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "channel is required"))
}

// --- handleStats ---

func TestServiceHandlers_handleStats_Good(t *core.T) {
	svc := serviceForTest(t)
	r := svc.handleStats(nil, core.NewOptions())
	core.AssertTrue(t, r.OK)
	stats, ok := r.Value.(HubStats)
	core.AssertTrue(t, ok)
	core.AssertEqual(t, 0, stats.Clients)
	core.AssertEqual(t, 0, stats.Channels)
	core.AssertEqual(t, 0, stats.Subscribers)
}

func TestServiceHandlers_handleStats_Bad(t *core.T) {
	r := nilService().handleStats(nil, core.NewOptions())
	core.AssertFalse(t, r.OK)
	core.AssertTrue(t, testContains(r.Error(), "service not initialised"))
}

func TestServiceHandlers_handleStats_Ugly(t *core.T) {
	svc := serviceForTest(t)
	first := svc.handleStats(nil, core.NewOptions())
	second := svc.handleStats(nil, core.NewOptions())
	core.AssertTrue(t, first.OK)
	core.AssertEqual(t, first.Value.(HubStats), second.Value.(HubStats))
}
