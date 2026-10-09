package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"go.temporal.io/sdk/temporal"
)

// send_file (docs/triggers-and-escalations.md §7): agents produce files in
// the sandbox but had no way to hand them to a person. The tool delivers a
// workspace file to the resolved recipient: every enabled transport that can
// carry files (matrix, telegram) receives an upload, and the web inbox sees
// the shared path through the tool.completed event — the chat renders it as a
// download link.

// sendFileUploadCap bounds one outbound attachment. Matrix homeservers cap
// uploads at 50MB by default and the Telegram Bot API refuses documents over
// 50MB, so one shared limit keeps behaviour predictable.
const sendFileUploadCap = 50 << 20

// resolveWorkspaceFile maps a model-supplied path ("/workspace/report.md" or
// "report.md") onto the host-side project workspace and proves the result
// stays inside it. WorkspacePath is the resolved absolute dir PrepareRun set.
func resolveWorkspaceFile(workspacePath, arg string) (abs, name string, err error) {
	rel := strings.TrimSpace(arg)
	rel = strings.TrimPrefix(rel, "/workspace")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", "", errors.New("path is required")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes the workspace", arg)
	}
	if workspacePath == "" {
		return "", "", errors.New("run has no workspace")
	}
	abs = filepath.Join(workspacePath, clean)
	if abs != workspacePath && !strings.HasPrefix(abs, workspacePath+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes the workspace", arg)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", fmt.Errorf("file %q is not available: %w", clean, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("path %q is a directory", clean)
	}
	if info.Size() > sendFileUploadCap {
		return "", "", fmt.Errorf("file %q is %d bytes, transport attachment cap is %d", clean, info.Size(), sendFileUploadCap)
	}
	return abs, filepath.Base(clean), nil
}

// sendFileDelivery is one transport outcome, reported back to the model.
type sendFileDelivery struct {
	Channel string `json:"channel"`
	Status  string `json:"status"` // sent | unavailable | failed
	Detail  string `json:"detail,omitempty"`
}

// handleSendFile backs the send_file kernel tool: resolve the file inside the
// run workspace, resolve the recipient like ask_human does, then push the file
// through every file-capable channel the recipient has. The effect is
// deliverable-only; the file itself never leaves the workspace unless a
// transport succeeds.
func (a *Activities) handleSendFile(ctx context.Context, request ToolRequest) (ToolResult, error) {
	pathArg, _ := request.Arguments["path"].(string)
	note, _ := request.Arguments["note"].(string)
	recipientArg, _ := request.Arguments["recipient"].(string)
	abs, name, err := resolveWorkspaceFile(request.WorkspacePath, pathArg)
	if err != nil {
		// Bad paths are deterministic caller errors, not transport faults:
		// never retry them.
		return ToolResult{}, nonRetryable(err)
	}
	payload, err := os.ReadFile(abs)
	if err != nil {
		return ToolResult{}, nonRetryable(fmt.Errorf("read %q: %w", name, err))
	}
	users, ok := a.fetchWorkspaceUsers(ctx)
	if !ok {
		return ToolResult{}, nonRetryable(errors.New("workspace users are unavailable"))
	}
	recipient, found := ResolveRecipient(recipientArg, request.ActorID, users)
	if !found {
		return ToolResult{}, nonRetryable(fmt.Errorf("recipient %q could not be resolved", recipientArg))
	}
	settings := a.transports(ctx)
	deliveries := []sendFileDelivery{}
	for _, channel := range recipient.Channels {
		if !channel.Enabled || strings.TrimSpace(channel.Address) == "" {
			continue
		}
		switch channel.Type {
		case "matrix":
			if !settings.Configured("matrix") {
				deliveries = append(deliveries, sendFileDelivery{Channel: "matrix", Status: "unavailable", Detail: "matrix transport is not configured"})
				continue
			}
			if sendErr := a.sendMatrixFile(ctx, channel.Address, name, note, payload, settings); sendErr != nil {
				deliveries = append(deliveries, sendFileDelivery{Channel: "matrix", Status: "failed", Detail: sendErr.Error()})
				continue
			}
			deliveries = append(deliveries, sendFileDelivery{Channel: "matrix", Status: "sent"})
		case "telegram":
			if !settings.Configured("telegram") {
				deliveries = append(deliveries, sendFileDelivery{Channel: "telegram", Status: "unavailable", Detail: "telegram transport is not configured"})
				continue
			}
			if sendErr := a.sendTelegramFile(ctx, channel.Address, name, note, payload); sendErr != nil {
				deliveries = append(deliveries, sendFileDelivery{Channel: "telegram", Status: "failed", Detail: sendErr.Error()})
				continue
			}
			deliveries = append(deliveries, sendFileDelivery{Channel: "telegram", Status: "sent"})
		}
	}
	summary := map[string]any{
		"file":      name,
		"path":      "/workspace/" + strings.TrimPrefix(strings.TrimPrefix(pathArg, "/workspace"), "/"),
		"recipient": recipient.ID,
		"delivered": deliveries,
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("file %s delivered to %s", name, recipient.ID)}, nil
	}
	return ToolResult{Content: string(encoded)}, nil
}

func nonRetryable(err error) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), "SendFileFailed", err)
}

// fileContentType guesses a MIME type for the attachment; messengers display
// files better when the type is right.
func fileContentType(name string) string {
	if detected := mime.TypeByExtension(filepath.Ext(name)); detected != "" {
		return detected
	}
	return "application/octet-stream"
}

// sendMatrixFile uploads the bytes to the homeserver media store, then sends
// an m.file room message referencing the returned content URI. The note, when
// present, rides along as the message body so the recipient gets context.
func (a *Activities) sendMatrixFile(ctx context.Context, roomID, name, note string, payload []byte, settings TransportSettings) error {
	homeserver := strings.TrimRight(strings.TrimSpace(settings.MatrixHomeserver), "/")
	accessToken := TrimBearerPrefix(settings.MatrixAccessToken)
	if homeserver == "" || accessToken == "" || roomID == "" {
		return errors.New("matrix transport is not configured")
	}
	uploadURL := fmt.Sprintf("%s/_matrix/media/v3/upload?filename=%s", homeserver, url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", fileContentType(name))
	resp, err := a.HTTP.Do(req)
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
	messageBody := note
	if strings.TrimSpace(messageBody) == "" {
		messageBody = name
	}
	txnID := "temporality-file-" + transactionFingerprint(roomID, name+uploaded.ContentURI)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", homeserver, url.PathEscape(roomID), txnID)
	message := map[string]any{
		"msgtype":  "m.file",
		"body":     messageBody,
		"filename": name,
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
	sendResp, err := a.HTTP.Do(sendReq)
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

// sendTelegramFile posts the document through the Bot API multipart endpoint.
func (a *Activities) sendTelegramFile(ctx context.Context, chatID, name, note string, payload []byte) error {
	botToken := strings.TrimSpace(a.transports(ctx).TelegramBotToken)
	if botToken == "" || chatID == "" {
		return errors.New("telegram transport is not configured")
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return err
	}
	if strings.TrimSpace(note) != "" {
		if err := writer.WriteField("caption", note); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile("document", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", botToken), bytes.NewReader(form.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}
