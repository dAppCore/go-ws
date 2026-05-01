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

	// ErrMissingUserID is returned when an authentication result marks a
	// request as successful but does not provide a user identifier.
	ErrMissingUserID = coreerr.E("", "authenticated user ID must not be empty", nil)

	// ErrInvalidAuthClaims is returned when an authentication result carries
	// claims that cannot be safely snapshotted.
	ErrInvalidAuthClaims = coreerr.E("", "authentication claims are invalid", nil)

	// ErrSubscriptionLimitExceeded is returned when a client exceeds the
	// configured per-client subscription cap.
	ErrSubscriptionLimitExceeded = coreerr.E("", "subscription limit exceeded", nil)
)
