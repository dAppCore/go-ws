// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"net/http"

	core "dappco.re/go/core"
	coreerr "dappco.re/go/core/log"
)

// AuthResult holds the outcome of an authentication attempt.
type AuthResult struct {
	// Valid indicates whether authentication succeeded.
	Valid bool

	// Authenticated is an RFC-compatible alias for Valid. The package
	// treats either field as a successful authentication result.
	Authenticated bool

	// UserID is the authenticated user's identifier.
	UserID string

	// Claims holds arbitrary metadata from the authentication source
	// (e.g. roles, scopes, tenant ID).
	Claims map[string]any

	// Error holds the reason for authentication failure, if any.
	Error error
}

// authenticatedResult builds a successful AuthResult with both success
// flags populated.
func authenticatedResult(userID string, claims map[string]any) AuthResult {
	if core.Trim(userID) == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingUserID,
		}
	}

	return AuthResult{
		Valid:         true,
		Authenticated: true,
		UserID:        userID,
		Claims:        cloneClaims(claims),
	}
}

// normalizeAuthResult ensures the compatibility alias fields stay in sync.
func normalizeAuthResult(result AuthResult) AuthResult {
	if result.Valid || result.Authenticated {
		result.Valid = true
		result.Authenticated = true
	}
	return result
}

// authResultAccepted reports whether an authentication attempt succeeded.
func authResultAccepted(result AuthResult) bool {
	return result.Valid || result.Authenticated
}

// finalizeAuthResult rejects successful authentication results that do not
// provide a usable user identity.
func finalizeAuthResult(result AuthResult) AuthResult {
	result = normalizeAuthResult(result)
	if !authResultAccepted(result) {
		return result
	}
	if core.Trim(result.UserID) == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingUserID,
		}
	}
	result.Claims = cloneClaims(result.Claims)
	return result
}

// cloneClaims makes a shallow copy of the auth claims map so caller-side
// mutations after authentication do not change the active session state.
func cloneClaims(claims map[string]any) map[string]any {
	if len(claims) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(claims))
	for key, value := range claims {
		cloned[key] = value
	}
	return cloned
}

// Authenticator validates an HTTP request during the WebSocket upgrade
// handshake. Implementations may inspect headers, query parameters,
// cookies, or any other request attribute.
type Authenticator interface {
	Authenticate(r *http.Request) AuthResult
}

// AuthenticatorFunc is an adapter that allows ordinary functions to be
// used as Authenticators. If f is a function with the appropriate
// signature, AuthenticatorFunc(f) is an Authenticator that calls f.
type AuthenticatorFunc func(r *http.Request) AuthResult

// Authenticate calls f(r).
func (f AuthenticatorFunc) Authenticate(r *http.Request) AuthResult {
	if f == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("AuthenticatorFunc.Authenticate", "authenticator function is nil", nil),
		}
	}

	return finalizeAuthResult(f(r))
}

// APIKeyAuthenticator validates requests against a static map of API
// keys. It expects the key in the Authorization header as a Bearer
// token: `Authorization: Bearer <key>`. Each key maps to a user ID.
type APIKeyAuthenticator struct {
	// Keys maps API key values to user IDs.
	Keys map[string]string
}

// NewAPIKeyAuth creates an APIKeyAuthenticator from the given key→userID
// mapping. The returned authenticator validates `Authorization: Bearer <key>`
// headers against the provided keys.
func NewAPIKeyAuth(keys map[string]string) *APIKeyAuthenticator {
	if keys == nil {
		return &APIKeyAuthenticator{}
	}

	snapshot := make(map[string]string, len(keys))
	for key, userID := range keys {
		snapshot[key] = userID
	}

	return &APIKeyAuthenticator{Keys: snapshot}
}

// NewBearerTokenAuth creates a bearer-token authenticator.
//
//	auth := ws.NewBearerTokenAuth(func(token string) ws.AuthResult {
//	    return ws.AuthResult{Valid: token == "secret", UserID: "user-1"}
//	})
//
// A custom validator should be supplied for production use. When no
// validator is configured, the authenticator rejects the connection.
func NewBearerTokenAuth(validateFns ...func(token string) AuthResult) *BearerTokenAuth {
	if len(validateFns) > 0 && validateFns[0] != nil {
		return &BearerTokenAuth{
			Validate: validateFns[0],
		}
	}

	return &BearerTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{
				Valid: false,
				Error: coreerr.E("BearerTokenAuth", "validate function is not configured", nil),
			}
		},
	}
}

