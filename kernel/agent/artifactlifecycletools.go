package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Artifact lifecycle tools (docs/artifact-lifecycle.md §6): agents author work
// products as drafts (artifact_create/artifact_update) and hand them to human
// review (artifact_submit). The approval decision itself stays human — these
// tools can never move an artifact into approved.

// artifactStringArg reads a string argument, blanking pure whitespace.
func artifactStringArg(request ToolRequest, name string) string {
	value, _ := request.Arguments[name].(string)
	return strings.TrimSpace(value)
}

// handleArtifactCreate posts a new draft artifact. origin_run is pinned to the
// calling run server-side: provenance is not model-controlled, and the run's
// submit quota is enforced against it.
func (a *Activities) handleArtifactCreate(ctx context.Context, request ToolRequest) (ToolResult, error) {
	title := artifactStringArg(request, "title")
	if title == "" {
		return ToolResult{Content: "error: title is required"}, nil
	}
	id := artifactStringArg(request, "id")
	if id == "" {
		id = skillSlug(title)
	}
	if id == "" {
		return ToolResult{Content: "error: could not derive an artifact id from the title; pass id explicitly"}, nil
	}
	payload := map[string]any{
		"id":          id,
		"title":       title,
		"description": artifactStringArg(request, "description"),
		"provider":    "builtin",
		"kind":        "work",
		"saga_id":     artifactStringArg(request, "saga_id"),
		"origin_run":  request.RunID,
	}
	if kind := artifactStringArg(request, "kind"); kind != "" {
		payload["kind"] = kind
	}
	if content := artifactStringArg(request, "content"); content != "" {
		payload["content"] = content
	}
	if mimeType := artifactStringArg(request, "mime_type"); mimeType != "" {
		payload["mime_type"] = mimeType
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost, a.WorkspaceURL+"/v1/workspace/artifacts", payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: artifact create failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	var created struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version int    `json:"version"`
		Kind    string `json:"kind"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	a.emitArtifactLifecycleToolEvent(ctx, request, "artifact.authored", created.ID, map[string]any{
		"artifact_id": created.ID, "title": title, "kind": created.Kind, "status": created.Status, "version": created.Version,
	})
	encoded, _ := json.Marshal(map[string]any{
		"artifact_id": created.ID, "status": created.Status, "version": created.Version,
		"kind": created.Kind, "next": "artifact_submit hands the draft to human review",
	})
	return ToolResult{Content: string(encoded)}, nil
}

// handleArtifactUpdate edits a draft/rejected artifact in place; an approved
// artifact is opened as a new revision draft via new_version=true (POST
// /revision) and then goes through artifact_submit → human review; in_review
// is locked.
func (a *Activities) handleArtifactUpdate(ctx context.Context, request ToolRequest) (ToolResult, error) {
	artifactID := artifactStringArg(request, "artifact_id")
	if artifactID == "" {
		return ToolResult{Content: "error: artifact_id is required"}, nil
	}
	newVersion, _ := request.Arguments["new_version"].(bool)
	if newVersion {
		return a.handleArtifactNewVersion(ctx, request, artifactID)
	}
	payload := map[string]any{
		"title":       artifactStringArg(request, "title"),
		"description": artifactStringArg(request, "description"),
		"kind":        artifactStringArg(request, "kind"),
	}
	// PUT writes saga_id as sent: only pass it when the caller named it, so an
	// update that never mentions the saga keeps the stored link (and passing
	// "" explicitly is the only way a tool call detaches one).
	if saga := artifactStringArg(request, "saga_id"); saga != "" {
		payload["saga_id"] = saga
	}
	if content := artifactStringArg(request, "content"); content != "" {
		payload["content"] = content
	}
	if mimeType := artifactStringArg(request, "mime_type"); mimeType != "" {
		payload["mime_type"] = mimeType
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPut,
		fmt.Sprintf("%s/v1/workspace/artifacts/%s", a.WorkspaceURL, url.PathEscape(artifactID)), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		bodyText := truncate(body, 600)
		if status == 409 && strings.Contains(bodyText, "approved") {
			// The classic «новая редакция» case: name the controlled path
			// instead of a bare refusal.
			return ToolResult{Content: "error: this artifact is approved — pass new_version=true to open a revision draft, then submit it for re-approval with artifact_submit"}, nil
		}
		return ToolResult{Content: fmt.Sprintf("error: artifact update failed (HTTP %d): %s", status, bodyText)}, nil
	}
	var updated struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal([]byte(body), &updated)
	encoded, _ := json.Marshal(map[string]any{
		"artifact_id": updated.ID, "status": updated.Status, "version": updated.Version,
	})
	return ToolResult{Content: string(encoded)}, nil
}

// handleArtifactNewVersion opens a new revision of an APPROVED artifact
// (docs/artifact-lifecycle.md §5): the approved version stays live while the
// revision is a draft; the model then submits it for re-approval with
// artifact_submit. The approval decision remains human.
func (a *Activities) handleArtifactNewVersion(ctx context.Context, request ToolRequest, artifactID string) (ToolResult, error) {
	payload := map[string]any{
		"title":       artifactStringArg(request, "title"),
		"description": artifactStringArg(request, "description"),
		"kind":        artifactStringArg(request, "kind"),
		"content":     artifactStringArg(request, "content"),
		"mime_type":   artifactStringArg(request, "mime_type"),
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/artifacts/%s/revision", a.WorkspaceURL, url.PathEscape(artifactID)), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: revision failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	var revision struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal([]byte(body), &revision)
	a.emitArtifactLifecycleToolEvent(ctx, request, "artifact.updated", artifactID, map[string]any{
		"artifact_id": artifactID, "new_version": true, "version": revision.Version, "status": revision.Status,
	})
	encoded, _ := json.Marshal(map[string]any{
		"artifact_id": artifactID, "status": revision.Status, "version": revision.Version,
		"live_version_stays": "approved — the revision serves nobody until approved",
		"next":              "artifact_submit hands the revision to human re-approval",
	})
	return ToolResult{Content: string(encoded)}, nil
}

// handleArtifactSubmit opens a review round for the artifact's current version
// (202 + the round). Reviewer syntax follows ask_human recipients (docs/inbound-messages.md
// §7): user:<id>, org:<unit>, role:<role>, project_owner, or a bare user id.
func (a *Activities) handleArtifactSubmit(ctx context.Context, request ToolRequest) (ToolResult, error) {
	artifactID := artifactStringArg(request, "artifact_id")
	if artifactID == "" {
		return ToolResult{Content: "error: artifact_id is required"}, nil
	}
	reviewersRaw, _ := request.Arguments["reviewers"].([]any)
	reviewers := make([]string, 0, len(reviewersRaw))
	for _, who := range reviewersRaw {
		if s, ok := who.(string); ok && strings.TrimSpace(s) != "" {
			reviewers = append(reviewers, strings.TrimSpace(s))
		}
	}
	if len(reviewers) == 0 {
		return ToolResult{Content: "error: reviewers is required (e.g. [\"user:ivanov\"] or [\"project_owner\"])"}, nil
	}
	payload := map[string]any{
		"reviewers": reviewers,
		"mode":      artifactStringArg(request, "mode"),
		"note":      artifactStringArg(request, "note"),
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/artifacts/%s/submit", a.WorkspaceURL, url.PathEscape(artifactID)), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: artifact submit failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	var review struct {
		ID              string `json:"id"`
		ArtifactVersion int    `json:"artifact_version"`
		Mode            string `json:"mode"`
		Status          string `json:"status"`
	}
	_ = json.Unmarshal([]byte(body), &review)
	a.emitArtifactLifecycleToolEvent(ctx, request, "artifact.submitted", artifactID, map[string]any{
		"artifact_id": artifactID, "review_id": review.ID, "version": review.ArtifactVersion, "reviewers": reviewers,
	})
	encoded, _ := json.Marshal(map[string]any{
		"artifact_id": artifactID, "review_id": review.ID, "version": review.ArtifactVersion,
		"mode": review.Mode, "status": review.Status,
		"note": "a human resolves the review; artifact_approve does not exist — watch inbox/notifications for the decision",
	})
	return ToolResult{Content: string(encoded)}, nil
}

// emitArtifactLifecycleToolEvent records the authoring act in the run scope so
// the trajectory shows which run produced or submitted which work product.
func (a *Activities) emitArtifactLifecycleToolEvent(ctx context.Context, request ToolRequest, eventType, artifactID string, data map[string]any) {
	if a.Events == nil {
		return
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("%s/%s/%d", request.RunID, eventType, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: request.Project, Run: request.RunID, Task: request.TaskID,
			Actor: observation.Actor{ID: a.SourceID, Type: "agent"},
		},
		Type: eventType,
		Data: data,
	}
	if err := a.Events.Enqueue(ctx, event); err != nil {
		// Best-effort trail: the workspace row stays the source of truth.
		_ = err
	}
}
