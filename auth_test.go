// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	// Note: AX-6 — internal concurrency primitive; structural for go-ws hub state (RFC mandates concurrent connection map).
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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("user-1", result.UserID) {
		t.Errorf("expected %v, got %v", "user-1", result.UserID)
	}
	if !testEqual("api_key", result.Claims["auth_method"]) {
		t.Errorf("expected %v, got %v", "api_key", result.Claims["auth_method"])
	}
	if err := result.Error; err != nil {
		t.Errorf("expected no error, got %v", err)
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
		t.Errorf("expected false")
	}
	if !testIsEmpty(result.UserID) {
		t.Errorf("expected empty value, got %v", result.UserID)
	}
	if !(core.Is(result.Error, ErrInvalidAPIKey)) {
		t.Errorf("expected true")
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
		t.Errorf("expected false")
	}
	if !(core.Is(result.Error, ErrMissingAuthHeader)) {
		t.Errorf("expected true")
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
				t.Errorf("expected false")
			}
			if !(core.Is(result.Error, ErrMalformedAuthHeader)) {
				t.Errorf("expected true")
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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("user-1", result.UserID) {
		t.Errorf("expected %v, got %v", "user-1", result.UserID)
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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !testEqual("user-2", result.UserID) {
		t.Errorf("expected %v, got %v", "user-2", result.UserID)
	}

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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !testEqual("user-1", result.UserID) {
		t.Errorf("expected %v, got %v", "user-1", result.UserID)
	}

}

func TestAPIKeyAuthenticator_SnapshotsInternalMap(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	auth.Keys["key-abc"] = "user-2"

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !testEqual("user-1", result.UserID) {
		t.Errorf("expected %v, got %v", "user-1", result.UserID)
	}

}

func TestAPIKeyAuthenticator_ManualLiteral_DoesNotUseExportedKeys(t *testing.T) {
	auth := &APIKeyAuthenticator{
		Keys: map[string]string{
			"key-abc": "user-1",
		},
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrInvalidAPIKey)) {
		t.Errorf("expected true")
	}

}

func TestAPIKeyAuthenticator_EmptyUserID_Bad(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrInvalidAPIKey)) {
		t.Errorf("expected true")
	}

}

func TestAPIKeyAuthenticator_NilMap_Good(t *testing.T) {
	auth := NewAPIKeyAuth(nil)
	if testIsNil(auth) {
		t.Fatalf("expected non-nil value")
	}
	if !testIsEmpty(auth.Keys) {
		t.Errorf("expected empty value, got %v", auth.Keys)
	}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrInvalidAPIKey)) {
		t.Errorf("expected true")
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
	if !(called) {
		t.Errorf("expected true")
	}
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !testEqual("func-user", result.UserID) {
		t.Errorf("expected %v, got %v", "func-user", result.UserID)
	}

}

func TestAuthenticatorFunc_Rejection(t *testing.T) {
	fn := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{Valid: false, Error: core.NewError("custom rejection")}
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil || err.Error() != "custom rejection" {
		t.Errorf("expected error %q, got %v", "custom rejection", err)
	}

}

func TestAuthenticatorFunc_NilFunction(t *testing.T) {
	var fn AuthenticatorFunc

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	result := fn.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "authenticator function is nil") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "authenticator function is nil")
	}

}

func TestAuth_NewBearerTokenAuth_DefaultValidator_Bad(t *testing.T) {
	auth := NewBearerTokenAuth()

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer token-123")

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

}

func TestAuth_NewBearerTokenAuth_Bad(t *testing.T) {
	auth := NewBearerTokenAuth()

	result := auth.Validate("")
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

}

func TestAuth_NewBearerTokenAuth_Ugly(t *testing.T) {
	auth := &BearerTokenAuth{}

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("custom-user", result.UserID) {
		t.Errorf("expected %v, got %v", "custom-user", result.UserID)
	}

}

func TestAuth_authenticatedResult_Good(t *testing.T) {
	claims := map[string]any{
		"role": "admin",
	}

	result := authenticatedResult("user-123", claims)
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("user-123", result.UserID) {
		t.Errorf("expected %v, got %v", "user-123", result.UserID)
	}
	if !testEqual(claims, result.Claims) {
		t.Errorf("expected %v, got %v", claims, result.Claims)
	}
	if err := result.Error; err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestAuth_authenticatedResult_Bad(t *testing.T) {
	result := authenticatedResult("   ", nil)
	if result.Valid {
		t.Errorf("expected false")
	}
	if result.Authenticated {
		t.Errorf("expected false")
	}
	if !testIsEmpty(result.UserID) {
		t.Errorf("expected empty value, got %v", result.UserID)
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrMissingUserID)) {
		t.Errorf("expected true")
	}

}

type authClaimNode struct {
	Next *authClaimNode
}

func deepAuthClaimNode(depth int) *authClaimNode {
	root := &authClaimNode{}
	current := root
	for i := 0; i < depth; i++ {
		next := &authClaimNode{}
		current.Next = next
		current = next
	}
	return root
}

