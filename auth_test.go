// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	core "dappco.re/go/core"
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
	assert.True(t, result.Authenticated)
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
	assert.True(t, core.Is(result.Error, ErrInvalidAPIKey))
}

func TestAPIKeyAuthenticator_MissingHeader(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	// No Authorization header set

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	assert.True(t, core.Is(result.Error, ErrMissingAuthHeader))
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
			assert.True(t, core.Is(result.Error, ErrMalformedAuthHeader))
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
	assert.True(t, result.Authenticated)
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

func TestAPIKeyAuthenticator_CopiesInputMap(t *testing.T) {
	keys := map[string]string{
		"key-abc": "user-1",
	}

	auth := NewAPIKeyAuth(keys)
	keys["key-abc"] = "user-2"

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)

	assert.True(t, result.Valid)
	assert.Equal(t, "user-1", result.UserID)
}

func TestAPIKeyAuthenticator_SnapshotsInternalMap(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	auth.Keys["key-abc"] = "user-2"

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)

	assert.True(t, result.Valid)
	assert.Equal(t, "user-1", result.UserID)
}

func TestAPIKeyAuthenticator_EmptyUserID_Bad(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.True(t, core.Is(result.Error, ErrInvalidAPIKey))
}

func TestAPIKeyAuthenticator_NilMap_Good(t *testing.T) {
	auth := NewAPIKeyAuth(nil)

	require.NotNil(t, auth)
	assert.Empty(t, auth.Keys)

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.True(t, core.Is(result.Error, ErrInvalidAPIKey))
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
		return AuthResult{Valid: false, Error: core.NewError("custom rejection")}
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)

	assert.False(t, result.Valid)
	assert.EqualError(t, result.Error, "custom rejection")
}

func TestAuthenticatorFunc_NilFunction(t *testing.T) {
	var fn AuthenticatorFunc

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "authenticator function is nil")
}

