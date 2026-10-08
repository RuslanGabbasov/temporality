package workspace

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ValidateChannels is storage-side structural validation: any plausible slug
// is accepted (the deliverable set is the kernel registry's business), junk
// and duplicates are not.
func TestValidateChannelsStructural(t *testing.T) {
	require.NoError(t, ValidateChannels(nil))
	require.NoError(t, ValidateChannels([]UserChannel{
		{Type: "matrix", Address: "!room:matrix.org", Enabled: true},
		{Type: "telegram", Address: "12345", Enabled: false},
		{Type: "slack", Address: "https://hooks.slack.com/services/x", Enabled: true},
		{Type: "webhook", Address: "https://example.com/hook", Enabled: true},
	}))
	// A future transport slug storable before the kernel knows it.
	require.NoError(t, ValidateChannels([]UserChannel{{Type: "future-messenger", Address: "@user"}}))

	err := ValidateChannels([]UserChannel{{Type: "Matrix", Address: "!room"}})
	require.ErrorContains(t, err, "invalid channel type")
	err = ValidateChannels([]UserChannel{{Type: "matrix"}})
	require.ErrorContains(t, err, `channel "matrix" needs an address`)
	err = ValidateChannels([]UserChannel{
		{Type: "matrix", Address: "a"},
		{Type: "matrix", Address: "b"},
	})
	require.ErrorContains(t, err, "configured twice")
	require.True(t, strings.Contains(err.Error(), "matrix"))
}
