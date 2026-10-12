package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// matrixDriver delivers through the Matrix client-server API using the
// kernel's shared bot account: text messages with a stable txn id
// (homeservers deduplicate retries server-side) and media uploads. The
// channel is two-way — replies in the room route back into the paused run
// (matrixinbound.go) — so text deliveries carry the reply hint.
type matrixDriver struct{}

func (matrixDriver) Type() string { return "matrix" }

// Poller: matrix is two-way — /sync long-poll with reply routing back into
// paused runs (driver_matrix_inbound.go).
func (matrixDriver) Poller() ChannelPoller { return matrixPoller{} }

// Webhook: matrix inbound is pull — /sync long-poll (driver_matrix_inbound.go).
func (matrixDriver) Webhook() ChannelWebhook { return nil }

func (matrixDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:             "Matrix",
		AddressHint:       "Matrix room ID, e.g. !room:matrix.org — the kernel bot must have joined it",
		NotConfiguredHint: "Matrix homeserver + bot access token",
		Capabilities: ChannelCapabilities{
			Text:     true,
			TwoWay:   true,
			FilesOut: true,
			FilesIn:  true,
		},
		Settings: []SettingField{
			{Name: "homeserver", Label: "Homeserver URL"},
			{Name: "access_token", Label: "Bot access token", Secret: true},
		},
	}
}

func (matrixDriver) IsConfigured(settings map[string]string) bool {
	return setting(settings, "homeserver") != "" && setting(settings, "access_token") != ""
}

func (matrixDriver) SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error {
	homeserver := strings.TrimRight(setting(settings, "homeserver"), "/")
	// TrimBearerPrefix guards against a token saved with its "Bearer "
	// header prefix (database or env) — homeservers reject the doubled prefix.
	accessToken := TrimBearerPrefix(setting(settings, "access_token"))
	roomID := address
	if homeserver == "" || accessToken == "" || roomID == "" {
		return errors.New("matrix transport is not configured")
	}
	txnID := "temporality-" + transactionFingerprint(roomID, msg.Body)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", homeserver, url.PathEscape(roomID), txnID)
	// The transport is two-way (matrixinbound.go): replying in the room routes
	// the answer back into the paused run, so say so next to the question.
	payload := map[string]any{"msgtype": "m.text", "body": msg.Body + "\n\nReply in this room to answer."}
	body, status, err := postJSON(ctx, client, http.MethodPut, endpoint, payload, map[string]string{"Authorization": "Bearer " + accessToken})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("matrix returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// SendFile uploads the bytes to the homeserver media store, then sends an
// m.file room message referencing the returned content URI. The note, when
// present, rides along as the message body so the recipient gets context.
func (matrixDriver) SendFile(ctx context.Context, client *http.Client, settings map[string]string, address string, file OutboundFile) error {
	homeserver := strings.TrimRight(setting(settings, "homeserver"), "/")
	accessToken := TrimBearerPrefix(setting(settings, "access_token"))
	roomID := address
	if homeserver == "" || accessToken == "" || roomID == "" {
		return errors.New("matrix transport is not configured")
	}
	uploadURL := fmt.Sprintf("%s/_matrix/media/v3/upload?filename=%s", homeserver, url.QueryEscape(file.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, strings.NewReader(string(file.Payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", fileContentType(file.Name))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("matrix upload returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var uploaded struct {
		ContentURI string `json:"content_uri"`
	}
	if err := json.Unmarshal(body, &uploaded); err != nil || uploaded.ContentURI == "" {
		return errors.New("matrix upload returned no content_uri")
	}
	messageBody := file.Note
	if strings.TrimSpace(messageBody) == "" {
		messageBody = file.Name
	}
	txnID := "temporality-file-" + transactionFingerprint(roomID, file.Name+uploaded.ContentURI)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", homeserver, url.PathEscape(roomID), txnID)
	message := map[string]any{
		"msgtype":  "m.file",
		"body":     messageBody,
		"filename": file.Name,
		"url":      uploaded.ContentURI,
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	sendReq, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	sendReq.Header.Set("Authorization", "Bearer "+accessToken)
	sendReq.Header.Set("Content-Type", "application/json")
	sendResp, err := client.Do(sendReq)
	if err != nil {
		return err
	}
	defer sendResp.Body.Close()
	sendBody, _ := io.ReadAll(io.LimitReader(sendResp.Body, 4096))
	if sendResp.StatusCode != http.StatusOK {
		return fmt.Errorf("matrix returned %d: %s", sendResp.StatusCode, truncate(string(sendBody), 200))
	}
	return nil
}

// transactionFingerprint derives a stable-but-unique Matrix txn id so retries
// of the same notification deduplicate server-side.
func transactionFingerprint(roomID, text string) string {
	digest := sha256.Sum256([]byte(roomID + "\x00" + text))
	return hex.EncodeToString(digest[:])[:16]
}
