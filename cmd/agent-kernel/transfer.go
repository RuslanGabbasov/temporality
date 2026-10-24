package main

// Stand-to-stand transfer of skills and agents (docs/user/operations.md):
// one portable JSON envelope in the internal format, produced and consumed by
// the same schema — export on stand A, import on stand B.
//
// Import is deliberately conservative: it creates what is missing and never
// silently overwrites human-approved content.
//   - skill, absent            → created (active)
//   - skill, present           → the incoming content lands as a DRAFT version
//     (provenance origin "import") awaiting human apply — identical content
//     is skipped instead
//   - agent, absent            → created
//   - agent, present           → skipped unless mode=overwrite (agent config
//     is human-owned; overwrite is an explicit decision)
//
// Stand-specific references (org unit bindings, provider ids) are preserved
// verbatim: the operator resolves them on the target stand.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// transferEnvelope is the portable container (temporality.transfer/v1).
type transferEnvelope struct {
	Format     string         `json:"format"`
	Kind       string         `json:"kind"` // skills | agents | mixed
	ExportedAt time.Time      `json:"exported_at"`
	Items      []transferItem `json:"items"`
}

type transferItem struct {
	Type          string                   `json:"type"` // skill | agent
	Skill         *workspace.Skill         `json:"skill,omitempty"`
	Versions      []workspace.SkillVersion `json:"versions,omitempty"`
	Agent         *workspace.Agent         `json:"agent,omitempty"`
	AgentVersions []workspace.AgentVersion `json:"agent_versions,omitempty"`
}

const transferFormat = "temporality.transfer/v1"

// SkillOriginImport marks versions created by a transfer import: human-authored
// content that arrived from another stand, awaiting local review.
const SkillOriginImport = "import"

func writeTransfer(w http.ResponseWriter, kind string, items []transferItem) {
	writeJSON(w, 200, transferEnvelope{
		Format:     transferFormat,
		Kind:       kind,
		ExportedAt: time.Now().UTC(),
		Items:      items,
	})
}

// registerTransferRoutes mounts the export/import pair. Both are admin-only:
// an export is a full copy of behavioral definitions (prompts, tool grants,
// secret names) and an import rewrites agent behavior.
func registerTransferRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store) {
	mux.HandleFunc("GET /v1/workspace/export", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		kind := r.URL.Query().Get("type")
		if kind == "" {
			kind = "all"
		}
		ids := map[string]bool{}
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids[id] = true
			}
		}
		wanted := func(id string) bool { return len(ids) == 0 || ids[id] }

		var items []transferItem
		if kind == "skills" || kind == "all" {
			skills, err := ws.ListAllSkills(r.Context())
			if err != nil {
				writeError(w, 500, err)
				return
			}
			sort.Slice(skills, func(i, j int) bool { return skills[i].ID < skills[j].ID })
			for _, sk := range skills {
				if !wanted(sk.ID) {
					continue
				}
				versions, err := ws.ListSkillVersions(r.Context(), sk.ID)
				if err != nil {
					writeError(w, 500, err)
					return
				}
				items = append(items, transferItem{Type: "skill", Skill: &sk, Versions: versions})
			}
		}
		if kind == "agents" || kind == "all" {
			agents, err := ws.ListAgentsVisible(r.Context(), nil)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
			for _, ag := range agents {
				if !wanted(ag.ID) {
					continue
				}
				versions, err := ws.ListAgentVersions(r.Context(), ag.ID)
				if err != nil {
					writeError(w, 500, err)
					return
				}
				items = append(items, transferItem{Type: "agent", Agent: &ag, AgentVersions: versions})
			}
		}
		if items == nil {
			items = []transferItem{}
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=temporality-transfer-%s-%s.json", kind, time.Now().UTC().Format("20060102-150405")))
		writeTransfer(w, kind, items)
	})

	mux.HandleFunc("POST /v1/workspace/import", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		overwrite := r.URL.Query().Get("mode") == "overwrite"
		var envelope transferEnvelope
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&envelope); err != nil {
			writeError(w, 400, fmt.Errorf("invalid transfer file: %w", err))
			return
		}
		if envelope.Format != transferFormat {
			writeError(w, 422, fmt.Errorf("unsupported format %q — expected %q", envelope.Format, transferFormat))
			return
		}
		report := transferReport{}
		for _, item := range envelope.Items {
			switch item.Type {
			case "skill":
				transferSkill(r.Context(), ws, item, &report)
			case "agent":
				transferAgent(r.Context(), ws, item, overwrite, &report)
			default:
				report.Ignored = append(report.Ignored, fmt.Sprintf("unknown item type %q", item.Type))
			}
		}
		writeJSON(w, 200, report)
	})
}