func deepAuthClaimsChain(depth int) map[string]any {
	return map[string]any{
		"chain": deepAuthClaimNode(depth),
	}
}

func TestAuth_authenticatedResult_Ugly(t *testing.T) {
	claims := deepAuthClaimsChain(maxClaimsCloneDepth + 64)

	result := authenticatedResult("user-123", claims)
	if result.Valid {
		t.Errorf("expected false")
	}
	if result.Authenticated {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrInvalidAuthClaims)) {
		t.Errorf("expected true")
	}

}

func TestAuth_finalizeAuthResult_Good(t *testing.T) {
	claims := map[string]any{
		"role": "admin",
		"scope": map[string]any{
			"channels": []string{"alpha", "beta"},
		},
	}

	result := finalizeAuthResult(AuthResult{
		Authenticated: true,
		UserID:        "  user-123  ",
		Claims:        claims,
	})
	if !(result.Valid) {
		t.Fatalf("expected true")
	}
	if !(result.Authenticated) {
		t.Fatalf("expected true")
	}
	if !testEqual("user-123", result.UserID) {
		t.Errorf("expected %v, got %v", "user-123", result.UserID)
	}
	if !testEqual("admin", result.Claims["role"]) {
		t.Errorf("expected %v, got %v", "admin", result.Claims["role"])
	}

	claims["role"] = "user"
	claimsScope := claims["scope"].(map[string]any)
	claimsScope["channels"] = []string{"gamma"}
	if !testEqual("admin", result.Claims["role"]) {
		t.Errorf("expected %v, got %v", "admin", result.Claims["role"])
	}

	resultScope := result.Claims["scope"].(map[string]any)
	if !testEqual([]string{"alpha", "beta"}, resultScope["channels"]) {
		t.Errorf("expected %v, got %v", []string{"alpha", "beta"}, resultScope["channels"])
	}

}

func TestAuth_finalizeAuthResult_Bad(t *testing.T) {
	result := finalizeAuthResult(AuthResult{
		Valid:  true,
		UserID: "   ",
	})
	if result.Valid {
		t.Errorf("expected false")
	}
	if result.Authenticated {
		t.Errorf("expected false")
	}
	if !testIsEmpty(result.UserID) {
		t.Errorf("expected empty value, got %v", result.UserID)
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrMissingUserID)) {
		t.Errorf("expected true")
	}

}

func TestAuth_finalizeAuthResult_Ugly(t *testing.T) {
	result := finalizeAuthResult(AuthResult{
		Valid:  true,
		UserID: "user-123",
		Claims: deepAuthClaimsChain(maxClaimsCloneDepth + 64),
	})
	if result.Valid {
		t.Errorf("expected false")
	}
	if result.Authenticated {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrInvalidAuthClaims)) {
		t.Errorf("expected true")
	}

}

func TestAuth_NewBearerTokenAuth_NilValidator_Bad(t *testing.T) {
	auth := NewBearerTokenAuth(nil)

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer token-123")

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

}

func TestAuth_NewQueryTokenAuth_DefaultValidator_ValidateCall_Bad(t *testing.T) {
	auth := NewQueryTokenAuth()

	r := httptest.NewRequest(http.MethodGet, "/ws?token=query-123", nil)

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

}

func TestAuth_NewQueryTokenAuth_Bad(t *testing.T) {
	auth := NewQueryTokenAuth()

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "missing token query parameter") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "missing token query parameter")
	}

}

func TestAuth_NewQueryTokenAuth_DefaultValidator_ValidateEmpty_Bad(t *testing.T) {
	auth := NewQueryTokenAuth()

	result := auth.Validate("")
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

}

func TestAuth_NewQueryTokenAuth_Ugly(t *testing.T) {
	auth := &QueryTokenAuth{}

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws?token=abc", nil))
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("browser-user", result.UserID) {
		t.Errorf("expected %v, got %v", "browser-user", result.UserID)
	}

}

func TestAuth_NewQueryTokenAuth_NilValidator_Bad(t *testing.T) {
	auth := NewQueryTokenAuth(nil)

	r := httptest.NewRequest(http.MethodGet, "/ws?token=query-123", nil)

	result := auth.Authenticate(r)
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "validate function is not configured") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "validate function is not configured")
	}

}

func TestAuth_CustomValidator_EmptyUserID_Bad(t *testing.T) {
	t.Run("bearer", func(t *testing.T) {
		auth := NewBearerTokenAuth(func(token string) AuthResult {
			return AuthResult{Valid: true, UserID: ""}
		})

		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.Header.Set("Authorization", "Bearer token-123")

		result := auth.Authenticate(r)
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !(core.Is(result.Error, ErrMissingUserID)) {
			t.Errorf("expected true")
		}

	})

	t.Run("query", func(t *testing.T) {
		auth := NewQueryTokenAuth(func(token string) AuthResult {
			return AuthResult{Authenticated: true}
		})

		r := httptest.NewRequest(http.MethodGet, "/ws?token=query-123", nil)

		result := auth.Authenticate(r)
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !(core.Is(result.Error, ErrMissingUserID)) {
			t.Errorf("expected true")
		}

	})
}

