// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "dappco.re/go/core"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Unit tests — APIKeyAuthenticator
// ---------------------------------------------------------------------------

func TestAPIKeyAuthenticator_ValidKey(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
		"key-def": "user-2",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if !result.Authenticated {
		t.Fatal("expected true")
	}
	if "user-1" != result.UserID {
		t.Fatalf("want %v, got %v", "user-1", result.UserID)
	}
	if "api_key" != result.Claims["auth_method"] {
		t.Fatalf("want %v, got %v", "api_key", result.Claims["auth_method"])
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

func TestAPIKeyAuthenticator_InvalidKey(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer wrong-key")

	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if len(result.UserID) != 0 {
		t.Fatalf("expected empty, got %v", result.UserID)
	}
	if !core.Is(result.Error, ErrInvalidAPIKey) {
		t.Fatal("expected true")
	}
}

func TestAPIKeyAuthenticator_MissingHeader(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	// No Authorization header set

	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if !core.Is(result.Error, ErrMissingAuthHeader) {
		t.Fatal("expected true")
	}
}

func TestAPIKeyAuthenticator_MalformedHeader(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	tests := []struct {
		name   string
		header string
	}{
		{"bearer without token", "Bearer "},
		{"bearer only", "Bearer"},
		{"wrong scheme", "Basic key-abc"},
		{"no scheme", "key-abc"},
		{"empty bearer with spaces", "Bearer   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			r.Header.Set("Authorization", tt.header)

			result := auth.Authenticate(r)
			if result.Valid {
				t.Fatal("expected false")
			}
			if !core.Is(result.Error, ErrMalformedAuthHeader) {
				t.Fatal("expected true")
			}
		})
	}
}

func TestAPIKeyAuthenticator_CaseInsensitiveScheme(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "bearer key-abc")

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if !result.Authenticated {
		t.Fatal("expected true")
	}
	if "user-1" != result.UserID {
		t.Fatalf("want %v, got %v", "user-1", result.UserID)
	}
}

func TestAPIKeyAuthenticator_SecondKey(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
		"key-def": "user-2",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-def")

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if "user-2" != result.UserID {
		t.Fatalf("want %v, got %v", "user-2", result.UserID)
	}
}

// ---------------------------------------------------------------------------
// Unit tests — AuthenticatorFunc adapter
// ---------------------------------------------------------------------------

func TestAuthenticatorFunc_Adapter(t *testing.T) {
	called := false
	fn := AuthenticatorFunc(func(r *http.Request) AuthResult {
		called = true
		return AuthResult{Valid: true, UserID: "func-user"}
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)
	if !called {
		t.Fatal("expected true")
	}
	if !result.Valid {
		t.Fatal("expected true")
	}
	if "func-user" != result.UserID {
		t.Fatalf("want %v, got %v", "func-user", result.UserID)
	}
}

func TestAuthenticatorFunc_Rejection(t *testing.T) {
	fn := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{Valid: false, Error: core.NewError("custom rejection")}
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if result.Error.Error() != "custom rejection" {
		t.Fatalf("want %v, got %v", "custom rejection", result.Error.Error())
	}
}

func TestAuthenticatorFunc_NilFunction(t *testing.T) {
	var fn AuthenticatorFunc

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(result.Error.Error(), "authenticator function is nil") {
		t.Fatalf("expected %v to contain %v", result.Error.Error(), "authenticator function is nil")
	}
}

// ---------------------------------------------------------------------------
// Unit tests — nil Authenticator (backward compat)
// ---------------------------------------------------------------------------

func TestNilAuthenticator_AllConnectionsAccepted(t *testing.T) {
	hub := NewHub() // No authenticator set
	if hub.config.Authenticator != nil {
		t.Fatalf("expected nil, got %v", hub.config.Authenticator)
	}
}

// ---------------------------------------------------------------------------
// Integration tests — httptest + gorilla/websocket Dial
// ---------------------------------------------------------------------------

// helper: start a hub with the given config, return server + cleanup
func startAuthTestHub(t *testing.T, config HubConfig) (*httptest.Server, *Hub, context.CancelFunc) {
	t.Helper()
	hub := NewHubWithConfig(config)
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)

	server := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		server.Close()
		cancel()
	})
	return server, hub, cancel
}

