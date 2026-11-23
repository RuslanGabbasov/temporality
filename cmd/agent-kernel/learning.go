package main

// Contextual learning infrastructure (docs/ai-guided-onboarding-and-learning.md,
// stage P0): a catalog of semantically identified UI targets and one
// supervisor-backed endpoint that explains a target in the context of the
// CURRENT organization. The frontend renders the returned plan (navigate /
// highlight / explanation / example) through its spotlight overlay and
// reports nothing automatically — explanations only.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// LearningProgressEvidence is the mastery fact as the learning center shows it.
type LearningProgressEvidence struct {
	Mastery   string    `json:"mastery"`
	Source    string    `json:"source,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
}

// learningTarget is one entry of the semantic UI-target catalog. The id is
// stable against layout, CSS classes, localized text and window size
// (§6.3): it names a CONCEPT, and the frontend resolves it to whatever
// element currently represents that concept.
type learningTarget struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Page        string `json:"page"`       // board page the target belongs to
	DocsLink    string `json:"docs_link"`  // user-guide anchor for further reading
	FeatureID   string `json:"feature_id"` // capability the target belongs to (§4.5 mastery)
}

// learningTargetCatalog seeds P0 with the concepts the board and the agent
// configuration actually expose today (§6.3 examples plus the operational
// surfaces). The frontend registry may declare MORE targets — this catalog
// only carries the supervisor-facing knowledge.
var learningTargetCatalog = map[string]learningTarget{
	"organization.roles.reviewer": {
		ID: "organization.roles.reviewer", Title: "Reviewer role acceptance criteria",
		Description: "What a reviewer checks before accepting the work: acceptance criteria live on this block; the agent cannot finish a review task without satisfying them.",
		Page:        "/agents", DocsLink: "docs/agents.html", FeatureID: "quality-control",
	},
	"organization.policies.inheritance": {
		ID: "organization.policies.inheritance", Title: "Policy inheritance",
		Description: "An org unit inherits the policies of its parent; a child can tighten them but cannot loosen what the parent restricts.",
		Page:        "/org/policies", DocsLink: "docs/org-policies.html", FeatureID: "governance",
	},
	"agent.delegation.configuration": {
		ID: "agent.delegation.configuration", Title: "Delegation",
		Description: "Delegation lets an agent split a large task into subtasks for other agents; width, depth and the agent pool are configured here.",
		Page:        "/agents", DocsLink: "docs/agent-delegation.html", FeatureID: "delegation",
	},
	"artifact.approval.rules": {
		ID: "artifact.approval.rules", Title: "Artifact approval",
		Description: "An artifact moves from draft to approved through a review round: reviewers, all/any mode and the decision belong to people, not to the agent.",
		Page:        "/artifacts", DocsLink: "docs/artifacts.html", FeatureID: "artifact-review",
	},
	"plan.execution.strategy": {
		ID: "plan.execution.strategy", Title: "Plan execution",
		Description: "A plan runs its task DAG across agents: order comes from dependencies, parallel width is bounded, review gates can be inserted between tasks.",
		Page:        "/org/work", DocsLink: "docs/agent-delegation.html", FeatureID: "plans",
	},
	"supervisor.settings": {
		ID: "supervisor.settings", Title: "Supervisor",
		Description: "The supervisor analyzes failed runs, anomalies and knowledge conflicts and prepares recommendations — it never applies them without a human decision.",
		Page:        "/supervisor", DocsLink: "docs/observability.html", FeatureID: "supervision",
	},
}

// learningTargetCatalogList is the stable JSON form for the frontend.
func learningTargetCatalogList() []learningTarget {
	out := make([]learningTarget, 0, len(learningTargetCatalog))
	for _, t := range learningTargetCatalog {
		out = append(out, t)
	}
	return out
}

// learningExplainRequest is the contextual-help call from the UI: which
// target, which page it is on now, and an optional free-form question
// (§12.3 — the user can ask in natural language).
type learningExplainRequest struct {
	Project  string `json:"project"`
	TargetID string `json:"target_id"`
	Page     string `json:"page,omitempty"`
	Question string `json:"question,omitempty"`
	ActorID  string `json:"actor_id,omitempty"`
}

// RegisterLearningRoutes mounts the P0 contextual-learning endpoints.
func RegisterLearningRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, visibleUnits func(r *http.Request) []string, modelClient func(ctx context.Context) (*llm.Client, error), log *slog.Logger) {
	registerLessonGenerate(mux, gate, ws, visibleUnits, modelClient)
	mux.HandleFunc("GET /v1/learning/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		writeJSON(w, 200, map[string]any{"targets": learningTargetCatalogList()})
	})

	mux.HandleFunc("POST /v1/learning/explain", func(w http.ResponseWriter, r *http.Request) {
		var input learningExplainRequest
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, err)
			return
		}
		input.Project = strings.TrimSpace(input.Project)
		input.TargetID = strings.TrimSpace(input.TargetID)
		input.Page = strings.TrimSpace(input.Page)
		input.Question = strings.TrimSpace(input.Question)
		if input.TargetID == "" {
			writeError(w, 422, fmt.Errorf("target_id is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleReader, input.Project) {
			return
		}
		target, ok := learningTargetCatalog[input.TargetID]
		if !ok {
			// An unknown target is not an error for the UI — the supervisor is
			// told the catalog has no material and answers generically (§6.3).
			target = learningTarget{ID: input.TargetID, Title: input.TargetID, Description: ""}
		}

		// Organizational context: the explanation must fit the CURRENT
		// configuration, not describe the feature in the abstract (§3.1).
		orgContext := learningOrgContext(r.Context(), ws, input.Project, visibleUnits(r))

		learnCtx := withSupervisorPurpose(r.Context(), "learning")
		client, err := modelClient(learnCtx)
		if err != nil {
			// Degrade honestly: without a model the catalog entry itself is
			// the explanation (§16.7 — no UI errors from missing material).
			explanation := target.Description
			if explanation == "" {
				explanation = "Обучающий материал для этого элемента пока не готов."
			}
			writeJSON(w, 200, map[string]any{"explanation": explanation, "docs_link": target.DocsLink, "mode": "informational"})
			return
		}
		plan, err := agent.LearningExplainCase(learnCtx, client, agent.LearningExplainInput{
			TargetID:    target.ID,
			TargetTitle: target.Title,
			TargetAbout: target.Description,
			Page:        input.Page,
			Question:    input.Question,
			OrgContext:  orgContext,
		})
		if err != nil {
			log.Warn("learning explain: model", "target", input.TargetID, "error", err.Error())
			if strings.TrimSpace(target.Description) == "" {
				writeError(w, 502, fmt.Errorf("supervisor model unavailable"))
				return
			}
			plan = agent.LearningExplainPlan{Explanation: target.Description, Mode: "informational"}
		}
		if plan.Mode == "" {
			plan.Mode = "informational"
		}
		// §15: guidance opened is an observable event (best-effort — the
		// answer must not fail because the journal is unavailable).
		_ = ws.RecordAccessAudit(r.Context(), actorIDOr(input.ActorID, requestUserID(r)), "learning.guidance_opened", "learning_target", input.TargetID,
			map[string]any{"project": input.Project, "question": input.Question != ""})
		writeJSON(w, 200, map[string]any{
			"explanation": plan.Explanation, "highlight": plan.Highlight,
			"example": plan.Example, "navigate_route": plan.NavigateRoute,
			"mode": plan.Mode, "docs_link": target.DocsLink,
		})
	})

	// GET /v1/learning/state?project= — the learning center payload: program,
	// capability mastery, proactive suggestions (§12.4).
	mux.HandleFunc("GET /v1/learning/state", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		userID := requestUserID(r)
		project := strings.TrimSpace(r.URL.Query().Get("project"))
		appCtx := learningAppContext(r.Context(), ws, project, visibleUnits(r))
		progress, err := ws.ListLearningProgress(r.Context(), userID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		mastery := map[string]LearningProgressEvidence{}
		for _, p := range progress {
			mastery[p.FeatureID] = LearningProgressEvidence{Mastery: p.Mastery, Source: p.Source, CheckedAt: p.CheckedAt}
		}
		capabilities := make([]map[string]any, 0, len(learningCapabilities))
		for _, cap := range learningCapabilities {
			applies := cap.AppliesTo == nil || cap.AppliesTo(appCtx)
			entry := map[string]any{"id": cap.ID, "title": cap.Title, "about": cap.About, "applies": applies}
			if ev, ok := mastery[cap.ID]; ok {
				entry["mastery"] = ev.Mastery
				entry["mastery_source"] = ev.Source
				entry["mastery_at"] = ev.CheckedAt
			} else {
				entry["mastery"] = "unknown"
			}
			capabilities = append(capabilities, entry)
		}
		program, err := ws.GetLearningProgram(r.Context(), userID)
		hasProgram := err == nil && program != nil
		suggestions, _ := ws.ListLearningSuggestions(r.Context(), userID)
		writeJSON(w, 200, map[string]any{
			"program":      program,
			"capabilities": capabilities,
			"has_program":  hasProgram,
			"suggestions":  suggestions,
		})
	})

	// POST /v1/learning/program/build — the P2 planner: the supervisor reads
	// the factual configuration and assembles the user's program from the
	// catalog lessons that APPLY here; without a model it degrades to the
	// deterministic onboarding ladder (applicable lessons by priority).
	mux.HandleFunc("POST /v1/learning/program/build", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		var input struct {
			Project string `json:"project"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, err)
			return
		}
		userID := requestUserID(r)

		type planItem struct {
			LessonID string `json:"lesson_id"`
			Why      string `json:"why,omitempty"`
		}
		items := []planItem{}
		learnCtx := withSupervisorPurpose(r.Context(), "learning")
		client, mErr := modelClient(learnCtx)
		if mErr == nil {
			planLessons := make([]agent.LearningPlanLesson, 0, len(learningLessons))
			for _, lesson := range learningLessons {
				planLessons = append(planLessons, agent.LearningPlanLesson{
					ID: lesson.ID, Goal: lesson.Goal, Audience: lesson.Audience, Feature: lesson.FeatureID,
				})
			}
			planFeatures := make([]string, 0, len(learningCapabilities))
			for _, cap := range learningCapabilities {
				planFeatures = append(planFeatures, cap.ID+" — "+cap.Title)
			}
			if plan, perr := agent.LearningPlanCase(learnCtx, client, agent.LearningPlanInput{
				Role:     principalRole(r),
				Context:  learningOrgContext(r.Context(), ws, strings.TrimSpace(input.Project), visibleUnits(r)),
				Lessons:  planLessons,
				Features: planFeatures,
			}); perr == nil {
				for _, item := range plan {
					if findLearningLesson(item.LessonID) != nil {
						items = append(items, planItem{LessonID: item.LessonID, Why: item.Why})
					}
				}
			} else if log != nil {
				log.Warn("learning planner: model", "error", perr.Error())
			}
		}
		if len(items) == 0 {
			// Deterministic ladder: applicable lessons by priority (§9.1 is an
			// example, not a fixed program — but a clean fallback beats nothing).
			type lessonRef struct {
				lesson *LearningLesson
				cap    *LearningCapability
			}
			var refs []lessonRef
			for i := range learningLessons {
				lesson := &learningLessons[i]
				if !audienceMatches(lesson.Audience, principalRole(r)) {
					continue
				}
				refs = append(refs, lessonRef{lesson: lesson})
			}
			sort.SliceStable(refs, func(i, j int) bool { return refs[i].lesson.Priority < refs[j].lesson.Priority })
			for _, ref := range refs {
				items = append(items, planItem{LessonID: ref.lesson.ID})
			}
		}

		progItems := make([]workspace.LearningProgramItem, 0, len(items))
		for _, item := range items {
			progItems = append(progItems, workspace.LearningProgramItem{LessonID: item.LessonID, State: "pending", Why: item.Why})
		}
		program := &workspace.LearningProgram{
			ID:     "lp-" + shortID(),
			UserID: userID,
			Kind:   "initial_onboarding",
			Goal:   "Освоить Temporality на реальной конфигурации организации",
			Status: "active",
			Items:  progItems,
		}
		if err := ws.UpsertLearningProgram(r.Context(), program); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), userID, "learning.program_built", "user", userID, map[string]any{"lessons": len(progItems)})
		writeJSON(w, 200, program)
	})

	// GET /v1/learning/lessons — the shipped lesson catalog (§10.1: part of
	// the feature delivery) plus the user's supervisor-generated lessons and
	// the teachable-page map the nudge offers generation on.
	mux.HandleFunc("GET /v1/learning/lessons", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		userID := requestUserID(r)
		lessons := make([]any, 0, len(learningLessons)+4)
		for i := range learningLessons {
			lessons = append(lessons, learningLessons[i])
		}
		if generated, err := ws.ListGeneratedLessons(r.Context(), userID); err == nil {
			for _, g := range generated {
				var l LearningLesson
				if json.Unmarshal(g.Lesson, &l) == nil {
					l.ID = g.ID // the column is canonical, not the JSON copy
					lessons = append(lessons, l)
				}
			}
		}
		whatsNew := map[string]any{"available": false}
		if rel := releaseFor(buildVersion); rel != nil {
			whatsNew = map[string]any{"available": true, "version": rel.Version, "changes": len(rel.Changes)}
		}
		writeJSON(w, 200, map[string]any{
			"lessons":   lessons,
			"pages":     teachableRoutes(),
			"whats_new": whatsNew,
		})
	})

	// POST /v1/learning/sessions/start {lesson_id} — opens a lesson run
	// (§8.1). The session lives server-side, so it survives restarts (§16.4).
	mux.HandleFunc("POST /v1/learning/sessions/start", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		var input struct {
			LessonID string `json:"lesson_id"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, err)
			return
		}
		userID := requestUserID(r)
		lesson, rerr := resolveLearningLesson(r.Context(), ws, userID, strings.TrimSpace(input.LessonID))
		if rerr != nil {
			writeError(w, 500, rerr)
			return
		}
		if lesson == nil {
			writeError(w, 404, fmt.Errorf("lesson not found"))
			return
		}
		// One active session per user: a previous unfinished session pauses.
		if prev, err := ws.GetCurrentLearningSession(r.Context(), userID); err == nil && prev != nil {
			_ = ws.SetLearningSessionStatus(r.Context(), prev.ID, "paused")
		}
		session := &workspace.LearningSession{
			ID: "ls-" + shortID(), UserID: userID, LessonID: lesson.ID,
			Status: "active", StepIndex: 0,
		}
		if err := ws.StartLearningSession(r.Context(), session); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), userID, "learning.lesson_started", "learning_lesson", lesson.ID, nil)
		writeJSON(w, 201, session)
	})

	// POST /v1/learning/suggestions/set {feature_id, status} — the user's
	// verdict on a proactive suggestion (§9.2: dismissal is a first-class
	// outcome, not a failure; accepted is recorded when a lesson starts from
	// the suggestion card).
	mux.HandleFunc("POST /v1/learning/suggestions/set", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		var input struct {
			FeatureID string `json:"feature_id"`
			Status    string `json:"status"` // accepted | dismissed
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, err)
			return
		}
		input.FeatureID = strings.TrimSpace(input.FeatureID)
		if input.FeatureID == "" || (input.Status != "accepted" && input.Status != "dismissed") {
			writeError(w, 422, fmt.Errorf("feature_id and status (accepted|dismissed) are required"))
			return
		}
		userID := requestUserID(r)
		if err := ws.UpsertLearningSuggestion(r.Context(), workspace.LearningSuggestion{UserID: userID, FeatureID: input.FeatureID, Status: input.Status}); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), userID, "learning.suggestion_"+input.Status, "learning_capability", input.FeatureID, nil)
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// GET /v1/learning/sessions/current — the resume path (§16.4): the user's
	// active/paused session with the lesson definition attached.
	mux.HandleFunc("GET /v1/learning/sessions/current", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		session, err := ws.GetCurrentLearningSession(r.Context(), requestUserID(r))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if session == nil {
			writeJSON(w, 200, map[string]any{"session": nil})
			return
		}
		lesson, rerr := resolveLearningLesson(r.Context(), ws, session.UserID, session.LessonID)
		if rerr != nil {
			writeError(w, 500, rerr)
			return
		}
		writeJSON(w, 200, map[string]any{"session": session, "lesson": lesson})
	})

	// POST /v1/learning/sessions/{id}/resume — a paused session returns to
	// active (§18 scenario E: interruption is not loss; the lesson continues
	// where it stopped).
	mux.HandleFunc("POST /v1/learning/sessions/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		session, err := ws.GetLearningSession(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if session == nil || session.UserID != requestUserID(r) {
			writeError(w, 404, fmt.Errorf("session not found"))
			return
		}
		if session.Status != "paused" && session.Status != "waiting_for_user" {
			writeError(w, 409, fmt.Errorf("session is %s, only paused sessions resume", session.Status))
			return
		}
		if err := ws.SetLearningSessionStatus(r.Context(), session.ID, "active"); err != nil {
			writeError(w, 500, err)
			return
		}
		session.Status = "active"
		lesson, rerr := resolveLearningLesson(r.Context(), ws, session.UserID, session.LessonID)
		if rerr != nil {
			writeError(w, 500, rerr)
			return
		}
		writeJSON(w, 200, map[string]any{"session": session, "lesson": lesson})
	})

	// POST /v1/learning/sessions/{id}/step — advance to the next step (the
	// UI executed the current one and reports optional evidence). Wait steps
	// flip the session to waiting_for_user until the user acts.
	mux.HandleFunc("POST /v1/learning/sessions/{id}/step", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		session, err := ws.GetLearningSession(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if session == nil || session.UserID != requestUserID(r) {
			writeError(w, 404, fmt.Errorf("session not found"))
			return
		}
		lesson, rerr := resolveLearningLesson(r.Context(), ws, session.UserID, session.LessonID)
		if rerr != nil {
			writeError(w, 500, rerr)
			return
		}
		if lesson == nil {
			writeError(w, 404, fmt.Errorf("lesson %q is not available", session.LessonID))
			return
		}
		var input struct {
			Action   string `json:"action"` // next | done | skip | pause | abort
			Evidence string `json:"evidence,omitempty"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, err)
			return
		}
		// Terminal sessions ignore further steps: a completed or aborted
		// lesson must not keep counting (a live session ran its StepIndex to
		// 10 on a 4-step lesson when the client re-sent clicks).
		if session.Status == "completed" || session.Status == "aborted" {
			writeJSON(w, 200, map[string]any{"session": session, "lesson": lesson})
			return
		}
		// §7.7/§4.5: a verification step's «Готово» is checked against the
		// FACTUAL configuration, not against the user's word. The check now
		// GATES the advance: when the kernel cannot see what the lesson
		// teaches, the step stays put and the response carries why — the
		// learner gets feedback and a retry instead of a silent completion
		// with mastery that never happened.
		var verification map[string]any
		if input.Action == "done" && session.StepIndex < len(lesson.Steps) &&
			lesson.Steps[session.StepIndex].Type == StepVerification {
			if capability := findLearningCapability(lesson.VerifyFeature); capability != nil && capability.Mastered != nil {
				appCtx := learningAppContext(r.Context(), ws, "", visibleUnits(r))
				mstatus, detail := capability.Mastered(appCtx)
				if mstatus != "practiced" && mstatus != "verified" {
					writeJSON(w, 200, map[string]any{
						"session": session, "lesson": lesson,
						"verification": map[string]any{"ok": false, "detail": detail},
					})
					return
				}
				recordLearningEvidence(r.Context(), ws, &workspace.LearningProgress{
					UserID: session.UserID, FeatureID: capability.ID, LessonID: lesson.ID,
					Mastery: mstatus, Source: "verification", Detail: detail,
				})
				verification = map[string]any{"ok": true, "detail": detail}
			}
		}
		next := session.StepIndex + 1
		status := ""
		switch input.Action {
		case "done", "next":
			next = session.StepIndex + 1
		case "skip":
			next = session.StepIndex + 1
		case "pause":
			status = "paused"
		case "abort":
			status = "aborted"
		default:
			writeError(w, 422, fmt.Errorf("action must be next|done|skip|pause|abort"))
			return
		}
		if status == "" && next >= len(lesson.Steps) {
			// The lesson ran out of steps: completion (§8.4). Mastery is NOT
			// implied — completion and mastery are separate facts (§4.5).
			status = "completed"
			recordLearningEvidence(r.Context(), ws, &workspace.LearningProgress{
				UserID: session.UserID, FeatureID: lesson.FeatureID, LessonID: lesson.ID,
				Completion: true, Mastery: "introduced", Source: "lesson",
			})
			if program, err := ws.GetLearningProgram(r.Context(), session.UserID); err == nil && program != nil {
				_ = ws.SetLearningProgramItemState(r.Context(), program.ID, lesson.ID, "completed")
			}
			_ = ws.RecordAccessAudit(r.Context(), session.UserID, "learning.lesson_completed", "learning_lesson", lesson.ID, nil)
		}
		historyEntry, _ := json.Marshal(map[string]any{"at": time.Now().UTC(), "action": input.Action, "step": session.StepIndex, "evidence": input.Evidence})
		if err := ws.UpdateLearningSession(r.Context(), session.ID, fmt.Sprint(next), status, historyEntry); err != nil {
			writeError(w, 500, err)
			return
		}
		session.StepIndex = next
		if status != "" {
			session.Status = status
		}
		payload := map[string]any{"session": session, "lesson": lesson}
		if verification != nil {
			payload["verification"] = verification
		}
		writeJSON(w, 200, payload)
	})
}

