// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"reflect"
	"testing"
	"time"

	core "dappco.re/go"
)

func testEqual(want, got any) bool {
	return reflect.DeepEqual(want, got)
}

func testErrorIs(err, target error) bool {
	return core.Is(err, target)
}

func testResultError(r core.Result) error {
	if r.OK {
		return nil
	}
	if err, ok := r.Value.(error); ok {
		return err
	}
	return core.NewError(r.Error())
}

func testAsError(value any) error {
	switch v := value.(type) {
	case nil:
		return nil
	case core.Result:
		return testResultError(v)
	case error:
		return v
	default:
		return core.NewError(core.Sprintf("%v", value))
	}
}

func assertNoError(t core.TB, value any, msg ...string) {
	core.AssertNoError(t, testAsError(value), msg...)
}

func requireNoError(t core.TB, value any, msg ...string) {
	core.RequireNoError(t, testAsError(value), msg...)
}

func assertError(t core.TB, value any, msg ...string) {
	core.AssertError(t, testAsError(value), msg...)
}

func assertErrorIs(t core.TB, value any, target error, msg ...string) {
	core.AssertErrorIs(t, testAsError(value), target, msg...)
}

func testIsNil(value any) bool {
	if value == nil {
		return true
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func testIsEmpty(value any) bool {
	if value == nil {
		return true
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Complex64, reflect.Complex128:
		return v.Complex() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	default:
		return reflect.DeepEqual(value, reflect.Zero(v.Type()).Interface())
	}
}

func testContains(container, element any) bool {
	if s, ok := container.(string); ok {
		needle, ok := element.(string)
		return ok && core.Contains(s, needle)
	}

	v := reflect.ValueOf(container)
	if !v.IsValid() {
		return false
	}

	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		for i := range v.Len() {
			if reflect.DeepEqual(v.Index(i).Interface(), element) {
				return true
			}
		}
	case reflect.Map:
		key := reflect.ValueOf(element)
		if !key.IsValid() {
			return false
		}
		if key.Type().AssignableTo(v.Type().Key()) {
			return v.MapIndex(key).IsValid()
		}
		if key.Type().ConvertibleTo(v.Type().Key()) {
			return v.MapIndex(key.Convert(v.Type().Key())).IsValid()
		}
	}

	return false
}

func testSame(want, got any) bool {
	if testIsNil(want) || testIsNil(got) {
		return testIsNil(want) && testIsNil(got)
	}

	wantValue := reflect.ValueOf(want)
	gotValue := reflect.ValueOf(got)
	if wantValue.Type() != gotValue.Type() {
		return false
	}
	if wantValue.Kind() != reflect.Pointer {
		return false
	}
	return wantValue.Pointer() == gotValue.Pointer()
}

func testIsZero(value any) bool {
	if value == nil {
		return true
	}
	return reflect.ValueOf(value).IsZero()
}

func testEventually(condition func() bool, waitFor, tick time.Duration) bool {
	deadline := time.Now().Add(waitFor)
	for {
		if condition() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}

		sleepFor := tick
		if remaining := time.Until(deadline); remaining < sleepFor {
			sleepFor = remaining
		}
		if sleepFor > 0 {
			time.Sleep(sleepFor)
		}
	}
}

func testClose(t testing.TB, closeFn any) {
	t.Helper()
	switch fn := closeFn.(type) {
	case func() error:
		_ = fn()
	case func() core.Result:
		_ = fn()
	case func():
		fn()
	}
}

func testNotPanics(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("expected no panic, got %v", recovered)
		}
	}()
	f()
}

func testRepeat(s string, count int) string {
	if count <= 0 || s == "" {
		return ""
	}

	builder := core.NewBuilder()
	for range count {
		_, _ = builder.WriteString(s)
	}
	return builder.String()
}

