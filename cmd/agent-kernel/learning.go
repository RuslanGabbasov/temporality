package main

// Contextual learning infrastructure (docs/ai-guided-onboarding-and-learning.md,
// stage P0): a catalog of semantically identified UI targets and one
// supervisor-backed endpoint that explains a target in the context of the
// CURRENT organization. The frontend renders the returned plan (navigate /
// highlight / explanation / example) through its spotlight overlay and
// reports nothing automatically — explanations only.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

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

		client, err := modelClient(r.Context())
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
		plan, err := agent.LearningExplainCase(r.Context(), client, agent.LearningExplainInput{
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
