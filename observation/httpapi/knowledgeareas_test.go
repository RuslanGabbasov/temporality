package httpapi

import (
	"net/url"
	"testing"

	"github.com/temporality-project/temporality/observation"
)

func TestApplyAreaAliases(t *testing.T) {
	items := []observation.Knowledge{
		{ID: "1", Topics: []string{"playwright e2e", "CI"}, Entities: []string{"Playwright"}},
		{ID: "2", Topics: []string{"e2e-sandbox-runbook.md", "CI"}},
		{ID: "3", Topics: nil},
	}
	aliases := map[string]AreaAlias{
		"playwright e2e":         {Canonical: "playwright"},
		"playwright":             {Canonical: "playwright"},
		"e2e-sandbox-runbook.md": {Hidden: true},
	}
	got := applyAreaAliases(items, aliases)

	// Merge + case variant collapse into one value; hidden value dropped;
	// empty lists stay empty; records without topics untouched.
	if got[0].Topics[0] != "playwright" || got[0].Topics[1] != "CI" {
		t.Errorf("topics = %v, want [playwright CI]", got[0].Topics)
	}
	if len(got[0].Entities) != 1 || got[0].Entities[0] != "playwright" {
		t.Errorf("entities after collapse = %v, want [playwright]", got[0].Entities)
	}
	if got[1].Topics[0] != "CI" || len(got[1].Topics) != 1 {
		t.Errorf("hidden alias must drop the value: %v", got[1].Topics)
	}
	if got[2].Topics != nil {
		t.Errorf("nil topics must stay nil: %v", got[2].Topics)
	}
}

func TestApplyAreaAliasesNoopWithoutMap(t *testing.T) {
	items := []observation.Knowledge{{ID: "1", Topics: []string{"CI"}}}
	if got := applyAreaAliases(items, nil); got[0].Topics[0] != "CI" {
		t.Errorf("empty alias map must keep raw values: %v", got[0].Topics)
	}
}

func TestKnowledgeAreaFilterMatchesTopicOrEntity(t *testing.T) {
	query, err := parseKnowledgeListQuery(url.Values{"area": {"playwright"}})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	items := []observation.Knowledge{
		{ID: "1", Topics: []string{"Playwright"}, Entities: nil},
		{ID: "2", Topics: nil, Entities: []string{"playwright"}},
		{ID: "3", Topics: []string{"ci"}, Entities: []string{"docker"}},
	}
	got := filterKnowledgeList(items, query)
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("area filter = %v, want items 1 and 2 (case-insensitive, topic or entity)", ids(got))
	}
	if all := filterKnowledgeList(items, knowledgeListQuery{}); len(all) != 3 {
		t.Fatalf("empty query must not filter: %v", ids(all))
	}
}

func ids(items []observation.Knowledge) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}
