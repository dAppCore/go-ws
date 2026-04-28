// SPDX-Licence-Identifier: EUPL-1.2

package ws

import core "dappco.re/go"

type T = core.T
type CancelFunc = core.CancelFunc
type HTTPTestServer = core.HTTPTestServer
type Request = core.Request
type Response = core.Response
type Listener = core.Listener
type Conn = core.Conn
type BufReader = core.BufReader
type HandlerFunc = core.HandlerFunc

const (
	Millisecond = core.Millisecond
	Second      = core.Second
)

var (
	AnError             = core.AnError
	Atoi                = core.Atoi
	AssertContains      = core.AssertContains
	AssertEmpty         = core.AssertEmpty
	AssertEqual         = core.AssertEqual
	AssertError         = core.AssertError
	AssertErrorIs       = core.AssertErrorIs
	AssertFalse         = core.AssertFalse
	AssertNil           = core.AssertNil
	AssertNoError       = core.AssertNoError
	AssertNotEmpty      = core.AssertNotEmpty
	AssertNotNil        = core.AssertNotNil
	AssertNotPanics     = core.AssertNotPanics
	AssertTrue          = core.AssertTrue
	Background          = core.Background
	Concat              = core.Concat
	Errorf              = core.Errorf
	HasPrefix           = core.HasPrefix
	HTTPGet             = core.HTTPGet
	Itoa                = core.Itoa
	JSONUnmarshal       = core.JSONUnmarshal
	NetListen           = core.NetListen
	NewBufReader        = core.NewBufReader
	NewHTTPTestRecorder = core.NewHTTPTestRecorder
	NewHTTPTestRequest  = core.NewHTTPTestRequest
	NewHTTPTestServer   = core.NewHTTPTestServer
	Now                 = core.Now
	RequireNoError      = core.RequireNoError
	RequireTrue         = core.RequireTrue
	Sleep               = core.Sleep
	Trim                = core.Trim
	TrimPrefix          = core.TrimPrefix
	Upper               = core.Upper
	WithCancel          = core.WithCancel
	WithTimeout         = core.WithTimeout
	WriteString         = core.WriteString
)

func ax7Client() *Client {
	return &Client{
		send:          make(chan []byte, 16),
		subscriptions: make(map[string]bool),
	}
}

func ax7Eventually(condition func() bool) bool {
	deadline := Now().Add(Second)
	for Now().Before(deadline) {
		if condition() {
			return true
		}
		Sleep(5 * Millisecond)
	}
	return condition()
}

func ax7StartHub(t *T) (*Hub, CancelFunc) {
	hub := NewHub()
	ctx, cancel := WithCancel(Background())
	go hub.Run(ctx)
	RequireTrue(t, ax7Eventually(func() bool { return hub.isRunning() }))
	t.Cleanup(cancel)
	return hub, cancel
}

func ax7StartWSServer(t *T, config HubConfig) (*Hub, *HTTPTestServer) {
	hub := NewHubWithConfig(config)
	ctx, cancel := WithCancel(Background())
	go hub.Run(ctx)
	RequireTrue(t, ax7Eventually(func() bool { return hub.isRunning() }))
	server := NewHTTPTestServer(hub.Handler())
	t.Cleanup(func() {
		server.Close()
		cancel()
	})
	return hub, server
}

func ax7WSURL(server *HTTPTestServer) string {
	return Concat("ws", TrimPrefix(server.URL, "http"))
}

func ax7BroadcastMessage(t *T, hub *Hub) Message {
	timeout, cancel := WithTimeout(Background(), Second)
	defer cancel()
	select {
	case raw := <-hub.broadcast:
		var msg Message
		RequireTrue(t, JSONUnmarshal(raw, &msg).OK)
		return msg
	case <-timeout.Done():
		t.Fatal("timed out waiting for broadcast")
		return Message{}
	}
}

func ax7ClientMessage(t *T, client *Client) Message {
	timeout, cancel := WithTimeout(Background(), Second)
	defer cancel()
	select {
	case raw := <-client.send:
		var msg Message
		RequireTrue(t, JSONUnmarshal(raw, &msg).OK)
		return msg
	case <-timeout.Done():
		t.Fatal("timed out waiting for client message")
		return Message{}
	}
}

func ax7AuthRequest(header string) *Request {
	req := NewHTTPTestRequest("GET", "/ws", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return req
}

func ax7StartRedis(t *T) string {
	r := NetListen("tcp", "127.0.0.1:0")
	RequireTrue(t, r.OK)
	listener := r.Value.(Listener)
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			AssertContains(t, err.Error(), "closed")
		}
	})
	go ax7AcceptRedis(listener)
	return listener.Addr().String()
}

func ax7AcceptRedis(listener Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go ax7ServeRedis(conn)
	}
}