// Authenticate checks the Authorization header for a valid Bearer token.
func (a *APIKeyAuthenticator) Authenticate(r *http.Request) AuthResult {
	if a == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("APIKeyAuthenticator.Authenticate", "authenticator is nil", nil),
		}
	}

	if r == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("APIKeyAuthenticator.Authenticate", "request is nil", nil),
		}
	}

	header := r.Header.Get("Authorization")
	if header == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingAuthHeader,
		}
	}

	parts := core.SplitN(header, " ", 2)
	if len(parts) != 2 || core.Lower(parts[0]) != "bearer" {
		return AuthResult{
			Valid: false,
			Error: ErrMalformedAuthHeader,
		}
	}

	token := core.Trim(parts[1])
	if token == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMalformedAuthHeader,
		}
	}

	userID, ok := a.Keys[token]
	if !ok {
		return AuthResult{
			Valid: false,
			Error: ErrInvalidAPIKey,
		}
	}

	if core.Trim(userID) == "" {
		return AuthResult{
			Valid: false,
			Error: ErrInvalidAPIKey,
		}
	}

	return authenticatedResult(userID, map[string]any{
		"auth_method": "api_key",
	})
}

// BearerTokenAuth extracts an Authorization: Bearer <token> header and
// validates it using a caller-supplied function. Unlike APIKeyAuthenticator,
// this authenticator delegates validation entirely to the caller, making
// it suitable for JWT verification, token introspection, or any custom
// bearer scheme.
type BearerTokenAuth struct {
	// Validate receives the raw bearer token string and should return
	// an AuthResult. The caller controls UserID, Claims, and error
	// semantics.
	Validate func(token string) AuthResult
}

// Authenticate implements the Authenticator interface for bearer tokens.
func (b *BearerTokenAuth) Authenticate(r *http.Request) AuthResult {
	if b == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("BearerTokenAuth.Authenticate", "authenticator is nil", nil),
		}
	}

	if b.Validate == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("BearerTokenAuth.Authenticate", "validate function is not configured", nil),
		}
	}

	if r == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("BearerTokenAuth.Authenticate", "request is nil", nil),
		}
	}

	header := r.Header.Get("Authorization")
	if header == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingAuthHeader,
		}
	}

	parts := core.SplitN(header, " ", 2)
	if len(parts) != 2 || core.Lower(parts[0]) != "bearer" {
		return AuthResult{
			Valid: false,
			Error: ErrMalformedAuthHeader,
		}
	}

	token := core.Trim(parts[1])
	if token == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMalformedAuthHeader,
		}
	}

	return finalizeAuthResult(b.Validate(token))
}

// QueryTokenAuth extracts a token from the ?token= query parameter and
// validates it using a caller-supplied function. This is useful for
// browser clients that cannot set custom headers on WebSocket connections
// (e.g. the browser's native WebSocket API does not support custom headers).
type QueryTokenAuth struct {
	// Validate receives the raw token value from the query string and
	// should return an AuthResult.
	Validate func(token string) AuthResult
}

// NewQueryTokenAuth creates a query-token authenticator.
//
//	auth := ws.NewQueryTokenAuth(func(token string) ws.AuthResult {
//	    return ws.AuthResult{Valid: token == "browser-token", UserID: "user-2"}
//	})
//
// A custom validator should be supplied for production use. When no
// validator is configured, the authenticator rejects the connection.
func NewQueryTokenAuth(validateFns ...func(token string) AuthResult) *QueryTokenAuth {
	if len(validateFns) > 0 && validateFns[0] != nil {
		return &QueryTokenAuth{
			Validate: validateFns[0],
		}
	}

	return &QueryTokenAuth{
		Validate: func(token string) AuthResult {
			return AuthResult{
				Valid: false,
				Error: coreerr.E("QueryTokenAuth", "validate function is not configured", nil),
			}
		},
	}
}

// Authenticate implements the Authenticator interface for query parameter tokens.
func (q *QueryTokenAuth) Authenticate(r *http.Request) AuthResult {
	if q == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("QueryTokenAuth.Authenticate", "authenticator is nil", nil),
		}
	}

	if q.Validate == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("QueryTokenAuth.Authenticate", "validate function is not configured", nil),
		}
	}

	if r == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("QueryTokenAuth.Authenticate", "request is nil", nil),
		}
	}

	if r.URL == nil {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("QueryTokenAuth.Authenticate", "request URL is nil", nil),
		}
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		return AuthResult{
			Valid: false,
			Error: coreerr.E("QueryTokenAuth.Authenticate", "missing token query parameter", nil),
		}
	}

	return finalizeAuthResult(q.Validate(token))
}
