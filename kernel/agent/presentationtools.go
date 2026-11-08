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

// presentationIRCatalog is the complete per-type shape catalog embedded in
// the present tool description (docs/presentation-engine.md §10.1). The model
// works in a project sandbox: the repo, the schema endpoint and the artifact
// registry are out of reach, so this catalog IS the schema — without it the
// model invents field names (items vs data vs props) and burns turns probing
// the validator. It must stay in sync with kernel/presentation validators.
const presentationIRCatalog = `Presentation IR document: {version:'1.0', type:'presentation', id?, title?, content:[components]}. A component carries ONLY the fields its type lists below — invented field names are rejected. Optional on every component: id (unique string).

Prose:
- text | markdown | paragraph: content (required text)
- heading: content + props.level (1..6)
- quote: content + props.attribution?
- code: content + props.language?
- link: content = visible label + props.href (https:// or /in-app/path)
- badge: content + props.status (success|info|warning|danger|neutral)
- divider: no fields

Data:
- list: items ['item' | {label, description?}], props.ordered?
- table: data {columns:[{key, title?, align:left|center|right, kind:text|number|status}], rows:[{KEY: cell}]}; a row maps COLUMN KEYS to cells; cell = scalar or {text, status?, href?}; >= 1 row
- key-value: items [{label, value, status?}]
- kpi: items [{label, value (number|string), status?, delta?, unit?, hint?}]
- progress: items [{label, value >= 0, max? (default 100), status?, unit?}]; value <= max
- comparison: items [{label, status?, fields:[{label, value}]}], >= 2 options
- status: content + props {status?, detail?}
- alert: content + props {severity: info|success|warning|danger (default info), title?}
- accordion: items [{label, content}]

Containers:
- card: props {title?}, children: [components] (nested composition)
- card-grid: props {columns: 1..4}, children: [card, ...] (cards only)

Charts:
- chart: props {chartType: line|bar|pie|donut|area, title?, showValues?, showLegend?} + data {labels:[...], values:[...] (or series:[{name, values}], <= 6 series), unit?, x_title?, y_title?}; values[i] pairs labels[i]; pie/donut need values >= 0 with a positive sum. Aliases set chartType: line-chart, bar-chart, pie-chart, area-chart, donut-chart
- gauge: data {value, min? (0), max? (100), label?, unit?}

Diagrams (node ids unique; edges reference declared ids):
- flowchart | dependency-graph: data {nodes:[{id, label, shape?: rect|round|diamond, status?}], edges:[{from, to, label?, dashed?}]}; acyclic
- sequence-diagram: data {participants:[{id, label}] (2..12), messages:[{from, to, label?, kind: sync|async|return}]}
- mindmap: data {root:{label, note?, children:[same shape]}}; <= 6 levels, <= 100 nodes
- state-diagram: data {initial?, states:[{id, label, status?}], transitions:[{from, to, label?, dashed?}]}
- er-diagram: data {entities:[{name, attributes?: ['type name', ...]}], relations:[{from, to, label?, cardinality: 1:1|1:N|N:1|N:M|0:1|0:N}]}
- class-diagram: data {classes:[{name, attributes?: [...], methods?: [...]}], relations:[{from, to, label?, kind: inheritance|composition|association}]}

Plans:
- timeline: items [{label, at (date or short mark), status?, description?}]
- gantt | roadmap: data {tasks:[{id?, label, start: 'YYYY-MM-DD', end, status?, assignee?, dependsOn?: [task ids]}]}; end >= start; props {scale: day|week|month, title?}; roadmap implies month

Media (src/href: in-app paths like /workspace/... — external images are disabled by the default policy):
- image: props {src (required), caption?}; content = alt text
- image-gallery: items [{src, alt?, caption?}], props.columns? (1..4)
- file: props {name (required), href?, artifact_id?, size?}

Interactive (the answer resumes the HITL ask; values validate server-side):
- button: content = label + actions [{type: submit|select, target: 'operation id', payload? {decision: 'approve'|'reject', ...}}] (payload is data, <= 4KB), or [{type: open, target: 'https://...'}]
- form: content? + props {schema: {type:'object', properties: {field: {type: string|integer|number|boolean|array, enum?, minLength?, maximum?, format?}, ...} <= 20 fields, required: [names]}, submit?: 'Button label'} + actions [{type: submit, target: 'operation id'}]

Caps: document <= 256KB, one content string <= 64KB, <= 200 components, nesting <= 8, <= 40 children, <= 8 actions. Validation errors name the exact path (content[2].items[0].label) — fix those and resend.`

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
		return presentationDoc{}, "error: document is required (Presentation IR JSON; compose it by the type catalog in the present tool description)"
	}
	doc, err := validatePresentationForTool(raw)
	if err != nil {
		return presentationDoc{}, "error: " + err.Error() + "\nMatch the per-type shapes in the present tool description exactly (items vs data vs props differ by type) and resend."
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
// markdown/text body rides SendText, static renders ride SendFile — PNG for
// raster channels (sent as inline images), SVG only for file-only channels.
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

// compileMarkdownDelivery compiles presentation fences in a markdown payload
// before the payload rides a channel: ```presentation blocks become embedded
// PNG data-URI images, so the single delivered file is self-contained
// (docs/presentation-engine.md §9 — the export applies everywhere an agent
// ships files to messengers). Non-markdown files pass through untouched; a
// document without fences passes through unchanged. Ref fences resolve
// against the workspace presentation store.
func (a *Activities) compileMarkdownDelivery(ctx context.Context, name string, payload []byte) ([]byte, error) {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".md") && !strings.HasSuffix(lower, ".markdown") {
		return payload, nil
	}
	resolver := func(id string) (json.RawMessage, error) {
		body, status, err := a.workspaceDo(ctx, http.MethodGet,
			fmt.Sprintf("%s/v1/workspace/presentations/%s", a.WorkspaceURL, id), nil)
		if err != nil {
			return nil, err
		}
		if status == 404 {
			return nil, fmt.Errorf("presentation %q not found", id)
		}
		if status >= 400 {
			return nil, fmt.Errorf("presentation read failed (HTTP %d)", status)
		}
		var row struct {
			Document json.RawMessage `json:"document"`
		}
		if err := json.Unmarshal([]byte(body), &row); err != nil {
			return nil, err
		}
		return row.Document, nil
	}
	compiled, _, err := presentation.CompileMarkdownCapped(payload, presentation.Policy{}, resolver)
	if err != nil {
		return nil, fmt.Errorf("compile presentation fences in %s: %w", name, err)
	}
	return compiled, nil
}