// transferResultItem is the per-item outcome line of the import report.
type transferResultItem struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Outcome string `json:"outcome"` // created | skipped | version_added | updated | error
	Detail  string `json:"detail,omitempty"`
}

type transferReport struct {
	Created []transferResultItem `json:"created"`
	Updated []transferResultItem `json:"updated"`
	Skipped []transferResultItem `json:"skipped"`
	Errors  []transferResultItem `json:"errors"`
	Ignored []string             `json:"ignored,omitempty"`
}

func (r *transferReport) add(outcome string, item transferResultItem) {
	switch outcome {
	case "created":
		r.Created = append(r.Created, item)
	case "updated", "version_added":
		// Both mutate the target stand: an agent config rewrite or a draft
		// version awaiting human apply — the report surfaces them together.
		r.Updated = append(r.Updated, item)
	case "error":
		r.Errors = append(r.Errors, item)
	default:
		r.Skipped = append(r.Skipped, item)
	}
}

// transferStore is the slice of the workspace store the transfer endpoints
// need. An interface (not *workspace.Store) keeps the decision logic unit-
// testable against a fake stand.
type transferStore interface {
	GetSkill(ctx context.Context, id string) (workspace.Skill, error)
	CreateSkill(ctx context.Context, sk *workspace.Skill) error
	ProposeSkillVersion(ctx context.Context, skillID, version, markdown string, manifest json.RawMessage, prov workspace.SkillProvenance) (workspace.SkillVersion, error)
	GetAgent(ctx context.Context, id string) (workspace.Agent, error)
	CreateAgent(ctx context.Context, a *workspace.Agent) error
	UpdateAgent(ctx context.Context, a workspace.Agent) error
}

func transferSkill(ctx context.Context, ws transferStore, item transferItem, report *transferReport) {
	src := item.Skill
	if src == nil || src.ID == "" {
		report.add("error", transferResultItem{Outcome: "error", Detail: "skill item without a skill payload"})
		return
	}
	line := transferResultItem{ID: src.ID, Name: src.Name}
	existing, err := ws.GetSkill(ctx, src.ID)
	switch {
	case err == nil:
		// Present: identical content is a no-op; otherwise the incoming
		// content lands as a draft version a human applies (or rejects) —
		// import never overwrites the active revision by itself.
		if sameSkillContent(existing, *src) {
			line.Outcome = "skipped"
			line.Detail = "identical content already active"
			report.add("skipped", line)
			return
		}
		prov := workspace.SkillProvenance{Origin: SkillOriginImport, ChangeSummary: "imported from another stand"}
		if _, err := ws.ProposeSkillVersion(ctx, existing.ID, "", src.Markdown, src.Manifest, prov); err != nil {
			line.Outcome = "error"
			line.Detail = err.Error()
			report.add("error", line)
			return
		}
		line.Outcome = "version_added"
		line.Detail = "imported content awaits human apply (draft)"
		report.add("version_added", line)
	case errors.Is(err, workspace.ErrNotFound):
		created := *src
		created.UpdatedAt = time.Now().UTC()
		if err := ws.CreateSkill(ctx, &created); err != nil {
			line.Outcome = "error"
			line.Detail = err.Error()
			report.add("error", line)
			return
		}
		line.Outcome = "created"
		report.add("created", line)
	default:
		line.Outcome = "error"
		line.Detail = err.Error()
		report.add("error", line)
	}
}

