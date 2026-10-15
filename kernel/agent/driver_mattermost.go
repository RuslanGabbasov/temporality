package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// mattermostDriver is the two-way Mattermost transport (docs/channels.md §9):
// a bot/personal access token posts questions through REST API v4, and the
// same token streams inbound `posted` events over the WebSocket API
// (mattermostPoller) — including direct messages, which outgoing webhooks
// could never serve. The server URL is a driver setting — self-hosted by
// nature.
type mattermostDriver struct{}

func (mattermostDriver) Type() string { return "mattermost" }

// Poller: mattermost inbound is pull — the WebSocket event stream (mattermostPoller).
func (mattermostDriver) Poller() ChannelPoller { return mattermostPoller{} }

// Webhook: inbound runs over the WebSocket poller; there is no push route.
// Deployments that configured outgoing webhooks in Mattermost should remove
// them — the poller starts working with the same base_url + token pair.
func (mattermostDriver) Webhook() ChannelWebhook { return nil }

func (mattermostDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:             "Mattermost",
		AddressHint:       "Mattermost channel ID (26 chars) — the token's account must have access",
		NotConfiguredHint: "Mattermost server URL + access token",
		Capabilities: ChannelCapabilities{
			Text:   true,
			TwoWay: true,
		},
		Settings: []SettingField{
			{Name: "base_url", Label: "Server URL, https://mattermost.example.com"},
			{Name: "token", Label: "Access token (bot or personal)", Secret: true},
		},
	}
}

func (mattermostDriver) IsConfigured(settings map[string]string) bool {
	return setting(settings, "base_url") != "" && setting(settings, "token") != ""
}

// Self resolves the token's own account id (users/me) so the inbound router
// can exclude the account's own posts from reply matching — the WebSocket
// stream echoes them back.
func (mattermostDriver) Self(ctx context.Context, client *http.Client, settings map[string]string) (string, error) {
	base, token, err := mattermostCredentials(settings)
	if err != nil {
		return "", err
	}
	body, err := mattermostAPI(ctx, client, http.MethodGet, base+"/api/v4/users/me", token, nil)
	if err != nil {
		return "", err
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if decoded.ID == "" {
		return "", errors.New("no id in mattermost users/me response")
	}
	return decoded.ID, nil
}

func (mattermostDriver) SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error {
	base, token, err := mattermostCredentials(settings)
	if err != nil {
		return err
	}
	channelID := strings.TrimSpace(address)
	if channelID == "" {
		return errors.New("mattermost channel address is empty")
	}
	// The transport is two-way: replies in the channel route back into the
	// paused run, so say so next to the question.
	payload := map[string]any{"channel_id": channelID, "message": msg.Body + "\n\nReply in this channel to answer."}
	body, err := mattermostAPI(ctx, client, http.MethodPost, base+"/api/v4/posts", token, payload)
	if err != nil {
		return err
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.ID == "" {
		return errors.New("mattermost post returned no id")
	}
	return nil
}

func (mattermostDriver) SendFile(context.Context, *http.Client, map[string]string, string, OutboundFile) error {
	return ErrChannelFilesUnsupported
}

// mattermostCredentials extracts and normalizes the server/token pair.
func mattermostCredentials(settings map[string]string) (base, token string, err error) {
	base = strings.TrimRight(setting(settings, "base_url"), "/")
	token = TrimBearerPrefix(setting(settings, "token"))
	if base == "" || token == "" {
		return "", "", errors.New("mattermost transport is not configured")
	}
	return base, token, nil
}

// mattermostAPI issues one REST v4 call; Mattermost answers 201 on created
// and 200 otherwise, errors carry a StatusIm message in the body.
func mattermostAPI(ctx context.Context, client *http.Client, method, endpoint, token string, payload any) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("mattermost returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}