func ax7ServeRedis(conn Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			return
		}
	}()

	reader := NewBufReader(conn)
	for {
		parts, err := ax7ReadRedisCommand(reader)
		if err != nil {
			return
		}
		if len(parts) == 0 {
			continue
		}

		switch Upper(parts[0]) {
		case "HELLO":
			if !ax7WriteRedis(conn, "%7\r\n+server\r\n+redis\r\n+version\r\n+7.2.0\r\n+proto\r\n:3\r\n+id\r\n:1\r\n+mode\r\n+standalone\r\n+role\r\n+master\r\n+modules\r\n*0\r\n") {
				return
			}
		case "PING":
			if !ax7WriteRedis(conn, "+PONG\r\n") {
				return
			}
		case "CLIENT", "SELECT", "READONLY":
			if !ax7WriteRedis(conn, "+OK\r\n") {
				return
			}
		case "PSUBSCRIBE":
			pattern := ""
			if len(parts) > 1 {
				pattern = parts[1]
			}
			if !ax7WriteRedis(conn, Concat("*3\r\n$10\r\npsubscribe\r\n$", Itoa(len(pattern)), "\r\n", pattern, "\r\n:1\r\n")) {
				return
			}
		case "PUBLISH":
			if !ax7WriteRedis(conn, ":1\r\n") {
				return
			}
		case "QUIT":
			if !ax7WriteRedis(conn, "+OK\r\n") {
				return
			}
			return
		default:
			if !ax7WriteRedis(conn, "+OK\r\n") {
				return
			}
		}
	}
}

func ax7ReadRedisCommand(reader *BufReader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = Trim(line)
	if !HasPrefix(line, "*") {
		return nil, Errorf("unexpected redis frame: %s", line)
	}

	count := Atoi(TrimPrefix(line, "*"))
	if !count.OK {
		return nil, count.Value.(error)
	}

	parts := make([]string, 0, count.Value.(int))
	for i := 0; i < count.Value.(int); i++ {
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, err
		}
		value, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		parts = append(parts, Trim(value))
	}
	return parts, nil
}

func ax7WriteRedis(conn Conn, payload string) bool {
	return WriteString(conn, payload).OK
}

// --- DefaultHubConfig ---

func TestAX7_DefaultHubConfig_Good(t *T) {
	cfg := DefaultHubConfig()
	AssertEqual(t, DefaultHeartbeatInterval, cfg.HeartbeatInterval)
	AssertEqual(t, DefaultPongTimeout, cfg.PongTimeout)
	AssertEqual(t, DefaultWriteTimeout, cfg.WriteTimeout)
	AssertEqual(t, DefaultMaxSubscriptionsPerClient, cfg.MaxSubscriptionsPerClient)
}

func TestAX7_DefaultHubConfig_Bad(t *T) {
	cfg := DefaultHubConfig()
	AssertNil(t, cfg.Authenticator)
	AssertNil(t, cfg.ChannelAuthoriser)
	AssertNil(t, cfg.CheckOrigin)
}

func TestAX7_DefaultHubConfig_Ugly(t *T) {
	cfg := DefaultHubConfig()
	cfg.AllowedOrigins = append(cfg.AllowedOrigins, "https://app.example")
	again := DefaultHubConfig()
	AssertEmpty(t, again.AllowedOrigins)
}

// --- Hub construction and loop ---

func TestAX7_NewHub_Good(t *T) {
	hub := NewHub()
	AssertNotNil(t, hub)
	AssertNotNil(t, hub.clients)
	AssertNotNil(t, hub.broadcast)
	AssertNotNil(t, hub.channels)
}

func TestAX7_NewHub_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, DefaultHeartbeatInterval, hub.config.HeartbeatInterval)
	AssertEqual(t, DefaultPongTimeout, hub.config.PongTimeout)
	AssertEqual(t, DefaultMaxSubscriptionsPerClient, hub.config.MaxSubscriptionsPerClient)
}

func TestAX7_NewHub_Ugly(t *T) {
	hub := NewHub()
	req := NewHTTPTestRequest("GET", "http://evil.example/ws", nil)
	req.Header.Set("Origin", "https://evil.example")
	AssertTrue(t, hub.config.CheckOrigin(req))
}

func TestAX7_Hub_Run_Good(t *T) {
	hub, cancel := ax7StartHub(t)
	AssertTrue(t, hub.isRunning())
	cancel()
	AssertTrue(t, ax7Eventually(func() bool { return !hub.isRunning() }))
}

func TestAX7_Hub_Run_Bad(t *T) {
	var hub *Hub
	AssertNotPanics(t, func() {
		hub.Run(Background())
	})
	AssertFalse(t, hub.isRunning())
}

func TestAX7_Hub_Run_Ugly(t *T) {
	hub := NewHub()
	ctx, cancel := WithCancel(Background())
	cancel()
	hub.Run(ctx)
	AssertFalse(t, hub.isRunning())
}

// --- Hub subscriptions and delivery ---

func TestAX7_Hub_Subscribe_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	err := hub.Subscribe(client, "agent.dispatch")
	AssertNoError(t, err)
	AssertEqual(t, 1, hub.ChannelSubscriberCount("agent.dispatch"))
}

func TestAX7_Hub_Subscribe_Bad(t *T) {
	hub := NewHub()
	client := ax7Client()
	err := hub.Subscribe(client, " agent.dispatch")
	AssertError(t, err, "invalid channel")
	AssertEmpty(t, client.Subscriptions())
}

func TestAX7_Hub_Subscribe_Ugly(t *T) {
	var hub *Hub
	client := ax7Client()
	err := hub.Subscribe(client, "agent.dispatch")
	AssertError(t, err, "hub must not be nil")
	AssertEmpty(t, client.Subscriptions())
}