func TestAuth_NewBearerTokenAuth_DefaultValidator_Bad(t *testing.T) {
	auth := NewBearerTokenAuth()

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer token-123")

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_NewBearerTokenAuth_Bad(t *testing.T) {
	auth := NewBearerTokenAuth()

	result := auth.Validate("")

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_NewBearerTokenAuth_Ugly(t *testing.T) {
	auth := &BearerTokenAuth{}

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_NewBearerTokenAuth_CustomValidator_Good(t *testing.T) {
	auth := NewBearerTokenAuth(func(token string) AuthResult {
		if token == "custom-token" {
			return AuthResult{Authenticated: true, UserID: "custom-user"}
		}
		return AuthResult{Valid: false, Error: core.NewError("bad token")}
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer custom-token")

	result := auth.Authenticate(r)

	assert.True(t, result.Valid)
	assert.True(t, result.Authenticated)
	assert.Equal(t, "custom-user", result.UserID)
}

func TestAuth_authenticatedResult_Good(t *testing.T) {
	claims := map[string]any{
		"role": "admin",
	}

	result := authenticatedResult("user-123", claims)

	assert.True(t, result.Valid)
	assert.True(t, result.Authenticated)
	assert.Equal(t, "user-123", result.UserID)
	assert.Equal(t, claims, result.Claims)
	assert.NoError(t, result.Error)
}

func TestAuth_authenticatedResult_Bad(t *testing.T) {
	result := authenticatedResult("   ", nil)

	assert.False(t, result.Valid)
	assert.False(t, result.Authenticated)
	assert.Empty(t, result.UserID)
	require.Error(t, result.Error)
	assert.True(t, core.Is(result.Error, ErrMissingUserID))
}

func TestAuth_NewBearerTokenAuth_NilValidator_Bad(t *testing.T) {
	auth := NewBearerTokenAuth(nil)

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer token-123")

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_NewQueryTokenAuth_DefaultValidator_ValidateCall_Bad(t *testing.T) {
	auth := NewQueryTokenAuth()

	r := httptest.NewRequest(http.MethodGet, "/ws?token=query-123", nil)

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_NewQueryTokenAuth_Bad(t *testing.T) {
	auth := NewQueryTokenAuth()

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "missing token query parameter")
}

func TestAuth_NewQueryTokenAuth_DefaultValidator_ValidateEmpty_Bad(t *testing.T) {
	auth := NewQueryTokenAuth()

	result := auth.Validate("")

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_NewQueryTokenAuth_Ugly(t *testing.T) {
	auth := &QueryTokenAuth{}

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws?token=abc", nil))

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_NewQueryTokenAuth_CustomValidator_Good(t *testing.T) {
	auth := NewQueryTokenAuth(func(token string) AuthResult {
		if token == "browser-token" {
			return AuthResult{Authenticated: true, UserID: "browser-user"}
		}
		return AuthResult{Valid: false, Error: core.NewError("bad token")}
	})

	r := httptest.NewRequest(http.MethodGet, "/ws?token=browser-token", nil)

	result := auth.Authenticate(r)

	assert.True(t, result.Valid)
	assert.True(t, result.Authenticated)
	assert.Equal(t, "browser-user", result.UserID)
}

func TestAuth_NewQueryTokenAuth_NilValidator_Bad(t *testing.T) {
	auth := NewQueryTokenAuth(nil)

	r := httptest.NewRequest(http.MethodGet, "/ws?token=query-123", nil)

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "validate function is not configured")
}

func TestAuth_CustomValidator_EmptyUserID_Bad(t *testing.T) {
	t.Run("bearer", func(t *testing.T) {
		auth := NewBearerTokenAuth(func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: ""}
		})

		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.Header.Set("Authorization", "Bearer token-123")

		result := auth.Authenticate(r)

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.True(t, core.Is(result.Error, ErrMissingUserID))
	})

	t.Run("query", func(t *testing.T) {
		auth := NewQueryTokenAuth(func(token string) AuthResult {
			return AuthResult{Authenticated: true}
		})

		r := httptest.NewRequest(http.MethodGet, "/ws?token=query-123", nil)

		result := auth.Authenticate(r)

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.True(t, core.Is(result.Error, ErrMissingUserID))
	})
}

func TestAuth_ClaimsAreCloned(t *testing.T) {
	claims := map[string]any{
		"role": "admin",
	}

	auth := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{Valid: true, UserID: "user-123", Claims: claims}
	})

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	require.True(t, result.Valid)
	require.NotNil(t, result.Claims)

	claims["role"] = "user"
	assert.Equal(t, "admin", result.Claims["role"])
}

func TestAuth_UserIDIsTrimmedOnSuccess(t *testing.T) {
	auth := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{
			Valid:  true,
			UserID: "  user-123  ",
		}
	})

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))

	require.True(t, result.Valid)
	assert.Equal(t, "user-123", result.UserID)
}

func TestAuth_Authenticate_NilReceivers_Ugly(t *testing.T) {
	t.Run("api key", func(t *testing.T) {
		var auth *APIKeyAuthenticator

		result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "authenticator is nil")
	})

	t.Run("bearer", func(t *testing.T) {
		var auth *BearerTokenAuth

		result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "authenticator is nil")
	})

	t.Run("query", func(t *testing.T) {
		var auth *QueryTokenAuth

		result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws?token=abc", nil))

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "authenticator is nil")
	})
}

func TestAuth_Authenticate_NilRequest_Ugly(t *testing.T) {
	t.Run("api key", func(t *testing.T) {
		auth := NewAPIKeyAuth(map[string]string{"key": "user"})

		result := auth.Authenticate(nil)

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "request is nil")
	})

	t.Run("bearer", func(t *testing.T) {
		auth := NewBearerTokenAuth()

		result := auth.Authenticate(nil)

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "request is nil")
	})

	t.Run("query", func(t *testing.T) {
		auth := NewQueryTokenAuth()

		result := auth.Authenticate(nil)

		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "request is nil")
	})
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
	assert.True(t, core.Is(failureResult.Error, ErrInvalidAPIKey))
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
		return AuthResult{Valid: false, Error: core.NewError("bad token")}
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

