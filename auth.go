// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"net/http"
	"reflect"

	core "dappco.re/go/core"
	coreerr "dappco.re/go/core/log"
)

const maxClaimsCloneDepth = 64

// AuthResult holds the outcome of an authentication attempt.
// result := ws.AuthResult{Authenticated: true, UserID: "user-123"}
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
	userID = core.Trim(userID)
	if userID == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingUserID,
		}
	}

	clonedClaims, ok := cloneClaims(claims)
	if !ok {
		return AuthResult{
			Valid: false,
			Error: ErrInvalidAuthClaims,
		}
	}

	return AuthResult{
		Valid:         true,
		Authenticated: true,
		UserID:        userID,
		Claims:        clonedClaims,
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
	result.UserID = core.Trim(result.UserID)
	if result.UserID == "" {
		return AuthResult{
			Valid: false,
			Error: ErrMissingUserID,
		}
	}
	clonedClaims, ok := cloneClaims(result.Claims)
	if !ok {
		return AuthResult{
			Valid: false,
			Error: ErrInvalidAuthClaims,
		}
	}
	result.Claims = clonedClaims
	return result
}

// cloneClaims snapshots the auth claims map so caller-side mutations after
// authentication do not change the active session state.
func cloneClaims(claims map[string]any) (map[string]any, bool) {
	if len(claims) == 0 {
		return nil, true
	}

	cloned := make(map[string]any, len(claims))
	seen := make(map[uintptr]reflect.Value)
	for key, value := range claims {
		clonedValue, ok := deepCloneValueWithState(reflect.ValueOf(value), seen, 0)
		if !ok {
			return nil, false
		}
		cloned[key] = clonedValue
	}
	return cloned, true
}

// deepCloneValue recursively copies common composite values so auth claims do
// not retain references to caller-owned mutable state. It preserves scalar
// values as-is and falls back to the original value for unsupported kinds.
func deepCloneValue(v reflect.Value) any {
	cloned, _ := deepCloneValueWithState(v, make(map[uintptr]reflect.Value), 0)
	return cloned
}

func deepCloneValueWithState(v reflect.Value, seen map[uintptr]reflect.Value, depth int) (any, bool) {
	if !v.IsValid() {
		return nil, true
	}

	if depth > maxClaimsCloneDepth {
		return nil, false
	}

	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil, true
		}

		ptr := v.Pointer()
		if cloned, ok := seen[ptr]; ok {
			return cloned.Interface(), true
		}

		clone := reflect.New(v.Elem().Type())
		seen[ptr] = clone
		if !setClonedValue(clone.Elem(), v.Elem(), seen, depth+1) {
			return nil, false
		}
		return clone.Interface(), true
	case reflect.Map:
		if v.IsNil() {
			return nil, true
		}

		ptr := v.Pointer()
		if cloned, ok := seen[ptr]; ok {
			return cloned.Interface(), true
		}

		clone := reflect.MakeMapWithSize(v.Type(), v.Len())
		seen[ptr] = clone
		iter := v.MapRange()
		for iter.Next() {
			clonedValue, ok := deepCloneValueWithState(iter.Value(), seen, depth+1)
			if !ok {
				return nil, false
			}
			if clonedValue == nil {
				clone.SetMapIndex(iter.Key(), reflect.Zero(v.Type().Elem()))
				continue
			}

			value := reflect.ValueOf(clonedValue)
			if value.Type().AssignableTo(v.Type().Elem()) {
				clone.SetMapIndex(iter.Key(), value)
				continue
			}
			if value.Type().ConvertibleTo(v.Type().Elem()) {
				clone.SetMapIndex(iter.Key(), value.Convert(v.Type().Elem()))
				continue
			}

			clone.SetMapIndex(iter.Key(), iter.Value())
		}
		return clone.Interface(), true
	case reflect.Slice:
		if v.IsNil() {
			return nil, true
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			clone := make([]byte, v.Len())
			reflect.Copy(reflect.ValueOf(clone), v)
			return clone, true
		}

		ptr := v.Pointer()
		if cloned, ok := seen[ptr]; ok {
			return cloned.Interface(), true
		}

		clone := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		seen[ptr] = clone
		for i := 0; i < v.Len(); i++ {
			if !setClonedValue(clone.Index(i), v.Index(i), seen, depth+1) {
				return nil, false
			}
		}
		return clone.Interface(), true
	case reflect.Array:
		clone := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			if !setClonedValue(clone.Index(i), v.Index(i), seen, depth+1) {
				return nil, false
			}
		}
		return clone.Interface(), true
	case reflect.Struct:
		clone := reflect.New(v.Type()).Elem()
		clone.Set(v)
		for i := 0; i < v.NumField(); i++ {
			field := clone.Field(i)
			if !field.CanSet() {
				continue
			}
			if !setClonedValue(field, v.Field(i), seen, depth+1) {
				return nil, false
			}
		}
		return clone.Interface(), true
	default:
		return v.Interface(), true
	}
}