func TestAX7_Hub_Unsubscribe_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	RequireNoError(t, hub.Subscribe(client, "agent.dispatch"))
	hub.Unsubscribe(client, "agent.dispatch")
	AssertEqual(t, 0, hub.ChannelSubscriberCount("agent.dispatch"))
}

func TestAX7_Hub_Unsubscribe_Bad(t *T) {
	hub := NewHub()
	client := ax7Client()
	RequireNoError(t, hub.Subscribe(client, "agent.dispatch"))
	hub.Unsubscribe(client, " agent.dispatch")
	AssertEqual(t, 1, hub.ChannelSubscriberCount("agent.dispatch"))
}

func TestAX7_Hub_Unsubscribe_Ugly(t *T) {
	var hub *Hub
	client := ax7Client()
	AssertNotPanics(t, func() {
		hub.Unsubscribe(client, "agent.dispatch")
	})
	AssertEmpty(t, client.Subscriptions())
}

func TestAX7_Hub_Broadcast_Good(t *T) {
	hub := NewHub()
	err := hub.Broadcast(Message{Type: TypeEvent, Data: "ready"})
	AssertNoError(t, err)
	msg := ax7BroadcastMessage(t, hub)
	AssertEqual(t, TypeEvent, msg.Type)
	AssertFalse(t, msg.Timestamp.IsZero())
}

func TestAX7_Hub_Broadcast_Bad(t *T) {
	hub := NewHub()
	err := hub.Broadcast(Message{Type: TypeProcessOutput, ProcessID: "bad:id"})
	AssertError(t, err, "invalid process ID")
	AssertEqual(t, 0, len(hub.broadcast))
}

func TestAX7_Hub_Broadcast_Ugly(t *T) {
	var hub *Hub
	err := hub.Broadcast(Message{Type: TypeEvent, Data: "ready"})
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, hub)
}

func TestAX7_Hub_SendToChannel_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	RequireNoError(t, hub.Subscribe(client, "agent.dispatch"))
	err := hub.SendToChannel("agent.dispatch", Message{Type: TypeEvent, Data: "queued"})
	AssertNoError(t, err)
	AssertEqual(t, "agent.dispatch", ax7ClientMessage(t, client).Channel)
}

func TestAX7_Hub_SendToChannel_Bad(t *T) {
	hub := NewHub()
	err := hub.SendToChannel(" agent.dispatch", Message{Type: TypeEvent})
	AssertError(t, err, "invalid channel")
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestAX7_Hub_SendToChannel_Ugly(t *T) {
	hub := NewHub()
	err := hub.SendToChannel("agent.dispatch", Message{Type: TypeEvent, Data: "nobody"})
	AssertNoError(t, err)
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestAX7_Hub_SendProcessOutput_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	RequireNoError(t, hub.Subscribe(client, "process:proc-1"))
	RequireNoError(t, hub.SendProcessOutput("proc-1", "line"))
	AssertEqual(t, TypeProcessOutput, ax7ClientMessage(t, client).Type)
}

func TestAX7_Hub_SendProcessOutput_Bad(t *T) {
	hub := NewHub()
	err := hub.SendProcessOutput("bad:id", "line")
	AssertError(t, err, "invalid process ID")
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestAX7_Hub_SendProcessOutput_Ugly(t *T) {
	hub := NewHub()
	err := hub.SendProcessOutput("proc-1", "")
	AssertNoError(t, err)
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestAX7_Hub_SendProcessStatus_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	RequireNoError(t, hub.Subscribe(client, "process:proc-1"))
	RequireNoError(t, hub.SendProcessStatus("proc-1", "running", 0))
	AssertEqual(t, TypeProcessStatus, ax7ClientMessage(t, client).Type)
}

func TestAX7_Hub_SendProcessStatus_Bad(t *T) {
	hub := NewHub()
	err := hub.SendProcessStatus("bad:id", "failed", 1)
	AssertError(t, err, "invalid process ID")
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestAX7_Hub_SendProcessStatus_Ugly(t *T) {
	hub := NewHub()
	err := hub.SendProcessStatus("proc-1", "", -1)
	AssertNoError(t, err)
	AssertEqual(t, 0, hub.ChannelCount())
}

func TestAX7_Hub_SendError_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendError("server refused"))
	msg := ax7BroadcastMessage(t, hub)
	AssertEqual(t, TypeError, msg.Type)
	AssertEqual(t, "server refused", msg.Data)
}

func TestAX7_Hub_SendError_Bad(t *T) {
	var hub *Hub
	err := hub.SendError("server refused")
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, hub)
}

func TestAX7_Hub_SendError_Ugly(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendError(""))
	msg := ax7BroadcastMessage(t, hub)
	AssertEqual(t, "", msg.Data)
}

func TestAX7_Hub_SendEvent_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendEvent("agent.ready", "payload"))
	msg := ax7BroadcastMessage(t, hub)
	AssertEqual(t, TypeEvent, msg.Type)
	AssertContains(t, msg.Data.(map[string]any), "event")
}

func TestAX7_Hub_SendEvent_Bad(t *T) {
	var hub *Hub
	err := hub.SendEvent("agent.ready", "payload")
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, hub)
}