func TestIntegration_AuthenticatorFuncNil_WithHub(t *testing.T) {
	var fn AuthenticatorFunc

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: fn,
	})

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	if conn != nil {
		conn.Close()
	}

	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, hub.ClientCount())
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

	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, hub.ClientCount())

	select {
	case result := <-failureCalled:
		assert.False(t, result.Valid)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "authenticator panicked")
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
	require.True(t, core.JSONUnmarshal(data, &msg).OK)
	assert.Equal(t, TypeEvent, msg.Type)
	assert.Equal(t, "hello", msg.Data)
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

	assert.True(t, result.Valid)
	assert.True(t, result.Authenticated)
	assert.Equal(t, "user-42", result.UserID)
	assert.Equal(t, "admin", result.Claims["role"])
	assert.Equal(t, "jwt", result.Claims["auth_method"])
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

	assert.False(t, result.Valid)
	assert.EqualError(t, result.Error, "token expired")
}

func TestBearerTokenAuth_MissingHeader_Bad(t *testing.T) {
	auth := &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	assert.True(t, core.Is(result.Error, ErrMissingAuthHeader))
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

			assert.False(t, result.Valid)
			assert.True(t, core.Is(result.Error, ErrMalformedAuthHeader))
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

	assert.True(t, result.Valid)
	assert.Equal(t, "user-1", result.UserID)
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
	require.NoError(t, err)
	defer conn.Close()
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	client := connectedClient
	mu.Unlock()

	require.NotNil(t, client)
	assert.Equal(t, "jwt-user", client.UserID)
	assert.Equal(t, "bearer", client.Claims["auth_method"])
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

	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, hub.ClientCount())
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

	assert.True(t, result.Valid)
	assert.True(t, result.Authenticated)
	assert.Equal(t, "browser-user", result.UserID)
	assert.Equal(t, "query_param", result.Claims["auth_method"])
}

func TestQueryTokenAuth_InvalidToken_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: false, Error: core.NewError("unknown token")}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws?token=bad-token", nil)

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	assert.EqualError(t, result.Error, "unknown token")
}

func TestQueryTokenAuth_MissingParam_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	assert.Contains(t, result.Error.Error(), "missing token query parameter")
}

func TestQueryTokenAuth_EmptyParam_Bad(t *testing.T) {
	auth := &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: "should-not-reach"}
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws?token=", nil)

	result := auth.Authenticate(r)

	assert.False(t, result.Valid)
	assert.Contains(t, result.Error.Error(), "missing token query parameter")
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

	assert.False(t, result.Valid)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "request URL is nil")
	assert.False(t, called, "validate should not be called when request URL is nil")
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
	require.NoError(t, err)
	defer conn.Close()
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, hub.ClientCount())

	mu.Lock()
	client := connectedClient
	mu.Unlock()

	require.NotNil(t, client)
	assert.Equal(t, "browser-user-99", client.UserID)
	assert.Equal(t, "browser", client.Claims["origin"])
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

	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, hub.ClientCount())
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

	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 0, hub.ClientCount())
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
	require.NoError(t, err)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Subscribe to a channel
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "events"})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, hub.ChannelSubscriberCount("events"))

	// Send a message to the channel
	err = hub.SendToChannel("events", Message{Type: TypeEvent, Data: "hello alice"})
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var received Message
	err = conn.ReadJSON(&received)
	require.NoError(t, err)
	assert.Equal(t, TypeEvent, received.Type)
	assert.Equal(t, "hello alice", received.Data)
}

func TestAPIKeyAuthenticator_AuthenticatedAlias(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)

	assert.True(t, result.Valid)
	assert.True(t, result.Authenticated)
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

	assert.True(t, result.Valid)
	assert.True(t, result.Authenticated)
	assert.Equal(t, "alias-token", result.UserID)
}
