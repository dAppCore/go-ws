// SPDX-Licence-Identifier: EUPL-1.2

package ws

import core "dappco.re/go"

// Authentication errors returned by the built-in APIKeyAuthenticator.
var (
	// ErrMissingAuthHeader is returned when no Authorization header is present.
	ErrMissingAuthHeader = core.E("", "missing Authorization header", nil)

	// ErrMalformedAuthHeader is returned when the Authorization header is
	// not in the expected "Bearer <token>" format.
	ErrMalformedAuthHeader = core.E("", "malformed Authorization header", nil)

	// ErrInvalidAPIKey is returned when the provided API key does not
	// match any known key.
	ErrInvalidAPIKey = core.E("", "invalid API key", nil)

	// ErrMissingUserID is returned when an authentication result marks a
	// request as successful but does not provide a user identifier.
	ErrMissingUserID = core.E("", "authenticated user ID must not be empty", nil)

	// ErrInvalidAuthClaims is returned when an authentication result carries
	// claims that cannot be safely snapshotted.
	ErrInvalidAuthClaims = core.E("", "authentication claims are invalid", nil)

	// ErrSubscriptionLimitExceeded is returned when a client exceeds the
	// configured per-client subscription cap.
	ErrSubscriptionLimitExceeded = core.E("", "subscription limit exceeded", nil)
)