func TestAX7_Hub_SendEvent_Ugly(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.SendEvent("", nil))
	msg := ax7BroadcastMessage(t, hub)
	AssertEqual(t, TypeEvent, msg.Type)
	AssertContains(t, msg.Data.(map[string]any), "data")
}

// --- Hub snapshots and counts ---

func TestAX7_Hub_ClientCount_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	hub.clients[client] = true
	AssertEqual(t, 1, hub.ClientCount())
}

func TestAX7_Hub_ClientCount_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, 0, hub.ClientCount())
	AssertNotNil(t, hub.clients)
}

func TestAX7_Hub_ClientCount_Ugly(t *T) {
	var hub *Hub
	AssertEqual(t, 0, hub.ClientCount())
	AssertNil(t, hub)
}

func TestAX7_Hub_ChannelCount_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.Subscribe(ax7Client(), "alpha"))
	RequireNoError(t, hub.Subscribe(ax7Client(), "beta"))
	AssertEqual(t, 2, hub.ChannelCount())
}

func TestAX7_Hub_ChannelCount_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, 0, hub.ChannelCount())
	AssertNotNil(t, hub.channels)
}

func TestAX7_Hub_ChannelCount_Ugly(t *T) {
	var hub *Hub
	AssertEqual(t, 0, hub.ChannelCount())
	AssertNil(t, hub)
}

func TestAX7_Hub_ChannelSubscriberCount_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.Subscribe(ax7Client(), "alpha"))
	RequireNoError(t, hub.Subscribe(ax7Client(), "alpha"))
	AssertEqual(t, 2, hub.ChannelSubscriberCount("alpha"))
}

func TestAX7_Hub_ChannelSubscriberCount_Bad(t *T) {
	hub := NewHub()
	AssertEqual(t, 0, hub.ChannelSubscriberCount("missing"))
	AssertNotNil(t, hub.channels)
}

func TestAX7_Hub_ChannelSubscriberCount_Ugly(t *T) {
	var hub *Hub
	AssertEqual(t, 0, hub.ChannelSubscriberCount("alpha"))
	AssertNil(t, hub)
}

func TestAX7_Hub_AllClients_Good(t *T) {
	hub := NewHub()
	hub.clients[&Client{UserID: "b"}] = true
	hub.clients[&Client{UserID: "a"}] = true
	var ids []string
	for client := range hub.AllClients() {
		ids = append(ids, client.UserID)
	}
	AssertEqual(t, []string{"a", "b"}, ids)
}

func TestAX7_Hub_AllClients_Bad(t *T) {
	hub := NewHub()
	var clients []*Client
	for client := range hub.AllClients() {
		clients = append(clients, client)
	}
	AssertEmpty(t, clients)
}

func TestAX7_Hub_AllClients_Ugly(t *T) {
	var hub *Hub
	var clients []*Client
	for client := range hub.AllClients() {
		clients = append(clients, client)
	}
	AssertEmpty(t, clients)
}

func TestAX7_Hub_AllChannels_Good(t *T) {
	hub := NewHub()
	RequireNoError(t, hub.Subscribe(ax7Client(), "beta"))
	RequireNoError(t, hub.Subscribe(ax7Client(), "alpha"))
	var channels []string
	for channel := range hub.AllChannels() {
		channels = append(channels, channel)
	}
	AssertEqual(t, []string{"alpha", "beta"}, channels)
}

func TestAX7_Hub_AllChannels_Bad(t *T) {
	hub := NewHub()
	var channels []string
	for channel := range hub.AllChannels() {
		channels = append(channels, channel)
	}
	AssertEmpty(t, channels)
}

func TestAX7_Hub_AllChannels_Ugly(t *T) {
	var hub *Hub
	var channels []string
	for channel := range hub.AllChannels() {
		channels = append(channels, channel)
	}
	AssertEmpty(t, channels)
}

func TestAX7_Hub_Stats_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	hub.clients[client] = true
	RequireNoError(t, hub.Subscribe(client, "alpha"))
	stats := hub.Stats()
	AssertEqual(t, 1, stats.Clients)
	AssertEqual(t, 1, stats.Channels)
	AssertEqual(t, 1, stats.Subscribers)
}

func TestAX7_Hub_Stats_Bad(t *T) {
	hub := NewHub()
	stats := hub.Stats()
	AssertEqual(t, HubStats{}, stats)
	AssertEqual(t, 0, stats.Subscribers)
}

func TestAX7_Hub_Stats_Ugly(t *T) {
	var hub *Hub
	stats := hub.Stats()
	AssertEqual(t, HubStats{}, stats)
	AssertNil(t, hub)
}

// --- HTTP handlers ---

func TestAX7_Hub_Handler_Good(t *T) {
	hub, _ := ax7StartHub(t)
	server := NewHTTPTestServer(hub.Handler())
	t.Cleanup(server.Close)
	resp := HTTPGet(server.URL)
	RequireTrue(t, resp.OK)
	AssertEqual(t, 400, resp.Value.(*Response).StatusCode)
	AssertNoError(t, resp.Value.(*Response).Body.Close())
}