func setClonedValue(dst reflect.Value, src reflect.Value, seen map[uintptr]reflect.Value, depth int) bool {
	cloned, ok := deepCloneValueWithState(src, seen, depth)
	if !ok {
		return false
	}
	if cloned == nil {
		dst.Set(reflect.Zero(dst.Type()))
		return true
	}

	value := reflect.ValueOf(cloned)
	if value.Type().AssignableTo(dst.Type()) {
		dst.Set(value)
		return true
	}
	if value.Type().ConvertibleTo(dst.Type()) {
		dst.Set(value.Convert(dst.Type()))
		return true
	}

	dst.Set(src)
	return true
}

//	auth := ws.NewBearerTokenAuth(func(token string) ws.AuthResult {
//	    return ws.AuthResult{Authenticated: true, UserID: "user-123"}
//	})
type Authenticator interface {
	Authenticate(r *http.Request) AuthResult
}

//	auth := ws.AuthenticatorFunc(func(r *http.Request) ws.AuthResult {
//	    return ws.AuthResult{Authenticated: true, UserID: "user-123"}
//	})
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

// auth := ws.NewAPIKeyAuth(map[string]string{"secret-key": "user-123"})
type APIKeyAuthenticator struct {
	// Keys is a construction-time snapshot of API key values to user IDs.
	// Treat it as read-only; Authenticate uses the internal snapshot.
	Keys map[string]string

	keys map[string]string
}

// NewAPIKeyAuth creates an APIKeyAuthenticator from the given key→userID
// mapping. The returned authenticator validates `Authorization: Bearer <key>`
// headers against the provided keys.
func NewAPIKeyAuth(keys map[string]string) *APIKeyAuthenticator {
	if keys == nil {
		return &APIKeyAuthenticator{
			Keys: nil,
			keys: nil,
		}
	}

	snapshot := cloneStringMap(keys)

	return &APIKeyAuthenticator{
		Keys: snapshot,
		keys: cloneStringMap(snapshot),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

// NewBearerTokenAuth creates a bearer-token authenticator.
//
//	auth := ws.NewBearerTokenAuth(func(token string) ws.AuthResult {
//	    return ws.AuthResult{Authenticated: token == "secret", UserID: "user-1"}
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

	userID, ok := a.keys[token]
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

//	auth := ws.NewBearerTokenAuth(func(token string) ws.AuthResult {
//	    return ws.AuthResult{Authenticated: true, UserID: "user-123"}
//	})
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

//	auth := ws.NewQueryTokenAuth(func(token string) ws.AuthResult {
//	    return ws.AuthResult{Authenticated: true, UserID: "user-123"}
//	})
type QueryTokenAuth struct {
	// Validate receives the raw token value from the query string and
	// should return an AuthResult.
	Validate func(token string) AuthResult
}

// NewQueryTokenAuth creates a query-token authenticator.
//
//	auth := ws.NewQueryTokenAuth(func(token string) ws.AuthResult {
//	    return ws.AuthResult{Authenticated: token == "browser-token", UserID: "user-2"}
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