func TestAuth_ClaimsAreCloned(t *testing.T) {
	claims := map[string]any{
		"role": "admin",
		"scope": map[string]any{
			"channels": []string{"alpha", "beta"},
		},
	}

	auth := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{Valid: true, UserID: "user-123", Claims: claims}
	})

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if !(result.Valid) {
		t.Fatalf("expected true")
	}
	if testIsNil(result.Claims) {
		t.Fatalf("expected non-nil value")
	}

	claims["role"] = "user"
	claimsScope := claims["scope"].(map[string]any)
	claimsScope["channels"] = []string{"gamma"}
	if !testEqual("admin", result.Claims["role"]) {
		t.Errorf("expected %v, got %v", "admin", result.Claims["role"])
	}

	resultScope := result.Claims["scope"].(map[string]any)
	if !testEqual([]string{"alpha", "beta"}, resultScope["channels"]) {
		t.Errorf("expected %v, got %v", []string{"alpha", "beta"}, resultScope["channels"])
	}

}

func TestAuth_ClaimsAreCloneSafeForCycles(t *testing.T) {
	claims := map[string]any{}
	claims["self"] = claims

	auth := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{Valid: true, UserID: "user-123", Claims: claims}
	})

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if !(result.Valid) {
		t.Fatalf("expected true")
	}
	if testIsNil(result.Claims) {
		t.Fatalf("expected non-nil value")
	}

	clonedSelf, ok := result.Claims["self"].(map[string]any)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if testEqual(reflect.ValueOf(claims).Pointer(), reflect.ValueOf(clonedSelf).Pointer()) {
		t.Errorf("expected values to differ: %v", reflect.ValueOf(clonedSelf).Pointer())
	}

}

func TestAuth_ClaimsRejectUnsupportedKinds(t *testing.T) {
	auth := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{
			Valid:  true,
			UserID: "user-123",
			Claims: map[string]any{
				"stream": make(chan int),
			},
		}
	})

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if result.Valid {
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !(core.Is(result.Error, ErrInvalidAuthClaims)) {
		t.Errorf("expected true")
	}

}

func TestAuth_deepCloneValueWithState_Good(t *testing.T) {
	type secretClaim struct {
		Name  string
		bytes []byte
		Next  *secretClaim
	}

	original := &secretClaim{
		Name:  "alice",
		bytes: []byte{1, 2, 3},
	}
	original.Next = original

	clonedValue, ok := deepCloneValueWithState(reflect.ValueOf(original), make(map[uintptr]reflect.Value), 0)
	if !(ok) {
		t.Fatalf("expected true")
	}

	clone := clonedValue.(*secretClaim)
	if testSame(original, clone) {
		t.Fatalf("expected different references")
	}
	if testIsNil(clone.Next) {
		t.Fatalf("expected non-nil value")
	}
	if !testSame(clone, clone.Next) {
		t.Errorf("expected same reference")
	}
	if !testEqual([]byte{1, 2, 3}, clone.bytes) {
		t.Errorf("expected %v, got %v", []byte{1, 2, 3}, clone.bytes)
	}

	original.bytes[0] = 9
	if !testEqual([]byte{1, 2, 3}, clone.bytes) {
		t.Errorf("expected %v, got %v", []byte{1, 2, 3}, clone.bytes)
	}

	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	clonedMap, ok := deepCloneValueWithState(reflect.ValueOf(cyclicMap), make(map[uintptr]reflect.Value), 0)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if testIsNil(clonedMap) {
		t.Fatalf("expected non-nil value")
	}

	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice
	clonedSlice, ok := deepCloneValueWithState(reflect.ValueOf(cyclicSlice), make(map[uintptr]reflect.Value), 0)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if testIsNil(clonedSlice) {
		t.Fatalf("expected non-nil value")
	}

}

func TestAuth_deepCloneValueWithState_Bad(t *testing.T) {
	value := reflect.ValueOf(struct {
		secret int
	}{secret: 123}).Field(0)

	cloned, ok := deepCloneValueWithState(value, make(map[uintptr]reflect.Value), 0)
	if ok {
		t.Errorf("expected false")
	}
	if !testIsNil(cloned) {
		t.Errorf("expected nil, got %T", cloned)
	}

}

func TestAuth_deepCloneValueWithState_Ugly(t *testing.T) {
	cloned, ok := deepCloneValueWithState(reflect.ValueOf(deepAuthClaimNode(maxClaimsCloneDepth+1)), make(map[uintptr]reflect.Value), 0)
	if ok {
		t.Errorf("expected false")
	}
	if !testIsNil(cloned) {
		t.Errorf("expected nil, got %T", cloned)
	}

}

func TestAuth_valueInterface_Good(t *testing.T) {
	type claim struct {
		secret int
	}

	value := reflect.ValueOf(&claim{secret: 7}).Elem().FieldByName("secret")
	if !testEqual(7, valueInterface(value)) {
		t.Errorf("expected %v, got %v", 7, valueInterface(value))
	}

}

func TestAuth_valueInterface_Bad(t *testing.T) {
	if !testIsNil(valueInterface(reflect.Value{})) {
		t.Errorf("expected nil, got %T", valueInterface(reflect.Value{}))
	}

}