func TestAX7_Hub_Handler_Bad(t *T) {
	var hub *Hub
	handler := hub.Handler()
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "/ws", nil)
	handler(rec, req)
	AssertEqual(t, 503, rec.Code)
	AssertContains(t, rec.Body.String(), "Hub is not configured")
}

func TestAX7_Hub_Handler_Ugly(t *T) {
	hub, _ := ax7StartHub(t)
	hub.config.CheckOrigin = func(*Request) bool { panic("origin panic") }
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "http://example.com/ws", nil)
	hub.Handler()(rec, req)
	AssertEqual(t, 403, rec.Code)
}

func TestAX7_Hub_HandleWebSocket_Good(t *T) {
	hub, _ := ax7StartHub(t)
	server := NewHTTPTestServer(HandlerFunc(hub.HandleWebSocket))
	t.Cleanup(server.Close)
	resp := HTTPGet(server.URL)
	RequireTrue(t, resp.OK)
	AssertEqual(t, 400, resp.Value.(*Response).StatusCode)
	AssertNoError(t, resp.Value.(*Response).Body.Close())
}

func TestAX7_Hub_HandleWebSocket_Bad(t *T) {
	var hub *Hub
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "/ws", nil)
	hub.HandleWebSocket(rec, req)
	AssertEqual(t, 503, rec.Code)
	AssertContains(t, rec.Body.String(), "Hub is not configured")
}

func TestAX7_Hub_HandleWebSocket_Ugly(t *T) {
	hub, _ := ax7StartHub(t)
	hub.config.CheckOrigin = func(*Request) bool { return false }
	rec := NewHTTPTestRecorder()
	req := NewHTTPTestRequest("GET", "http://example.com/ws", nil)
	hub.HandleWebSocket(rec, req)
	AssertEqual(t, 403, rec.Code)
}

// --- Client methods ---

func TestAX7_Client_Subscriptions_Good(t *T) {
	client := ax7Client()
	client.subscriptions["beta"] = true
	client.subscriptions["alpha"] = true
	AssertEqual(t, []string{"alpha", "beta"}, client.Subscriptions())
}

func TestAX7_Client_Subscriptions_Bad(t *T) {
	var client *Client
	subscriptions := client.Subscriptions()
	AssertNil(t, subscriptions)
	AssertEmpty(t, subscriptions)
}

func TestAX7_Client_Subscriptions_Ugly(t *T) {
	client := ax7Client()
	client.subscriptions["alpha"] = true
	snapshot := client.Subscriptions()
	snapshot[0] = "mutated"
	AssertEqual(t, []string{"alpha"}, client.Subscriptions())
}

func TestAX7_Client_AllSubscriptions_Good(t *T) {
	client := ax7Client()
	client.subscriptions["beta"] = true
	client.subscriptions["alpha"] = true
	var subscriptions []string
	for channel := range client.AllSubscriptions() {
		subscriptions = append(subscriptions, channel)
	}
	AssertEqual(t, []string{"alpha", "beta"}, subscriptions)
}

func TestAX7_Client_AllSubscriptions_Bad(t *T) {
	client := ax7Client()
	var subscriptions []string
	for channel := range client.AllSubscriptions() {
		subscriptions = append(subscriptions, channel)
	}
	AssertEmpty(t, subscriptions)
}

func TestAX7_Client_AllSubscriptions_Ugly(t *T) {
	var client *Client
	var subscriptions []string
	for channel := range client.AllSubscriptions() {
		subscriptions = append(subscriptions, channel)
	}
	AssertEmpty(t, subscriptions)
}

func TestAX7_Client_Close_Good(t *T) {
	hub := NewHub()
	client := ax7Client()
	client.hub = hub
	hub.clients[client] = true
	RequireNoError(t, hub.Subscribe(client, "alpha"))
	AssertNoError(t, client.Close())
	AssertEqual(t, 0, hub.ClientCount())
	AssertEmpty(t, client.Subscriptions())
}

func TestAX7_Client_Close_Bad(t *T) {
	var client *Client
	err := client.Close()
	AssertNoError(t, err)
	AssertNil(t, client)
}

func TestAX7_Client_Close_Ugly(t *T) {
	hub, _ := ax7StartHub(t)
	client := ax7Client()
	client.hub = hub
	hub.register <- client
	RequireTrue(t, ax7Eventually(func() bool { return hub.ClientCount() == 1 }))
	AssertNoError(t, client.Close())
	AssertTrue(t, ax7Eventually(func() bool { return hub.ClientCount() == 0 }))
}

// --- Reconnecting client ---

func TestAX7_NewReconnectingClient_Good(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{URL: "ws://example.invalid/ws"})
	AssertEqual(t, StateDisconnected, rc.State())
	AssertEqual(t, Second, rc.config.InitialBackoff)
	AssertEqual(t, 30*Second, rc.config.MaxBackoff)
	AssertNotNil(t, rc.config.Dialer)
}

func TestAX7_NewReconnectingClient_Bad(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{InitialBackoff: 10 * Millisecond, MaxBackoff: 5 * Millisecond})
	AssertEqual(t, 5*Millisecond, rc.config.InitialBackoff)
	AssertEqual(t, 5*Millisecond, rc.config.MaxBackoff)
	AssertEqual(t, 2.0, rc.config.BackoffMultiplier)
}

