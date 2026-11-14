package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/presentation"
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

// artifactRawArg reads a string argument WITHOUT trimming: patch fragments
// must match the stored body byte for byte (edit_file semantics) and a
// document body keeps its own leading whitespace.
func artifactRawArg(request ToolRequest, name string) string {
	value, _ := request.Arguments[name].(string)
	return value
}

// artifactPatchMaxBody is how much of the body one patch read fetches — a
// safety rail, not a document limit: patching happens server-side in the
// kernel, the model never re-sends a body it already wrote.
const artifactPatchMaxBody = 4 << 20

// fetchArtifactBody loads the artifact's CURRENT body in full for a patch.
// A truncated read refuses: editing against a partial body would corrupt the
// document (the whole point of the patch modes).
func (a *Activities) fetchArtifactBody(ctx context.Context, artifactID string) (string, error) {
	body, status, err := a.workspaceDo(ctx, http.MethodGet,
		fmt.Sprintf("%s/v1/workspace/artifacts/%s?offset=0&limit=%d", a.WorkspaceURL, url.PathEscape(artifactID), artifactPatchMaxBody), nil)
	if err != nil {
		return "", err
	}
	if status == 404 {
		return "", fmt.Errorf("artifact %q not found; use artifact_search to list ids", artifactID)
	}
	if status >= 400 {
		return "", fmt.Errorf("artifact read failed (HTTP %d): %s", status, truncate(body, 400))
	}
	var page struct {
		Artifact struct {
			Content string `json:"content"`
		} `json:"artifact"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		return "", err
	}
	if page.Truncated {
		return "", fmt.Errorf("the body exceeds %d bytes and cannot be patched in one piece", artifactPatchMaxBody)
	}
	return page.Artifact.Content, nil
}

// artifactPatch applies one exact-fragment edit or an append to the current
// body. The fragment must occur EXACTLY once (edit_file semantics): an
// ambiguous edit is refused instead of silently corrupting one of the
// matches.
func artifactPatch(current, oldString, newString, appendText string) (string, error) {
	switch {
	case oldString != "" && appendText != "":
		return "", fmt.Errorf("pass either old_string/new_string or append, not both")
	case appendText != "":
		if current != "" && !strings.HasSuffix(current, "\n") {
			current += "\n"
		}
		return current + appendText, nil
	case oldString != "":
		count := strings.Count(current, oldString)
		switch {
		case count == 0:
			return "", fmt.Errorf("old_string was not found in the body — re-read the current body with artifact_read and retry with the exact fragment")
		case count > 1:
			return "", fmt.Errorf("old_string matches %d places in the body — extend it with surrounding text until it is unique", count)
		}
		return strings.Replace(current, oldString, newString, 1), nil
	default:
		return "", fmt.Errorf("no patch given: pass old_string/new_string or append")
	}
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
		// The run's human starter rides along: the workspace stamps them into
		// contributors, so the author/org-unit filters can find agent-authored
		// work (origin_user stays empty on the agent path).
		"actor": request.ActorID,
	}
	if kind := artifactStringArg(request, "kind"); kind != "" {
		payload["kind"] = kind
	}
	if content := artifactStringArg(request, "content"); content != "" {
		// Presentation fences are validated at authoring time: the agent fixes
		// broken visuals before the artifact exists, not at download time.
		if err := presentation.ValidateMarkdownFences([]byte(content), presentation.Policy{}); err != nil {
			return ToolResult{Content: "error: " + err.Error() + "\nCompose visual blocks by the catalog in the present tool description."}, nil
		}
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
		// The classic truncation trap: a call carrying a huge content body is
		// cut by the model output limit mid-arguments and arrives EMPTY.
		return ToolResult{Content: "error: artifact_id is required (if the call carried a large content body it was truncated by the model output limit — edit with old_string/new_string or append instead of resending the whole body)"}, nil
	}
	newVersion, _ := request.Arguments["new_version"].(bool)
	oldString := artifactRawArg(request, "old_string")
	newString := artifactRawArg(request, "new_string")
	appendText := artifactRawArg(request, "append")
	if newString != "" && oldString == "" {
		return ToolResult{Content: "error: new_string requires old_string (the exact fragment to replace)"}, nil
	}
	content := artifactRawArg(request, "content")
	if oldString != "" || appendText != "" {
		// Patch mode: the kernel edits the CURRENT body — the model only ever
		// sends the fragment, so a 100 KB document edits fine under the output
		// limit that kills a full-body call.
		if strings.TrimSpace(content) != "" {
			return ToolResult{Content: "error: pass either a full content body or an old_string/append patch, not both"}, nil
		}
		current, err := a.fetchArtifactBody(ctx, artifactID)
		if err != nil {
			return ToolResult{Content: "error: " + err.Error()}, nil
		}
		patched, err := artifactPatch(current, oldString, newString, appendText)
		if err != nil {
			return ToolResult{Content: "error: " + err.Error()}, nil
		}
		content = patched
	}
	if newVersion {
		return a.handleArtifactNewVersion(ctx, request, artifactID, content)
	}
	payload := map[string]any{
		"title":       artifactStringArg(request, "title"),
		"description": artifactStringArg(request, "description"),
		"kind":        artifactStringArg(request, "kind"),
		"actor":       request.ActorID,
	}
	// PUT writes saga_id as sent: only pass it when the caller named it, so an
	// update that never mentions the saga keeps the stored link (and passing
	// "" explicitly is the only way a tool call detaches one).
	if saga := artifactStringArg(request, "saga_id"); saga != "" {
		payload["saga_id"] = saga
	}
	if strings.TrimSpace(content) != "" {
		if err := presentation.ValidateMarkdownFences([]byte(content), presentation.Policy{}); err != nil {
			return ToolResult{Content: "error: " + err.Error() + "\nCompose visual blocks by the catalog in the present tool description."}, nil
		}
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
// artifact_submit. The approval decision remains human. The body arrives
// already patched (or as the raw content argument).
func (a *Activities) handleArtifactNewVersion(ctx context.Context, request ToolRequest, artifactID, content string) (ToolResult, error) {
	if strings.TrimSpace(content) != "" {
		if err := presentation.ValidateMarkdownFences([]byte(content), presentation.Policy{}); err != nil {
			return ToolResult{Content: "error: " + err.Error() + "\nCompose visual blocks by the catalog in the present tool description."}, nil
		}
	}
	payload := map[string]any{
		"title":       artifactStringArg(request, "title"),
		"description": artifactStringArg(request, "description"),
		"kind":        artifactStringArg(request, "kind"),
		"content":     content,
		"mime_type":   artifactStringArg(request, "mime_type"),
		"actor":       request.ActorID,
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
		"next":               "artifact_submit hands the revision to human re-approval",
	})
	return ToolResult{Content: string(encoded)}, nil
}

// handleArtifactSubmit opens a review round for the artifact's current version
// (202 + the round). Reviewer syntax follows ask_human recipients (docs/inbound-messages.md
// §7): user:<id>, org:<unit>, role:<role>, project_owner, or a bare user id.

// handleArtifactDeprecate retires an approved artifact: it drops out of the
// default listing and stops being a sane reference. A skill manifest still
// pointing at it blocks the transition (409 with the skill ids) — the model
// is told to update the manifests or pick a replacement first.
func (a *Activities) handleArtifactDeprecate(ctx context.Context, request ToolRequest) (ToolResult, error) {
	artifactID := artifactStringArg(request, "artifact_id")
	if artifactID == "" {
		return ToolResult{Content: "error: artifact_id is required"}, nil
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/artifacts/%s/deprecate", a.WorkspaceURL, url.PathEscape(artifactID)), map[string]any{})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: artifact deprecate failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	a.emitArtifactLifecycleToolEvent(ctx, request, "artifact.deprecated", artifactID, map[string]any{
		"artifact_id": artifactID,
	})
	return ToolResult{Content: fmt.Sprintf("Artifact %s is now deprecated: it dropped out of the default listing and must not be referenced by new work. Skills still listing it are blocked until their manifests change.", artifactID)}, nil
}

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
		"actor":     request.ActorID,
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
		ID                  string   `json:"id"`
		ArtifactVersion     int      `json:"artifact_version"`
		Mode                string   `json:"mode"`
		Status              string   `json:"status"`
		ReviewersResolved   []string `json:"reviewers_resolved"`
		ReviewersUnresolved []string `json:"reviewers_unresolved"`
	}
	_ = json.Unmarshal([]byte(body), &review)
	a.emitArtifactLifecycleToolEvent(ctx, request, "artifact.submitted", artifactID, map[string]any{
		"artifact_id": artifactID, "review_id": review.ID, "version": review.ArtifactVersion, "reviewers": reviewers,
	})
	// The resolution report is the honest half of the answer: a group entry
	// (project_owner, org:, role:) that matched nobody means the round can
	// never be decided — the model must re-submit to a concrete user NOW.
	encoded, _ := json.Marshal(map[string]any{
		"artifact_id": artifactID, "review_id": review.ID, "version": review.ArtifactVersion,
		"mode": review.Mode, "status": review.Status,
		"notified_user_ids":    review.ReviewersResolved,
		"unresolved_reviewers": review.ReviewersUnresolved,
		"note":                 "a human resolves the review; artifact_approve does not exist — watch inbox/notifications for the decision. If unresolved_reviewers is non-empty, re-submit with a concrete user:<id> instead.",
	})
	return ToolResult{Content: string(encoded)}, nil
}

// handleArtifactReturnToDraft sends the agent's OWN submitted work back to
// draft when the human asks for rework: the open round closes (cancelled) and
// the artifact becomes editable with artifact_update again. It never approves
// anything — the human decision stays with humans (docs/artifact-lifecycle.md
// §6). Mirrors the human's «отменить согласование» for the rework loop.
func (a *Activities) handleArtifactReturnToDraft(ctx context.Context, request ToolRequest) (ToolResult, error) {
	artifactID := artifactStringArg(request, "artifact_id")
	if artifactID == "" {
		return ToolResult{Content: "error: artifact_id is required"}, nil
	}
	reason := artifactStringArg(request, "reason")
	payload := map[string]any{"reason": reason, "actor": request.ActorID}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/artifacts/%s/return-to-draft", a.WorkspaceURL, url.PathEscape(artifactID)), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: artifact return to draft failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	a.emitArtifactLifecycleToolEvent(ctx, request, "artifact.returned_to_draft", artifactID, map[string]any{
		"artifact_id": artifactID, "reason": reason,
	})
	encoded, _ := json.Marshal(map[string]any{
		"artifact_id": artifactID, "status": "draft",
		"note": "the open review round was cancelled; edit the artifact with artifact_update and re-submit with artifact_submit when the rework is ready",
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
