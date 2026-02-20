// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"fmt"
	"net/http"
	"strings"
)

// AuthResult holds the outcome of an authentication attempt.
type AuthResult struct {
	// Valid indicates whether authentication succeeded.
	Valid bool

	// UserID is the authenticated user's identifier.
	UserID string

	// Claims holds arbitrary metadata from the authentication source
	// (e.g. roles, scopes, tenant ID).
	Claims map[string]any

	// Error holds the reason for authentication failure, if any.
	Error error
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
	return f(r)
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
	return &APIKeyAuthenticator{Keys: keys}
}

// Authenticate checks the Authorization header for a valid Bearer token.
func (a *APIKeyAuthenticator) Authenticate(r *http.Request) AuthResult {
	header := r.Header.Get("Authorization")
	if header == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingAuthHeader,
		}
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return AuthResult{
			Valid: false,
			Error: ErrMalformedAuthHeader,
		}
	}

	token := strings.TrimSpace(parts[1])
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

	return AuthResult{
		Valid:  true,
		UserID: userID,
		Claims: map[string]any{
			"auth_method": "api_key",
		},
	}
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
	header := r.Header.Get("Authorization")
	if header == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingAuthHeader,
		}
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return AuthResult{
			Valid: false,
			Error: ErrMalformedAuthHeader,
		}
	}

	token := strings.TrimSpace(parts[1])
	if token == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMalformedAuthHeader,
		}
	}

	return b.Validate(token)
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

// Authenticate implements the Authenticator interface for query parameter tokens.
func (q *QueryTokenAuth) Authenticate(r *http.Request) AuthResult {
	token := r.URL.Query().Get("token")
	if token == "" {
		return AuthResult{
			Valid: false,
			Error: fmt.Errorf("missing token query parameter"),
		}
	}

	return q.Validate(token)
}
