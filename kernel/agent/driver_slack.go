package agent

import (
	"context"
	"fmt"
	"net/http"
)

// slackDriver posts the message through a per-user incoming webhook: the
// user creates the webhook in their Slack workspace, the address is the URL.
// Outbound-only — the two-way Slack bot (Events API, threads) is a separate
// future driver (docs/channels.md §9).
type slackDriver struct{}

func (slackDriver) Type() string { return "slack" }

func (slackDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:        "Slack",
		AddressHint:  "Slack incoming webhook URL, https://hooks.slack.com/services/…",
		Capabilities: ChannelCapabilities{Text: true},
	}
}

// IsConfigured: the webhook URL carries everything — always ready.
func (slackDriver) IsConfigured(map[string]string) bool { return true }

func (slackDriver) SendText(ctx context.Context, client *http.Client, _ map[string]string, address string, msg OutboundText) error {
	parsed, err := parseHTTPURL(address)
	if err != nil {
		return err
	}
	payload := map[string]any{"text": msg.Body}
	body, status, err := postJSON(ctx, client, http.MethodPost, parsed, payload, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("slack returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

func (slackDriver) SendFile(context.Context, *http.Client, map[string]string, string, OutboundFile) error {
	return ErrChannelFilesUnsupported
}
