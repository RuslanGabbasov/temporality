package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// Knowledge-area normalization (docs/human-knowledge-explorer.md §4.2): the
// judge model sweeps a project's raw topic/entity values and decides, per
// value, the canonical area it belongs to — or that it is not an area at all
// (file names, run ids, one-off noise). The resulting alias map is a
// presentation lens: the journal applies it when aggregating the area map and
// facets; knowledge records stay untouched.
//
// The judge is deliberately the same model the quality loop uses — one
// installation-wide arbiter of model judgment (docs/agent-quality-loop.md
// §6.2). When the judge is off the sweep refuses with guidance instead of
// silently falling back to some other model.

// RegisterKnowledgeAreaRoutes mounts the area normalization endpoint.
func RegisterKnowledgeAreaRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, masterKey []byte, observationURL, apiToken string, log interface{ Warn(string, ...any) }) {
	mux.HandleFunc("POST /v1/workspace/knowledge-areas/normalize", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Project string `json:"project"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		project := strings.TrimSpace(req.Project)
		if project == "" {
			writeError(w, 422, fmt.Errorf("project is required"))
			return
		}
		ctx := r.Context()
		client, err := judgeModelClient(ctx, ws, masterKey)
		if err != nil {
			writeError(w, 422, err)
			return
		}

		// Raw area values with their sizes, straight from the journal's
		// knowledge projection (topics and entities share one namespace — the
		// area aggregation merges them by name anyway).
		valueSeen := map[string]int{}
		if err := collectKnowledgeAreaValues(ctx, observationURL, apiToken, project, valueSeen); err != nil {
			writeError(w, 502, err)
			return
		}
		if len(valueSeen) == 0 {
			writeJSON(w, 200, map[string]any{"areas": 0, "merged": 0, "hidden": 0})
			return
		}

		result, err := agent.NormalizeKnowledgeAreas(ctx, client, valueSeen)
		if err != nil {
			writeError(w, 502, err)
			return
		}

		// Keep existing decisions for values the sweep did not see; stale
		// entries age out only via an explicit re-run replacing them here.
		existing, err := ws.AreaAliases(ctx, project)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		aliases := make(map[string]workspace.KnowledgeAreaAlias, len(result.Aliases))
		for value, alias := range result.Aliases {
			aliases[value] = workspace.KnowledgeAreaAlias{Canonical: alias.Canonical, Hidden: alias.Hidden}
		}
		for value, alias := range existing {
			if _, fresh := aliases[value]; !fresh {
				aliases[value] = alias
			}
		}
		if err := ws.SetAreaAliases(ctx, project, aliases); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(ctx, requestUserID(r), workspace.AuditKnowledgeAreasNormalized, "project", project, map[string]any{"aliases": len(aliases), "merged": result.Merged, "hidden": result.Hidden})
		log.Warn("knowledge areas normalized", "project", project, "aliases", len(aliases), "merged", result.Merged, "hidden", result.Hidden)
		writeJSON(w, 200, map[string]any{"areas": len(aliases), "merged": result.Merged, "hidden": result.Hidden})
	})
}

// judgeModelClient builds an llm client for the installation's judge model —
// the same resolution the quality loop uses (UI setting first, env fallback).
func judgeModelClient(ctx context.Context, ws *workspace.Store, masterKey []byte) (*llm.Client, error) {
	config, err := ws.JudgeConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve judge config: %w", err)
	}
	if !config.Configured {
		return nil, fmt.Errorf("judge model is not configured — pick one in Agents → Quality settings (the same judge the quality loop uses)")
	}
	providerID := config.ProviderID
	if providerID == "" {
		id, ok, pErr := ws.ProviderOfModel(ctx, config.Model)
		if pErr != nil {
			return nil, pErr
		}
		if !ok {
			return nil, fmt.Errorf("judge model %q is not listed by any configured provider", config.Model)
		}
		providerID = id
	}
	provider, err := ws.GetProvider(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("judge provider %q: %w", providerID, err)
	}
	apiKey, err := resolveProviderAPIKey(ctx, ws, provider.APIKeyRef, masterKey)
	if err != nil {
		return nil, fmt.Errorf("judge provider %q credential: %w", providerID, err)
	}
	optional, err := llm.OptionalConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("judge model config: %w", err)
	}
	optional.BaseURL = provider.BaseURL
	optional.Model = config.Model
	optional.APIKey = apiKey
	return llm.New(optional), nil
}

// collectKnowledgeAreaValues loads the project's knowledge projection from
// the journal and counts distinct topic/entity values. Retired states are
// skipped: the map must reflect live knowledge, not its history.
func collectKnowledgeAreaValues(ctx context.Context, observationURL, apiToken, project string, into map[string]int) error {
	body, err := journalGet(ctx, observationURL, apiToken, "/v1/observations/knowledge?project="+url.QueryEscape(project))
	if err != nil {
		return fmt.Errorf("load project knowledge: %w", err)
	}
	var projection struct {
		Knowledge []struct {
			State    string   `json:"state"`
			Topics   []string `json:"topics"`
			Entities []string `json:"entities"`
		} `json:"knowledge"`
	}
	if err := json.Unmarshal(body, &projection); err != nil {
		return fmt.Errorf("decode project knowledge: %w", err)
	}
	for _, item := range projection.Knowledge {
		if item.State == "invalidated" || item.State == "superseded" {
			continue
		}
		for _, value := range append(append([]string{}, item.Topics...), item.Entities...) {
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "" {
				into[value]++
			}
		}
	}
	return nil
}

func journalGet(ctx context.Context, observationURL, apiToken, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, observationURL+path, nil)
	if err != nil {
		return nil, err
	}
	if apiToken != "" {
		request.Header.Set("Authorization", "Bearer "+apiToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("journal %s: status %d", path, response.StatusCode)
	}
	// The projection without a limit is bounded by the project's knowledge
	// count; cap the read anyway so a pathological journal cannot OOM us.
	return io.ReadAll(io.LimitReader(response.Body, 32<<20))
}
