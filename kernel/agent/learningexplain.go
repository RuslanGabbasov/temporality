package agent

// LearningExplainCase (docs/ai-guided-onboarding-and-learning.md, stage P0):
// the supervisor explains ONE UI target in the context of the CURRENT
// organization. The answer is a structured plan the frontend executes —
// explanation, optional example, optional navigation — never a configuration
// change (§14.2: learning never mutates settings by itself).

import (
	"context"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// LearningExplainInput carries the target catalog entry, the page the user is
// on, an optional free-form question and the factual organization context.
type LearningExplainInput struct {
	TargetID    string
	TargetTitle string
	TargetAbout string // catalog description; may be empty for unknown targets
	Page        string
	Question    string // the user's free-form question, empty for a plain explain
	OrgContext  string // factual lines: agents, projects, tasks — the current config
}

// LearningExplainPlan is the structured answer the UI executes.
type LearningExplainPlan struct {
	Explanation   string `json:"explanation"`
	Highlight     string `json:"highlight,omitempty"`      // one-line callout over the target
	Example       string `json:"example,omitempty"`        // a concrete example for THIS organization
	NavigateRoute string `json:"navigate_route,omitempty"` // in-app route to open first, when the user is elsewhere
	Mode          string `json:"mode,omitempty"`           // informational | guided | focused
}

const learningExplainSystemPrompt = `You are the Temporality supervisor acting as a mentor for a platform user.
The user asks about one element of the product interface. Explain it FOR THEIR
organization: use the factual configuration context, adapt the example to their
setup, and be concrete. You cannot change any configuration — you only explain.

Answer with one JSON object and nothing else:
{"explanation": "<3-6 sentences: what this element does and how to use it in THEIR organization>",
 "highlight": "<one-line callout shown over the element>",
 "example": "<optional: a concrete example value or configuration for their setup>",
 "navigate_route": "<optional: in-app route like /org/work to open first, empty otherwise>",
 "mode": "informational"}

Rules:
- The explanation must reference their actual configuration when the context provides it.
- The example must be applicable to their setup, not a generic placeholder.
- stay on topic: explain the requested element, do not pitch unrelated features.
- Write in the language of the user's question, or Russian when there is no question.`

// LearningExplainPrompt renders the user message for the explain case.
func LearningExplainPrompt(in LearningExplainInput) string {
	var b strings.Builder
	b.WriteString("UI ELEMENT: " + in.TargetID + " — " + in.TargetTitle + "\n")
	if in.TargetAbout != "" {
		b.WriteString("ABOUT: " + in.TargetAbout + "\n")
	} else {
		b.WriteString("ABOUT: (no curated material for this element — explain from the name and context)\n")
	}
	if in.Page != "" {
		b.WriteString("USER IS ON PAGE: " + in.Page + "\n")
	}
	if in.Question != "" {
		b.WriteString("USER QUESTION: " + in.Question + "\n")
	}
	b.WriteString("CURRENT ORGANIZATION CONFIGURATION:\n" + in.OrgContext + "\n")
	b.WriteString("\nExplain this element for their organization and answer their question if present.")
	return b.String()
}

// LearningExplainCase runs the supervisor mentor over one target explanation.
func LearningExplainCase(ctx context.Context, model *llm.Client, in LearningExplainInput) (LearningExplainPlan, error) {
	var plan LearningExplainPlan
	if model == nil {
		return plan, fmt.Errorf("model client is not configured")
	}
	return completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: learningExplainSystemPrompt},
		{Role: "user", Content: LearningExplainPrompt(in)},
	}, func(content string) (LearningExplainPlan, error) {
		if err := parseCaseReply(content, &plan); err != nil {
			return plan, err
		}
		if strings.TrimSpace(plan.Explanation) == "" {
			return plan, fmt.Errorf("model returned an empty explanation")
		}
		switch plan.Mode {
		case "informational", "guided", "focused":
		default:
			plan.Mode = "informational"
		}
		plan.NavigateRoute = strings.TrimSpace(plan.NavigateRoute)
		if plan.NavigateRoute != "" && !strings.HasPrefix(plan.NavigateRoute, "/") {
			plan.NavigateRoute = ""
		}
		return plan, nil
	})
}

// LearningPlanLesson is the catalog entry the planner sees: strings only, so
// the caller's catalog types never leak into this package.
type LearningPlanLesson struct {
	ID       string
	Goal     string
	Audience []string
	Feature  string
}

// LearningPlanInput: the factual configuration digest plus the lesson and
// capability catalogs — the planner picks WHAT to teach this user and WHY.
type LearningPlanInput struct {
	Role     string
	Context  string
	Lessons  []LearningPlanLesson
	Features []string
}

// LearningPlanItem is one planned lesson slot.
type LearningPlanItem struct {
	LessonID string `json:"lesson_id"`
	Why      string `json:"why,omitempty"`
}

// LearningPlanCase (stage P2): the supervisor assembles the personal program
// from the catalog — applicable lessons first, with a reason per lesson. The
// caller degrades to the deterministic ladder when the model is unavailable.
func LearningPlanCase(ctx context.Context, model *llm.Client, in LearningPlanInput) ([]LearningPlanItem, error) {
	if model == nil {
		return nil, fmt.Errorf("model client is not configured")
	}
	var lessonsDesc strings.Builder
	for _, lesson := range in.Lessons {
		fmt.Fprintf(&lessonsDesc, "- %s: %s (для ролей: %s; возможности: %s)\n",
			lesson.ID, lesson.Goal, strings.Join(lesson.Audience, ","), lesson.Feature)
	}
	var featuresDesc strings.Builder
	for _, feature := range in.Features {
		fmt.Fprintf(&featuresDesc, "- %s\n", feature)
	}
	prompt := fmt.Sprintf(`Собери программу обучения пользователя Temporality.

РОЛЬ ПОЛЬЗОВАТЕЛЯ: %s
КОНФИГУРАЦИЯ ОРГАНИЗАЦИИ:
%s

ДОСТУПНЫЕ УРОКИ:
%s

ВОЗМОЖНОСТИ ПЛАТФОРМЫ:
%s

Правила:
- включай только уроки, применимые к этой организации и роли;
- порядок: от базы к продвинутым;
- для каждого урока — короткое «почему» из фактов конфигурации;
- не выдумывай id уроков вне списка.

Ответ одним JSON-объектом:
{"items": [{"lesson_id": "...", "why": "..."}]}`, in.Role, in.Context, lessonsDesc.String(), featuresDesc.String())
	plan := struct {
		Items []LearningPlanItem `json:"items"`
	}{}
	if _, err := completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You assemble personal learning programs for Temporality users. You answer with JSON only."},
		{Role: "user", Content: prompt},
	}, func(content string) (struct {
		Items []LearningPlanItem `json:"items"`
	}, error) {
		if err := parseCaseReply(content, &plan); err != nil {
			return plan, err
		}
		if len(plan.Items) == 0 {
			return plan, fmt.Errorf("model returned an empty program")
		}
		return plan, nil
	}); err != nil {
		return nil, err
	}
	return plan.Items, nil
}
