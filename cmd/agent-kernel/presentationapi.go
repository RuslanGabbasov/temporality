package main

// Presentation Engine HTTP surface (docs/presentation-engine.md §10, §12–§15).
//
// The kernel-internal service identity — the one agent tools authenticate as —
// creates and updates presentations with run provenance pinned server-side
// (run_id/agent_id/created_by come from the tool path, never from model
// arguments). Humans read presentations with the artifacts they reference;
// archive/expire are lifecycle moves, not deletes — a communication record
// stays addressable (§13).
//
// Events ride the durable outbox: presentation.created, presentation.validated,
// presentation.validation_failed, presentation.published, presentation.updated,
// presentation.archived, presentation.rendered, presentation.render_failed,
// presentation.fallback, artifact.attached (§15).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/presentation"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// presentationStore is the storage surface this router needs;
// *workspace.Store satisfies it and tests provide fakes.
type presentationStore interface {
	CreatePresentation(ctx context.Context, p *workspace.Presentation) error
	GetPresentation(ctx context.Context, id string) (workspace.Presentation, error)
	UpdatePresentation(ctx context.Context, p *workspace.Presentation, revision presentation.Revision) error
	SetPresentationStatus(ctx context.Context, id string, from, to presentation.Status) error
	ListPresentations(ctx context.Context, projectID string, limit int) ([]workspace.Presentation, error)
	ListPresentationsByRun(ctx context.Context, runID string, limit int) ([]workspace.Presentation, error)
	ListPresentationRevisions(ctx context.Context, id string) ([]presentation.Revision, error)
	AttachArtifact(ctx context.Context, id, artifactID, actor string) (workspace.Presentation, error)
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// presentationOutbox is the durable-outbox surface for lifecycle events.
type presentationOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// presentationSignaler resumes a paused workflow — the temporal client
// surface the interaction handler needs (docs/presentation-engine.md §11.3:
// interaction input rides the existing HITL signal).
type presentationSignaler interface {
	SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg any) error
}

// presentationHitlStore is the human-request slice the interaction handler
// resolves targets against; *workspace.Store satisfies it.
type presentationHitlStore interface {
	GetHumanRequest(ctx context.Context, id string) (workspace.HumanRequest, error)
	AnswerHumanRequest(ctx context.Context, id, response, answeredBy string) (workspace.HumanRequest, bool, error)
	CancelHumanRequest(ctx context.Context, id, cancelledBy string) error
}

// presentationPolicyLoad resolves the effective organization policy.
// Presentations from the kernel-internal identity and humans share the same
// safety contract; the default policy keeps external images off.
func defaultPresentationPolicy() presentation.Policy {
	return presentation.Policy{}
}

// emitPresentationEvent journals one lifecycle event with the run/project
// context so the SSE run stream delivers it to the web renderer, best-effort:
// the presentation row stays the source of truth.
func emitPresentationEvent(ctx context.Context, events presentationOutbox, eventType string, p workspace.Presentation, extra map[string]any) {
	if events == nil {
		return
	}
	data := map[string]any{
		"presentation_id": p.ID,
		"title":           p.Title,
		"status":          p.Status,
		"version":         p.Version,
	}
	for key, value := range extra {
		data[key] = value
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("presentation-lifecycle/%s/%s/%d", p.ID, eventType, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: p.ProjectID,
			Run:     p.RunID,
			Task:    p.TaskID,
			Actor:   observation.Actor{ID: "workspace", Type: "system"},
		},
		Type: eventType,
		Data: data,
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit presentation event", "presentation", p.ID, "type", eventType, "error", err)
	}
}

