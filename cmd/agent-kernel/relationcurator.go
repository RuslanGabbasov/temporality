package main

// Relation curator (docs/knowledge-evolution.md): candidate knowledge
// relations are heuristic and cheap to produce — a human cannot triage
// thousands of shared-topic pairs, so unreviewed candidates would sit there
// forever. The curator resolves them the way trust actually forms:
//
//   - Usefulness: a pair repeatedly co-offered in hints whose outcomes came
//     back helpful IS related — the system already bet on it and the runs
//     agreed. Threshold confirm, no model.
//   - Judge: strong candidates without telemetry go through the supervisor
//     model once; related → explicit edge, unrelated → dismissed forever.
//
// Verdicts are journal events (knowledge.relation_confirmed / dismissed), so
// the explore engine's candidate pool reads them back on the next projection.
// Best-effort on every pass; a failure costs one tick, never knowledge.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

const (
	// relationCuratorInterval is deliberately slow: the verdicts are sticky,
	// so the sweep only needs to keep up with new knowledge, not race it.
	relationCuratorInterval = 30 * time.Minute
	// relationJudgeBudget caps model calls per pass — same philosophy as the
	// conflict predictor: a stalled or expensive judge must not dominate.
	relationJudgeBudget = 6
)

// relationCuratorDeps is the sweep's surface; *agent.Activities and the
// journal HTTP fetchers satisfy it, tests provide fakes.
type relationCuratorDeps struct {
	activities *agent.Activities
	log        *slog.Logger
	// fetchMeta loads every knowledge item's (id, project, proposition,
	// topics). The journal's knowledge listing, faked in tests.
	fetchMeta func(ctx context.Context) ([]relationKnowledgeMeta, error)
	// fetchTelemetry loads hint offer/outcome events (bounded, newest first).
	fetchTelemetry func(ctx context.Context) ([]hintEvent, error)
	// fetchVerdicts loads already-recorded curator verdicts as "a|b" pairs.
	fetchVerdicts func(ctx context.Context) (map[string]bool, error)
	// judge resolves one pair; nil or error = fail-open (skip, no verdict).
	judge func(ctx context.Context, a, b relationKnowledgeMeta) (related bool, explanation string, err error)
}

type relationKnowledgeMeta struct {
	ID          string
	Project     string
	Proposition string
	Topics      []string
	States      string // lifecycle state; retired items never enter pairing
}

type hintEvent struct {
	HintID      string
	KnowledgeID string
	Outcome     string // offered | used | ignored | helpful | harmful
}

