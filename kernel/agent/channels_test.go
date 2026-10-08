package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTransportSettingsOverlayEnv pins the precedence contract: the database
// overlay wins per field, env covers whatever the overlay leaves empty. A
// partial overlay is the normal case right after the admin fills one field.
func TestTransportSettingsOverlayEnv(t *testing.T) {
	t.Setenv("KERNEL_MATRIX_HOMESERVER", "https://env.matrix.org")
	t.Setenv("KERNEL_MATRIX_ACCESS_TOKEN", "env-matrix-token")
	t.Setenv("KERNEL_TELEGRAM_BOT_TOKEN", "env-telegram-token")
	t.Setenv("KERNEL_WEBHOOK_SECRET", "env-webhook-secret")
	t.Setenv("KERNEL_UI_URL", "http://env-ui:3000")

	stored := TransportSettings{
		MatrixHomeserver:  "https://db.matrix.org",
		MatrixAccessToken: "db-matrix-token",
	}
	effective := stored.OverlayEnv()

	require.Equal(t, "https://db.matrix.org", effective.MatrixHomeserver)
	require.Equal(t, "db-matrix-token", effective.MatrixAccessToken)
	require.Equal(t, "env-telegram-token", effective.TelegramBotToken)
	require.Equal(t, "env-webhook-secret", effective.WebhookSecret)
	require.Equal(t, "http://env-ui:3000", effective.UIURL)
}

func TestTransportSettingsConfiguredFlags(t *testing.T) {
	require.True(t, (TransportSettings{MatrixHomeserver: "https://m.org", MatrixAccessToken: "t"}).Configured("matrix"))
	require.False(t, (TransportSettings{MatrixHomeserver: "https://m.org"}).Configured("matrix"), "token missing")
	require.False(t, (TransportSettings{}).Configured("telegram"))
	require.True(t, (TransportSettings{}).Configured("slack"), "self-contained transports are always ready")
	require.True(t, (TransportSettings{}).Configured("webhook"))
	require.False(t, (TransportSettings{}).Configured("smoke-signals"))
}

// TestTrimBearerPrefix: a pasted "Authorization: Bearer …" header value must
// collapse to the bare token — sending "Bearer Bearer …" makes homeservers
// answer M_MISSING_TOKEN and the delivery fail.
func TestTrimBearerPrefix(t *testing.T) {
	require.Equal(t, "syt_foo_bar", TrimBearerPrefix("Bearer syt_foo_bar"))
	require.Equal(t, "syt_foo_bar", TrimBearerPrefix("bearer syt_foo_bar"))
	require.Equal(t, "syt_foo_bar", TrimBearerPrefix("  Bearer   syt_foo_bar  "))
	require.Equal(t, "Authorization: Bearer XYZ", TrimBearerPrefix("Authorization: Bearer XYZ"), "only a leading Bearer prefix is stripped")
	require.Equal(t, "syt_plain", TrimBearerPrefix("syt_plain"), "a clean token passes through untouched")
	require.Equal(t, "Bearer", TrimBearerPrefix("Bearer"), "the bare word alone is not a prefix")
}

// TestChannelRegistryReflectsEffectiveSettings: the registry's configured
// flags follow the settings passed in — the UI's channel picker and the
// admin settings screen see the same truth.
func TestChannelRegistryReflectsEffectiveSettings(t *testing.T) {
	registry := ChannelTypesWith(TransportSettings{MatrixHomeserver: "https://m.org", MatrixAccessToken: "t"})
	byType := map[string]ChannelTypeSpec{}
	for _, spec := range registry {
		byType[spec.Type] = spec
	}
	require.True(t, byType["matrix"].Configured)
	require.False(t, byType["telegram"].Configured)
	require.NotEmpty(t, byType["telegram"].NotConfiguredHint)
}

func TestSecretHint(t *testing.T) {
	require.Equal(t, "", SecretHint(""))
	require.Equal(t, "…", SecretHint("ab"))
	require.Equal(t, "…ab12", SecretHint("s3cr3t-ab12"))
	require.NotContains(t, SecretHint("s3cr3t-ab12"), "s3cr3t")
}

// TestNotifyChannelUsesSettingsLoader covers the database overlay path: the
// matrix homeserver/token come from the injected loader, not from env (env is
// empty here), and saving credentials in the UI therefore takes effect
// without a restart.
func TestNotifyChannelUsesSettingsLoader(t *testing.T) {
	var seenAuth string
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer homeserver.Close()

	users := `{"users":[{"id":"ruslan","name":"Ruslan","active":true,"channels":[{"type":"matrix","address":"!room:matrix.org","enabled":true}],"preferred_channel":"matrix"}]}`
	ws := workspaceStub(t, users, nil)
	activities := &Activities{
		HTTP: homeserver.Client(), WorkspaceURL: ws.URL, WorkspaceToken: "internal",
		ChannelSettings: func(context.Context) (TransportSettings, error) {
			return TransportSettings{MatrixHomeserver: homeserver.URL, MatrixAccessToken: "db-token"}, nil
		},
	}
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{
		Recipient: "ruslan", Question: "Deploy?", OperationID: "op-1", RunID: "run-1", Project: "repo",
	})
	require.NoError(t, err)
	require.Equal(t, "matrix", delivery.Channel)
	require.Equal(t, "delivered", delivery.Status)
	require.Equal(t, "Bearer db-token", seenAuth)
}