// learningOrgContext renders the current configuration into a few factual
// lines the supervisor can ground its explanation in.
func learningOrgContext(ctx context.Context, ws *workspace.Store, project string, units []string) string {
	var b strings.Builder
	if project != "" {
		if agents, err := ws.ListAgentsByProject(ctx, project); err == nil {
			fmt.Fprintf(&b, "agents configured: %d", len(agents))
			for i, a := range agents {
				if i == 5 {
					break
				}
				fmt.Fprintf(&b, "; %s (model %s)", a.Name, a.Model)
			}
			b.WriteString("\n")
		}
		if tasks, err := ws.ListTasks(ctx, project); err == nil {
			fmt.Fprintf(&b, "tasks in project %q: %d\n", project, len(tasks))
		}
	}
	if projects, err := ws.ListProjects(ctx); err == nil {
		fmt.Fprintf(&b, "projects: %d\n", len(projects))
	}
	if b.Len() == 0 {
		return "the organization configuration is not readable right now"
	}
	return strings.TrimSpace(b.String())
}

func actorIDOr(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

// principalRole renders the caller's role for the planner prompt (§13 — the
// program follows the user's actual powers).
func principalRole(r *http.Request) string {
	if principal, ok := controlplane.FromContext(r.Context()); ok && principal.Role.String() != "" {
		return principal.Role.String()
	}
	return "operator"
}

// learningMasteryRank orders the mastery ladder (§4.5). needs_refresh is a
// flag, not a higher rung — it ranks below verified.
func learningMasteryRank(m string) int {
	switch m {
	case "verified":
		return 3
	case "practiced":
		return 2
	case "introduced":
		return 1
	default:
		return 0
	}
}

// recordLearningEvidence stores one evidence fact without ever DOWNGRADING
// an earned mastery level (§4.5): a lesson completion (introduced) arriving
// after a factual verification (practiced/verified) keeps the higher rung.
func recordLearningEvidence(ctx context.Context, ws *workspace.Store, p *workspace.LearningProgress) {
	if prev, err := ws.ListLearningProgress(ctx, p.UserID); err == nil {
		for _, old := range prev {
			if old.FeatureID != p.FeatureID || learningMasteryRank(old.Mastery) <= learningMasteryRank(p.Mastery) {
				continue
			}
			p.Mastery, p.Source, p.Detail = old.Mastery, old.Source, old.Detail
			break
		}
	}
	_ = ws.UpsertLearningProgress(ctx, p)
}