func TestAX7_NewReconnectingClient_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{BackoffMultiplier: -4, InitialBackoff: -1, MaxBackoff: -1})
	AssertEqual(t, Second, rc.config.InitialBackoff)
	AssertEqual(t, 30*Second, rc.config.MaxBackoff)
	AssertEqual(t, 2.0, rc.config.BackoffMultiplier)
}

func TestAX7_ReconnectingClient_Connect_Good(t *T) {
	_, server := ax7StartWSServer(t, HubConfig{})
	rc := NewReconnectingClient(ReconnectConfig{URL: ax7WSURL(server)})
	done := make(chan error, 1)
	go func() { done <- rc.Connect(Background()) }()
	RequireTrue(t, ax7Eventually(func() bool { return rc.State() == StateConnected }))
	AssertNoError(t, rc.Close())
	timeout, cancel := WithTimeout(Background(), Second)
	defer cancel()
	select {
	case err := <-done:
		if err != nil {
			AssertContains(t, err.Error(), "context canceled")
		}
	case <-timeout.Done():
		t.Fatal("timed out waiting for reconnecting client shutdown")
	}
}

func TestAX7_ReconnectingClient_Connect_Bad(t *T) {
	var rc *ReconnectingClient
	err := rc.Connect(Background())
	AssertError(t, err, "client must not be nil")
	AssertNil(t, rc)
}

func TestAX7_ReconnectingClient_Connect_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{
		URL:                  "ws://127.0.0.1:1/ws",
		InitialBackoff:       Millisecond,
		MaxBackoff:           Millisecond,
		MaxReconnectAttempts: 1,
	})
	err := rc.Connect(Background())
	AssertError(t, err, "max retries")
	AssertEqual(t, StateDisconnected, rc.State())
}

func TestAX7_ReconnectingClient_Send_Good(t *T) {
	_, server := ax7StartWSServer(t, HubConfig{})
	rc := NewReconnectingClient(ReconnectConfig{URL: ax7WSURL(server)})
	done := make(chan error, 1)
	go func() { done <- rc.Connect(Background()) }()
	RequireTrue(t, ax7Eventually(func() bool { return rc.State() == StateConnected }))
	AssertNoError(t, rc.Send(Message{Type: TypePing}))
	AssertNoError(t, rc.Close())
	<-done
}

func TestAX7_ReconnectingClient_Send_Bad(t *T) {
	var rc *ReconnectingClient
	err := rc.Send(Message{Type: TypePing})
	AssertError(t, err, "client must not be nil")
	AssertNil(t, rc)
}

func TestAX7_ReconnectingClient_Send_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{URL: "ws://example.invalid/ws"})
	err := rc.Send(Message{Type: TypePing})
	AssertError(t, err, "not connected")
	AssertEqual(t, StateDisconnected, rc.State())
}

func TestAX7_ReconnectingClient_State_Good(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	rc.setState(StateConnecting)
	AssertEqual(t, StateConnecting, rc.State())
}

func TestAX7_ReconnectingClient_State_Bad(t *T) {
	var rc *ReconnectingClient
	state := rc.State()
	AssertEqual(t, StateDisconnected, state)
	AssertNil(t, rc)
}

func TestAX7_ReconnectingClient_State_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	rc.setState(ConnectionState(99))
	AssertEqual(t, ConnectionState(99), rc.State())
}

func TestAX7_ReconnectingClient_Close_Good(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	rc.setState(StateConnected)
	AssertNoError(t, rc.Close())
	AssertEqual(t, StateDisconnected, rc.State())
}

func TestAX7_ReconnectingClient_Close_Bad(t *T) {
	var rc *ReconnectingClient
	err := rc.Close()
	AssertNoError(t, err)
	AssertNil(t, rc)
}

func TestAX7_ReconnectingClient_Close_Ugly(t *T) {
	rc := NewReconnectingClient(ReconnectConfig{})
	AssertNoError(t, rc.Close())
	AssertNoError(t, rc.Close())
	AssertEqual(t, StateDisconnected, rc.State())
}

// --- Authentication ---

func TestAX7_NewAPIKeyAuth_Good(t *T) {
	auth := NewAPIKeyAuth(map[string]string{"secret": "user-1"})
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertTrue(t, result.Valid)
	AssertEqual(t, "user-1", result.UserID)
	AssertEqual(t, "api_key", result.Claims["auth_method"])
}

func TestAX7_NewAPIKeyAuth_Bad(t *T) {
	auth := NewAPIKeyAuth(nil)
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertFalse(t, result.Valid)
	AssertErrorIs(t, result.Error, ErrInvalidAPIKey)
}

func TestAX7_NewAPIKeyAuth_Ugly(t *T) {
	keys := map[string]string{"secret": "user-1"}
	auth := NewAPIKeyAuth(keys)
	keys["secret"] = "mutated"
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertTrue(t, result.Valid)
	AssertEqual(t, "user-1", result.UserID)
}

func TestAX7_APIKeyAuthenticator_Authenticate_Good(t *T) {
	auth := NewAPIKeyAuth(map[string]string{"secret": "user-1"})
	result := auth.Authenticate(ax7AuthRequest("bearer secret"))
	AssertTrue(t, result.Valid)
	AssertTrue(t, result.Authenticated)
	AssertEqual(t, "user-1", result.UserID)
}