func TestAuth_valueInterface_Ugly(t *testing.T) {
	type claim struct {
		secret int
	}
	if !testIsNil(valueInterface(reflect.ValueOf(claim{secret: 7}).FieldByName("secret"))) {
		t.Errorf("expected nil, got %T", valueInterface(reflect.ValueOf(claim{secret: 7}).FieldByName("secret")))
	}

}

func TestAuth_setReflectValue_Good(t *testing.T) {
	type claim struct {
		Value int
	}

	original := &claim{}
	field := reflect.ValueOf(original).Elem().FieldByName("Value")
	if !(setReflectValue(field, reflect.ValueOf(7))) {
		t.Errorf("expected true")
	}
	if !testEqual(7, original.Value) {
		t.Errorf("expected %v, got %v", 7, original.Value)
	}

}

func TestAuth_setReflectValue_Bad(t *testing.T) {
	if setReflectValue(reflect.Value{}, reflect.ValueOf(7)) {
		t.Errorf("expected false")
	}

}

func TestAuth_setReflectValue_Ugly(t *testing.T) {
	type claim struct {
		secret int
	}

	original := &claim{}
	field := reflect.ValueOf(original).Elem().FieldByName("secret")
	if !(setReflectValue(field, reflect.ValueOf(7))) {
		t.Errorf("expected true")
	}
	if !testEqual(7, original.secret) {
		t.Errorf("expected %v, got %v", 7, original.secret)
	}

}

func TestAuth_assignClonedValue_Good(t *testing.T) {
	type alias int

	var dst alias
	if !(assignClonedValue(reflect.ValueOf(&dst).Elem(), int64(7))) {
		t.Errorf("expected true")
	}
	if !testEqual(alias(7), dst) {
		t.Errorf("expected %v, got %v", alias(7), dst)
	}

}

func TestAuth_assignClonedValue_Bad(t *testing.T) {
	var dst int
	if assignClonedValue(reflect.Value{}, 7) {
		t.Errorf("expected false")
	}
	if assignClonedValue(reflect.ValueOf(&dst).Elem(), struct {
	}{}) {
		t.Errorf("expected false")
	}

}

func TestAuth_assignClonedValue_Ugly(t *testing.T) {
	var dst int
	if !(assignClonedValue(reflect.ValueOf(&dst).Elem(), nil)) {
		t.Errorf("expected true")
	}
	if !testIsZero(dst) {
		t.Errorf("expected zero value, got %v", dst)
	}

}

func TestAuth_cloneStringMap_Good(t *testing.T) {
	original := map[string]string{
		"key-abc": "user-1",
	}

	clone := cloneStringMap(original)
	if testIsNil(clone) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual(original, clone) {
		t.Errorf("expected %v, got %v", original, clone)
	}

	original["key-abc"] = "user-2"
	if !testEqual("user-1", clone["key-abc"]) {
		t.Errorf("expected %v, got %v", "user-1", clone["key-abc"])
	}

}

func TestAuth_cloneStringMap_Bad(t *testing.T) {
	if !testIsNil(cloneStringMap(nil)) {
		t.Errorf("expected nil, got %T", cloneStringMap(nil))
	}

}

func TestAuth_cloneStringMap_Ugly(t *testing.T) {
	if !testIsNil(cloneStringMap(map[string]string{})) {
		t.Errorf("expected nil, got %T", cloneStringMap(map[string]string{}))
	}

}

func TestAuth_deepCloneValue_Good(t *testing.T) {
	type nestedClaim struct {
		Name   string
		Tags   []string
		Bytes  []byte
		Meta   map[string]any
		Counts [2]int
		Child  *struct {
			Enabled bool
			Flags   []string
		}
		Optional *struct {
			Label string
		}
	}

	original := nestedClaim{
		Name:   "alice",
		Tags:   []string{"alpha", "beta"},
		Bytes:  []byte{1, 2, 3},
		Meta:   map[string]any{"channels": []string{"one", "two"}},
		Counts: [2]int{7, 9},
		Child: &struct {
			Enabled bool
			Flags   []string
		}{
			Enabled: true,
			Flags:   []string{"root", "admin"},
		},
		Optional: nil,
	}

	cloned := deepCloneValue(reflect.ValueOf(original))
	if testIsNil(cloned) {
		t.Fatalf("expected non-nil value")
	}

	clone := cloned.(nestedClaim)
	if testSame(original.Child, clone.Child) {
		t.Fatalf("expected different references")
	}
	if !testEqual(original, clone) {
		t.Errorf("expected %v, got %v", original, clone)
	}

	original.Tags[0] = "mutated"
	original.Bytes[0] = 9
	original.Meta["channels"] = []string{"changed"}
	original.Counts[0] = 42
	original.Child.Enabled = false
	original.Child.Flags[0] = "guest"
	if !testEqual([]string{"alpha", "beta"}, clone.Tags) {
		t.Errorf("expected %v, got %v", []string{"alpha", "beta"}, clone.Tags)
	}
	if !testEqual([]byte{1, 2, 3}, clone.Bytes) {
		t.Errorf("expected %v, got %v", []byte{1, 2, 3}, clone.Bytes)
	}
	if !testEqual([]string{"one", "two"}, clone.Meta["channels"]) {
		t.Errorf("expected %v, got %v", []string{"one", "two"}, clone.Meta["channels"])
	}
	if !testEqual([2]int{7, 9}, clone.Counts) {
		t.Errorf("expected %v, got %v", [2]int{7, 9}, clone.Counts)
	}
	if !(clone.Child.Enabled) {
		t.Errorf("expected true")
	}
	if !testEqual([]string{"root", "admin"}, clone.Child.Flags) {
		t.Errorf("expected %v, got %v", []string{"root", "admin"}, clone.Child.Flags)
	}
	if !testIsNil(clone.Optional) {
		t.Errorf("expected nil, got %T", clone.Optional)
	}

}

