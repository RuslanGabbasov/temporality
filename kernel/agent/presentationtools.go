package agent

// Presentation tools (docs/presentation-engine.md §10): the agent-facing API
// of the Presentation Engine. Simple components ride the declarative answer
// (```presentation fences, published by the workflow); these tools cover the
// heavier operations — present now, draft/update/attach, channel capability
// lookups. Provenance is pinned server-side: run, task, agent and actor come
// from the ToolRequest, never from model arguments.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/presentation"
	"github.com/temporality-project/temporality/observation"
)

// presentationRawArg extracts the raw document argument: the IR reaches the
// workspace API as the model wrote it, validation happens server-side where
// the policy lives.
func presentationRawArg(request ToolRequest) json.RawMessage {
	switch value := request.Arguments["document"].(type) {
	case string:
		return json.RawMessage(value)
	case map[string]any, []any:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		return encoded
	default:
		return nil
	}
}

// presentationStringArg reads one trimmed string argument (the sendfile.go
// type-assertion pattern).
func presentationStringArg(request ToolRequest, name string) string {
	value, _ := request.Arguments[name].(string)
	return strings.TrimSpace(value)
}

// validatePresentationForTool runs the kernel validator with the default
// policy and returns the normalized document metadata the tool path needs.
func validatePresentationForTool(raw json.RawMessage) (presentationDoc, error) {
	doc, err := presentation.Validate(raw, presentation.Policy{})
	if err != nil {
		return presentationDoc{}, fmt.Errorf("document rejected: %v", err)
	}
	return presentationDoc{raw: raw, id: doc.ID, title: doc.Title}, nil
}

// presentationDocArg validates the document argument with the kernel policy
// and returns the normalized envelope title — the shared front door of
// present/create/update.
func presentationDocArg(request ToolRequest) (presentationDoc, string) {
	raw := presentationRawArg(request)
	if len(raw) == 0 {
		return presentationDoc{}, "error: document is required (Presentation IR JSON)"
	}
	doc, err := validatePresentationForTool(raw)
	if err != nil {
		return presentationDoc{}, "error: " + err.Error()
	}
	return doc, ""
}