func authWSURL(server *httptest.Server) string {
	return "ws" + core.TrimPrefix(server.URL, "http")
}

func TestIntegration_AuthenticatedConnect(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"valid-key": "user-42",
	})

	var connectedClient *Client
	var mu sync.Mutex

	server, _, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
		OnConnect: func(client *Client) {
			mu.Lock()
			connectedClient = client
			mu.Unlock()
		},
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer valid-key")

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), header)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	if http.StatusSwitchingProtocols != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	// Give the hub a moment to process registration
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	client := connectedClient
	mu.Unlock()
	if client == nil {
		t.Fatal("expected non-nil")
	}
	if "user-42" != client.UserID {
		t.Fatalf("want %v, got %v", "user-42", client.UserID)
	}
	if "api_key" != client.Claims["auth_method"] {
		t.Fatalf("want %v, got %v", "api_key", client.Claims["auth_method"])
	}
}

func TestIntegration_RejectedConnect_InvalidKey(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"valid-key": "user-42",
	})

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer wrong-key")

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), header)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if http.StatusUnauthorized != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
}

func TestIntegration_RejectedConnect_NoAuthHeader(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"valid-key": "user-42",
	})

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
	})

	// No Authorization header
	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if http.StatusUnauthorized != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
}

func TestIntegration_NilAuthenticator_BackwardCompat(t *testing.T) {
	// No authenticator — all connections should be accepted
	server, hub, _ := startAuthTestHub(t, HubConfig{})

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	if http.StatusSwitchingProtocols != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}
}

func TestIntegration_OnAuthFailure_Callback(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"valid-key": "user-42",
	})

	var failureMu sync.Mutex
	var failureResult AuthResult
	var failureRequest *http.Request
	failureCalled := false

	server, _, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
		OnAuthFailure: func(r *http.Request, result AuthResult) {
			failureMu.Lock()
			failureCalled = true
			failureResult = result
			failureRequest = r
			failureMu.Unlock()
		},
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer bad-key")

	conn, _, _ := websocket.DefaultDialer.Dial(authWSURL(server), header)
	if conn != nil {
		conn.Close()
	}

	// Give callback time to execute
	time.Sleep(50 * time.Millisecond)

	failureMu.Lock()
	defer failureMu.Unlock()
	if !failureCalled {
		t.Fatal("expected true")
	}
	if failureResult.Valid {
		t.Fatal("expected false")
	}
	if !core.Is(failureResult.Error, ErrInvalidAPIKey) {
		t.Fatal("expected true")
	}
	if failureRequest == nil {
		t.Fatal("expected non-nil")
	}
}

func TestIntegration_MultipleClients_DifferentKeys(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-alpha": "user-alpha",
		"key-beta":  "user-beta",
		"key-gamma": "user-gamma",
	})

	var mu sync.Mutex
	connectedClients := make(map[string]*Client)

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
		OnConnect: func(client *Client) {
			mu.Lock()
			connectedClients[client.UserID] = client
			mu.Unlock()
		},
	})

	keys := []struct {
		key    string
		userID string
	}{
		{"key-alpha", "user-alpha"},
		{"key-beta", "user-beta"},
		{"key-gamma", "user-gamma"},
	}

	conns := make([]*websocket.Conn, 0, len(keys))
	for _, k := range keys {
		header := http.Header{}
		header.Set("Authorization", "Bearer "+k.key)

		conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), header)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if http.StatusSwitchingProtocols != resp.StatusCode {
			t.Fatalf("want %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
		}
		conns = append(conns, conn)
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	time.Sleep(100 * time.Millisecond)
	if 3 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 3, hub.ClientCount())
	}

	mu.Lock()
	defer mu.Unlock()
	for _, k := range keys {
		client, ok := connectedClients[k.userID]
		if !ok {
			t.Fatal("expected true")
		}
		if k.userID != client.UserID {
			t.Fatalf("want %v, got %v", k.userID, client.UserID)
		}
	}
}

