// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"fmt"
	"testing"

	core "dappco.re/go/core"
	"github.com/stretchr/testify/assert"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, tt.err)
			assert.EqualError(t, tt.err, tt.want)
		})
	}
}

func TestErrors_AuthSentinels_Bad(t *testing.T) {
	assert.NotEqual(t, ErrMissingAuthHeader.Error(), ErrMalformedAuthHeader.Error())
	assert.NotEqual(t, ErrMissingAuthHeader.Error(), ErrInvalidAPIKey.Error())
	assert.NotEqual(t, ErrMalformedAuthHeader.Error(), ErrInvalidAPIKey.Error())
}

func TestErrors_AuthSentinels_Ugly(t *testing.T) {
	wrapped := fmt.Errorf("auth rejected: %w", ErrMissingAuthHeader)
	assert.True(t, core.Is(wrapped, ErrMissingAuthHeader))
}