func TestAX7_APIKeyAuthenticator_Authenticate_Bad(t *T) {
	auth := NewAPIKeyAuth(map[string]string{"secret": "user-1"})
	result := auth.Authenticate(ax7AuthRequest("Bearer wrong"))
	AssertFalse(t, result.Valid)
	AssertErrorIs(t, result.Error, ErrInvalidAPIKey)
	AssertEqual(t, "", result.UserID)
}

func TestAX7_APIKeyAuthenticator_Authenticate_Ugly(t *T) {
	var auth *APIKeyAuthenticator
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "authenticator is nil")
}

func TestAX7_AuthenticatorFunc_Authenticate_Good(t *T) {
	auth := AuthenticatorFunc(func(*Request) AuthResult {
		return AuthResult{Authenticated: true, UserID: " user-1 "}
	})
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws", nil))
	AssertTrue(t, result.Valid)
	AssertEqual(t, "user-1", result.UserID)
}

func TestAX7_AuthenticatorFunc_Authenticate_Bad(t *T) {
	var auth AuthenticatorFunc
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws", nil))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "authenticator function is nil")
}

func TestAX7_AuthenticatorFunc_Authenticate_Ugly(t *T) {
	auth := AuthenticatorFunc(func(*Request) AuthResult {
		return AuthResult{Authenticated: true, UserID: ""}
	})
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws", nil))
	AssertFalse(t, result.Valid)
	AssertErrorIs(t, result.Error, ErrMissingUserID)
}

func TestAX7_NewBearerTokenAuth_Good(t *T) {
	auth := NewBearerTokenAuth(func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	})
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertTrue(t, result.Valid)
	AssertEqual(t, "user-1", result.UserID)
}

func TestAX7_NewBearerTokenAuth_Bad(t *T) {
	auth := NewBearerTokenAuth()
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "validate function is not configured")
}

func TestAX7_NewBearerTokenAuth_Ugly(t *T) {
	auth := NewBearerTokenAuth(nil)
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "validate function is not configured")
}

func TestAX7_BearerTokenAuth_Authenticate_Good(t *T) {
	auth := &BearerTokenAuth{Validate: func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	}}
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertTrue(t, result.Valid)
	AssertEqual(t, "user-1", result.UserID)
}

func TestAX7_BearerTokenAuth_Authenticate_Bad(t *T) {
	auth := NewBearerTokenAuth(func(string) AuthResult {
		return AuthResult{Valid: false, Error: AnError}
	})
	result := auth.Authenticate(ax7AuthRequest(""))
	AssertFalse(t, result.Valid)
	AssertErrorIs(t, result.Error, ErrMissingAuthHeader)
}

func TestAX7_BearerTokenAuth_Authenticate_Ugly(t *T) {
	var auth *BearerTokenAuth
	result := auth.Authenticate(ax7AuthRequest("Bearer secret"))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "authenticator is nil")
}

func TestAX7_NewQueryTokenAuth_Good(t *T) {
	auth := NewQueryTokenAuth(func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	})
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws?token=secret", nil))
	AssertTrue(t, result.Valid)
	AssertEqual(t, "user-1", result.UserID)
}

func TestAX7_NewQueryTokenAuth_Bad(t *T) {
	auth := NewQueryTokenAuth()
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws?token=secret", nil))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "validate function is not configured")
}

func TestAX7_NewQueryTokenAuth_Ugly(t *T) {
	auth := NewQueryTokenAuth(nil)
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws?token=secret", nil))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "validate function is not configured")
}

func TestAX7_QueryTokenAuth_Authenticate_Good(t *T) {
	auth := &QueryTokenAuth{Validate: func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	}}
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws?token=secret", nil))
	AssertTrue(t, result.Valid)
	AssertEqual(t, "user-1", result.UserID)
}

func TestAX7_QueryTokenAuth_Authenticate_Bad(t *T) {
	auth := NewQueryTokenAuth(func(string) AuthResult {
		return AuthResult{Authenticated: true, UserID: "user-1"}
	})
	result := auth.Authenticate(NewHTTPTestRequest("GET", "/ws", nil))
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "missing token")
}

func TestAX7_QueryTokenAuth_Authenticate_Ugly(t *T) {
	auth := NewQueryTokenAuth(func(string) AuthResult {
		return AuthResult{Authenticated: true, UserID: "user-1"}
	})
	req := NewHTTPTestRequest("GET", "/ws?token=secret", nil)
	req.URL = nil
	result := auth.Authenticate(req)
	AssertFalse(t, result.Valid)
	AssertError(t, result.Error, "request URL is nil")
}

// --- Redis bridge ---

func TestAX7_NewRedisBridge_Good(t *T) {
	addr := ax7StartRedis(t)
	hub := NewHub()
	bridge, err := NewRedisBridge(hub, RedisConfig{Addr: addr, Prefix: "ws"})
	RequireNoError(t, err)
	AssertEqual(t, hub, bridge.hub)
	AssertNotEmpty(t, bridge.SourceID())
	AssertNoError(t, bridge.Stop())
}

func TestAX7_NewRedisBridge_Bad(t *T) {
	bridge, err := NewRedisBridge(nil, RedisConfig{Addr: "127.0.0.1:1"})
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, bridge)
}