func TestIntegration_AuthenticatorFunc_WithHub(t *testing.T) {
	// Use AuthenticatorFunc as the hub's authenticator
	fn := AuthenticatorFunc(func(r *http.Request) AuthResult {
		token := r.URL.Query().Get("token")
		if token == "magic" {
			return AuthResult{
				Valid:  true,
				UserID: "magic-user",
				Claims: map[string]any{"source": "query_param"},
			}
		}
		return AuthResult{Valid: false, Error: core.NewError("bad token")}
	})

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: fn,
	})

	// Valid token via query parameter
	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server)+"?token=magic", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	if http.StatusSwitchingProtocols != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}

	// Invalid token
	conn2, resp2, _ := websocket.DefaultDialer.Dial(authWSURL(server)+"?token=wrong", nil)
	if conn2 != nil {
		conn2.Close()
	}
	if http.StatusUnauthorized != resp2.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp2.StatusCode)
	}
}

func TestIntegration_AuthenticatorFuncNil_WithHub(t *testing.T) {
	var fn AuthenticatorFunc

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: fn,
	})

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if http.StatusUnauthorized != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
}

func TestIntegration_AuthenticatorFuncPanic_WithHub(t *testing.T) {
	failureCalled := make(chan AuthResult, 1)
	fn := AuthenticatorFunc(func(r *http.Request) AuthResult {
		panic("boom")
	})

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: fn,
		OnAuthFailure: func(r *http.Request, result AuthResult) {
			select {
			case failureCalled <- result:
			default:
			}
		},
	})

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if http.StatusUnauthorized != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}

	select {
	case result := <-failureCalled:
		if result.Valid {
			t.Fatal("expected false")
		}
		if result.Error == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(result.Error.Error(), "authenticator panicked") {
			t.Fatalf("expected %v to contain %v", result.Error.Error(), "authenticator panicked")
		}
	case <-time.After(time.Second):
		t.Fatal("OnAuthFailure should be called when authenticator panics")
	}
}

func TestIntegration_AuthenticatedClient_ReceivesMessages(t *testing.T) {
	// Verify that an authenticated client can still receive messages normally
	auth := NewAPIKeyAuth(map[string]string{
		"key-1": "user-1",
	})

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer key-1")

	conn, _, err := websocket.DefaultDialer.Dial(authWSURL(server), header)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Broadcast a message
	err = hub.Broadcast(Message{Type: TypeEvent, Data: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read it
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var msg Message
	if !core.JSONUnmarshal(data, &msg).OK {
		t.Fatal("expected true")
	}
	if TypeEvent != msg.Type {
		t.Fatalf("want %v, got %v", TypeEvent, msg.Type)
	}
	if "hello" != msg.Data {
		t.Fatalf("want %v, got %v", "hello", msg.Data)
	}
}

// ---------------------------------------------------------------------------
// Unit tests — BearerTokenAuth
// ---------------------------------------------------------------------------

func TestBearerTokenAuth_ValidToken_Good(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			if token == "jwt-abc-123" {
				return AuthResult{
					Valid:  true,
					UserID: "user-42",
					Claims: map[string]any{"role": "admin", "auth_method": "jwt"},
				}
			}
			return AuthResult{Valid: false, Error: core.NewError("invalid token")}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer jwt-abc-123")

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if !result.Authenticated {
		t.Fatal("expected true")
	}
	if "user-42" != result.UserID {
		t.Fatalf("want %v, got %v", "user-42", result.UserID)
	}
	if "admin" != result.Claims["role"] {
		t.Fatalf("want %v, got %v", "admin", result.Claims["role"])
	}
	if "jwt" != result.Claims["auth_method"] {
		t.Fatalf("want %v, got %v", "jwt", result.Claims["auth_method"])
	}
}

func TestBearerTokenAuth_InvalidToken_Bad(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: false, Error: core.NewError("token expired")}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer expired-token")

	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if result.Error.Error() != "token expired" {
		t.Fatalf("want %v, got %v", "token expired", result.Error.Error())
	}
}

func TestBearerTokenAuth_MissingHeader_Bad(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if !core.Is(result.Error, ErrMissingAuthHeader) {
		t.Fatal("expected true")
	}
}

func TestBearerTokenAuth_MalformedHeader_Bad(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	tests := []struct {
		name   string
		header string
	}{
		{"wrong scheme", "Basic dXNlcjpwYXNz"},
		{"no scheme", "raw-token-value"},
		{"empty bearer", "Bearer "},
		{"empty bearer with spaces", "Bearer   "},
		{"bearer only", "Bearer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			r.Header.Set("Authorization", tt.header)

			result := auth.Authenticate(r)
			if result.Valid {
				t.Fatal("expected false")
			}
			if !core.Is(result.Error, ErrMalformedAuthHeader) {
				t.Fatal("expected true")
			}
		})
	}
}