func TestAuth_ClaimsDeepClone_UnexportedMutableFields(t *testing.T) {
	type opaqueClaim struct {
		Name  string
		roles []string
		meta  map[string]any
	}

	original := &opaqueClaim{
		Name:  "alice",
		roles: []string{"admin", "ops"},
		meta: map[string]any{
			"channels": []string{"alpha", "beta"},
		},
	}

	auth := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{Valid: true, UserID: "user-123", Claims: map[string]any{"opaque": original}}
	})

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if !(result.Valid) {
		t.Fatalf("expected true")
	}

	cloned, ok := result.Claims["opaque"].(*opaqueClaim)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if testSame(original, cloned) {
		t.Fatalf("expected different references")
	}

	original.roles[0] = "viewer"
	original.meta["channels"] = []string{"gamma"}
	if !testEqual([]string{"admin", "ops"}, cloned.roles) {
		t.Errorf("expected %v, got %v", []string{"admin", "ops"}, cloned.roles)
	}
	if !testEqual([]string{"alpha", "beta"}, cloned.meta["channels"]) {
		t.Errorf("expected %v, got %v", []string{"alpha", "beta"}, cloned.meta["channels"])
	}

}

func TestAuth_cloneClaimsValue_Good(t *testing.T) {
	type opaqueClaim struct {
		Name  string
		roles []string
		meta  map[string]any
	}

	original := &opaqueClaim{
		Name:  "alice",
		roles: []string{"admin", "ops"},
		meta: map[string]any{
			"channels": []string{"alpha", "beta"},
		},
	}

	claims := map[string]any{
		"profile": original,
		"self":    nil,
	}
	claims["self"] = claims

	clonedValue, ok := cloneClaimsValue(reflect.ValueOf(claims), make(map[uintptr]reflect.Value), 0)
	if !(ok) {
		t.Fatalf("expected true")
	}

	cloned, ok := clonedValue.(map[string]any)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if testEqual(reflect.ValueOf(claims).Pointer(), reflect.ValueOf(cloned).Pointer()) {
		t.Errorf("expected values to differ: %v", reflect.ValueOf(cloned).Pointer())
	}

	clonedProfile, ok := cloned["profile"].(*opaqueClaim)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if testSame(original, clonedProfile) {
		t.Fatalf("expected different references")
	}

	clonedSelf, ok := cloned["self"].(map[string]any)
	if !(ok) {
		t.Fatalf("expected true")
	}
	if testEqual(reflect.ValueOf(claims).Pointer(), reflect.ValueOf(clonedSelf).Pointer()) {
		t.Errorf("expected values to differ: %v", reflect.ValueOf(clonedSelf).Pointer())
	}
	if !testEqual("alice", clonedProfile.Name) {
		t.Errorf("expected %v, got %v", "alice", clonedProfile.Name)
	}
	if !testEqual([]string{"admin", "ops"}, clonedProfile.roles) {
		t.Errorf("expected %v, got %v", []string{"admin", "ops"}, clonedProfile.roles)
	}
	if !testEqual([]string{"alpha", "beta"}, clonedProfile.meta["channels"]) {
		t.Errorf("expected %v, got %v", []string{"alpha", "beta"}, clonedProfile.meta["channels"])
	}

	original.roles[0] = "viewer"
	original.meta["channels"] = []string{"gamma"}
	if !testEqual([]string{"admin", "ops"}, clonedProfile.roles) {
		t.Errorf("expected %v, got %v", []string{"admin", "ops"}, clonedProfile.roles)
	}
	if !testEqual([]string{"alpha", "beta"}, clonedProfile.meta["channels"]) {
		t.Errorf("expected %v, got %v", []string{"alpha", "beta"}, clonedProfile.meta["channels"])
	}

}

func TestAuth_cloneClaimsValue_Bad(t *testing.T) {
	tests := []struct {
		name  string
		value reflect.Value
	}{
		{name: "unsupported kind", value: reflect.ValueOf(make(chan int))},
		{name: "unsupported func", value: reflect.ValueOf(func() {})},
		{
			name:  "unaddressable unexported field",
			value: reflect.ValueOf(struct{ secret int }{secret: 1}).Field(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloned, ok := cloneClaimsValue(tt.value, make(map[uintptr]reflect.Value), 0)
			if ok {
				t.Errorf("expected false")
			}
			if !testIsNil(cloned) {
				t.Errorf("expected nil, got %T", cloned)
			}

		})
	}
}

