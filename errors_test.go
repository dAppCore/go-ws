// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"fmt"
	"testing"

	core "dappco.re/go"
)

func TestErrors_AuthSentinels_Good(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "missing header", err: ErrMissingAuthHeader, want: "missing Authorization header"},
		{name: "malformed header", err: ErrMalformedAuthHeader, want: "malformed Authorization header"},
		{name: "invalid api key", err: ErrInvalidAPIKey, want: "invalid API key"},
		{name: "missing user id", err: ErrMissingUserID, want: "authenticated user ID must not be empty"},
		{name: "invalid auth claims", err: ErrInvalidAuthClaims, want: "authentication claims are invalid"},
		{name: "subscription limit exceeded", err: ErrSubscriptionLimitExceeded, want: "subscription limit exceeded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.err; err == nil || err.Error() != tt.want {
				t.Errorf("expected error %q, got %v", tt.want, err)
			}
		})
	}
}

func TestErrors_AuthSentinels_Bad(t *testing.T) {
	if testEqual(ErrMissingAuthHeader.Error(), ErrMalformedAuthHeader.Error()) {
		t.Errorf("expected values to differ: %v", ErrMalformedAuthHeader.Error())
	}
	if testEqual(ErrMissingAuthHeader.Error(), ErrInvalidAPIKey.Error()) {
		t.Errorf("expected values to differ: %v", ErrInvalidAPIKey.Error())
	}
	if testEqual(ErrMalformedAuthHeader.Error(), ErrInvalidAPIKey.Error()) {
		t.Errorf("expected values to differ: %v", ErrInvalidAPIKey.Error())
	}

}

func TestErrors_AuthSentinels_Ugly(t *testing.T) {
	wrapped := fmt.Errorf("auth rejected: %w", ErrMissingAuthHeader)
	if !(core.Is(wrapped, ErrMissingAuthHeader)) {
		t.Errorf("expected true")
	}

}
