package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// Knowledge-area normalization (docs/human-knowledge-explorer.md §4.2): the
// judge model sweeps a project's raw topic/entity values and decides, per
// value, the canonical area it belongs to — or that it is not an area at all
// (file names like "e2e-sandbox-runbook.md", run ids, one-off noise). Pure
// model call + parsing; alias storage and the values collection live at the
// caller (cmd/agent-kernel).

// KnowledgeAreaAlias mirrors workspace.KnowledgeAreaAlias without importing
// the workspace package here (kernel/agent stays below it).
type KnowledgeAreaAlias struct {
	Canonical string `json:"canonical,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
}

// KnowledgeAreaNormalization is the judge's reply for one sweep.
type KnowledgeAreaNormalization struct {
	Aliases map[string]KnowledgeAreaAlias
	Merged  int
	Hidden  int
}

// NormalizeKnowledgeAreas runs the judge sweep over the raw area values
// (value → use count).
func NormalizeKnowledgeAreas(ctx context.Context, model *llm.Client, values map[string]int) (KnowledgeAreaNormalization, error) {
	if model == nil {
		return KnowledgeAreaNormalization{}, fmt.Errorf("model client is not configured")
	}
	if len(values) == 0 {
		return KnowledgeAreaNormalization{Aliases: map[string]KnowledgeAreaAlias{}}, nil
	}
	reply, err := completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You normalize knowledge-area taxonomies. You answer with JSON only."},
		{Role: "user", Content: knowledgeAreaPrompt(values)},
	}, parseKnowledgeAreaReply)
	if err != nil {
		return KnowledgeAreaNormalization{}, err
	}
	return validateKnowledgeAreaReply(reply, values), nil
}

// validateKnowledgeAreaReply post-validates the judge's decisions: values we
// never asked about are dropped (hallucinations), keep-as-is values produce
// no mapping entry, merged/hidden counts only cover applied decisions.
func validateKnowledgeAreaReply(reply knowledgeAreaReply, values map[string]int) KnowledgeAreaNormalization {
	aliases := make(map[string]KnowledgeAreaAlias, len(reply.Areas))
	merged, hidden := 0, 0
	for value, alias := range reply.Areas {
		value = strings.ToLower(strings.TrimSpace(value))
		if _, known := values[value]; !known {
			continue // hallucinated value — never apply it
		}
		clean := KnowledgeAreaAlias{Canonical: strings.ToLower(strings.TrimSpace(alias.Canonical)), Hidden: alias.Hidden}
		if clean.Hidden {
			hidden++
		} else if clean.Canonical == "" || clean.Canonical == value {
			continue // keep-as-is: no mapping entry needed
		} else {
			merged++
		}
		aliases[value] = clean
	}
	return KnowledgeAreaNormalization{Aliases: aliases, Merged: merged, Hidden: hidden}
}

// knowledgeAreaPrompt grounds the judge: the raw values with their sizes and
// the decision schema.
func knowledgeAreaPrompt(values map[string]int) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if values[keys[i]] != values[keys[j]] {
			return values[keys[i]] > values[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var b strings.Builder
	b.WriteString(`You are normalizing knowledge-area names. Below are raw area values extracted from knowledge records, with how many records carry each.

Decide for every value:
- "canonical": the clean area name it belongs to. Merge spelling/case/language variants into ONE area (e.g. "playwright", "playwright e2e" -> "playwright"). Broader grouping is welcome when the values are aspects of one theme. Reuse the raw value itself when it is already clean.
- or "hidden": true when the value is not an area at all — file names ("e2e-sandbox-runbook.md"), paths, run ids, single-use noise.

Canonical names: lowercase, short (1-4 words), no file extensions. Keep meaningful distinct areas separate — do not collapse everything into one bucket.

Values:
`)
	for _, key := range keys {
		fmt.Fprintf(&b, "- %s (%d)\n", key, values[key])
	}
	b.WriteString(`
Respond with one JSON object: {"areas": {"<raw value>": {"canonical": "<name>"} or {"hidden": true}}}.
Every raw value must appear exactly once in "areas".`)
	return b.String()
}

type knowledgeAreaReply struct {
	Areas map[string]KnowledgeAreaAlias `json:"areas"`
}

func parseKnowledgeAreaReply(content string) (knowledgeAreaReply, error) {
	var reply knowledgeAreaReply
	payload, ok := extractJSONObject(content)
	if !ok {
		return reply, fmt.Errorf("model returned no JSON object")
	}
	if err := json.Unmarshal([]byte(payload), &reply); err != nil {
		return reply, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	if len(reply.Areas) == 0 {
		return reply, fmt.Errorf("model returned no area decisions")
	}
	return reply, nil
}
