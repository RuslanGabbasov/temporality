package main

// Personal daily brief («Мой день») — the landing page for users who do not
// get the full operational dashboard. One row per (user, day).
//
// The split that keeps it honest: the KERNEL collects the factual snapshot
// (runs, artifact approvals, learning progress, month metrics) and the
// SUPERVISOR MODEL writes only the narrative summary on top of it. Lists,
// badges and links always render from the stored facts, never from model
// text — the model cannot hallucinate a run that isn't there, and a stale
// narrative cannot invent a dead link.
//
// Generation happens twice: the hourly loop pre-builds briefs for users with
// recent activity, and the first GET of the day falls back to lazy
// generation (with a deterministic no-model digest when the model is out).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// GTD groups of the personal todo widget (Getting Things Done clusters).
var todoGroups = map[string]bool{
	"inbox": true, "next": true, "waiting": true, "someday": true, "done": true,
}

// normalizeTodoGroup maps a client-supplied group to the closed vocabulary:
// unknown, empty or wrongly-cased values degrade to the inbox instead of
// failing the write — a todo must never be lost to a typo.
func normalizeTodoGroup(group string) string {
	g := strings.ToLower(strings.TrimSpace(group))
	if todoGroups[g] {
		return g
	}
	return "inbox"
}

// briefTerminalStatuses: run statuses past the point of attention. Anything
// else (started, running, waiting_human, …) counts as «still going».
var briefTerminalStatuses = map[string]bool{
	"succeeded": true, "completed": true, "failed": true, "cancelled": true, "turn_limit": true,
}

const (
	// Generation cap per loop pass: a fresh install with hundreds of users
	// must not turn one tick into hundreds of model calls — the rest arrive
	// lazily on their first GET.
	briefMaxPerPass = 25
	// The loop pre-builds briefs only for users whose runs show recent
	// activity; everybody else is served lazily on first visit.
	briefActivityWindow = 72 * time.Hour
)

type UserBriefRun struct {
	RunID   string    `json:"run_id"`
	TaskID  string    `json:"task_id,omitempty"`
	Title   string    `json:"title,omitempty"`
	Project string    `json:"project,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	Status  string    `json:"status"`
	At      time.Time `json:"at"`
	Error   string    `json:"error,omitempty"`
}

type UserBriefApproval struct {
	ArtifactID    string    `json:"artifact_id"`
	ArtifactTitle string    `json:"artifact_title"`
	ReviewID      string    `json:"review_id"`
	Mode          string    `json:"mode,omitempty"`
	RequestedAt   time.Time `json:"requested_at,omitempty"`
	OriginUser    string    `json:"origin_user,omitempty"`
}

type UserBriefLearning struct {
	Active    bool   `json:"active"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	NextID    string `json:"next_id,omitempty"`
	NextState string `json:"next_state,omitempty"`
}

type UserBriefMetrics struct {
	Month      string  `json:"month"` // YYYY-MM
	Runs       int     `json:"runs"`
	FailedRuns int     `json:"failed_runs"`
	Tokens     int64   `json:"tokens"`
	CostUSD    float64 `json:"cost_usd"`
	ModelCalls int     `json:"model_calls"`
}

// UserBriefPayload is what the UI renders: the model narrative (summary)
// plus the live-fact sections it must never override.
type UserBriefPayload struct {
	Day         string              `json:"day"`
	GeneratedAt time.Time           `json:"generated_at"`
	Source      string              `json:"source"` // model | fallback
	Summary     []string            `json:"summary"`
	Running     []UserBriefRun      `json:"running"`
	Recent      []UserBriefRun      `json:"recent"`
	Failed      []UserBriefRun      `json:"failed"`
	Approvals   []UserBriefApproval `json:"approvals"`
	Learning    UserBriefLearning   `json:"learning"`
	Metrics     UserBriefMetrics    `json:"metrics"`
}

