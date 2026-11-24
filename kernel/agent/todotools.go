package agent

// Personal todo tools (docs/system-dashboard.md «Мой день»): the agent
// manages the GTD todo board of the run's HUMAN STARTER — planning and
// executing the starter's tasks on their board. The scope rule is the whole
// feature: the kernel pins every call to request.ActorID (the run starter)
// and refuses anything else, so another user's list is unreachable by
// construction and the model never passes a user id at all. Service/trigger
// runs without a human starter are refused here, client-side, with a hint.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// todoGroups is the closed GTD vocabulary (kernel folds unknown values into
// the inbox; these tools validate BEFORE the call so the model learns the
// vocabulary from the error, not from a silently wrong board).
var todoGroups = map[string]bool{
	"inbox": true, "next": true, "waiting": true, "someday": true, "done": true,
}

// todoActor resolves the only list the tools may touch: the run's starter.
func todoActor(request ToolRequest) (string, string) {
	actor := strings.TrimSpace(request.ActorID)
	if actor == "" {
		return "", "error: personal todos belong to the run's human starter and this run has none (service or trigger run) — ask the human to manage their «Мой день» notes in the web UI"
	}
	return actor, ""
}

// todoGroupArg reads an optional group argument; a non-empty out-of-vocabulary
// value is an error listing the valid groups.
func todoGroupArg(request ToolRequest, name string) (string, string) {
	raw, _ := request.Arguments[name].(string)
	g := strings.ToLower(strings.TrimSpace(raw))
	if g == "" {
		return "", ""
	}
	if !todoGroups[g] {
		return "", fmt.Sprintf("error: unknown group %q — use one of: inbox, next, waiting, someday, done", raw)
	}
	return g, ""
}

func todoIDArg(request ToolRequest) (string, string) {
	id, _ := request.Arguments["todo_id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return "", "error: todo_id is required (from todo_list)"
	}
	return id, ""
}

// todoDueArg reads an optional due date ("YYYY-MM-DD"); a non-empty value
// must parse, so the board never stores a date the UI cannot render.
func todoDueArg(request ToolRequest, name string) (string, string) {
	raw, _ := request.Arguments[name].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", fmt.Sprintf("error: %s must be YYYY-MM-DD (e.g. 2026-12-01)", name)
	}
	return raw, ""
}

// handleTodoList lists the starter's todos, optionally filtered by GTD group.
func (a *Activities) handleTodoList(ctx context.Context, request ToolRequest) (ToolResult, error) {
	actor, errText := todoActor(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	query := url.Values{"actor": {actor}}
	group, errText := todoGroupArg(request, "group")
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	if group != "" {
		query.Set("group", group)
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet,
		fmt.Sprintf("%s/v1/internal/todos?%s", a.WorkspaceURL, query.Encode()), nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: todo list failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	return ToolResult{Content: body}, nil
}

// handleTodoAdd adds a note to the starter's board: text verbatim-short,
// group defaults to the kernel's inbox fold.
func (a *Activities) handleTodoAdd(ctx context.Context, request ToolRequest) (ToolResult, error) {
	actor, errText := todoActor(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	text, _ := request.Arguments["text"].(string)
	if strings.TrimSpace(text) == "" {
		return ToolResult{Content: "error: text is required (capture the task verbatim, short)"}, nil
	}
	note, _ := request.Arguments["note"].(string)
	payload := map[string]any{"actor": actor, "text": text, "note": note}
	group, errText := todoGroupArg(request, "group")
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	if group != "" {
		payload["group"] = group
	}
	due, errText := todoDueArg(request, "due")
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	if due != "" {
		payload["due"] = due
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/internal/todos", a.WorkspaceURL), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: todo add failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	return ToolResult{Content: body}, nil
}

// handleTodoUpdate edits one starter todo: text/note rewrite, a GTD group
// move, or a sort-order change. Only the arguments actually present ride the
// call — an omitted group never moves the note anywhere.
func (a *Activities) handleTodoUpdate(ctx context.Context, request ToolRequest) (ToolResult, error) {
	actor, errText := todoActor(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	id, errText := todoIDArg(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	payload := map[string]any{"actor": actor}
	group, errText := todoGroupArg(request, "group")
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	if group != "" {
		payload["group"] = group
	}
	if v, ok := request.Arguments["text"]; ok {
		payload["text"] = v
	}
	if v, ok := request.Arguments["note"]; ok {
		payload["note"] = v
	}
	if _, ok := request.Arguments["due"]; ok {
		due, errText := todoDueArg(request, "due")
		if errText != "" {
			return ToolResult{Content: errText}, nil
		}
		payload["due"] = due // empty string clears the date
	}
	if v, ok := request.Arguments["sort"]; ok {
		payload["sort"] = v
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPatch,
		fmt.Sprintf("%s/v1/internal/todos/%s", a.WorkspaceURL, url.PathEscape(id)), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: todo update failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	return ToolResult{Content: body}, nil
}

// handleTodoDone marks one starter todo done (moves it to the done group) —
// the bookkeeping half of «execute a tracked task».
func (a *Activities) handleTodoDone(ctx context.Context, request ToolRequest) (ToolResult, error) {
	actor, errText := todoActor(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	id, errText := todoIDArg(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPatch,
		fmt.Sprintf("%s/v1/internal/todos/%s", a.WorkspaceURL, url.PathEscape(id)),
		map[string]any{"actor": actor, "group": "done"})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: todo done failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	return ToolResult{Content: body}, nil
}

// handleTodoDelete removes one starter todo permanently (only on request).
func (a *Activities) handleTodoDelete(ctx context.Context, request ToolRequest) (ToolResult, error) {
	actor, errText := todoActor(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	id, errText := todoIDArg(request)
	if errText != "" {
		return ToolResult{Content: errText}, nil
	}
	body, status, err := a.workspaceDo(ctx, http.MethodDelete,
		fmt.Sprintf("%s/v1/internal/todos/%s?%s", a.WorkspaceURL, url.PathEscape(id), url.Values{"actor": {actor}}.Encode()), nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: todo delete failed (HTTP %d): %s", status, truncate(body, 600))}, nil
	}
	return ToolResult{Content: body}, nil
}
