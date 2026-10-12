package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChannelSettingsOverlayEnv pins the precedence contract: the database
// overlay wins per field, env covers whatever the overlay leaves empty. A
// partial overlay is the normal case right after the admin fills one field.
func TestChannelSettingsOverlayEnv(t *testing.T) {
	t.Setenv("KERNEL_CHANNEL__MATRIX__HOMESERVER", "https://env.matrix.org")
	t.Setenv("KERNEL_CHANNEL__MATRIX__ACCESS_TOKEN", "env-matrix-token")
	t.Setenv("KERNEL_CHANNEL__TELEGRAM__BOT_TOKEN", "env-telegram-token")
	t.Setenv("KERNEL_CHANNEL__WEBHOOK__SECRET", "env-webhook-secret")
	t.Setenv("KERNEL_UI_URL", "http://env-ui:3000")

	stored := ChannelSettings{Channels: map[string]map[string]string{
		"matrix": {"homeserver": "https://db.matrix.org", "access_token": "db-matrix-token"},
	}}
	effective := stored.OverlayEnv()

	require.Equal(t, "https://db.matrix.org", effective.Channels["matrix"]["homeserver"])
	require.Equal(t, "db-matrix-token", effective.Channels["matrix"]["access_token"])
	require.Equal(t, "env-telegram-token", effective.Channels["telegram"]["bot_token"])
	require.Equal(t, "env-webhook-secret", effective.Channels["webhook"]["secret"])
	require.Equal(t, "http://env-ui:3000", effective.UIURL)
}

// TestChannelSettingsLegacyEnvAliases: the pre-generic names still feed the
// same fields — env-only deployments must not break by the rename.
func TestChannelSettingsLegacyEnvAliases(t *testing.T) {
	t.Setenv("KERNEL_MATRIX_HOMESERVER", "https://legacy.matrix.org")
	t.Setenv("KERNEL_MATRIX_ACCESS_TOKEN", "legacy-matrix-token")
	t.Setenv("KERNEL_TELEGRAM_BOT_TOKEN", "legacy-telegram-token")
	t.Setenv("KERNEL_WEBHOOK_SECRET", "legacy-webhook-secret")

	effective := ChannelSettings{}.OverlayEnv()
	require.Equal(t, "https://legacy.matrix.org", effective.Channels["matrix"]["homeserver"])
	require.Equal(t, "legacy-matrix-token", effective.Channels["matrix"]["access_token"])
	require.Equal(t, "legacy-telegram-token", effective.Channels["telegram"]["bot_token"])
	require.Equal(t, "legacy-webhook-secret", effective.Channels["webhook"]["secret"])
}

// TestChannelSettingsCanonicalEnvWinsOverAlias: when both the canonical
// KERNEL_CHANNEL__* name and the legacy alias are set, the canonical one wins
// deterministically.
func TestChannelSettingsCanonicalEnvWinsOverAlias(t *testing.T) {
	t.Setenv("KERNEL_MATRIX_HOMESERVER", "https://legacy.matrix.org")
	t.Setenv("KERNEL_CHANNEL__MATRIX__HOMESERVER", "https://canonical.matrix.org")

	effective := ChannelSettings{}.OverlayEnv()
	require.Equal(t, "https://canonical.matrix.org", effective.Channels["matrix"]["homeserver"])
}

func TestChannelSettingsConfiguredFlags(t *testing.T) {
	matrixOn := ChannelSettings{Channels: map[string]map[string]string{
		"matrix": {"homeserver": "https://m.org", "access_token": "t"},
	}}
	require.True(t, matrixOn.Configured("matrix"))
	noToken := ChannelSettings{Channels: map[string]map[string]string{
		"matrix": {"homeserver": "https://m.org"},
	}}
	require.False(t, noToken.Configured("matrix"), "token missing")
	require.False(t, (ChannelSettings{}).Configured("telegram"))
	require.True(t, (ChannelSettings{}).Configured("slack"), "self-contained transports are always ready")
	require.True(t, (ChannelSettings{}).Configured("webhook"))
	require.False(t, (ChannelSettings{}).Configured("smoke-signals"))
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
	registry := ChannelTypesWith(ChannelSettings{Channels: map[string]map[string]string{
		"matrix": {"homeserver": "https://m.org", "access_token": "t"},
	}})
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
		ChannelSettings: func(context.Context) (ChannelSettings, error) {
			return ChannelSettings{Channels: map[string]map[string]string{
				"matrix": {"homeserver": homeserver.URL, "access_token": "db-token"},
			}}, nil
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