// publishPresentationPOST persists one presentation through the workspace
// API. publish=false stores a draft (create_presentation), true publishes.
func (a *Activities) publishPresentationPOST(ctx context.Context, request ToolRequest, doc presentationDoc, publish bool) (ToolResult, error) {
	payload := map[string]any{
		"id":       doc.id,
		"title":    doc.title,
		"document": doc.raw,
		"publish":  publish,
		"run_id":   request.RunID,
		"task_id":  request.TaskID,
		"actor":    request.ActorID,
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/presentations?project=%s", a.WorkspaceURL, request.Project), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: presentation publish failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	var created struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	verb := "published"
	if !publish {
		verb = "saved as draft"
	}
	return ToolResult{Content: fmt.Sprintf("%s (id %s, status %s, version %d). The web UI renders it inside the run; text channels receive the fallback. The document id is %s — reference it with get_presentation or update_presentation.",
		verb, created.ID, created.Status, created.Version, created.ID)}, nil
}

// presentationDoc carries the validated tool-path document.
type presentationDoc struct {
	raw   json.RawMessage
	id    string
	title string
}

// handlePresent implements the `present` tool (§10.2): validate + publish
// now, and optionally deliver the adapted representation through the
// recipient's messaging channels (§9.3: Matrix takes SVG files, Telegram
// PNG photos, text channels the fallback body).
func (a *Activities) handlePresent(ctx context.Context, request ToolRequest) (ToolResult, error) {
	doc, refusal := presentationDocArg(request)
	if refusal != "" {
		return ToolResult{Content: refusal}, nil
	}
	if doc.id == "" {
		doc.id = presentationIDFromTitle(doc.title, request.OperationID)
	}
	result, err := a.publishPresentationPOST(ctx, request, doc, true)
	if err != nil || strings.HasPrefix(result.Content, "error:") {
		return result, err
	}
	// Channel delivery is opt-in: the run's channel watcher already carries
	// final answers, so explicit delivery serves mid-run publishing and
	// recipients outside the run's dialog.
	recipientArg := presentationStringArg(request, "recipient")
	if deliver, _ := request.Arguments["deliver"].(bool); deliver {
		deliveries := a.deliverPresentation(ctx, doc.raw, recipientArg, request.ActorID)
		if len(deliveries) > 0 {
			return ToolResult{Content: result.Content + " Channel deliveries: " + strings.Join(deliveries, "; ")}, nil
		}
		if recipientArg != "" {
			return ToolResult{Content: result.Content + " No channel carried the presentation (recipient has no enabled file/text channel or transports are unconfigured)."}, nil
		}
	}
	return result, nil
}

// deliverPresentation adapts the document per channel capability and pushes
// the result through every enabled channel of the recipient (§9.3): the
// markdown/text body rides SendText, static renders ride SendFile.
func (a *Activities) deliverPresentation(ctx context.Context, raw json.RawMessage, recipientArg, actorID string) []string {
	doc, err := presentation.ParseDocument(raw)
	if err != nil {
		return nil
	}
	users, ok := a.fetchWorkspaceUsers(ctx)
	if !ok {
		return nil
	}
	recipient, found := ResolveRecipient(recipientArg, actorID, users)
	if !found {
		return nil
	}
	settings := a.transports(ctx)
	presets := presentation.ChannelPresets()
	deliveries := []string{}
	for _, channel := range recipient.Channels {
		if !channel.Enabled || strings.TrimSpace(channel.Address) == "" {
			continue
		}
		driver := channelDriverByType(channel.Type)
		if driver == nil {
			continue
		}
		caps := presets[channel.Type]
		delivery := presentation.Adapt(doc, caps)
		channelSettings := settings.Channels[channel.Type]
		if !driver.IsConfigured(channelSettings) {
			deliveries = append(deliveries, channel.Type+": unavailable ("+channelSettingUnconfiguredDetail(channel.Type)+")")
			continue
		}
		sent := false
		if delivery.Body != "" {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			sendErr := driver.SendText(ctx, a.HTTP, channelSettings, channel.Address, OutboundText{Body: delivery.Body, Lang: ""})
			cancel()
			if sendErr != nil {
				deliveries = append(deliveries, channel.Type+": text failed ("+sendErr.Error()+")")
				continue
			}
			sent = true
		}
		if driver.Spec().Capabilities.FilesOut {
			for _, attachment := range delivery.Attachments {
				ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				sendErr := driver.SendFile(ctx, a.HTTP, channelSettings, channel.Address, OutboundFile{Name: attachment.Name, Note: doc.Title, Payload: attachment.Data})
				cancel()
				if sendErr != nil {
					deliveries = append(deliveries, channel.Type+": attachment failed ("+sendErr.Error()+")")
					continue
				}
				sent = true
			}
		}
		if sent {
			deliveries = append(deliveries, channel.Type+": sent ("+delivery.Kind+")")
		}
		// Adaptation notes are the §15 fallback signal, best-effort journaled.
		for _, note := range delivery.Notes {
			a.emitPresentationFallback(ctx, doc.ID, channel.Type, note)
		}
	}
	return deliveries
}

// emitPresentationFallback journals one presentation.fallback event through
// the durable outbox (§15) — adaptation quality telemetry.
func (a *Activities) emitPresentationFallback(ctx context.Context, presentationID, channel, note string) {
	if a.Events == nil {
		return
	}
	_ = a.Events.Enqueue(ctx, observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("presentation-fallback/%s/%s/%d", presentationID, channel, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "kernel", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "kernel", Type: "system"}},
		Type:       "presentation.fallback",
		Data:       map[string]any{"presentation_id": presentationID, "channel": channel, "note": note},
	})
}

// handleCreatePresentation implements `create_presentation`: a draft.
func (a *Activities) handleCreatePresentation(ctx context.Context, request ToolRequest) (ToolResult, error) {
	doc, refusal := presentationDocArg(request)
	if refusal != "" {
		return ToolResult{Content: refusal}, nil
	}
	if doc.id == "" {
		doc.id = presentationIDFromTitle(doc.title, request.OperationID)
	}
	return a.publishPresentationPOST(ctx, request, doc, false)
}