// presentationCreateRequest is the create/present payload. Document is the
// raw Presentation IR; the kernel validator is the only authority on what it
// may contain.
type presentationCreateRequest struct {
	ID           string          `json:"id,omitempty"`
	Title        string          `json:"title,omitempty"`
	Document     json.RawMessage `json:"document,omitempty"`
	Publish      bool            `json:"publish,omitempty"`
	RunID        string          `json:"run_id,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
	Actor        string          `json:"actor,omitempty"`
	TextFallback string          `json:"text_fallback,omitempty"`
}

// registerPresentationRoutes wires the HTTP surface. Called once at boot.
// The signaler is the temporal client: interaction actions (§11) resume the
// same paused runs ask_human paused.
func registerPresentationRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws presentationStore, events presentationOutbox, signaler presentationSignaler, hitl presentationHitlStore, defaultSourceID string) {
	// Create (present) — agent tools and API callers. The kernel-internal
	// identity pins provenance and publishes by default: `present` IS the
	// communication act (§10.2). Humans create drafts.
	mux.HandleFunc("POST /v1/workspace/presentations", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req presentationCreateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&req); err != nil {
			writeError(w, 400, fmt.Errorf("invalid request body: %v", err))
			return
		}
		doc, err := presentation.Validate(req.Document, defaultPresentationPolicy())
		if err != nil {
			p := workspace.Presentation{ID: req.ID, ProjectID: projectOfRequest(r), Title: req.Title, Status: "draft"}
			emitPresentationEvent(r.Context(), events, "presentation.validation_failed", p, map[string]any{"error": truncateDetail(err.Error())})
			writeError(w, 422, err)
			return
		}
		if doc.Title != "" && req.Title != "" && req.Title != doc.Title {
			// The envelope title wins: it is what channels announce.
			doc.Title = req.Title
		}
		status := "draft"
		if req.Publish || isKernelInternal(r) {
			status = "published"
		}
		id := req.ID
		if id == "" {
			id = fmt.Sprintf("presentation-%d", time.Now().UnixNano())
		}
		row := workspace.Presentation{
			ID:           id,
			ProjectID:    projectOfRequest(r),
			RunID:        req.RunID,
			TaskID:       req.TaskID,
			Title:        doc.Title,
			Status:       status,
			Version:      1,
			Document:     req.Document,
			TextFallback: req.TextFallback,
			CreatedBy:    createdBy(r, req.Actor),
		}
		if row.TextFallback == "" {
			row.TextFallback = presentation.RenderText(doc)
		}
		if row.ProjectID == "" {
			writeError(w, 400, errors.New("project query parameter or project_id is required"))
			return
		}
		if err := ws.CreatePresentation(r.Context(), &row); err != nil {
			writeError(w, 409, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), row.CreatedBy, "presentation.created", "presentation", row.ID, map[string]any{"title": row.Title, "status": row.Status, "run": row.RunID})
		emitPresentationEvent(r.Context(), events, "presentation.created", row, map[string]any{"component_types": componentTypesOf(doc)})
		if row.Status == "published" {
			emitPresentationEvent(r.Context(), events, "presentation.published", row, map[string]any{
				// The IR rides the event so the web renderer draws the
				// presentation straight from the run stream (bounded by the
				// validator's document limit).
				"presentation": json.RawMessage(row.Document),
			})
		} else {
			emitPresentationEvent(r.Context(), events, "presentation.validated", row, nil)
		}
		writeJSON(w, 201, row)
	})

	// Read one.
	mux.HandleFunc("GET /v1/workspace/presentations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		row, err := ws.GetPresentation(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 404, err)
			return
		}
		writeJSON(w, 200, row)
	})

	// Update — new version, previous document into the history (§13).
	mux.HandleFunc("PUT /v1/workspace/presentations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req presentationCreateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&req); err != nil {
			writeError(w, 400, fmt.Errorf("invalid request body: %v", err))
			return
		}
		current, err := ws.GetPresentation(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 404, err)
			return
		}
		doc, err := presentation.Validate(req.Document, defaultPresentationPolicy())
		if err != nil {
			emitPresentationEvent(r.Context(), events, "presentation.validation_failed", current, map[string]any{"error": truncateDetail(err.Error())})
			writeError(w, 422, err)
			return
		}
		updated := current
		updated.Title = doc.Title
		if req.Title != "" {
			updated.Title = req.Title
		}
		updated.Document = req.Document
		updated.TextFallback = req.TextFallback
		if updated.TextFallback == "" {
			updated.TextFallback = presentation.RenderText(doc)
		}
		updated.RunID = firstNonEmpty(req.RunID, current.RunID)
		updated.TaskID = firstNonEmpty(req.TaskID, current.TaskID)
		if err := ws.UpdatePresentation(r.Context(), &updated, presentation.Revision{ChangedBy: createdBy(r, req.Actor)}); err != nil {
			writeError(w, 409, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), createdBy(r, req.Actor), "presentation.updated", "presentation", updated.ID, map[string]any{"version": updated.Version})
		emitPresentationEvent(r.Context(), events, "presentation.updated", updated, map[string]any{
			"presentation": json.RawMessage(updated.Document),
		})
		writeJSON(w, 200, updated)
	})

	// Archive — the lifecycle move, never a delete (§13).
	mux.HandleFunc("POST /v1/workspace/presentations/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		id := r.PathValue("id")
		current, err := ws.GetPresentation(r.Context(), id)
		if err != nil {
			writeError(w, 404, err)
			return
		}
		from := presentation.Status(current.Status)
		if err := ws.SetPresentationStatus(r.Context(), id, from, presentation.StatusArchived); err != nil {
			writeError(w, 409, err)
			return
		}
		current.Status = "archived"
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), "presentation.archived", "presentation", id, nil)
		emitPresentationEvent(r.Context(), events, "presentation.archived", current, nil)
		row, err := ws.GetPresentation(r.Context(), id)
		if err != nil {
			writeJSON(w, 200, current)
			return
		}
		writeJSON(w, 200, row)
	})

	// List by project or run.
	mux.HandleFunc("GET /v1/workspace/presentations", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			fmt.Sscanf(raw, "%d", &limit)
		}
		var rows []workspace.Presentation
		var err error
		if runID := r.URL.Query().Get("run_id"); runID != "" {
			rows, err = ws.ListPresentationsByRun(r.Context(), runID, limit)
		} else {
			project := r.URL.Query().Get("project")
			if project == "" {
				writeError(w, 400, errors.New("project or run_id query parameter is required"))
				return
			}
			rows, err = ws.ListPresentations(r.Context(), project, limit)
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if rows == nil {
			rows = []workspace.Presentation{}
		}
		writeJSON(w, 200, map[string]any{"presentations": rows})
	})

	// Version history (§13).
	mux.HandleFunc("GET /v1/workspace/presentations/{id}/revisions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		revisions, err := ws.ListPresentationRevisions(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if revisions == nil {
			revisions = []presentation.Revision{}
		}
		writeJSON(w, 200, map[string]any{"revisions": revisions})
	})

	// Attach one artifact (§10.2 attach_artifact, §12).
	mux.HandleFunc("POST /v1/workspace/presentations/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			ArtifactID string `json:"artifact_id"`
			Actor      string `json:"actor,omitempty"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, 400, fmt.Errorf("invalid request body: %v", err))
			return
		}
		row, err := ws.AttachArtifact(r.Context(), r.PathValue("id"), req.ArtifactID, createdBy(r, req.Actor))
		if err != nil {
			writeError(w, 409, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), createdBy(r, req.Actor), "artifact.attached", "presentation", row.ID, map[string]any{"artifact": req.ArtifactID})
		emitPresentationEvent(r.Context(), events, "artifact.attached", row, map[string]any{"artifact_id": req.ArtifactID})
		writeJSON(w, 200, row)
	})

	// Static rendering (§7): text, svg or png of one stored presentation.
	mux.HandleFunc("GET /v1/workspace/presentations/{id}/render", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		row, err := ws.GetPresentation(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 404, err)
			return
		}
		format := strings.ToLower(r.URL.Query().Get("format"))
		if format == "" {
			format = "text"
		}
		doc, err := presentation.ParseDocument(row.Document)
		if err != nil {
			// A stored document always passed validation at write time; a
			// parse failure here means storage corruption — refuse rather
			// than render garbage.
			emitPresentationEvent(r.Context(), events, "presentation.render_failed", row, map[string]any{"error": truncateDetail(err.Error())})
			writeError(w, 500, err)
			return
		}
		switch format {
		case "text":
			writeJSON(w, 200, map[string]any{"format": "text", "body": presentation.RenderText(doc)})
		case "markdown":
			writeJSON(w, 200, map[string]any{"format": "markdown", "body": presentation.RenderMarkdown(doc)})
		case "svg":
			drawing := presentation.LayoutDocument(doc, presentation.LayoutOptions{IncludeTitle: true})
			svgBytes := presentation.RenderSVG(drawing)
			emitPresentationEvent(r.Context(), events, "presentation.rendered", row, map[string]any{"format": "svg"})
			w.Header().Set("Content-Type", "image/svg+xml")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(svgBytes)
		case "png":
			drawing := presentation.LayoutDocument(doc, presentation.LayoutOptions{IncludeTitle: true})
			pngBytes, err := presentation.RenderPNG(drawing)
			if err != nil {
				emitPresentationEvent(r.Context(), events, "presentation.render_failed", row, map[string]any{"format": "png", "error": truncateDetail(err.Error())})
				writeError(w, 500, err)
				return
			}
			emitPresentationEvent(r.Context(), events, "presentation.rendered", row, map[string]any{"format": "png"})
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pngBytes)
		case "pdf":
			drawing := presentation.LayoutDocument(doc, presentation.LayoutOptions{IncludeTitle: true})
			pdfBytes, err := presentation.RenderPDF(drawing)
			if err != nil {
				emitPresentationEvent(r.Context(), events, "presentation.render_failed", row, map[string]any{"format": "pdf", "error": truncateDetail(err.Error())})
				writeError(w, 500, err)
				return
			}
			emitPresentationEvent(r.Context(), events, "presentation.rendered", row, map[string]any{"format": "pdf"})
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s.pdf\"", presentationFileBase(row.ID)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pdfBytes)
		default:
			writeError(w, 400, fmt.Errorf("format must be text|markdown|svg|png|pdf"))
		}
	})

	// Adapted delivery preview (§9): what a channel would receive.
	mux.HandleFunc("GET /v1/workspace/presentations/{id}/adapt", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		row, err := ws.GetPresentation(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 404, err)
			return
		}
		channel := r.URL.Query().Get("channel")
		caps, ok := presentation.ChannelPresets()[channel]
		if !ok {
			writeError(w, 400, fmt.Errorf("channel must be one of web|matrix|telegram|slack|mattermost|webhook"))
			return
		}
		doc, err := presentation.ParseDocument(row.Document)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		delivery := presentation.Adapt(doc, caps)
		// JSON-safe view: attachment bytes are base64-free metadata here;
		// actual byte delivery happens through the channel drivers.
		attachments := make([]map[string]any, 0, len(delivery.Attachments))
		for _, attachment := range delivery.Attachments {
			attachments = append(attachments, map[string]any{
				"component_type": attachment.ComponentType,
				"component_id":   attachment.ComponentID,
				"format":         attachment.Format,
				"name":           attachment.Name,
				"size_bytes":     len(attachment.Data),
			})
		}
		writeJSON(w, 200, map[string]any{
			"channel":     channel,
			"kind":        delivery.Kind,
			"body":        delivery.Body,
			"attachments": attachments,
			"notes":       delivery.Notes,
		})
	})

	// The formal JSON Schema of the IR (§4.1) — tooling fetches it here.
	mux.HandleFunc("GET /v1/workspace/presentations/schema", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		presentation.ServeSchema(w)
	})

	// Capability negotiation (§9.2): what each channel can carry. This is
	// what get_capabilities serves the agent.
	mux.HandleFunc("GET /v1/workspace/presentation-capabilities", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		writeJSON(w, 200, map[string]any{"channels": presentation.ChannelPresets()})
	})

	// Interaction Handler (docs/presentation-engine.md §11): a user action on
	// an interactive component (button click, form submit) resolves through
	// the declared action and lands in the existing HITL machinery — the run
	// paused on ask_human resumes with the decision; the approval/business
	// logic itself stays in the runtime (§11.3). Safety rules of §11.2:
	// registered action type, unambiguous target, declared-action match,
	// user rights (RoleOperator + project), replay protection (the human
	// request row transitions once), expiry (the row lifecycle) and audit.
	mux.HandleFunc("POST /v1/workspace/presentations/{id}/interact", func(w http.ResponseWriter, r *http.Request) {
		var req presentationInteractionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, 400, fmt.Errorf("invalid request body: %v", err))
			return
		}
		row, err := ws.GetPresentation(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 404, err)
			return
		}
		if !gate.Allow(w, r, controlplane.RoleOperator, row.ProjectID) {
			return
		}
		action, err := presentation.FindDeclaredAction(row.Document, req.ComponentID)
		if err != nil {
			writeError(w, 422, err)
			return
		}
		if req.ActionType == "" {
			req.ActionType = action.Type
		}
		if req.ActionType != action.Type {
			writeError(w, 403, fmt.Errorf("action type %q is not declared by component %q", req.ActionType, req.ComponentID))
			return
		}
		target := req.Target
		if target == "" {
			target = action.Target
		}
		if action.Target != "" && target != action.Target {
			writeError(w, 403, fmt.Errorf("target %q does not match the declared action target", target))
			return
		}
		switch action.Type {
		case "open":
			// Navigation is client-side; the handler has nothing to resolve.
			writeError(w, 400, errors.New("open actions resolve in the client"))
			return
		case "submit", "select":
			if handlePresentationHitlInteraction(r, w, ws, hitl, signaler, events, row, action, target, req, defaultSourceID) {
				return // response already written
			}
		default:
			writeError(w, 422, fmt.Errorf("unknown action type %q", action.Type))
			return
		}
	})
}