func TestAuth_cloneClaimsValue_Ugly(t *testing.T) {
	cloned, ok := cloneClaimsValue(reflect.ValueOf(deepAuthClaimNode(maxClaimsCloneDepth+1)), make(map[uintptr]reflect.Value), 0)
	if ok {
		t.Errorf("expected false")
	}
	if !testIsNil(cloned) {
		t.Errorf("expected nil, got %T", cloned)
	}

}

func TestAuth_deepCloneValue_Bad(t *testing.T) {
	var nilSlice []string
	var nilMap map[string]int
	var nilPtr *int
	if !testIsNil(deepCloneValue(reflect.ValueOf(nilSlice))) {
		t.Errorf("expected nil, got %T", deepCloneValue(reflect.ValueOf(nilSlice)))
	}
	if !testIsNil(deepCloneValue(reflect.ValueOf(nilMap))) {
		t.Errorf("expected nil, got %T", deepCloneValue(reflect.ValueOf(nilMap)))
	}
	if !testIsNil(deepCloneValue(reflect.ValueOf(nilPtr))) {
		t.Errorf("expected nil, got %T", deepCloneValue(reflect.ValueOf(nilPtr)))
	}
	if !testIsNil(deepCloneValue(reflect.Value{})) {
		t.Errorf("expected nil, got %T", deepCloneValue(reflect.Value{}))
	}
	if !testEqual(42, deepCloneValue(reflect.ValueOf(42))) {
		t.Errorf("expected %v, got %v", 42, deepCloneValue(reflect.ValueOf(42)))
	}

}

func TestAuth_deepCloneValue_Ugly(t *testing.T) {
	ch := make(chan int, 1)
	fn := func() {}
	if !testEqual(ch, deepCloneValue(reflect.ValueOf(ch))) {
		t.Errorf("expected %v, got %v", ch, deepCloneValue(reflect.ValueOf(ch)))
	}
	testNotPanics(t, func() {
		_ = deepCloneValue(reflect.ValueOf(fn))
	})

}

func TestAuth_UserIDIsTrimmedOnSuccess(t *testing.T) {
	auth := AuthenticatorFunc(func(r *http.Request) AuthResult {
		return AuthResult{
			Valid:  true,
			UserID: "  user-123  ",
		}
	})

	result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if !(result.Valid) {
		t.Fatalf("expected true")
	}
	if !testEqual("user-123", result.UserID) {
		t.Errorf("expected %v, got %v", "user-123", result.UserID)
	}

}

func TestAuth_Authenticate_NilReceivers_Ugly(t *testing.T) {
	t.Run("api key", func(t *testing.T) {
		var auth *APIKeyAuthenticator

		result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "authenticator is nil") {
			t.Errorf("expected %v to contain %v", result.Error.Error(), "authenticator is nil")
		}

	})

	t.Run("bearer", func(t *testing.T) {
		var auth *BearerTokenAuth

		result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "authenticator is nil") {
			t.Errorf("expected %v to contain %v", result.Error.Error(), "authenticator is nil")
		}

	})

	t.Run("query", func(t *testing.T) {
		var auth *QueryTokenAuth

		result := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws?token=abc", nil))
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "authenticator is nil") {
			t.Errorf("expected %v to contain %v", result.Error.Error(), "authenticator is nil")
		}

	})
}

func TestAuth_Authenticate_NilRequest_Ugly(t *testing.T) {
	t.Run("api key", func(t *testing.T) {
		auth := NewAPIKeyAuth(map[string]string{"key": "user"})

		result := auth.Authenticate(nil)
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "request is nil") {
			t.Errorf("expected %v to contain %v", result.Error.Error(), "request is nil")
		}

	})

	t.Run("bearer", func(t *testing.T) {
		auth := NewBearerTokenAuth()

		result := auth.Authenticate(nil)
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "request is nil") {
			t.Errorf("expected %v to contain %v", result.Error.Error(), "request is nil")
		}

	})

	t.Run("query", func(t *testing.T) {
		auth := NewQueryTokenAuth()

		result := auth.Authenticate(nil)
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "request is nil") {

			// ---------------------------------------------------------------------------
			// Unit tests — nil Authenticator (backward compat)
			// ---------------------------------------------------------------------------
			t.Errorf("expected %v to contain %v", result.Error.Error(), "request is nil")
		}

	})
}

