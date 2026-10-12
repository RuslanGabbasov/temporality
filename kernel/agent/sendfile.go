package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
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
// stays inside it (symlinks included, via resolveWorkspacePath). WorkspacePath
// is the resolved absolute dir PrepareRun set.
func resolveWorkspaceFile(workspacePath, arg string) (abs, name string, err error) {
	abs, err = resolveWorkspacePath(workspacePath, arg)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", fmt.Errorf("file %q is not available: %w", cleanPath(arg), err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("path %q is a directory", cleanPath(arg))
	}
	if info.Size() > sendFileUploadCap {
		return "", "", fmt.Errorf("file %q is %d bytes, transport attachment cap is %d", cleanPath(arg), info.Size(), sendFileUploadCap)
	}
	return abs, filepath.Base(abs), nil
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
	perChannel := settings.channelMaps()
	deliveries := []sendFileDelivery{}
	for _, channel := range recipient.Channels {
		if !channel.Enabled || strings.TrimSpace(channel.Address) == "" {
			continue
		}
		driver := channelDriverByType(channel.Type)
		// Text-only and unknown transports never carry attachments — the web
		// inbox (the shared path in the tool result) covers the user.
		if driver == nil || !driver.Spec().Capabilities.FilesOut {
			continue
		}
		if !driver.IsConfigured(perChannel[channel.Type]) {
			deliveries = append(deliveries, sendFileDelivery{Channel: channel.Type, Status: "unavailable", Detail: channelSettingUnconfiguredDetail(channel.Type)})
			continue
		}
		sendErr := driver.SendFile(ctx, a.HTTP, perChannel[channel.Type], channel.Address, OutboundFile{Name: name, Note: note, Payload: payload})
		if sendErr != nil {
			deliveries = append(deliveries, sendFileDelivery{Channel: channel.Type, Status: "failed", Detail: sendErr.Error()})
			continue
		}
		deliveries = append(deliveries, sendFileDelivery{Channel: channel.Type, Status: "sent"})
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