func TestBearerTokenAuth_CaseInsensitiveScheme_Good(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "user-1"}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "bearer my-token")

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if "user-1" != result.UserID {
		t.Fatalf("want %v, got %v", "user-1", result.UserID)
	}
}

// ---------------------------------------------------------------------------
// Integration tests — BearerTokenAuth with Hub
// ---------------------------------------------------------------------------

func TestIntegration_BearerTokenAuth_AcceptsValidToken_Good(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			if token == "valid-jwt" {
				return AuthResult{
					Valid:  true,
					UserID: "jwt-user",
					Claims: map[string]any{"auth_method": "bearer"},
				}
			}
			return AuthResult{Valid: false, Error: core.NewError("invalid")}
		},
	}

	var connectedClient *Client
	var mu sync.Mutex

	server, _, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
		OnConnect: func(client *Client) {
			mu.Lock()
			connectedClient = client
			mu.Unlock()
		},
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer valid-jwt")

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), header)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	if http.StatusSwitchingProtocols != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	client := connectedClient
	mu.Unlock()
	if client == nil {
		t.Fatal("expected non-nil")
	}
	if "jwt-user" != client.UserID {
		t.Fatalf("want %v, got %v", "jwt-user", client.UserID)
	}
	if "bearer" != client.Claims["auth_method"] {
		t.Fatalf("want %v, got %v", "bearer", client.Claims["auth_method"])
	}
}

func TestIntegration_BearerTokenAuth_RejectsInvalidToken_Bad(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: false, Error: core.NewError("invalid")}
		},
	}

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer bad-token")

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), header)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if http.StatusUnauthorized != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
}

// ---------------------------------------------------------------------------
// Unit tests — QueryTokenAuth
// ---------------------------------------------------------------------------

func TestQueryTokenAuth_ValidToken_Good(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			if token == "browser-token-456" {
				return AuthResult{
					Valid:  true,
					UserID: "browser-user",
					Claims: map[string]any{"auth_method": "query_param"},
				}
			}
			return AuthResult{Valid: false, Error: core.NewError("unknown token")}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws?token=browser-token-456", nil)

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if !result.Authenticated {
		t.Fatal("expected true")
	}
	if "browser-user" != result.UserID {
		t.Fatalf("want %v, got %v", "browser-user", result.UserID)
	}
	if "query_param" != result.Claims["auth_method"] {
		t.Fatalf("want %v, got %v", "query_param", result.Claims["auth_method"])
	}
}

func TestQueryTokenAuth_InvalidToken_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: false, Error: core.NewError("unknown token")}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws?token=bad-token", nil)

	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if result.Error.Error() != "unknown token" {
		t.Fatalf("want %v, got %v", "unknown token", result.Error.Error())
	}
}

func TestQueryTokenAuth_MissingParam_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if !strings.Contains(result.Error.Error(), "missing token query parameter") {
		t.Fatalf("expected %v to contain %v", result.Error.Error(), "missing token query parameter")
	}
}

func TestQueryTokenAuth_EmptyParam_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws?token=", nil)

	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if !strings.Contains(result.Error.Error(), "missing token query parameter") {
		t.Fatalf("expected %v to contain %v", result.Error.Error(), "missing token query parameter")
	}
}