func TestNilAuthenticator_AllConnectionsAccepted(t *testing.T) {
	hub := NewHub()
	if // No authenticator set
	!testIsNil(hub.config.Authenticator) {
		t.Errorf("expected nil, got %T", hub.config.Authenticator)
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
	if !testEventually(func() bool {
		return hub.isRunning()
	}, time.Second, 10*time.Millisecond) {
		t.Fatalf("condition was not met before timeout")
	}

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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer conn.Close()
	if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
		t.Errorf(

			// Give the hub a moment to process registration
			"expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	client := connectedClient
	mu.Unlock()
	if testIsNil(client) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual("user-42", client.UserID) {
		t.Errorf("expected %v, got %v", "user-42", client.UserID)
	}
	if !testEqual("api_key", client.Claims["auth_method"]) {
		t.Errorf("expected %v, got %v", "api_key", client.Claims["auth_method"])
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
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
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
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount(

	// No authenticator — all connections should be accepted
	)) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}

}

func TestIntegration_NilAuthenticator_BackwardCompat(t *testing.T) {

	server, hub, _ := startAuthTestHub(t, HubConfig{})

	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server), nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer conn.Close()
	if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 1, hub.ClientCount())
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
	if !(failureCalled) {
		t.Errorf("expected true")
	}
	if failureResult.Valid {
		t.Errorf("expected false")
	}
	if !(core.Is(failureResult.Error, ErrInvalidAPIKey)) {
		t.Errorf("expected true")
	}
	if testIsNil(failureRequest) {
		t.Errorf("expected non-nil value")
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
		if err := err; err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
			t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
		}

		conns = append(conns, conn)
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	time.Sleep(100 * time.Millisecond)
	if !testEqual(3, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 3, hub.ClientCount())
	}

	mu.Lock()
	defer mu.Unlock()
	for _, k := range keys {
		client, ok := connectedClients[k.userID]
		if !(ok) {
			t.Fatalf("expected true")
		}
		if !testEqual(k.userID, client.UserID) {
			t.Errorf("expected %v, got %v", k.userID, client.UserID)
		}

	}
}