// presentationInteractionRequest is one user action on a presentation
// component (§11.1).
type presentationInteractionRequest struct {
	ComponentID string         `json:"component_id"`
	ActionType  string         `json:"action_type,omitempty"`
	Target      string         `json:"target,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	Actor       string         `json:"actor,omitempty"`
}

// handlePresentationHitlInteraction routes one submit/select action into the
// HITL machinery. The target is a human_request operation id; form values
// validate against the request's schema BEFORE the run resumes (the same
// authority the ask_human card uses).
func handlePresentationHitlInteraction(r *http.Request, w http.ResponseWriter, ws presentationStore, hitl presentationHitlStore, signaler presentationSignaler, events presentationOutbox, row workspace.Presentation, action presentation.Action, target string, req presentationInteractionRequest, defaultSourceID string) bool {
	if target == "" {
		writeError(w, 422, errors.New("action target is required"))
		return true
	}
	ask, err := hitl.GetHumanRequest(r.Context(), target)
	if err != nil {
		writeError(w, 404, fmt.Errorf("action target %q is not an open ask", target))
		return true
	}
	// Replay protection (§11.2): only a pending/delivered ask may transition.
	// An already-answered ask returns 409 — the same click twice must not
	// resume the run twice.
	switch ask.Status {
	case workspace.HumanStatusPending, workspace.HumanStatusDelivered:
	default:
		writeError(w, 409, fmt.Errorf("ask %q is already %s", target, ask.Status))
		return true
	}
	actor := requestUserID(r)
	if isKernelInternal(r) && req.Actor != "" {
		actor = req.Actor
	}
	// Decision semantics (§11.1 example): payload.decision "approve"/
	// "reject"; a form submission without an explicit decision reads as an
	// affirmative answer carrying the values.
	approved := true
	if decision, ok := req.Payload["decision"].(string); ok {
		switch strings.ToLower(decision) {
		case "approve", "approved", "yes", "true":
			approved = true
			delete(req.Payload, "decision")
		case "reject", "rejected", "deny", "no", "false":
			approved = false
			delete(req.Payload, "decision")
		default:
			writeError(w, 422, fmt.Errorf("decision %q is not approve/reject", decision))
			return true
		}
	}
	approval := agent.Approval{OperationID: target, Approved: approved, ActorID: actor}
	if len(req.Payload) > 0 && approved {
		approval.Values = req.Payload
		if len(ask.Schema) > 0 {
			if err := workspace.ValidateFormValues(ask.Schema, approval.Values); err != nil {
				writeError(w, 422, err)
				return true
			}
		}
		canonical, cerr := workspace.CanonicalFormValues(approval.Values)
		if cerr != nil {
			writeError(w, 422, cerr)
			return true
		}
		approval.Response = canonical
	}
	// Resume the run through the same approval signal the ask card uses.
	sourceID := querySourceID(r, defaultSourceID)
	workflowID := workflowIDFor(sourceID, ask.ProjectID, ask.RunID)
	signalErr, done := temporalRPC(w, r.Context(), func(ctx context.Context) error {
		return signaler.SignalWorkflow(ctx, workflowID, "", agent.ApprovalSignal, approval)
	})
	if done {
		return true
	}
	if signalErr != nil {
		detail := strings.ToLower(signalErr.Error())
		if strings.Contains(detail, "not found") || strings.Contains(detail, "already completed") {
			// The run ended before the click: close the zombie ask durably so
			// the stale button stops working (§11.2 expiry semantics).
			_ = hitl.CancelHumanRequest(r.Context(), target, "system-sweep")
			writeError(w, 410, fmt.Errorf("the run behind ask %q has already finished", target))
			return true
		}
		writeError(w, 409, signalErr)
		return true
	}
	// Close the ask row: transitioned=false means the web/card path raced us
	// (the row closed first) — the run already resumed, so the click is a
	// replay and reports 409.
	response := approval.Response
	if response == "" && !approved {
		response = "rejected"
	}
	_, transitioned, cerr := hitl.AnswerHumanRequest(r.Context(), target, response, actor)
	if cerr != nil && !errors.Is(cerr, workspace.ErrNotFound) {
		writeError(w, 500, cerr)
		return true
	}
	if !transitioned {
		writeError(w, 409, fmt.Errorf("ask %q was already answered", target))
		return true
	}
	_ = ws.RecordAccessAudit(r.Context(), actor, workspace.AuditHumanAnswered, "presentation_interaction", row.ID, map[string]any{
		"operation": target, "component": req.ComponentID, "approved": approved,
	})
	emitPresentationEvent(r.Context(), events, "presentation.interaction", row, map[string]any{
		"component_id": req.ComponentID, "action": action.Type, "target": target, "approved": approved, "actor": actor,
	})
	writeJSON(w, 202, map[string]any{"accepted": true, "target": target})
	return false
}

// projectOfRequest resolves the target project: query parameter first, then
// the JSON body's project_id (decoded by callers holding the body twice is
// wasteful, so the create handler re-reads it from the request URL only —
// agents pass ?project=).
func projectOfRequest(r *http.Request) string {
	return r.URL.Query().Get("project")
}

// createdBy resolves the audit actor: the authenticated principal, or the
// run's human starter riding with the kernel-internal tool call.
func createdBy(r *http.Request, actor string) string {
	user := requestUserID(r)
	if isKernelInternal(r) && actor != "" {
		return actor
	}
	return user
}

// componentTypesOf lists the document's component types for observability
// (§15: the component-type distribution metric).
func componentTypesOf(doc presentation.Document) []string {
	seen := map[string]bool{}
	var walk func(components []presentation.Component)
	walk = func(components []presentation.Component) {
		for _, component := range components {
			seen[component.Type] = true
			walk(component.Children)
		}
	}
	walk(doc.Content)
	types := make([]string, 0, len(seen))
	for componentType := range seen {
		types = append(types, componentType)
	}
	return types
}

func truncateDetail(text string) string {
	runes := []rune(text)
	if len(runes) > 400 {
		return string(runes[:400]) + "…"
	}
	return text
}

// presentationFileBase strips a presentation id into a download-safe file
// name fragment.
func presentationFileBase(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "presentation"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// artifactExportStore is the storage surface of the artifact export route.
type artifactExportStore interface {
	GetArtifactRange(ctx context.Context, id string, offset, limit int) (workspace.Artifact, error)
	ListArtifactVersions(ctx context.Context, id string) ([]workspace.ArtifactVersion, error)
	GetPresentation(ctx context.Context, id string) (workspace.Presentation, error)
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// registerArtifactExportRoute serves a compiled, self-contained markdown copy
// of an artifact (docs/presentation-engine.md × docs/artifact-registry.md):
// presentation fences compile into embedded PNG data-URI images, so the
// downloaded file opens in any standalone viewer with the visuals in place.
// The stored artifact keeps its readable source — versions keep diffing
// clean.
func registerArtifactExportRoute(mux *http.ServeMux, gate *controlplane.Gate, store artifactExportStore, events presentationOutbox, resolveOrgUnits func(r *http.Request) []string) {
	mux.HandleFunc("GET /v1/workspace/artifacts/{artifactID}/export", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		id := r.PathValue("artifactID")
		var body string
		var title, hash string
		versionParam := strings.TrimSpace(r.URL.Query().Get("version"))
		if versionParam == "" {
			artifact, err := store.GetArtifactRange(r.Context(), id, 0, 0)
			if err != nil {
				writeError(w, 404, workspace.ErrNotFound)
				return
			}
			if !workspace.OrgVisible(artifact.OrgUnitID, resolveOrgUnits(r)) {
				writeError(w, 404, workspace.ErrNotFound)
				return
			}
			body, title, hash = artifact.Content, artifact.Title, artifact.ContentHash
		} else {
			version, err := strconv.Atoi(versionParam)
			if err != nil {
				writeError(w, 400, errors.New("version must be an integer"))
				return
			}
			versions, err := store.ListArtifactVersions(r.Context(), id)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			for _, v := range versions {
				if v.Version == version {
					body, title, hash = v.Content, v.Title, v.ContentHash
					break
				}
			}
			if body == "" && hash == "" {
				writeError(w, 404, workspace.ErrNotFound)
				return
			}
		}
		resolver := func(presentationID string) (json.RawMessage, error) {
			row, err := store.GetPresentation(r.Context(), presentationID)
			if err != nil {
				return nil, fmt.Errorf("presentation %q not found", presentationID)
			}
			return row.Document, nil
		}
		compiled, images, err := presentation.CompileMarkdownCapped([]byte(body), defaultPresentationPolicy(), resolver)
		if err != nil {
			emitPresentationEvent(r.Context(), events, "presentation.render_failed", workspace.Presentation{ID: id, ProjectID: projectOfRequest(r)}, map[string]any{"format": "artifact_export", "error": truncateDetail(err.Error())})
			writeError(w, 422, err)
			return
		}
		if images > 0 {
			// The export is a render in the observability sense (§15).
			emitPresentationEvent(r.Context(), events, "presentation.rendered", workspace.Presentation{ID: id, ProjectID: projectOfRequest(r)}, map[string]any{"format": "artifact_export", "images": images})
			_ = store.RecordAccessAudit(r.Context(), requestUserID(r), "artifact.exported", "artifact", id, map[string]any{"version": versionParam, "images": images})
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.md\"", artifactFileBase(title, id)))
		w.Header().Set("ETag", fmt.Sprintf("\"%s\"", hash))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compiled)
	})
}

// artifactFileBase builds a download-safe file name from the artifact title.
func artifactFileBase(title, id string) string {
	base := strings.TrimSpace(title)
	if base == "" {
		base = id
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "artifact"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