func TestAX7_NewRedisBridge_Ugly(t *T) {
	bridge, err := NewRedisBridge(NewHub(), RedisConfig{Addr: "127.0.0.1:1", Prefix: "bad prefix"})
	AssertError(t, err, "invalid redis prefix")
	AssertNil(t, bridge)
}

func TestAX7_RedisBridge_Start_Good(t *T) {
	addr := ax7StartRedis(t)
	bridge, err := NewRedisBridge(NewHub(), RedisConfig{Addr: addr, Prefix: "ws"})
	RequireNoError(t, err)
	AssertNoError(t, bridge.Start(Background()))
	AssertTrue(t, redisBridgeListening(bridge))
	AssertNoError(t, bridge.Stop())
}

func TestAX7_RedisBridge_Start_Bad(t *T) {
	var bridge *RedisBridge
	err := bridge.Start(Background())
	AssertError(t, err, "bridge must not be nil")
	AssertNil(t, bridge)
}

func TestAX7_RedisBridge_Start_Ugly(t *T) {
	bridge := &RedisBridge{prefix: "ws"}
	err := bridge.Start(nil)
	AssertError(t, err, "redis client is not available")
	AssertFalse(t, redisBridgeListening(bridge))
}

func TestAX7_RedisBridge_Stop_Good(t *T) {
	addr := ax7StartRedis(t)
	bridge, err := NewRedisBridge(NewHub(), RedisConfig{Addr: addr, Prefix: "ws"})
	RequireNoError(t, err)
	AssertNoError(t, bridge.Stop())
	AssertNil(t, bridge.client)
}

func TestAX7_RedisBridge_Stop_Bad(t *T) {
	var bridge *RedisBridge
	err := bridge.Stop()
	AssertNoError(t, err)
	AssertNil(t, bridge)
}

func TestAX7_RedisBridge_Stop_Ugly(t *T) {
	bridge := &RedisBridge{}
	AssertNoError(t, bridge.Stop())
	AssertNoError(t, bridge.Stop())
	AssertNil(t, bridge.client)
}

func TestAX7_RedisBridge_PublishToChannel_Good(t *T) {
	addr := ax7StartRedis(t)
	bridge, err := NewRedisBridge(NewHub(), RedisConfig{Addr: addr, Prefix: "ws"})
	RequireNoError(t, err)
	bridge.ctx = Background()
	AssertNoError(t, bridge.PublishToChannel("agent.dispatch", Message{Type: TypeEvent, Data: "ready"}))
	AssertNoError(t, bridge.Stop())
}

func TestAX7_RedisBridge_PublishToChannel_Bad(t *T) {
	bridge := &RedisBridge{hub: NewHub(), prefix: "ws"}
	err := bridge.PublishToChannel(" agent.dispatch", Message{Type: TypeEvent})
	AssertError(t, err, "invalid channel")
	AssertEqual(t, 0, bridge.hub.ChannelCount())
}

func TestAX7_RedisBridge_PublishToChannel_Ugly(t *T) {
	var bridge *RedisBridge
	err := bridge.PublishToChannel("agent.dispatch", Message{Type: TypeEvent})
	AssertError(t, err, "bridge must not be nil")
	AssertNil(t, bridge)
}

func TestAX7_RedisBridge_PublishBroadcast_Good(t *T) {
	addr := ax7StartRedis(t)
	bridge, err := NewRedisBridge(NewHub(), RedisConfig{Addr: addr, Prefix: "ws"})
	RequireNoError(t, err)
	bridge.ctx = Background()
	AssertNoError(t, bridge.PublishBroadcast(Message{Type: TypeEvent, Data: "ready"}))
	AssertNoError(t, bridge.Stop())
}

func TestAX7_RedisBridge_PublishBroadcast_Bad(t *T) {
	bridge := &RedisBridge{prefix: "ws"}
	err := bridge.PublishBroadcast(Message{Type: TypeEvent})
	AssertError(t, err, "hub must not be nil")
	AssertNil(t, bridge.hub)
}

func TestAX7_RedisBridge_PublishBroadcast_Ugly(t *T) {
	bridge := &RedisBridge{hub: NewHub(), prefix: "ws"}
	err := bridge.PublishBroadcast(Message{Type: TypeProcessOutput, ProcessID: "bad:id"})
	AssertError(t, err, "invalid process ID")
	AssertEqual(t, 0, bridge.hub.ChannelCount())
}

func TestAX7_RedisBridge_SourceID_Good(t *T) {
	bridge := &RedisBridge{sourceID: "source-1"}
	sourceID := bridge.SourceID()
	AssertEqual(t, "source-1", sourceID)
	AssertNotEmpty(t, sourceID)
}

func TestAX7_RedisBridge_SourceID_Bad(t *T) {
	var bridge *RedisBridge
	sourceID := bridge.SourceID()
	AssertEqual(t, "", sourceID)
	AssertNil(t, bridge)
}

func TestAX7_RedisBridge_SourceID_Ugly(t *T) {
	bridge := &RedisBridge{}
	sourceID := bridge.SourceID()
	AssertEqual(t, "", sourceID)
	AssertEmpty(t, sourceID)
}