// --- v0.9.0 compliance helpers ---

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
	AssertError         = assertError
	AssertErrorIs       = assertErrorIs
	AssertFalse         = core.AssertFalse
	AssertNil           = core.AssertNil
	AssertNoError       = assertNoError
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
	RequireNoError      = requireNoError
	RequireTrue         = core.RequireTrue
	Sleep               = core.Sleep
	Trim                = core.Trim
	TrimPrefix          = core.TrimPrefix
	Upper               = core.Upper
	WithCancel          = core.WithCancel
	WithTimeout         = core.WithTimeout
	WriteString         = core.WriteString
)

func complianceClient() *Client {
	return &Client{
		send:          make(chan []byte, 16),
		subscriptions: make(map[string]bool),
	}
}

func complianceEventually(condition func() bool) bool {
	deadline := Now().Add(Second)
	for Now().Before(deadline) {
		if condition() {
			return true
		}
		Sleep(5 * Millisecond)
	}
	return condition()
}

func complianceStartHub(t *T) (*Hub, CancelFunc) {
	hub := NewHub()
	ctx, cancel := WithCancel(Background())
	go hub.Run(ctx)
	RequireTrue(t, complianceEventually(func() bool { return hub.isRunning() }))
	t.Cleanup(cancel)
	return hub, cancel
}

func complianceStartWSServer(t *T, config HubConfig) (*Hub, *HTTPTestServer) {
	hub := NewHubWithConfig(config)
	ctx, cancel := WithCancel(Background())
	go hub.Run(ctx)
	RequireTrue(t, complianceEventually(func() bool { return hub.isRunning() }))
	server := NewHTTPTestServer(hub.Handler())
	t.Cleanup(func() {
		server.Close()
		cancel()
	})
	return hub, server
}

func complianceWSURL(server *HTTPTestServer) string {
	return Concat("ws", TrimPrefix(server.URL, "http"))
}

func complianceBroadcastMessage(t *T, hub *Hub) Message {
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

func complianceClientMessage(t *T, client *Client) Message {
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

func complianceAuthRequest(header string) *Request {
	req := NewHTTPTestRequest("GET", "/ws", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return req
}

func complianceStartRedis(t *T) string {
	r := NetListen("tcp", "127.0.0.1:0")
	RequireTrue(t, r.OK)
	listener := r.Value.(Listener)
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			AssertContains(t, err.Error(), "closed")
		}
	})
	go complianceAcceptRedis(listener)
	return listener.Addr().String()
}

func complianceAcceptRedis(listener Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go complianceServeRedis(conn)
	}
}

func complianceServeRedis(conn Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			return
		}
	}()

	reader := NewBufReader(conn)
	for {
		parts, err := complianceReadRedisCommand(reader)
		if err != nil {
			return
		}
		if len(parts) == 0 {
			continue
		}

		switch Upper(parts[0]) {
		case "HELLO":
			if !complianceWriteRedis(conn, "%7\r\n+server\r\n+redis\r\n+version\r\n+7.2.0\r\n+proto\r\n:3\r\n+id\r\n:1\r\n+mode\r\n+standalone\r\n+role\r\n+master\r\n+modules\r\n*0\r\n") {
				return
			}
		case "PING":
			if !complianceWriteRedis(conn, "+PONG\r\n") {
				return
			}
		case "CLIENT", "SELECT", "READONLY":
			if !complianceWriteRedis(conn, "+OK\r\n") {
				return
			}
		case "PSUBSCRIBE":
			pattern := ""
			if len(parts) > 1 {
				pattern = parts[1]
			}
			if !complianceWriteRedis(conn, Concat("*3\r\n$10\r\npsubscribe\r\n$", Itoa(len(pattern)), "\r\n", pattern, "\r\n:1\r\n")) {
				return
			}
		case "PUBLISH":
			if !complianceWriteRedis(conn, ":1\r\n") {
				return
			}
		case "QUIT":
			if !complianceWriteRedis(conn, "+OK\r\n") {
				return
			}
			return
		default:
			if !complianceWriteRedis(conn, "+OK\r\n") {
				return
			}
		}
	}
}

func complianceReadRedisCommand(reader *BufReader) ([]string, error) {
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

func complianceWriteRedis(conn Conn, payload string) bool {
	return WriteString(conn, payload).OK
}
