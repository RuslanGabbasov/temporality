package main

// Supervisor-generated lessons (docs/ai-guided-onboarding-and-learning.md
// §10.2 extended): the catalog ships HIGHLIGHTS (what each screen is, what
// each release changed) instead of every lesson pre-written; the model turns
// them into a lesson for THIS user against their factual progress delta.
//
// The pipeline: highlights + delta → LearningGenerateCase (kernel/agent) →
// sanitize against the closed vocabularies → store per (user, kind, page) →
// the runner executes it like any shipped lesson.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// resolveLearningLesson loads a lesson for one user: the shipped static
// catalog first (stable ids), then this user's generated lessons. Returns
// (nil, nil) when nothing matches — callers map that to 404.
func resolveLearningLesson(ctx context.Context, ws *workspace.Store, userID, id string) (*LearningLesson, error) {
	if l := findLearningLesson(id); l != nil {
		return l, nil
	}
	g, err := ws.GetGeneratedLesson(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, nil
	}
	var l LearningLesson
	if err := json.Unmarshal(g.Lesson, &l); err != nil {
		return nil, err
	}
	l.ID = g.ID // the column is canonical, not the JSON copy
	return &l, nil
}

// learningDelta renders the user's factual progress for the generation case:
// mastery per capability plus the program's state. This is what keeps the
// lesson per-user — the model teaches the NEXT gap, never what is known.
func learningDelta(ctx context.Context, ws *workspace.Store, userID string) string {
	var b strings.Builder
	progress, err := ws.ListLearningProgress(ctx, userID)
	if err != nil || len(progress) == 0 {
		b.WriteString("no lessons taken yet — treat the user as a newcomer\n")
	} else {
		b.WriteString("progress delta (mastery is checked against configuration, not clicks):\n")
		for _, p := range progress {
			fmt.Fprintf(&b, "- %s: %s", p.FeatureID, p.Mastery)
			if p.Completion {
				b.WriteString(" (lesson taken)")
			}
			if p.Detail != "" {
				b.WriteString(" — " + p.Detail)
			}
			b.WriteString("\n")
		}
	}
	if program, err := ws.GetLearningProgram(ctx, userID); err == nil && program != nil {
		done, total := 0, len(program.Items)
		var next string
		for _, it := range program.Items {
			if it.State == "completed" || it.State == "skipped" || it.State == "not_applicable" {
				done++
			} else if next == "" && (it.State == "pending" || it.State == "available" || it.State == "in_progress") {
				next = it.LessonID + " (" + it.State + ")"
			}
		}
		fmt.Fprintf(&b, "program: %d of %d lessons done", done, total)
		if next != "" {
			b.WriteString("; next in program: " + next)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("program: not built yet\n")
	}
	return b.String()
}

// sanitizeGeneratedLesson validates the model's answer against the closed
// vocabularies and the runner's contract: known step types, whitelisted
// routes and targets, an existing capability as the feature, a summary at
// the end, and no verification gate that could never pass (§7.7 gates must
// be checkable — an observational capability never flips to practiced).
func sanitizeGeneratedLesson(g agent.GeneratedLesson, kind, page string) (*LearningLesson, error) {
	title := strings.TrimSpace(g.Title)
	if title == "" {
		return nil, fmt.Errorf("generated lesson has no title")
	}
	validTypes := map[string]bool{
		StepExplain: true, StepNavigate: true, StepHighlight: true, StepExample: true,
		StepUserAction: true, StepConfiguration: true, StepVerification: true, StepSummary: true,
	}
	routes := map[string]bool{}
	for _, r := range allLearningRoutes() {
		routes[r] = true
	}
	targets := map[string]bool{}
	for _, t := range allLearningTargets() {
		targets[t] = true
	}

	feature := strings.TrimSpace(g.FeatureID)
	if findLearningCapability(feature) == nil {
		feature = "orientation" // a real capability; never invent ids
	}

	steps := make([]LearningStep, 0, len(g.Steps))
	for _, s := range g.Steps {
		if !validTypes[s.Type] {
			continue // drop unknown types rather than fail the whole lesson
		}
		st := LearningStep{
			Type: s.Type, Title: strings.TrimSpace(s.Title), Text: s.Text,
			TargetID: strings.TrimSpace(s.TargetID), Route: strings.TrimSpace(s.Route),
			Example: s.Example, ActionHint: s.ActionHint,
		}
		if st.Title == "" {
			continue
		}
		if st.Route != "" && !routes[st.Route] {
			st.Route = "" // never navigate to an invented route
		}
		if st.TargetID != "" && !targets[st.TargetID] {
			st.TargetID = "" // the spotlight falls back to a centered card
		}
		steps = append(steps, st)
	}
	if len(steps) < 3 {
		return nil, fmt.Errorf("generated lesson has %d valid steps, need at least 3", len(steps))
	}
	if steps[len(steps)-1].Type != StepSummary {
		goal := strings.TrimSpace(g.Goal)
		if goal == "" {
			goal = "Вы разобрались с этой темой."
		}
		steps = append(steps, LearningStep{Type: StepSummary, Title: "Итог", Text: goal})
	}

	// The verification gate needs a capability whose Mastered check can
	// actually flip — otherwise the learner faces a wall only «skip» clears.
	verify := ""
	cap := findLearningCapability(feature)
	if cap != nil && !cap.Observational && cap.Mastered != nil {
		for i := range steps {
			if steps[i].Type == StepVerification {
				verify = feature
				break
			}
		}
	} else {
		for i := range steps {
			if steps[i].Type == StepVerification {
				steps[i].Type = StepUserAction // keep the beat, drop the gate
				if steps[i].ActionHint == "" {
					steps[i].ActionHint = "выполнить действие из шага"
				}
			}
		}
	}

	duration := g.DurationMin
	if duration < 3 || duration > 15 {
		duration = 6
	}
	return &LearningLesson{
		Version: 1, Title: title, Goal: strings.TrimSpace(g.Goal),
		Description: strings.TrimSpace(g.Description),
		Page:        page, FeatureID: feature, Steps: steps,
		VerifyFeature: verify, Priority: 0, DurationMin: duration,
	}, nil
}

// registerLessonGenerate mounts POST /v1/learning/lessons/generate — the
// one call the nudge and the learning center make to have the supervisor
// write a lesson for this user right now.
func registerLessonGenerate(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store,
	visibleUnits func(r *http.Request) []string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	mux.HandleFunc("POST /v1/learning/lessons/generate", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		var input struct {
			Kind  string `json:"kind"`  // page | whats_new (default page)
			Page  string `json:"page"`  // route for kind=page
			Force bool   `json:"force"` // regenerate an existing slot
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, err)
			return
		}
		kind := strings.TrimSpace(input.Kind)
		if kind == "" {
			kind = "page"
		}
		page := strings.TrimSpace(input.Page)
		if page != "" && !strings.HasPrefix(page, "/") {
			page = "/" + page
		}
		slotPage := page
		if kind == "whats_new" {
			slotPage = ""
		}
		if kind != "page" && kind != "whats_new" {
			writeError(w, 422, fmt.Errorf("kind must be page or whats_new"))
			return
		}
		if kind == "page" && pageHighlightFor(page) == nil {
			writeError(w, 422, fmt.Errorf("no learning highlight for %q — teachable pages are listed in GET /v1/learning/lessons", page))
			return
		}
		userID := requestUserID(r)

		// Freshness: the same slot at the same product version is served from
		// the store — generation costs a model call and content only changes
		// when the user or the product does (§9.2 no repeats without new
		// circumstances). force=true rewrites it explicitly.
		currentVersion := buildVersion
		if kind == "whats_new" {
			rel := releaseFor(buildVersion)
			if rel == nil {
				writeError(w, 422, fmt.Errorf("no release highlights to teach"))
				return
			}
			currentVersion = rel.Version
		}
		if !input.Force {
			if existing, err := ws.GetGeneratedLessonBySlot(r.Context(), userID, kind, slotPage); err == nil && existing != nil && existing.AppVersion == currentVersion {
				var l LearningLesson
				if json.Unmarshal(existing.Lesson, &l) == nil {
					l.ID = existing.ID
					writeJSON(w, 200, map[string]any{"lesson": l, "cached": true})
					return
				}
			}
		}

		// Assemble the model's material: screen/release highlights, the
		// factual organization digest and the user's progress delta.
		in := agent.LearningGenerateInput{
			Kind: kind, Role: principalRole(r),
			OrgContext: learningOrgContext(r.Context(), ws, "", visibleUnits(r)),
			Delta:      learningDelta(r.Context(), ws, userID),
			Routes:     allLearningRoutes(),
		}
		for _, cap := range learningCapabilities {
			in.Features = append(in.Features, cap.ID)
		}
		if kind == "whats_new" {
			rel := releaseFor(buildVersion)
			in.Version = rel.Version
			for _, c := range rel.Changes {
				in.Changes = append(in.Changes, c.Page+" | "+c.Title+" | "+c.Detail)
			}
		} else {
			h := pageHighlightFor(page)
			in.PageRoute, in.PageTitle, in.PageAbout, in.Targets = h.Route, h.Title, h.About, h.Targets
		}

		learnCtx := withSupervisorPurpose(r.Context(), "learning")
		client, err := modelClient(learnCtx)
		if err != nil {
			writeError(w, 502, fmt.Errorf("supervisor model unavailable: %w", err))
			return
		}
		gen, err := agent.LearningGenerateCase(learnCtx, client, in)
		if err != nil {
			writeError(w, 502, fmt.Errorf("lesson generation failed: %w", err))
			return
		}
		lesson, err := sanitizeGeneratedLesson(gen, kind, slotPage)
		if err != nil {
			writeError(w, 502, err)
			return
		}

		raw, err := json.Marshal(lesson)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		row := &workspace.GeneratedLesson{
			ID: "gen-" + shortID(), UserID: userID, Kind: kind, Page: slotPage,
			FeatureID: lesson.FeatureID, AppVersion: currentVersion,
			Title: lesson.Title, Lesson: raw,
		}
		if err := ws.UpsertGeneratedLesson(r.Context(), row); err != nil {
			writeError(w, 500, err)
			return
		}
		lesson.ID = row.ID // the store keeps the row id across regenerations
		_ = ws.RecordAccessAudit(r.Context(), userID, "learning.lesson_generated", "learning_lesson", row.ID,
			map[string]any{"kind": kind, "page": slotPage, "version": currentVersion})
		writeJSON(w, 200, map[string]any{"lesson": lesson, "cached": false})
	})
}