// briefDay renders the UTC calendar-day key of a brief.
func briefDay(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// briefMonth renders the YYYY-MM metrics window key.
func briefMonth(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// briefRun projects a store run for the brief; the title resolves from the
// task (the store's run row has none) and degrades to the run id.
func briefRun(run workspace.Run, titles map[string]string) UserBriefRun {
	out := UserBriefRun{
		RunID:   run.RunID,
		TaskID:  run.TaskID,
		Project: run.ProjectID,
		Agent:   run.AgentID,
		Status:  run.Status,
		At:      run.UpdatedAt,
		Error:   run.Error,
	}
	if out.RunID == "" {
		out.RunID = run.ID
	}
	if t := titles[run.TaskID]; t != "" {
		out.Title = t
	}
	return out
}

// collectUserBriefFacts assembles the factual snapshot for one user: active
// and recent runs (24h), failed runs (72h), open artifact approvals, learning
// progress and the month's spend. Journal outages degrade the metrics to
// DB-only numbers — the brief never 500s on the journal.
func collectUserBriefFacts(ctx context.Context, ws *workspace.Store, userID, principal string, now time.Time, observationURL, journalToken string, prices cost.Config) (UserBriefPayload, error) {
	payload := UserBriefPayload{Day: briefDay(now), GeneratedAt: now.UTC(), Source: "fallback", Metrics: UserBriefMetrics{Month: briefMonth(now)}}

	runs, err := ws.ListRunsByActorSince(ctx, userID, now.Add(-briefActivityWindow), 200)
	if err != nil {
		return payload, err
	}

	// Task titles for the runs that made the cut (bounded: ≤ the caps below).
	titles := map[string]string{}
	needTitle := map[string]bool{}
	for i := range runs {
		if runs[i].TaskID != "" {
			needTitle[runs[i].TaskID] = true
		}
	}
	for taskID := range needTitle {
		task, err := ws.GetTask(ctx, taskID)
		if err == nil {
			titles[taskID] = task.Title
		}
	}

	since24 := now.Add(-24 * time.Hour)
	for i := range runs {
		run := &runs[i]
		switch {
		case !briefTerminalStatuses[run.Status] && run.CreatedAt.After(since24):
			payload.Running = append(payload.Running, briefRun(*run, titles))
		case run.Status == "failed":
			payload.Failed = append(payload.Failed, briefRun(*run, titles))
		case run.CreatedAt.After(since24):
			payload.Recent = append(payload.Recent, briefRun(*run, titles))
		}
	}
	// ListRunsByActorSince returns runs newest-first (ORDER BY created_at DESC),
	// so the caps keep the most recent slice of each list.
	payload.Running = capBriefRuns(payload.Running, 10)
	payload.Failed = capBriefRuns(payload.Failed, 5)
	payload.Recent = capBriefRuns(payload.Recent, 8)

	// Approvals: the same matching the artifacts page's «На моём согласовании»
	// tab applies (user:/role:/unit-head reviewer entries + requested by me).
	users, err := ws.ListUsers(ctx)
	if err == nil {
		entries, inboxErr := ws.ListArtifactReviewInbox(ctx, principal, users, nil)
		if inboxErr == nil {
			for _, entry := range entries {
				payload.Approvals = append(payload.Approvals, UserBriefApproval{
					ArtifactID:    entry.ArtifactID,
					ArtifactTitle: entry.ArtifactTitle,
					ReviewID:      entry.Review.ID,
					Mode:          entry.Review.Mode,
					RequestedAt:   entry.Review.RequestedAt,
					OriginUser:    entry.OriginUser,
				})
			}
		}
	}

	// Learning progress: the active program's completion plus the next item.
	if program, err := ws.GetLearningProgram(ctx, userID); err == nil && program != nil && program.Status == "active" {
		payload.Learning.Active = true
		payload.Learning.Total = len(program.Items)
		for _, it := range program.Items {
			if it.State == "completed" || it.State == "skipped" || it.State == "not_applicable" {
				payload.Learning.Done++
			} else if payload.Learning.NextID == "" && (it.State == "pending" || it.State == "available" || it.State == "in_progress") {
				payload.Learning.NextID, payload.Learning.NextState = it.LessonID, it.State
			}
		}
	}

	// Month metrics: DB run counts plus the journal-priced model spend.
	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	monthRuns, err := ws.ListRunsByActorSince(ctx, userID, monthStart, 2000)
	if err != nil {
		monthRuns = nil // degrade: metrics keep the day-level numbers
	}
	payload.Metrics.Runs = len(monthRuns)
	for i := range monthRuns {
		if monthRuns[i].Status == "failed" {
			payload.Metrics.FailedRuns++
		}
	}
	runIndex := map[string]*userRunAgg{}
	for i := range monthRuns {
		agg := &userRunAgg{project: monthRuns[i].ProjectID, agent: monthRuns[i].AgentID}
		runIndex[monthRuns[i].ID] = agg
		if monthRuns[i].RunID != "" && monthRuns[i].RunID != monthRuns[i].ID {
			runIndex[monthRuns[i].RunID] = agg
		}
	}
	if events, err := userStatsJournalPages(ctx, observationURL, journalToken, "model.completed", monthStart, userID, 6); err == nil {
		tokens, usd, calls := foldUserUsage(events, runIndex, prices)
		payload.Metrics.Tokens, payload.Metrics.CostUSD, payload.Metrics.ModelCalls = tokens, usd, calls
	}
	return payload, nil
}

func capBriefRuns(runs []UserBriefRun, n int) []UserBriefRun {
	if len(runs) <= n {
		return runs
	}
	return runs[:n]
}

// capBriefRunsLast keeps the LAST n rows (store order is oldest-first, so the
// newest runs sit at the tail).

// briefDigest renders the deterministic no-model summary: every line is a
// fact already stored in the payload, phrased the way the model is asked to.
func briefDigest(p UserBriefPayload) []string {
	var out []string
	if len(p.Running) > 0 {
		out = append(out, fmt.Sprintf("Сейчас в работе: %d запуск(ов).", len(p.Running)))
	} else {
		out = append(out, "Сейчас активных запусков нет.")
	}
	if len(p.Recent) > 0 {
		out = append(out, fmt.Sprintf("За последние сутки завершено запусков: %d.", len(p.Recent)))
	}
	if len(p.Failed) > 0 {
		out = append(out, fmt.Sprintf("Требуют внимания: %d неудачных запусков за 3 дня.", len(p.Failed)))
	}
	if len(p.Approvals) > 0 {
		out = append(out, fmt.Sprintf("Ждут твоего согласования: %d артефакт(ов).", len(p.Approvals)))
	}
	if p.Learning.Active {
		line := fmt.Sprintf("Обучение: пройдено %d из %d.", p.Learning.Done, p.Learning.Total)
		if p.Learning.NextID != "" {
			line += fmt.Sprintf(" Следующий урок: %s.", p.Learning.NextID)
		}
		out = append(out, line)
	}
	out = append(out, fmt.Sprintf("За месяц: %d запусков, %d токенов, $%.2f.", p.Metrics.Runs, p.Metrics.Tokens, p.Metrics.CostUSD))
	return out
}

// briefFactsDigest renders the payload as the compact factual text the model
// summarizes. Numbers only — no ids the narrative doesn't need.
func briefFactsDigest(p UserBriefPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "день: %s\n", p.Day)
	fmt.Fprintf(&b, "активные запуски сейчас: %d\n", len(p.Running))
	fmt.Fprintf(&b, "завершено за сутки: %d\n", len(p.Recent))
	fmt.Fprintf(&b, "неудачных за 3 дня: %d\n", len(p.Failed))
	for _, r := range p.Failed {
		title := r.Title
		if title == "" {
			title = r.RunID
		}
		fmt.Fprintf(&b, "  - неудачный запуск: %s (%s)\n", title, r.Project)
	}
	fmt.Fprintf(&b, "артефактов ждёт согласования: %d\n", len(p.Approvals))
	for _, a := range p.Approvals {
		fmt.Fprintf(&b, "  - на согласовании: %s\n", a.ArtifactTitle)
	}
	if p.Learning.Active {
		fmt.Fprintf(&b, "обучение: пройдено %d из %d", p.Learning.Done, p.Learning.Total)
		if p.Learning.NextID != "" {
			fmt.Fprintf(&b, ", следующий урок: %s", p.Learning.NextID)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("обучение: активной программы нет\n")
	}
	fmt.Fprintf(&b, "за месяц: %d запусков (из них неудачных %d), %d токенов, $%.2f\n",
		p.Metrics.Runs, p.Metrics.FailedRuns, p.Metrics.Tokens, p.Metrics.CostUSD)
	return b.String()
}

const briefSystemPrompt = `Ты — супервайзер цифровой организации. Пишешь пользователю короткое утреннее резюме его рабочего дня («Мой день») на основе ТОЛЬКО приведённых фактов.
Правила:
- 3-6 пунктов, каждый — одна короткая фраза по-русски (до 200 символов).
- Начинай с самого важного: неудачные запуски и ожидающие согласования — первыми.
- Никаких выдумок: упоминай только числа, названия и статусы из фактов.
- Никаких markdown-разметок, только текст пунктов.
Ответь строго JSON вида {"summary": ["пункт 1", "пункт 2", ...]} без пояснений.`

// sanitizeUserBriefSummary parses the model answer into narrative bullets:
// strict JSON object with a summary array of strings, trimmed, de-fenced,
// capped (8 bullets × 400 chars). Anything else is an error — the caller
// falls back to the deterministic digest.
func sanitizeUserBriefSummary(content string) ([]string, error) {
	raw := strings.TrimSpace(content)
	if start := strings.Index(raw, "{"); start >= 0 {
		if end := strings.LastIndex(raw, "}"); end > start {
			raw = raw[start : end+1]
		}
	}
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var parsed struct {
		Summary []any `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Summary))
	for _, item := range parsed.Summary {
		s, ok := item.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if runes := []rune(s); len(runes) > 400 {
			s = string(runes[:400])
		}
		out = append(out, s)
		if len(out) == 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("model summary is empty")
	}
	return out, nil
}

// generateUserBrief builds the day's brief: facts first, then the model's
// narrative on top; a model failure (or a malformed answer) silently degrades
// to the deterministic digest — the page must always have something true to
// show.
// principal is the value the artifact inbox matches reviewer entries against
// (the artifacts page passes the same caller subject); the loop only has the
// user id, so it passes that.
func generateUserBrief(ctx context.Context, ws *workspace.Store, userID, principal string, now time.Time, observationURL, journalToken string, prices cost.Config, modelClient func(context.Context) (*llm.Client, error), log *slog.Logger) (UserBriefPayload, error) {
	payload, err := collectUserBriefFacts(ctx, ws, userID, principal, now, observationURL, journalToken, prices)
	if err != nil {
		return payload, err
	}
	payload.Summary = briefDigest(payload)
	if modelClient != nil {
		learnCtx := withSupervisorPurpose(ctx, "brief")
		if client, err := modelClient(learnCtx); err == nil {
			completion, err := client.Complete(learnCtx, []llm.Message{
				{Role: "system", Content: briefSystemPrompt},
				{Role: "user", Content: "Факты:\n" + briefFactsDigest(payload)},
			}, nil)
			if err == nil {
				if summary, err := sanitizeUserBriefSummary(completion.Content); err == nil {
					payload.Summary = summary
					payload.Source = "model"
				} else if log != nil {
					log.Warn("personal brief summary rejected", "user", userID, "error", err)
				}
			} else if log != nil {
				log.Warn("personal brief model call failed", "user", userID, "error", err)
			}
		} else if log != nil {
			log.Warn("personal brief model unavailable", "user", userID, "error", err)
		}
	}
	return payload, nil
}

// storeUserBrief persists the payload as today's row.
func storeUserBrief(ctx context.Context, ws *workspace.Store, userID string, payload UserBriefPayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return ws.UpsertUserDailyBrief(ctx, &workspace.UserDailyBrief{
		UserID: userID, Day: payload.Day, Payload: raw, GeneratedAt: payload.GeneratedAt,
	})
}

// registerPersonalBriefRoutes mounts GET /v1/dashboard/brief (the day brief,
// lazily generated on first view) and the personal GTD todo CRUD under
// /v1/dashboard/todos. Every path here is strictly self-scoped.
func registerPersonalBriefRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store,
	observationURL, journalToken string, prices cost.Config,
	modelClient func(context.Context) (*llm.Client, error), log *slog.Logger) {

	mux.HandleFunc("GET /v1/dashboard/brief", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		principal, ok := controlplane.FromContext(r.Context())
		if !ok || principal.Subject == "" {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		userID := requestUserID(r)
		if userID == "" {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		now := time.Now()
		day := briefDay(now)
		if q := strings.TrimSpace(r.URL.Query().Get("day")); q != "" {
			if _, err := time.Parse("2006-01-02", q); err != nil {
				writeError(w, 400, fmt.Errorf("day must be YYYY-MM-DD"))
				return
			}
			day = q
		}
		refresh := r.URL.Query().Get("refresh") == "1"
		if !refresh {
			if existing, err := ws.GetUserDailyBrief(r.Context(), userID, day); err == nil && existing != nil {
				var payload UserBriefPayload
				if json.Unmarshal(existing.Payload, &payload) == nil {
					writeJSON(w, 200, map[string]any{"brief": payload, "cached": true})
					return
				}
			}
		}
		payload, err := generateUserBrief(r.Context(), ws, userID, principal.Subject, now, observationURL, journalToken, prices, modelClient, log)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if err := storeUserBrief(r.Context(), ws, userID, payload); err != nil {
			log.Warn("personal brief store", "user", userID, "error", err) // still serve it
		}
		_ = ws.RecordAccessAudit(r.Context(), userID, "dashboard.brief_generated", "user_brief", userID,
			map[string]any{"day": payload.Day, "source": payload.Source})
		writeJSON(w, 200, map[string]any{"brief": payload, "cached": false})
	})

	mux.HandleFunc("GET /v1/dashboard/todos", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		userID := requestUserID(r)
		if userID == "" {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		todos, err := ws.ListUserTodos(r.Context(), userID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"todos": todos})
	})

	mux.HandleFunc("POST /v1/dashboard/todos", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		userID := requestUserID(r)
		if userID == "" {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		var req struct {
			Group string `json:"group"`
			Text  string `json:"text"`
			Note  string `json:"note"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, 400, err)
			return
		}
		text := strings.TrimSpace(req.Text)
		if text == "" {
			writeError(w, 422, errors.New("text is required"))
			return
		}
		if runes := []rune(text); len(runes) > 500 {
			text = string(runes[:500])
		}
		note := strings.TrimSpace(req.Note)
		if runes := []rune(note); len(runes) > 2000 {
			note = string(runes[:2000])
		}
		todo := &workspace.UserTodo{
			ID: "todo-" + shortID(), UserID: userID,
			Group: normalizeTodoGroup(req.Group), Text: text, Note: note,
		}
		if err := ws.CreateUserTodo(r.Context(), todo); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"todo": todo})
	})

	mux.HandleFunc("PATCH /v1/dashboard/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		userID := requestUserID(r)
		if userID == "" {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		var req struct {
			Group *string `json:"group"`
			Text  *string `json:"text"`
			Note  *string `json:"note"`
			Sort  *int    `json:"sort"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, 400, err)
			return
		}
		todos, err := ws.ListUserTodos(r.Context(), userID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		var todo *workspace.UserTodo
		for i := range todos {
			if todos[i].ID == r.PathValue("id") {
				todo = &todos[i]
				break
			}
		}
		if todo == nil {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		if req.Group != nil {
			todo.Group = normalizeTodoGroup(*req.Group)
		}
		if req.Text != nil {
			text := strings.TrimSpace(*req.Text)
			if text == "" {
				writeError(w, 422, errors.New("text is required"))
				return
			}
			if runes := []rune(text); len(runes) > 500 {
				text = string(runes[:500])
			}
			todo.Text = text
		}
		if req.Note != nil {
			note := strings.TrimSpace(*req.Note)
			if runes := []rune(note); len(runes) > 2000 {
				note = string(runes[:2000])
			}
			todo.Note = note
		}
		if req.Sort != nil {
			todo.Sort = *req.Sort
		}
		if err := ws.UpdateUserTodo(r.Context(), todo); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"todo": todo})
	})

	mux.HandleFunc("DELETE /v1/dashboard/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		userID := requestUserID(r)
		if userID == "" {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		if err := ws.DeleteUserTodo(r.Context(), userID, r.PathValue("id")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}

// StartPersonalBriefLoop pre-builds day briefs in the background: hourly,
// only for users whose runs show recent activity, capped per pass. The first
// visit of the day generates lazily anyway — this loop just makes sure the
// morning landing is instant for the people actually working.
func StartPersonalBriefLoop(ctx context.Context, log *slog.Logger, ws *workspace.Store,
	observationURL, journalToken string, prices cost.Config, modelClient func(context.Context) (*llm.Client, error)) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPassGuarded(log, "personal-brief", ctx, func(pctx context.Context) {
					generateDailyBriefs(pctx, log, ws, observationURL, journalToken, prices, modelClient)
				})
			}
		}
	}()
}

// generateDailyBriefs builds today's brief for up to briefMaxPerPass active
// users without one. Per-user failures log and continue — one broken profile
// must not starve the rest of the pass.
func generateDailyBriefs(ctx context.Context, log *slog.Logger, ws *workspace.Store,
	observationURL, journalToken string, prices cost.Config, modelClient func(context.Context) (*llm.Client, error)) {
	users, err := ws.ListUsers(ctx)
	if err != nil {
		log.Warn("personal brief pass: list users", "error", err)
		return
	}
	now := time.Now()
	day := briefDay(now)
	generated := 0
	for _, u := range users {
		if !u.Active || generated >= briefMaxPerPass {
			continue
		}
		// Only users with recent activity: the brief is a work-day surface.
		recent, err := ws.ListRunsByActorSince(ctx, u.ID, now.Add(-briefActivityWindow), 1)
		if err != nil || len(recent) == 0 {
			continue
		}
		if existing, err := ws.GetUserDailyBrief(ctx, u.ID, day); err != nil || existing != nil {
			continue
		}
		payload, err := generateUserBrief(ctx, ws, u.ID, u.ID, now, observationURL, journalToken, prices, modelClient, log)
		if err != nil {
			log.Warn("personal brief generate", "user", u.ID, "error", err)
			continue
		}
		if err := storeUserBrief(ctx, ws, u.ID, payload); err != nil {
			log.Warn("personal brief store", "user", u.ID, "error", err)
			continue
		}
		generated++
	}
}

// canSeeFullDashboard gates the operational dashboard (health, users, usage
// tabs): admins, holders of any org-unit role and org-unit heads. Everyone
// else lands on «Мой день» — their personal brief — instead.
func canSeeFullDashboard(ctx context.Context, ws *workspace.Store, principal controlplane.Principal) bool {
	if principal.Role >= controlplane.RoleAdmin {
		return true
	}
	// Org-role holders (department managers) keep access without a store
	// read: the grants ride on the principal.
	if len(principal.OrgRoles) > 0 {
		return true
	}
	if ws == nil {
		return false
	}
	if principal.UserID == "" {
		return false
	}
	units, err := ws.ListOrgUnits(ctx)
	if err != nil {
		return false // degrade closed: no org tree read, no dashboard
	}
	return isOrgHeadOf(units, principal.UserID)
}

// isOrgHeadOf reports whether userID heads any unit — the pure half of
// canSeeFullDashboard, split out for the unit test.
func isOrgHeadOf(units []workspace.OrgUnit, userID string) bool {
	for _, u := range units {
		if u.HeadUserID != "" && strings.EqualFold(u.HeadUserID, userID) {
			return true
		}
	}
	return false
}