// handleUpdatePresentation implements `update_presentation`: new version of
// an existing presentation, previous document into the history.
func (a *Activities) handleUpdatePresentation(ctx context.Context, request ToolRequest) (ToolResult, error) {
	id := presentationStringArg(request, "id")
	if id == "" {
		return ToolResult{Content: "error: id of the presentation to update is required"}, nil
	}
	doc, refusal := presentationDocArg(request)
	if refusal != "" {
		return ToolResult{Content: refusal}, nil
	}
	payload := map[string]any{
		"document": doc.raw,
		"actor":    request.ActorID,
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPut,
		fmt.Sprintf("%s/v1/workspace/presentations/%s", a.WorkspaceURL, id), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status == 404 {
		return ToolResult{Content: fmt.Sprintf("error: presentation %q not found — check the id with get_presentation", id)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: presentation update failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	var updated struct {
		Version int    `json:"version"`
		Status  string `json:"status"`
	}
	_ = json.Unmarshal([]byte(body), &updated)
	return ToolResult{Content: fmt.Sprintf("updated (version %d, status %s); the previous version is in the history", updated.Version, updated.Status)}, nil
}

// handleAttachArtifact implements `attach_artifact`: link one artifact to a
// presentation (§12) — the reference rides the row, not a copy.
func (a *Activities) handleAttachArtifact(ctx context.Context, request ToolRequest) (ToolResult, error) {
	id := presentationStringArg(request, "presentation_id")
	artifactID := presentationStringArg(request, "artifact_id")
	if id == "" || artifactID == "" {
		return ToolResult{Content: "error: presentation_id and artifact_id are required"}, nil
	}
	payload := map[string]any{"artifact_id": artifactID, "actor": request.ActorID}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/presentations/%s/artifacts", a.WorkspaceURL, id), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: artifact attach failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: fmt.Sprintf("artifact %s attached to presentation %s", artifactID, id)}, nil
}

// handleGetPresentation implements `get_presentation`.
func (a *Activities) handleGetPresentation(ctx context.Context, request ToolRequest) (ToolResult, error) {
	id := presentationStringArg(request, "id")
	if id == "" {
		return ToolResult{Content: "error: id is required"}, nil
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet,
		fmt.Sprintf("%s/v1/workspace/presentations/%s", a.WorkspaceURL, id), nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status == 404 {
		return ToolResult{Content: fmt.Sprintf("error: presentation %q not found", id)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: presentation read failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	var row struct {
		ID           string          `json:"id"`
		Title        string          `json:"title"`
		Status       string          `json:"status"`
		Version      int             `json:"version"`
		TextFallback string          `json:"text_fallback"`
		Document     json.RawMessage `json:"document"`
	}
	if err := json.Unmarshal([]byte(body), &row); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	// The text fallback is what the agent can still reason about; the raw IR
	// would blow the context for nothing.
	return ToolResult{Content: fmt.Sprintf("presentation %s %q: status %s, version %d. Text fallback:\n%s",
		row.ID, row.Title, row.Status, row.Version, truncate(row.TextFallback, 2000))}, nil
}

// handleGetCapabilities implements `get_capabilities` (§10.2): the channel
// capability matrix the agent should adapt its answer to (§16: «Учитывай
// возможности целевого канала»).
func (a *Activities) handleGetCapabilities(ctx context.Context, request ToolRequest) (ToolResult, error) {
	body, status, err := a.workspaceDo(ctx, http.MethodGet,
		a.WorkspaceURL+"/v1/workspace/presentation-capabilities", nil)
	if err != nil || status >= 400 {
		// The workspace API may be unavailable in unit-style runs; the
		// kernel-side presets are the same data.
		return ToolResult{Content: capabilitiesText(nil)}, nil
	}
	var parsed struct {
		Channels map[string]map[string]bool `json:"channels"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ToolResult{Content: capabilitiesText(nil)}, nil
	}
	return ToolResult{Content: capabilitiesText(parsed.Channels)}, nil
}

// capabilitiesText renders the capability matrix for the model: compact,
// factual, with the delivery strategy each channel implies.
func capabilitiesText(channels map[string]map[string]bool) string {
	if channels == nil {
		return "channel capabilities: web renders the full Presentation IR natively (all components, charts, diagrams, forms); other channels receive markdown plus static images (SVG/PNG) or the text fallback. Presentations ride the run stream; heavy visuals never block the answer."
	}
	var b strings.Builder
	b.WriteString("Channel capabilities (adapt the answer to the target channel):\n")
	for _, channel := range sortedChannelNames(channels) {
		caps := channels[channel]
		fmt.Fprintf(&b, "- %s:", channel)
		for _, feature := range []string{"markdown", "tables", "images", "charts", "diagrams", "interactive_forms", "buttons", "files"} {
			if caps[feature] {
				fmt.Fprintf(&b, " %s", feature)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("Web always receives the full Presentation IR; channels without charts/diagrams receive static images or the text fallback automatically.")
	return b.String()
}

func sortedChannelNames(channels map[string]map[string]bool) []string {
	names := make([]string, 0, len(channels))
	for name := range channels {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// presentationIDFromTitle derives a stable id from the title, falling back
// to the operation id so retries of one tool call reuse the row.
func presentationIDFromTitle(title, operationID string) string {
	if slug := skillSlug(title); slug != "" {
		return "presentation-" + slug
	}
	if operationID != "" {
		return "presentation-" + strings.ToLower(operationID)
	}
	return ""
}
