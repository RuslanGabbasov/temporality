package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

// fakeTransferStore is a second stand: an in-memory workspace with the same
// ErrNotFound contract, enough to exercise the transfer decisions.
type fakeTransferStore struct {
	skills        map[string]workspace.Skill
	agents        map[string]workspace.Agent
	versions      map[string][]workspace.SkillVersion
	createdSkill  []string
	updatedAgent  []string
}

func newFakeTransferStore() *fakeTransferStore {
	return &fakeTransferStore{
		skills:   map[string]workspace.Skill{},
		agents:   map[string]workspace.Agent{},
		versions: map[string][]workspace.SkillVersion{},
	}
}

func (f *fakeTransferStore) GetSkill(_ context.Context, id string) (workspace.Skill, error) {
	if sk, ok := f.skills[id]; ok {
		return sk, nil
	}
	return workspace.Skill{}, workspace.ErrNotFound
}

func (f *fakeTransferStore) CreateSkill(_ context.Context, sk *workspace.Skill) error {
	if _, ok := f.skills[sk.ID]; ok {
		return fmt.Errorf("duplicate skill %q", sk.ID)
	}
	sk.VersionStatus = "active"
	f.skills[sk.ID] = *sk
	return nil
}

func (f *fakeTransferStore) ProposeSkillVersion(_ context.Context, skillID, _, markdown string, manifest json.RawMessage, prov workspace.SkillProvenance) (workspace.SkillVersion, error) {
	if _, ok := f.skills[skillID]; !ok {
		return workspace.SkillVersion{}, workspace.ErrNotFound
	}
	version := fmt.Sprintf("0.%d.0", len(f.versions[skillID])+1)
	created := workspace.SkillVersion{SkillID: skillID, Version: version, Markdown: markdown, Manifest: manifest, SkillProvenance: prov, Status: "draft"}
	f.versions[skillID] = append(f.versions[skillID], created)
	return created, nil
}

func (f *fakeTransferStore) GetAgent(_ context.Context, id string) (workspace.Agent, error) {
	if a, ok := f.agents[id]; ok {
		return a, nil
	}
	return workspace.Agent{}, workspace.ErrNotFound
}

func (f *fakeTransferStore) CreateAgent(_ context.Context, a *workspace.Agent) error {
	if _, ok := f.agents[a.ID]; ok {
		return fmt.Errorf("duplicate agent %q", a.ID)
	}
	f.agents[a.ID] = *a
	return nil
}

func (f *fakeTransferStore) UpdateAgent(_ context.Context, a workspace.Agent) error {
	if _, ok := f.agents[a.ID]; !ok {
		return workspace.ErrNotFound
	}
	f.agents[a.ID] = a
	f.updatedAgent = append(f.updatedAgent, a.ID)
	return nil
}

func importSkill(t *testing.T, ws *fakeTransferStore, sk workspace.Skill) transferResultItem {
	t.Helper()
	item := transferItem{Type: "skill", Skill: &sk}
	report := transferReport{}
	transferSkill(context.Background(), ws, item, &report)
	if len(report.Errors) > 0 {
		t.Fatalf("unexpected import error: %+v", report.Errors[0])
	}
	for _, line := range report.Created {
		return line
	}
	for _, line := range report.Skipped {
		return line
	}
	for _, line := range report.Updated {
		return line
	}
	// version_added lands in Skipped only via add("version_added") → Skipped
	// bucket; return it explicitly from the fake's state instead.
	return transferResultItem{Outcome: "version_added"}
}