func relationCuratorIntervalFromEnv() time.Duration {
	if v := strings.TrimSpace(os.Getenv("RELATION_CURATOR_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return relationCuratorInterval
}

func runRelationCurator(ctx context.Context, deps relationCuratorDeps) {
	interval := relationCuratorIntervalFromEnv()
	if interval <= 0 {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		curateRelations(ctx, deps)
		timer.Reset(interval)
	}
}

// pairKey is order-independent: a relation verdict covers the pair, not a
// direction.
func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// curateRelations is one pass: telemetry confirms, the judge resolves the
// rest, both write sticky verdicts. Production emitter = the activities.
func curateRelations(ctx context.Context, deps relationCuratorDeps) {
	relationCuratorSweep{deps: deps, emit: deps.activities}.curate(ctx)
}

// relationVerdictEmitter is the journal-write surface; *agent.Activities
// satisfies it, tests record calls.
type relationVerdictEmitter interface {
	EmitRelationVerdict(ctx context.Context, project, knowledgeID, targetID string, dismissed bool, method, explanation string, stats map[string]any) error
}

// relationCuratorSweep is one stateless pass over the base.
type relationCuratorSweep struct {
	deps relationCuratorDeps
	emit relationVerdictEmitter
}

func (s relationCuratorSweep) curate(ctx context.Context) {
	deps := s.deps
	meta, err := deps.fetchMeta(ctx)
	if err != nil {
		deps.log.Warn("relation curator: load knowledge", "error", err.Error())
		return
	}
	if len(meta) < 2 {
		return
	}
	byID := make(map[string]relationKnowledgeMeta, len(meta))
	live := make([]relationKnowledgeMeta, 0, len(meta))
	for _, item := range meta {
		byID[item.ID] = item
		// Retired statements carry no relation value: their edges follow
		// replacements, and pairing them re-suggests dead knowledge.
		if item.States != "invalidated" && item.States != "superseded" && item.States != "corrected" && item.States != "disproved" {
			live = append(live, item)
		}
	}
	verdicts, err := deps.fetchVerdicts(ctx)
	if err != nil {
		deps.log.Warn("relation curator: load verdicts", "error", err.Error())
		return
	}

	// Candidate pairs from shared topics — the same heuristic explore uses,
	// computed here over the full base rather than one expand window.
	type candidate struct {
		a, b relationKnowledgeMeta
	}
	topics := make(map[string][]relationKnowledgeMeta)
	for _, item := range live {
		for _, topic := range item.Topics {
			normalized := strings.ToLower(strings.TrimSpace(topic))
			if normalized != "" {
				topics[normalized] = append(topics[normalized], item)
			}
		}
	}
	seen := map[string]bool{}
	var candidates []candidate
	for _, group := range topics {
		for i := range group {
			for j := i + 1; j < len(group); j++ {
				key := pairKey(group[i].ID, group[j].ID)
				if seen[key] || verdicts[key] {
					continue
				}
				seen[key] = true
				candidates = append(candidates, candidate{a: group[i], b: group[j]})
			}
		}
	}
	if len(candidates) == 0 {
		return
	}

	// Usefulness pass: group hint telemetry by hint id (one hint offers
	// several items; a pair co-offered in many hints is the system repeatedly
	// betting on the relation), then let the outcomes grade the bet.
	offersByHint := make(map[string][]string)
	outcomeByHint := make(map[string]string)
	telemetry, err := deps.fetchTelemetry(ctx)
	if err != nil {
		deps.log.Warn("relation curator: load telemetry", "error", err.Error())
		telemetry = nil // judge-only pass
	}
	for _, ev := range telemetry {
		switch ev.Outcome {
		case "offered":
			offersByHint[ev.HintID] = append(offersByHint[ev.HintID], ev.KnowledgeID)
		case "helpful", "harmful":
			outcomeByHint[ev.HintID] = ev.Outcome
		}
	}
	pairOffers := make(map[string]int)
	pairHelpful := make(map[string]int)
	pairHarmful := make(map[string]int)
	for hintID, ids := range offersByHint {
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				key := pairKey(ids[i], ids[j])
				pairOffers[key]++
				switch outcomeByHint[hintID] {
				case "helpful":
					pairHelpful[key]++
				case "harmful":
					pairHarmful[key]++
				}
			}
		}
	}

	judged := 0
	for _, pair := range candidates {
		if ctx.Err() != nil {
			return
		}
		key := pairKey(pair.a.ID, pair.b.ID)
		offers, helpful, harmful := pairOffers[key], pairHelpful[key], pairHarmful[key]
		switch {
		case offers >= 3 && helpful >= 2 && harmful == 0:
			// The system bet on this relation at least three times and every
			// graded outcome agreed it paid off. That is a confirmed relation
			// by any reasonable standard — no essay required.
			if err := s.emit.EmitRelationVerdict(ctx, pair.a.Project, pair.a.ID, pair.b.ID, false, "hint_usage",
				"Пара вместе предлагалась в подсказках "+itoa(offers)+" раз(а), исходы полезными "+itoa(helpful)+" раз(а) без вредных — связь подтверждена использованием.", map[string]any{
					"hint_offers": offers, "helpful": helpful, "harmful": harmful,
				}); err != nil {
				deps.log.Warn("relation curator: confirm", "pair", key, "error", err.Error())
			} else {
				deps.log.Info("relation curator: confirmed by usage", "a", pair.a.ID, "b", pair.b.ID, "offers", offers, "helpful", helpful)
				verdicts[key] = true
			}
			continue
		case harmful >= 2 && helpful == 0:
			// Repeatedly harmful co-offers: the relation misleads.
			if err := s.emit.EmitRelationVerdict(ctx, pair.a.Project, pair.a.ID, pair.b.ID, true, "harmful_usage",
				"Совместные предложения пары дважды привели к вредным исходам без полезных — связь помечена нерелевантной.", map[string]any{
					"hint_offers": offers, "helpful": helpful, "harmful": harmful,
				}); err != nil {
				deps.log.Warn("relation curator: dismiss", "pair", key, "error", err.Error())
			} else {
				deps.log.Info("relation curator: dismissed by usage", "a", pair.a.ID, "b", pair.b.ID, "harmful", harmful)
				verdicts[key] = true
			}
			continue
		}
		// Judge pass for the strongest remaining candidates: those the
		// system already offered together at least once. Pure topic noise
		// (never co-offered) does not earn a model call.
		if offers == 0 || judged >= relationJudgeBudget {
			continue
		}
		if deps.judge == nil {
			continue
		}
		related, explanation, err := deps.judge(ctx, pair.a, pair.b)
		if err != nil {
			deps.log.Warn("relation curator: judge", "pair", key, "error", err.Error())
			continue // fail-open: the pair stays a candidate for the next pass
		}
		judged++
		project := pair.a.Project
		if project == "" {
			project = pair.b.Project
		}
		if err := s.emit.EmitRelationVerdict(ctx, project, pair.a.ID, pair.b.ID, !related, "judge", explanation, map[string]any{
			"hint_offers": offers,
		}); err != nil {
			deps.log.Warn("relation curator: judge verdict", "pair", key, "error", err.Error())
			continue
		}
		if related {
			deps.log.Info("relation curator: judge confirmed", "a", pair.a.ID, "b", pair.b.ID, "explanation", explanation)
		} else {
			deps.log.Info("relation curator: judge dismissed", "a", pair.a.ID, "b", pair.b.ID, "explanation", explanation)
		}
		verdicts[key] = true
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	if neg {
		return "-" + digits
	}
	return digits
}

// judgeAdapter wires the sweep's judge signature to the agent-level judge
// call (nil-safe: an unconfigured judge disables the judge pass entirely).
func relationJudgeAdapter(ctx context.Context, clientFn func(ctx context.Context) (*llm.Client, error)) func(ctx context.Context, a, b relationKnowledgeMeta) (bool, string, error) {
	return func(ctx context.Context, a, b relationKnowledgeMeta) (bool, string, error) {
		client, err := clientFn(ctx)
		if err != nil {
			return false, "", err
		}
		verdict, err := agent.RelationRelatedCase(ctx, client, a.Proposition, b.Proposition)
		if err != nil {
			return false, "", err
		}
		return verdict.Related, verdict.Explanation, nil
	}
}

// ── journal fetchers ────────────────────────────────────────────────────

// workspaceProjectLister is the project enumeration the meta fetcher needs;
// *workspace.Store satisfies it.
type workspaceProjectLister interface {
	ListProjects(ctx context.Context) ([]workspace.Project, error)
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

func jsonUnmarshal(body []byte, target any) error {
	return json.Unmarshal(body, target)
}

// relationMetaFetcher loads the knowledge projection for every project the
// installation has. The journal scopes per project, so this is one request
// per project — fine at install scale, bounded by the 32 MiB read guard.
func relationMetaFetcher(ctx context.Context, observationURL, journalToken string, ws workspaceProjectLister) func(ctx context.Context) ([]relationKnowledgeMeta, error) {
	return func(ctx context.Context) ([]relationKnowledgeMeta, error) {
		projects, err := ws.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		var out []relationKnowledgeMeta
		for _, project := range projects {
			body, err := journalGet(ctx, observationURL, journalToken, "/v1/observations/knowledge?project="+urlQueryEscape(project.ID))
			if err != nil {
				return nil, err
			}
			var page struct {
				Knowledge []struct {
					ID          string   `json:"id"`
					Proposition string   `json:"proposition"`
					Topics      []string `json:"topics"`
					State       string   `json:"state"`
					Project     string   `json:"project"`
				} `json:"knowledge"`
			}
			if err := jsonUnmarshal(body, &page); err != nil {
				return nil, err
			}
			for _, item := range page.Knowledge {
				if item.Proposition == "" {
					continue
				}
				out = append(out, relationKnowledgeMeta{
					ID: item.ID, Project: firstNonEmpty(item.Project, project.ID),
					Proposition: item.Proposition, Topics: item.Topics, States: item.State,
				})
			}
		}
		return out, nil
	}
}

// relationTelemetryFetcher loads hint offer and outcome events. Offer and
// outcome ride separate event types; both come back through the same typed
// event fetcher, normalized into one slice.
func relationTelemetryFetcher(observationURL, journalToken string) func(ctx context.Context) ([]hintEvent, error) {
	return func(ctx context.Context) ([]hintEvent, error) {
		var out []hintEvent
		for eventType, outcome := range map[string]string{
			"hint.offered": "offered",
			"hint.used":    "used",
			"hint.outcome": "", // the outcome itself rides data.outcome
		} {
			events, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, eventType, 500)
			if err != nil {
				return nil, err
			}
			for _, ev := range events {
				hintID, _ := ev.Data["hint_id"].(string)
				knowledgeID, _ := ev.Data["knowledge_id"].(string)
				if hintID == "" || knowledgeID == "" {
					continue
				}
				resolved := outcome
				if resolved == "" {
					if value, ok := ev.Data["outcome"].(string); ok {
						resolved = value
					}
				}
				out = append(out, hintEvent{HintID: hintID, KnowledgeID: knowledgeID, Outcome: resolved})
			}
		}
		return out, nil
	}
}

// relationVerdictFetcher loads already-recorded curator verdicts as order-
// independent pair keys, so a restart never re-judges a settled pair.
func relationVerdictFetcher(observationURL, journalToken string) func(ctx context.Context) (map[string]bool, error) {
	return func(ctx context.Context) (map[string]bool, error) {
		verdicts := make(map[string]bool)
		for _, eventType := range []string{"knowledge.relation_confirmed", "knowledge.relation_dismissed"} {
			events, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, eventType, 500)
			if err != nil {
				return nil, err
			}
			for _, ev := range events {
				a, _ := ev.Data["knowledge_id"].(string)
				b, _ := ev.Data["target_id"].(string)
				if a != "" && b != "" {
					verdicts[pairKey(a, b)] = true
				}
			}
		}
		return verdicts, nil
	}
}
