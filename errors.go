// SPDX-Licence-Identifier: EUPL-1.2

package ws

import "errors"

// Authentication errors returned by the built-in APIKeyAuthenticator.
var (
	// ErrMissingAuthHeader is returned when no Authorization header is present.
	ErrMissingAuthHeader = errors.New("missing Authorization header")

	// ErrMalformedAuthHeader is returned when the Authorization header is
	// not in the expected "Bearer <token>" format.
	ErrMalformedAuthHeader = errors.New("malformed Authorization header")

	// ErrInvalidAPIKey is returned when the provided API key does not
	// match any known key.
	ErrInvalidAPIKey = errors.New("invalid API key")
)