func TestTransferSkillRoundTrip(t *testing.T) {
	target := newFakeTransferStore()
	src := workspace.Skill{ID: "ci-fix", Name: "ci-fix", Description: "fix CI", Markdown: "# CI fix", Manifest: json.RawMessage(`{"name":"ci-fix"}`)}

	line := importSkill(t, target, src)
	if line.Outcome != "created" {
		t.Fatalf("first import: want created, got %q (%s)", line.Outcome, line.Detail)
	}
	stored := target.skills["ci-fix"]
	if stored.Markdown != src.Markdown || string(stored.Manifest) != string(src.Manifest) {
		t.Fatalf("imported skill drifted: %+v", stored)
	}

	// Identical content → skipped (idempotent re-import).
	line = importSkill(t, target, src)
	if line.Outcome != "skipped" {
		t.Fatalf("second import: want skipped, got %q (%s)", line.Outcome, line.Detail)
	}

	// Changed content → a draft version awaiting human apply; the active
	// revision stays untouched.
	changed := src
	changed.Markdown = "# CI fix v2"
	line = importSkill(t, target, changed)
	if line.Outcome != "version_added" {
		t.Fatalf("changed import: want version_added, got %q (%s)", line.Outcome, line.Detail)
	}
	if target.skills["ci-fix"].Markdown != src.Markdown {
		t.Fatalf("active revision was overwritten by import")
	}
	versions := target.versions["ci-fix"]
	if len(versions) != 1 || versions[0].Status != "draft" || versions[0].Origin != SkillOriginImport {
		t.Fatalf("expected one import draft, got %+v", versions)
	}
}

func TestTransferAgentPolicy(t *testing.T) {
	target := newFakeTransferStore()
	src := workspace.Agent{ID: "dev-1", Name: "Developer", Model: "m1", SystemPrompt: "do things"}

	report := transferReport{}
	transferAgent(context.Background(), target, transferItem{Type: "agent", Agent: &src}, false, &report)
	if len(report.Created) != 1 {
		t.Fatalf("absent agent: want created, got %+v %+v", report.Created, report.Skipped)
	}

	// Present, overwrite off → skipped even when content differs.
	changed := src
	changed.SystemPrompt = "do other things"
	report = transferReport{}
	transferAgent(context.Background(), target, transferItem{Type: "agent", Agent: &changed}, false, &report)
	if len(report.Skipped) != 1 || target.agents["dev-1"].SystemPrompt != "do things" {
		t.Fatalf("overwrite-off must keep the existing agent: %+v", report)
	}

	// Present, identical, overwrite on → skipped as no-op.
	report = transferReport{}
	transferAgent(context.Background(), target, transferItem{Type: "agent", Agent: &src}, true, &report)
	if len(report.Skipped) != 1 {
		t.Fatalf("identical overwrite must be a no-op: %+v", report)
	}

	// Present, changed, overwrite on → updated.
	report = transferReport{}
	transferAgent(context.Background(), target, transferItem{Type: "agent", Agent: &changed}, true, &report)
	if len(report.Updated) != 1 || target.agents["dev-1"].SystemPrompt != "do other things" {
		t.Fatalf("overwrite must update: %+v", report)
	}
}

// The envelope round-trips through JSON byte-identically on the behavioral
// payload — the portability contract between stands.
func TestTransferEnvelopeRoundTrip(t *testing.T) {
	envelope := transferEnvelope{
		Format: transferFormat,
		Kind:   "mixed",
		Items: []transferItem{
			{Type: "skill", Skill: &workspace.Skill{ID: "s1", Name: "s1", Manifest: json.RawMessage(`{"a":1}`)}, Versions: []workspace.SkillVersion{{SkillID: "s1", Version: "1.0.0"}}},
			{Type: "agent", Agent: &workspace.Agent{ID: "a1", Name: "a1", Secrets: []string{"github-token"}}, AgentVersions: []workspace.AgentVersion{{AgentID: "a1", Version: 1}}},
		},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var back transferEnvelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Format != transferFormat || len(back.Items) != 2 {
		t.Fatalf("envelope drifted: %+v", back)
	}
	if back.Items[0].Skill.Manifest == nil || string(back.Items[0].Skill.Manifest) != `{"a":1}` {
		t.Fatalf("skill manifest did not survive the round trip: %s", back.Items[0].Skill.Manifest)
	}
	if !strings.Contains(string(raw), "github-token") {
		t.Fatalf("agent secrets names must be part of the transfer")
	}
}
