// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	assert.True(t, result.Valid)
	assert.Equal(t, "user-1", result.UserID)
	assert.Equal(t, "api_key", result.Claims["auth_method"])
	assert.NoError(t, result.Error)
}

func TestAPIKeyAuthenticator_InvalidKey(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer wrong-key")

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	assert.Empty(t, result.UserID)
	assert.True(t, errors.Is(result.Error, ErrInvalidAPIKey))
}

func TestAPIKeyAuthenticator_MissingHeader(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	// No Authorization header set

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	assert.True(t, errors.Is(result.Error, ErrMissingAuthHeader))
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

			assert.False(t, result.Valid)
			assert.True(t, errors.Is(result.Error, ErrMalformedAuthHeader))
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

	assert.True(t, result.Valid)
	assert.Equal(t, "user-1", result.UserID)
}

func TestAPIKeyAuthenticator_SecondKey(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
		"key-def": "user-2",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-def")

	result := auth.Authenticate(r)

	assert.True(t, result.Valid)
	assert.Equal(t, "user-2", result.UserID)
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

	assert.True(t, called)
	assert.True(t, result.Valid)
	assert.Equal(t, "func-user", result.UserID)
}

func TestAuthenticatorFunc_Rejection(t *testing.T) {
	fn := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{Valid: false, Error: errors.New("custom rejection")}
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)

	assert.False(t, result.Valid)
	assert.EqualError(t, result.Error, "custom rejection")
}

// ---------------------------------------------------------------------------
// Unit tests — nil Authenticator (backward compat)
// ---------------------------------------------------------------------------

func TestNilAuthenticator_AllConnectionsAccepted(t *testing.T) {
	hub := NewHub() // No authenticator set
	assert.Nil(t, hub.config.Authenticator)
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
	return "ws" + strings.TrimPrefix(server.URL, "http")
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
	require.NoError(t, err)
	defer conn.Close()
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	// Give the hub a moment to process registration
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	client := connectedClient
	mu.Unlock()

	require.NotNil(t, client, "OnConnect should have fired")
	assert.Equal(t, "user-42", client.UserID)
	assert.Equal(t, "api_key", client.Claims["auth_method"])
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

	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, hub.ClientCount())
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

	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, hub.ClientCount())
}

func TestIntegration_NilAuthenticator_BackwardCompat(t *testing.T) {
	// No authenticator — all connections should be accepted
	server, hub, _ := startAuthTestHub(t, HubConfig{})

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	require.NoError(t, err)
	defer conn.Close()
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, hub.ClientCount())
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

	assert.True(t, failureCalled, "OnAuthFailure should have been called")
	assert.False(t, failureResult.Valid)
	assert.True(t, errors.Is(failureResult.Error, ErrInvalidAPIKey))
	assert.NotNil(t, failureRequest)
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
		require.NoError(t, err, "key %s should connect", k.key)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		conns = append(conns, conn)
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, 3, hub.ClientCount())

	mu.Lock()
	defer mu.Unlock()
	for _, k := range keys {
		client, ok := connectedClients[k.userID]
		require.True(t, ok, "should have client for %s", k.userID)
		assert.Equal(t, k.userID, client.UserID)
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
		return AuthResult{Valid: false, Error: errors.New("bad token")}
	})

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: fn,
	})

	// Valid token via query parameter
	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server)+"?token=magic", nil)
	require.NoError(t, err)
	defer conn.Close()
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, hub.ClientCount())

	// Invalid token
	conn2, resp2, _ := websocket.DefaultDialer.Dial(authWSURL(server)+"?token=wrong", nil)
	if conn2 != nil {
		conn2.Close()
	}
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
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
	require.NoError(t, err)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Broadcast a message
	err = hub.Broadcast(Message{Type: TypeEvent, Data: "hello"})
	require.NoError(t, err)

	// Read it
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)

	var msg Message
	require.NoError(t, json.Unmarshal(data, &msg))
	assert.Equal(t, TypeEvent, msg.Type)
	assert.Equal(t, "hello", msg.Data)
}
