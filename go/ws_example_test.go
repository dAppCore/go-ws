// SPDX-Licence-Identifier: EUPL-1.2

package ws

import core "dappco.re/go"

func ExampleDefaultHubConfig() {
	config := DefaultHubConfig()
	core.Println(config.HeartbeatInterval, config.MaxSubscriptionsPerClient)
	// Output: 30s 1024
}

func ExampleNewHub() {
	hub := NewHub()
	core.Println(hub.ClientCount(), hub.ChannelCount())
	// Output: 0 0
}

func ExampleNewHubWithConfig() {
	hub := NewHubWithConfig(HubConfig{MaxSubscriptionsPerClient: 2})
	core.Println(hub.config.MaxSubscriptionsPerClient)
	// Output: 2
}

func ExampleHub_Run() {
	hub := NewHub()
	ctx, cancel := core.WithCancel(core.Background())
	cancel()
	hub.Run(ctx)
	core.Println(hub.isRunning())
	// Output: false
}

func ExampleHub_Subscribe() {
	hub := NewHub()
	client := &Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}
	r := hub.Subscribe(client, "events")
	core.Println(r.OK, hub.ChannelSubscriberCount("events"))
	// Output: true 1
}

func ExampleHub_Unsubscribe() {
	hub := NewHub()
	client := &Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}
	_ = hub.Subscribe(client, "events")
	hub.Unsubscribe(client, "events")
	core.Println(hub.ChannelSubscriberCount("events"))
	// Output: 0
}

func ExampleHub_Broadcast() {
	hub := NewHub()
	r := hub.Broadcast(Message{Type: TypeEvent, Data: "ready"})
	core.Println(r.OK, len(hub.broadcast))
	// Output: true 1
}

func ExampleHub_SendToChannel() {
	hub := NewHub()
	client := &Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}
	_ = hub.Subscribe(client, "events")
	r := hub.SendToChannel("events", Message{Type: TypeEvent, Data: "ready"})
	core.Println(r.OK, len(client.send))
	// Output: true 1
}

func ExampleHub_SendProcessOutput() {
	hub := NewHub()
	client := &Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}
	_ = hub.Subscribe(client, "process:build-1")
	r := hub.SendProcessOutput("build-1", "compiled")
	core.Println(r.OK, len(client.send))
	// Output: true 1
}

func ExampleHub_SendProcessStatus() {
	hub := NewHub()
	client := &Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}
	_ = hub.Subscribe(client, "process:build-1")
	r := hub.SendProcessStatus("build-1", "exited", 0)
	core.Println(r.OK, len(client.send))
	// Output: true 1
}

func ExampleHub_SendError() {
	hub := NewHub()
	r := hub.SendError("upgrade rejected")
	core.Println(r.OK, len(hub.broadcast))
	// Output: true 1
}

func ExampleHub_SendEvent() {
	hub := NewHub()
	r := hub.SendEvent("agent.ready", "payload")
	core.Println(r.OK, len(hub.broadcast))
	// Output: true 1
}

func ExampleHub_ClientCount() {
	hub := NewHub()
	hub.clients[&Client{UserID: "user-1"}] = true
	core.Println(hub.ClientCount())
	// Output: 1
}

func ExampleHub_ChannelCount() {
	hub := NewHub()
	_ = hub.Subscribe(&Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}, "events")
	core.Println(hub.ChannelCount())
	// Output: 1
}

func ExampleHub_ChannelSubscriberCount() {
	hub := NewHub()
	_ = hub.Subscribe(&Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}, "events")
	core.Println(hub.ChannelSubscriberCount("events"))
	// Output: 1
}

func ExampleHub_AllClients() {
	hub := NewHub()
	hub.clients[&Client{UserID: "beta"}] = true
	hub.clients[&Client{UserID: "alpha"}] = true
	for client := range hub.AllClients() {
		core.Println(client.UserID)
	}
	// Output:
	// alpha
	// beta
}

func ExampleHub_AllChannels() {
	hub := NewHub()
	_ = hub.Subscribe(&Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}, "beta")
	_ = hub.Subscribe(&Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}, "alpha")
	for channel := range hub.AllChannels() {
		core.Println(channel)
	}
	// Output:
	// alpha
	// beta
}

func ExampleHub_Stats() {
	hub := NewHub()
	client := &Client{send: make(chan []byte, 1), subscriptions: make(map[string]bool)}
	hub.clients[client] = true
	_ = hub.Subscribe(client, "events")
	stats := hub.Stats()
	core.Println(stats.Clients, stats.Channels, stats.Subscribers)
	// Output: 1 1 1
}

func ExampleHub_HandleWebSocket() {
	var hub *Hub
	recorder := core.NewHTTPTestRecorder()
	request := core.NewHTTPTestRequest("GET", "/ws", nil)
	hub.HandleWebSocket(recorder, request)
	core.Println(recorder.Code)
	// Output: 503
}

func ExampleHub_Handler() {
	var hub *Hub
	handler := hub.Handler()
	recorder := core.NewHTTPTestRecorder()
	request := core.NewHTTPTestRequest("GET", "/ws", nil)
	handler(recorder, request)
	core.Println(recorder.Code)
	// Output: 503
}

func ExampleClient_Subscriptions() {
	client := &Client{subscriptions: map[string]bool{"beta": true, "alpha": true}}
	core.Println(core.Join(",", client.Subscriptions()...))
	// Output: alpha,beta
}

func ExampleClient_AllSubscriptions() {
	client := &Client{subscriptions: map[string]bool{"beta": true, "alpha": true}}
	for channel := range client.AllSubscriptions() {
		core.Println(channel)
	}
	// Output:
	// alpha
	// beta
}

func ExampleClient_Close() {
	var client *Client
	r := client.Close()
	core.Println(r.OK)
	// Output: true
}

func ExampleNewReconnectingClient() {
	client := NewReconnectingClient(ReconnectConfig{URL: "ws://example.invalid/ws"})
	core.Println(client.State())
	// Output: 0
}

func ExampleReconnectingClient_Connect() {
	var client *ReconnectingClient
	r := client.Connect(core.Background())
	core.Println(!r.OK)
	// Output: true
}

func ExampleReconnectingClient_Send() {
	var client *ReconnectingClient
	r := client.Send(Message{Type: TypePing})
	core.Println(!r.OK)
	// Output: true
}

func ExampleReconnectingClient_State() {
	var client *ReconnectingClient
	core.Println(client.State())
	// Output: 0
}

func ExampleReconnectingClient_Close() {
	var client *ReconnectingClient
	r := client.Close()
	core.Println(r.OK)
	// Output: true
}