func TestQueryTokenAuth_NilURL_Bad(t *testing.T) {
	called := false
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			called = true
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	r := &http.Request{Method: http.MethodGet}
	result := auth.Authenticate(r)
	if result.Valid {
		t.Fatal("expected false")
	}
	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(result.Error.Error(), "request URL is nil") {
		t.Fatalf("expected %v to contain %v", result.Error.Error(), "request URL is nil")
	}
	if called {
		t.Fatal("expected false")
	}
}

// ---------------------------------------------------------------------------
// Integration tests — QueryTokenAuth with Hub
// ---------------------------------------------------------------------------

func TestIntegration_QueryTokenAuth_AcceptsValidToken_Good(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			if token == "browser-secret" {
				return AuthResult{
					Valid:  true,
					UserID: "browser-user-99",
					Claims: map[string]any{"origin": "browser"},
				}
			}
			return AuthResult{Valid: false, Error: core.NewError("invalid")}
		},
	}

	var connectedClient *Client
	var mu sync.Mutex

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
		OnConnect: func(client *Client) {
			mu.Lock()
			connectedClient = client
			mu.Unlock()
		},
	})

	conn, resp, err := websocket.DefaultDialer.Dial(
		authWSURL(server)+"?token=browser-secret", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	if http.StatusSwitchingProtocols != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 1, hub.ClientCount())
	}

	mu.Lock()
	client := connectedClient
	mu.Unlock()
	if client == nil {
		t.Fatal("expected non-nil")
	}
	if "browser-user-99" != client.UserID {
		t.Fatalf("want %v, got %v", "browser-user-99", client.UserID)
	}
	if "browser" != client.Claims["origin"] {
		t.Fatalf("want %v, got %v", "browser", client.Claims["origin"])
	}
}

func TestIntegration_QueryTokenAuth_RejectsInvalidToken_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: false, Error: core.NewError("invalid")}
		},
	}

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
	})

	conn, resp, err := websocket.DefaultDialer.Dial(
		authWSURL(server)+"?token=wrong", nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if http.StatusUnauthorized != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
}

func TestIntegration_QueryTokenAuth_RejectsMissingToken_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
	})

	// No ?token= parameter
	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if http.StatusUnauthorized != resp.StatusCode {
		t.Fatalf("want %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if 0 != hub.ClientCount() {
		t.Fatalf("want %v, got %v", 0, hub.ClientCount())
	}
}

func TestIntegration_QueryTokenAuth_EndToEnd_Good(t *testing.T) {
	// Authenticated via query param, then subscribe and receive messages
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			if token == "good-token" {
				return AuthResult{Valid: true, UserID: "alice"}
			}
			return AuthResult{Valid: false, Error: core.NewError("invalid")}
		},
	}

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: auth,
	})

	conn, _, err := websocket.DefaultDialer.Dial(
		authWSURL(server)+"?token=good-token", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Subscribe to a channel
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "events"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if 1 != hub.ChannelSubscriberCount("events") {
		t.Fatalf("want %v, got %v", 1, hub.ChannelSubscriberCount("events"))
	}

	// Send a message to the channel
	err = hub.SendToChannel("events", Message{Type: TypeEvent, Data: "hello alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var received Message
	err = conn.ReadJSON(&received)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if TypeEvent != received.Type {
		t.Fatalf("want %v, got %v", TypeEvent, received.Type)
	}
	if "hello alice" != received.Data {
		t.Fatalf("want %v, got %v", "hello alice", received.Data)
	}
}

func TestAPIKeyAuthenticator_AuthenticatedAlias(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if !result.Authenticated {
		t.Fatal("expected true")
	}
}

func TestQueryTokenAuth_AuthenticatedAlias(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{
				Authenticated: true,
				UserID:        token,
			}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws?token=alias-token", nil)

	result := auth.Authenticate(r)
	if !result.Valid {
		t.Fatal("expected true")
	}
	if !result.Authenticated {
		t.Fatal("expected true")
	}
	if "alias-token" != result.UserID {
		t.Fatalf("want %v, got %v", "alias-token", result.UserID)
	}
}