func TestIntegration_AuthenticatorFunc_WithHub(t *testing.T) {
	// Use AuthenticatorFunc as the hub's authenticator
	claims := map[string]any{
		"source": "query_param",
		"scope": map[string]any{
			"channels": []string{"alpha", "beta"},
		},
	}
	fn := AuthenticatorFunc(func(r *http.Request) AuthResult {
		token := r.URL.Query().Get("token")
		if token == "magic" {
			return AuthResult{
				Valid:  true,
				UserID: "magic-user",
				Claims: claims,
			}
		}
		return AuthResult{Valid: false, Error: core.NewError("bad token")}
	})

	server, hub, _ := startAuthTestHub(t, HubConfig{
		Authenticator: fn,
	})

	// Valid token via query parameter
	conn, resp, err := websocket.DefaultDialer.Dial(authWSURL(server)+"?token=magic", nil)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer conn.Close()
	if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 1, hub.ClientCount())
	}

	claims["source"] = "mutated"
	claimsScope := claims["scope"].(map[string]any)
	claimsScope["channels"] = []string{"gamma"}

	hub.mu.RLock()
	var attachedClient *Client
	for client := range hub.clients {
		attachedClient = client
		break
	}
	hub.mu.RUnlock()
	if testIsNil(attachedClient) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual("magic-user", attachedClient.UserID) {
		t.Errorf("expected %v, got %v", "magic-user", attachedClient.UserID)
	}
	if !testEqual("query_param", attachedClient.Claims["source"]) {
		t.Errorf("expected %v, got %v", "query_param",

			// Invalid token
			attachedClient.Claims["source"])
	}

	scope := attachedClient.Claims["scope"].(map[string]any)
	if !testEqual([]string{"alpha", "beta"}, scope["channels"]) {
		t.Errorf("expected %v, got %v", []string{"alpha", "beta"}, scope["channels"])
	}

	conn2, resp2, _ := websocket.DefaultDialer.Dial(authWSURL(server)+"?token=wrong", nil)
	if conn2 != nil {
		conn2.Close()
	}
	if !testEqual(http.StatusUnauthorized, resp2.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp2.StatusCode)
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
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
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
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}

	select {
	case result := <-failureCalled:
		if result.Valid {
			t.Errorf("expected false")
		}
		if err := result.Error; err == nil {
			t.Fatalf("expected error")
		}
		if !testContains(result.Error.Error(), "authenticator panicked") {
			t.Errorf("expected %v to contain %v", result.Error.Error(), "authenticator panicked")
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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Broadcast a message
	err = hub.Broadcast(Message{Type: TypeEvent, Data: "hello"})
	if err := err; err != nil {
		t.Fatalf(

			// Read it
			"expected no error, got %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var msg Message
	if !(core.JSONUnmarshal(data, &msg).OK) {
		t.Fatalf("expected true")
	}
	if !testEqual(TypeEvent, msg.Type) {
		t.Errorf("expected %v, got %v", TypeEvent,

			// ---------------------------------------------------------------------------
			// Unit tests — BearerTokenAuth
			// ---------------------------------------------------------------------------
			msg.Type)
	}
	if !testEqual("hello", msg.Data) {
		t.Errorf("expected %v, got %v", "hello", msg.Data)
	}

}

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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("user-42", result.UserID) {
		t.Errorf("expected %v, got %v", "user-42", result.UserID)
	}
	if !testEqual("admin", result.Claims["role"]) {
		t.Errorf("expected %v, got %v", "admin", result.Claims["role"])
	}
	if !testEqual("jwt", result.Claims["auth_method"]) {
		t.Errorf("expected %v, got %v", "jwt", result.Claims["auth_method"])
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
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil || err.Error() != "token expired" {
		t.Errorf("expected error %q, got %v", "token expired", err)
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
		t.Errorf("expected false")
	}
	if !(core.Is(result.Error, ErrMissingAuthHeader)) {
		t.Errorf("expected true")
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
				t.Errorf("expected false")
			}
			if !(core.Is(result.Error, ErrMalformedAuthHeader)) {
				t.Errorf("expected true")
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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !testEqual("user-1", result.UserID) {

		// ---------------------------------------------------------------------------
		// Integration tests — BearerTokenAuth with Hub
		// ---------------------------------------------------------------------------
		t.Errorf("expected %v, got %v", "user-1", result.UserID)
	}

}

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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer conn.Close()
	if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	client := connectedClient
	mu.Unlock()
	if testIsNil(client) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual("jwt-user", client.UserID) {
		t.Errorf("expected %v, got %v", "jwt-user", client.UserID)
	}
	if !testEqual("bearer", client.Claims["auth_method"]) {
		t.Errorf("expected %v, got %v", "bearer", client.Claims["auth_method"])
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
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v",

			// ---------------------------------------------------------------------------
			// Unit tests — QueryTokenAuth
			// ---------------------------------------------------------------------------
			http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}

}

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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("browser-user", result.UserID) {
		t.Errorf("expected %v, got %v", "browser-user", result.UserID)
	}
	if !testEqual("query_param", result.Claims["auth_method"]) {
		t.Errorf("expected %v, got %v", "query_param", result.Claims["auth_method"])
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
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil || err.Error() != "unknown token" {
		t.Errorf("expected error %q, got %v", "unknown token", err)
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
		t.Errorf("expected false")
	}
	if !testContains(result.Error.Error(), "missing token query parameter") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "missing token query parameter")
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
		t.Errorf("expected false")
	}
	if !testContains(result.Error.Error(), "missing token query parameter") {
		t.Errorf("expected %v to contain %v", result.Error.Error(), "missing token query parameter")
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
		t.Errorf("expected false")
	}
	if err := result.Error; err == nil {
		t.Fatalf("expected error")
	}
	if !testContains(result.Error.Error(), "request URL is nil") {
		t.Errorf("expected %v to contain %v", result.Error.Error(),

			// ---------------------------------------------------------------------------
			// Integration tests — QueryTokenAuth with Hub
			// ---------------------------------------------------------------------------
			"request URL is nil")
	}
	if called {
		t.Errorf("expected false")
	}

}

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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer conn.Close()
	if !testEqual(http.StatusSwitchingProtocols, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 1, hub.ClientCount())
	}

	mu.Lock()
	client := connectedClient
	mu.Unlock()
	if testIsNil(client) {
		t.Fatalf("expected non-nil value")
	}
	if !testEqual("browser-user-99", client.UserID) {
		t.Errorf("expected %v, got %v", "browser-user-99", client.UserID)
	}
	if !testEqual("browser", client.Claims["origin"]) {
		t.Errorf("expected %v, got %v", "browser", client.Claims["origin"])
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
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount()) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
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
	if err := err; err == nil {
		t.Fatalf("expected error")
	}
	if !testEqual(http.StatusUnauthorized, resp.StatusCode) {
		t.Errorf("expected %v, got %v", http.StatusUnauthorized, resp.StatusCode)
	}
	if !testEqual(0, hub.ClientCount(

	// Authenticated via query param, then subscribe and receive messages
	)) {
		t.Errorf("expected %v, got %v", 0, hub.ClientCount())
	}

}

func TestIntegration_QueryTokenAuth_EndToEnd_Good(t *testing.T) {

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
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Subscribe to a channel
	err = conn.WriteJSON(Message{Type: TypeSubscribe, Data: "events"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if !testEqual(1, hub.ChannelSubscriberCount("events")) {
		t.Errorf("expected %v, got %v",

			// Send a message to the channel
			1, hub.ChannelSubscriberCount("events"))
	}

	err = hub.SendToChannel("events", Message{Type: TypeEvent, Data: "hello alice"})
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(time.Second))
	var received Message
	err = conn.ReadJSON(&received)
	if err := err; err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !testEqual(TypeEvent, received.Type) {
		t.Errorf("expected %v, got %v", TypeEvent, received.Type)
	}
	if !testEqual("hello alice", received.Data) {
		t.Errorf("expected %v, got %v", "hello alice", received.Data)
	}

}

func TestAPIKeyAuthenticator_AuthenticatedAlias(t *testing.T) {
	auth := NewAPIKeyAuth(map[string]string{
		"key-abc": "user-1",
	})

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer key-abc")

	result := auth.Authenticate(r)
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
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
	if !(result.Valid) {
		t.Errorf("expected true")
	}
	if !(result.Authenticated) {
		t.Errorf("expected true")
	}
	if !testEqual("alias-token", result.UserID) {
		t.Errorf("expected %v, got %v", "alias-token", result.UserID)
	}

}
