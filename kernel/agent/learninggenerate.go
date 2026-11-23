package agent

// LearningGenerateCase (docs/ai-guided-onboarding-and-learning.md §10.2
// extended): the supervisor turns compact HIGHLIGHTS — what an interface is,
// what changed in the release, what THIS user already mastered — into one
// lesson definition the runner can execute. Unlike the pre-baked catalog the
// lesson is per-user: it skips what the user already knows, uses the release
// increment when there is one, and speaks about their organization.

import (
	"context"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// LearningGenerateInput is everything the model needs: the screen material,
// the user's factual progress delta and the whitelists that keep the answer
// executable (routes, targets, features are CLOSED vocabularies — the kernel
// validates against them after parsing).
type LearningGenerateInput struct {
	Kind string // "page" (teach a screen) | "whats_new" (release increment)

	// Screen material (page kind).
	PageRoute string
	PageTitle string
	PageAbout string
	Targets   []string // semantic ids highlightable on this page

	// Release material (whats_new kind).
	Version string
	Changes []string // "route | title | detail" lines

	Role       string
	OrgContext string // factual configuration digest
	Delta      string // what the user already knows: mastery + program state

	// Closed vocabularies the answer must stay inside.
	Routes   []string
	Features []string // capability ids (verify_feature must be one of these)
}

// GeneratedLessonStep mirrors the kernel's LearningStep JSON — kept local so
// the catalog types never leak into this package.
type GeneratedLessonStep struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	Text       string `json:"text,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	Route      string `json:"route,omitempty"`
	Example    string `json:"example,omitempty"`
	ActionHint string `json:"action_hint,omitempty"`
}

// GeneratedLesson is the model's answer: a full lesson definition the kernel
// validates, assigns an id to and stores for this user.
type GeneratedLesson struct {
	Title       string                `json:"title"`
	Goal        string                `json:"goal"`
	Description string                `json:"description,omitempty"`
	DurationMin int                   `json:"duration_min"`
	FeatureID   string                `json:"feature_id"`
	Steps       []GeneratedLessonStep `json:"steps"`
}

const learningGenerateSystemPrompt = `You are the Temporality supervisor writing ONE lesson for ONE user.

You receive: a screen's highlight (what the interface is and its targets), or
release changes (what is new); the user's role, their organization's factual
configuration and their PROGRESS DELTA (what they already mastered and what
their program covers).

Write a lesson that closes exactly ONE gap for THIS user:
- if their delta says a capability is already practiced/verified, do not teach it;
- anchor the lesson to their real configuration (name their agents/projects when the context provides them);
- for "whats_new" kind: teach only the changes they have not met yet, as
  «что нового» for someone who knew the old behaviour;
- 4 to 7 steps: open with explain, navigate to the page, highlight 1-2 real
  targets, optionally give an example or one user_action, end with summary;
- verification step ONLY when the lesson's feature is in the features list AND
  the user's delta shows it is not mastered yet — its text says what will be
  checked in the configuration;
- step types are CLOSED: explain | navigate | highlight | example | user_action | verification | summary;
- route/target_id may only come from the provided whitelists — never invent ids;
- feature_id must be one of the provided features;
- write in Russian (the product's UI language), concise and concrete, no marketing.

Answer with one JSON object and nothing else:
{"title": "<≤60 chars>",
 "goal": "<1-2 sentences: what the user will be able to do>",
 "description": "<1-2 sentences: what the lesson covers>",
 "duration_min": <4-10>,
 "feature_id": "<from features>",
 "steps": [{"type": "explain", "title": "<short>", "text": "<2-4 sentences>"}]}`

// LearningGeneratePrompt renders the user message from the structured input.
func LearningGeneratePrompt(in LearningGenerateInput) string {
	var b strings.Builder
	if in.Kind == "whats_new" {
		b.WriteString("LESSON KIND: what's new in " + in.Version + "\n")
		b.WriteString("CHANGES:\n")
		for _, c := range in.Changes {
			b.WriteString("- " + c + "\n")
		}
	} else {
		page := in.PageRoute
		if page == "" {
			page = "/dashboard"
		}
		b.WriteString("LESSON KIND: teach one screen\n")
		b.WriteString("PAGE: " + page + " — " + in.PageTitle + "\n")
		b.WriteString("ABOUT: " + in.PageAbout + "\n")
		if len(in.Targets) > 0 {
			b.WriteString("HIGHLIGHTABLE TARGETS (only these ids): " + strings.Join(in.Targets, ", ") + "\n")
		}
	}
	b.WriteString("\nUSER ROLE: " + in.Role + "\n")
	b.WriteString("ORGANIZATION CONFIGURATION:\n" + in.OrgContext + "\n")
	b.WriteString("\nUSER'S PROGRESS DELTA:\n" + in.Delta + "\n")
	b.WriteString("\nALLOWED ROUTES (navigate.route only from this list): " + strings.Join(in.Routes, ", ") + "\n")
	b.WriteString("ALLOWED FEATURES (feature_id only from this list): " + strings.Join(in.Features, ", ") + "\n")
	b.WriteString("\nWrite the lesson for this user and answer with the JSON object only.")
	return b.String()
}

// LearningGenerateCase runs the supervisor lesson writer.
func LearningGenerateCase(ctx context.Context, model *llm.Client, in LearningGenerateInput) (GeneratedLesson, error) {
	var lesson GeneratedLesson
	if model == nil {
		return lesson, fmt.Errorf("model client is not configured")
	}
	if len(in.Routes) == 0 || len(in.Features) == 0 {
		return lesson, fmt.Errorf("closed vocabularies are empty — refusing to generate")
	}
	return completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: learningGenerateSystemPrompt},
		{Role: "user", Content: LearningGeneratePrompt(in)},
	}, func(content string) (GeneratedLesson, error) {
		if err := parseCaseReply(content, &lesson); err != nil {
			return lesson, err
		}
		if strings.TrimSpace(lesson.Title) == "" || len(lesson.Steps) == 0 {
			return lesson, fmt.Errorf("model returned an empty lesson")
		}
		if len(lesson.Steps) > 8 {
			lesson.Steps = lesson.Steps[:8]
		}
		if lesson.DurationMin < 3 || lesson.DurationMin > 15 {
			lesson.DurationMin = 6
		}
		return lesson, nil
	})
}
