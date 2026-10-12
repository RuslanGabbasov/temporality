package agent

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChannelDriverRegistryOrder pins the registry order: it is the
// preference order the recipient's channel picker falls back through, so an
// accidental reorder changes delivery behavior, not just listing.
func TestChannelDriverRegistryOrder(t *testing.T) {
	order := make([]string, 0, len(channelDrivers))
	for _, driver := range channelDrivers {
		order = append(order, driver.Type())
	}
	require.Equal(t, []string{"matrix", "telegram", "slack", "slackbot", "mattermost", "webhook"}, order)
}

// TestChannelDriversTolerateAbsentSettings: the generic settings store only
// materializes channels with at least one value set — drivers regularly see
// a nil or partial map and must report configuration errors, never panic.
func TestChannelDriversTolerateAbsentSettings(t *testing.T) {
	for _, driver := range channelDrivers {
		require.NotPanics(t, func() {
			_ = driver.SendText(context.Background(), http.DefaultClient, nil, "", OutboundText{})
			_ = driver.SendFile(context.Background(), http.DefaultClient, nil, "addr", OutboundFile{Name: "f.txt"})
		}, "%s must tolerate missing settings", driver.Type())
	}
}

// TestChannelDriversFilesContract: capability flags and SendFile behavior
// must agree — the kernel routes files by Capabilities.FilesOut and treats
// the sentinel as unreachable.
func TestChannelDriversFilesContract(t *testing.T) {
	for _, driver := range channelDrivers {
		spec := driver.Spec()
		if !spec.Capabilities.FilesOut {
			err := driver.SendFile(context.Background(), http.DefaultClient, map[string]string{}, "addr", OutboundFile{Name: "f.txt"})
			require.ErrorIs(t, err, ErrChannelFilesUnsupported, "%s without FilesOut must return the sentinel", driver.Type())
			continue
		}
		require.NotEmpty(t, spec.Settings, "%s declares FilesOut and needs credentials — its settings schema must not be empty", driver.Type())
	}
}