func transferAgent(ctx context.Context, ws transferStore, item transferItem, overwrite bool, report *transferReport) {
	src := item.Agent
	if src == nil || src.ID == "" {
		report.add("error", transferResultItem{Outcome: "error", Detail: "agent item without an agent payload"})
		return
	}
	line := transferResultItem{ID: src.ID, Name: src.Name}
	existing, err := ws.GetAgent(ctx, src.ID)
	switch {
	case err == nil:
		if !overwrite {
			line.Outcome = "skipped"
			line.Detail = "agent exists; re-run with mode=overwrite to update"
			report.add("skipped", line)
			return
		}
		if sameAgentContent(existing, *src) {
			line.Outcome = "skipped"
			line.Detail = "identical configuration already present"
			report.add("skipped", line)
			return
		}
		updated := *src
		updated.CreatedAt = existing.CreatedAt
		if err := ws.UpdateAgent(ctx, updated); err != nil {
			line.Outcome = "error"
			line.Detail = err.Error()
			report.add("error", line)
			return
		}
		line.Outcome = "updated"
		report.add("updated", line)
	case errors.Is(err, workspace.ErrNotFound):
		created := *src
		created.CreatedAt = time.Now().UTC()
		created.UpdatedAt = created.CreatedAt
		if err := ws.CreateAgent(ctx, &created); err != nil {
			line.Outcome = "error"
			line.Detail = err.Error()
			report.add("error", line)
			return
		}
		line.Outcome = "created"
		report.add("created", line)
	default:
		line.Outcome = "error"
		line.Detail = err.Error()
		report.add("error", line)
	}
}

// sameSkillContent compares the behavioral payload (name, description,
// manifest and body) — timestamps and lifecycle counters do not count.
func sameSkillContent(a, b workspace.Skill) bool {
	return a.Name == b.Name && a.Description == b.Description &&
		a.Version == b.Version && a.Markdown == b.Markdown &&
		string(a.Manifest) == string(b.Manifest)
}

// sameAgentContent compares the configuration payload — audit timestamps do
// not count.
func sameAgentContent(a, b workspace.Agent) bool {
	return agentsEqual(a, b)
}

// agentsEqual compares every behavioral field of an agent definition.
func agentsEqual(a, b workspace.Agent) bool {
	return a.Name == b.Name && a.Description == b.Description &&
		a.ProjectID == b.ProjectID && a.Model == b.Model && a.Provider == b.Provider &&
		a.SystemPrompt == b.SystemPrompt && fmt.Sprint(a.Temperature) == fmt.Sprint(b.Temperature) &&
		fmt.Sprint(a.MaxTokens) == fmt.Sprint(b.MaxTokens) &&
		strings.Join(a.Skills, ",") == strings.Join(b.Skills, ",") &&
		strings.Join(a.MCPServers, ",") == strings.Join(b.MCPServers, ",") &&
		strings.Join(a.Tools, ",") == strings.Join(b.Tools, ",") &&
		strings.Join(a.Secrets, ",") == strings.Join(b.Secrets, ",") &&
		a.SandboxProfile == b.SandboxProfile &&
		fmt.Sprint(a.NetworkAccess) == fmt.Sprint(b.NetworkAccess) &&
		fmt.Sprint(a.ReadOnly) == fmt.Sprint(b.ReadOnly) &&
		fmt.Sprint(a.MaxTurns) == fmt.Sprint(b.MaxTurns) &&
		fmt.Sprint(a.ApprovalMode) == fmt.Sprint(b.ApprovalMode) &&
		a.OrgUnitID == b.OrgUnitID
}
