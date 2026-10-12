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
	require.Equal(t, []string{"matrix", "telegram", "slack", "webhook"}, order)
}

// TestChannelDriverSettingsCoverage: every registered driver must find its
// settings map — a type missing from channelMaps would silently deliver with
// a nil map and then report "not configured". The probe uses an empty
// address: drivers must reject it as a configuration error, not panic.
func TestChannelDriverSettingsCoverage(t *testing.T) {
	maps := (TransportSettings{
		MatrixHomeserver: "https://m.org", MatrixAccessToken: "t",
		TelegramBotToken: "tg", WebhookSecret: "wh",
	}).channelMaps()
	for _, driver := range channelDrivers {
		require.Contains(t, maps, driver.Type(), "channelMaps must cover %q", driver.Type())
		require.NotPanics(t, func() {
			_ = driver.SendText(context.Background(), http.DefaultClient, maps[driver.Type()], "", OutboundText{})
		}, "%s must reject an empty address without panicking", driver.Type())
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
