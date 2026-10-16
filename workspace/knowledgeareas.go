package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Knowledge-area aliases (docs/human-knowledge-explorer.md §4.2): the judge
// sweeps the raw topic/entity values of a project and decides, per value,
// which canonical area it belongs to — or that it is not an area at all
// (file names, one-off noise). The map is a presentation-layer lens: the
// journal applies it when aggregating areas and facets; knowledge records
// themselves stay untouched.
//
// Storage reuses the workspace_setting KV (one JSON row per project) — the
// map is bounded by the number of distinct raw values and needs no queries
// inside it, so a dedicated table would buy nothing.

// KnowledgeAreaAlias is one raw value → canonical area decision.
type KnowledgeAreaAlias struct {
	// Canonical is the display name the raw value collapses into. Empty with
	// Hidden=false means "the value is its own area" (keep as-is).
	Canonical string `json:"canonical,omitempty"`
	// Hidden marks values that must not surface as areas at all (files,
	// run ids, noise).
	Hidden bool `json:"hidden,omitempty"`
}

// areaAliasSettingKey is the KV key holding a project's alias map.
func areaAliasSettingKey(project string) string {
	return "knowledge.area.aliases." + project
}

// AreaAliases reads a project's alias map; a missing row reads as "no
// normalization yet" (empty map, no error).
func (s *Store) AreaAliases(ctx context.Context, project string) (map[string]KnowledgeAreaAlias, error) {
	raw, err := s.GetSetting(ctx, areaAliasSettingKey(project))
	if err != nil {
		if err == ErrNotFound {
			return map[string]KnowledgeAreaAlias{}, nil
		}
		return nil, err
	}
	var aliases map[string]KnowledgeAreaAlias
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return nil, fmt.Errorf("decode area aliases for %q: %w", project, err)
	}
	return aliases, nil
}

// SetAreaAliases replaces a project's alias map.
func (s *Store) SetAreaAliases(ctx context.Context, project string, aliases map[string]KnowledgeAreaAlias) error {
	if strings.TrimSpace(project) == "" {
		return fmt.Errorf("project is required")
	}
	encoded, err := json.Marshal(aliases)
	if err != nil {
		return fmt.Errorf("encode area aliases: %w", err)
	}
	return s.SetSetting(ctx, areaAliasSettingKey(project), string(encoded))
}

// SortAliasKeys is a small helper for deterministic prompt/JSON output.
func SortAliasKeys(aliases map[string]KnowledgeAreaAlias) []string {
	keys := make([]string, 0, len(aliases))
	for key := range aliases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
