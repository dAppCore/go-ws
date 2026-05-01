// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"net/http"

	core "dappco.re/go"
)

func ExampleAuthenticatorFunc_Authenticate() {
	auth := AuthenticatorFunc(func(*http.Request) AuthResult {
		return AuthResult{Authenticated: true, UserID: "user-1"}
	})

	result := auth.Authenticate(core.NewHTTPTestRequest("GET", "/ws", nil))
	core.Println(result.Valid, result.UserID)
	// Output: true user-1
}

func ExampleNewAPIKeyAuth() {
	auth := NewAPIKeyAuth(map[string]string{"secret": "user-1"})
	request := core.NewHTTPTestRequest("GET", "/ws", nil)
	request.Header.Set("Authorization", "Bearer secret")

	result := auth.Authenticate(request)
	core.Println(result.Valid, result.UserID)
	// Output: true user-1
}

func ExampleNewBearerTokenAuth() {
	auth := NewBearerTokenAuth(func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	})
	request := core.NewHTTPTestRequest("GET", "/ws", nil)
	request.Header.Set("Authorization", "Bearer secret")

	result := auth.Authenticate(request)
	core.Println(result.Valid, result.UserID)
	// Output: true user-1
}

func ExampleAPIKeyAuthenticator_Authenticate() {
	auth := NewAPIKeyAuth(map[string]string{"secret": "user-1"})
	request := core.NewHTTPTestRequest("GET", "/ws", nil)
	request.Header.Set("Authorization", "Bearer wrong")

	result := auth.Authenticate(request)
	core.Println(result.Valid, core.Is(result.Error, ErrInvalidAPIKey))
	// Output: false true
}

func ExampleBearerTokenAuth_Authenticate() {
	auth := &BearerTokenAuth{Validate: func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	}}
	request := core.NewHTTPTestRequest("GET", "/ws", nil)
	request.Header.Set("Authorization", "Bearer secret")

	result := auth.Authenticate(request)
	core.Println(result.Valid, result.UserID)
	// Output: true user-1
}

func ExampleNewQueryTokenAuth() {
	auth := NewQueryTokenAuth(func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	})

	result := auth.Authenticate(core.NewHTTPTestRequest("GET", "/ws?token=secret", nil))
	core.Println(result.Valid, result.UserID)
	// Output: true user-1
}

func ExampleQueryTokenAuth_Authenticate() {
	auth := &QueryTokenAuth{Validate: func(token string) AuthResult {
		return AuthResult{Authenticated: token == "secret", UserID: "user-1"}
	}}

	result := auth.Authenticate(core.NewHTTPTestRequest("GET", "/ws", nil))
	core.Println(result.Valid, result.Error != nil)
	// Output: false true
}
