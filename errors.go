// SPDX-Licence-Identifier: EUPL-1.2

package ws

import coreerr "dappco.re/go/log"

// Authentication errors returned by the built-in APIKeyAuthenticator.
var (
	// ErrMissingAuthHeader is returned when no Authorization header is present.
	ErrMissingAuthHeader = coreerr.E("", "missing Authorization header", nil)

	// ErrMalformedAuthHeader is returned when the Authorization header is
	// not in the expected "Bearer <token>" format.
	ErrMalformedAuthHeader = coreerr.E("", "malformed Authorization header", nil)

	// ErrInvalidAPIKey is returned when the provided API key does not
	// match any known key.
	ErrInvalidAPIKey = coreerr.E("", "invalid API key", nil)
)
