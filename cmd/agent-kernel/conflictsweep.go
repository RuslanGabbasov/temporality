package main

// Conflict auto-resolution sweep (docs/supervisor-model.md §3, C5): the
// conflict judge classifies close-in-embedding knowledge pairs into a closed
// vocabulary — only «conflict» (a genuine contradiction) deserves a human;
// the mechanical outcomes (generalize / outdated / keep_both) apply
// themselves. The sweep walks the OPEN knowledge_conflict cases and applies
// the payload's resolution: fresh payloads carry it from the predictor's
// judge call, the legacy backlog (payloads published before the vocabulary
// existed) is judged once here. A case judged «conflict» gets its payload
// enriched with resolution=conflict through a marker verdict — same case
// key, FULL original payload — so later passes skip it without a second
// judge call and the human keeps a working conflict card. Off-switch:
// KERNEL_CONFLICT_AUTORESOLVE=off.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

const (
	// A backlog must not burn the model budget or invalidate half the
	// knowledge base in one pass — same per-pass cap discipline as the
	// other supervisor cases.
	conflictSweepCap = 4
	// Reviewer/actor identity of every automatic resolution (the queue shows
	// decided_by; journal events carry it as the actor).
	conflictSweepActor = "supervisor"
)

func conflictAutoResolveEnabled() bool {
	return strings.TrimSpace(strings.ToLower(env("KERNEL_CONFLICT_AUTORESOLVE", "on"))) != "off"
}

func sweepKnowledgeConflicts(ctx context.Context, log *slog.Logger, activities *agent.Activities, ws supervisorCaseStore, observationURL, journalToken string, client *llm.Client) {
	if !conflictAutoResolveEnabled() || ws == nil {
		return
	}
	queue, err := ws.ListSupervisorQueue(ctx, workspace.SupervisorQueueFilter{
		Family: agent.CaseKnowledgeConflict,
		Status: workspace.SupervisorCaseOpen,
		Limit:  100,
	})
	if err != nil {
		log.Warn("conflict sweep: list", "error", err.Error())
		return
	}
	applied := 0
	for _, c := range queue.Cases {
		if applied >= conflictSweepCap {
			return
		}
		payload := map[string]any{}
		if len(c.Payload) > 0 {
			if err := json.Unmarshal(c.Payload, &payload); err != nil {
				payload = map[string]any{}
			}
		}
		resolution, _ := payload["resolution"].(string)
		if resolution == "" && client != nil {
			// Legacy backlog: no resolution in the payload — judge once here.
			newProp, _ := payload["new_proposition"].(string)
			oldProp, _ := payload["old_proposition"].(string)
			verdict, vErr := agent.ConflictPredictionCase(ctx, client, newProp, oldProp)
			if vErr != nil {
				log.Warn("conflict sweep: judge legacy case", "case", c.CaseKey, "error", vErr.Error())
				continue // stays visible to the human; the next pass retries
			}
			resolution = verdict.Resolution
			if resolution == agent.ConflictResolutionConflict {
				publishConflictReservedMarker(ctx, log, activities, c, payload)
				continue
			}
			payload["resolution"] = resolution
			if resolution == agent.ConflictResolutionGeneralize {
				if g := strings.TrimSpace(verdict.GeneralizedProposition); g != "" {
					payload["generalized_proposition"] = g
				}
			}
		}
		if resolution == "" || resolution == agent.ConflictResolutionConflict {
			continue // no judge available, or already reserved for the human
		}
		if err := applyConflictResolution(ctx, log, activities, ws, observationURL, journalToken, c, payload, resolution); err != nil {
			log.Warn("conflict sweep: apply", "case", c.CaseKey, "resolution", resolution, "error", err.Error())
			continue // the case stays open; the next pass retries
		}
		applied++
	}
}

// publishConflictReservedMarker journals a supervisor.verdict over the SAME
// case (subject included) whose payload is the FULL original payload plus
// resolution=conflict. The queue upsert overwrites payload wholesale on
// every folded verdict, so the marker must not be a reduced payload — that
// would erase the conflict card the human acts on. The resolution field is
// what later sweep passes read to skip the case without re-judging.
func publishConflictReservedMarker(ctx context.Context, log *slog.Logger, activities *agent.Activities, c workspace.SupervisorCase, payload map[string]any) {
	marker := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		marker[k] = v
	}
	marker["kind"] = "conflict_sweep"
	marker["resolution"] = agent.ConflictResolutionConflict
	if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseKnowledgeConflict, c.Subject, c.Project, c.Summary, c.Recommendation, marker); err != nil {
		log.Warn("conflict sweep: conflict marker", "case", c.CaseKey, "error", err.Error())
	}
}

// applyConflictResolution executes one mechanical resolution and closes the
// case. Journal actions go first, the decide last: a failed action leaves
// the case open for the next pass instead of a closed case with unfinished
// work. A concurrently-decided case (409) is success — the goal state.
func applyConflictResolution(ctx context.Context, log *slog.Logger, activities *agent.Activities, ws supervisorCaseStore, observationURL, journalToken string, c workspace.SupervisorCase, payload map[string]any, resolution string) error {
	newID, _ := payload["knowledge_id"].(string)
	oldID, _ := payload["neighbor_id"].(string)
	newProject, _ := payload["new_project"].(string)
	oldProject, _ := payload["old_project"].(string)
	// Present-but-empty payload strings fall back to the case project — the
	// same semantics the queue UI applies (empty strings would 422).
	if newProject == "" {
		newProject = c.Project
	}
	if oldProject == "" {
		oldProject = c.Project
	}
	decision := ""
	switch resolution {
	case agent.ConflictResolutionKeepBoth:
		decision = "auto_keep_both"
	case agent.ConflictResolutionOutdated:
		if oldID == "" || oldProject == "" {
			return fmt.Errorf("outdated needs the existing side: old=%q project=%q", oldID, oldProject)
		}
		reason := "устарело: судья подтвердил, что новая формулировка заменяет эту (автозачистка конфликтов)"
		if err := knowledgeInvalidate(ctx, observationURL, journalToken, oldID, oldProject, newID, reason); err != nil {
			// 404: the side is invisible/gone — the goal state is already
			// reached; 409: already retired — same. Anything else retries.
			if !strings.Contains(err.Error(), "status 404") && !strings.Contains(err.Error(), "status 409") {
				return err
			}
		}
		decision = "auto_invalidated_old"
	case agent.ConflictResolutionGeneralize:
		text, _ := payload["generalized_proposition"].(string)
		text = strings.TrimSpace(text)
		if text == "" {
			return errors.New("generalize needs generalized_proposition")
		}
		anchor := oldProject
		if anchor == "" {
			anchor = newProject
		}
		if anchor == "" {
			return errors.New("generalize needs a project anchor")
		}
		genID := "gen-" + conflictSweepHash(text)
		// Order matters: the replacement must be a live, visible item before
		// the invalidations name it (the endpoint 404s otherwise). Re-proposal
		// is idempotent per knowledge_id, so a retry pass is safe.
		if err := proposeSweepKnowledge(ctx, activities, genID, text, anchor); err != nil {
			return err
		}
		reason := fmt.Sprintf("заменено генерализацией %s (автозачистка конфликтов)", genID)
		for _, side := range []struct{ id, project string }{{newID, newProject}, {oldID, oldProject}} {
			// An unresolvable side is skipped, not sent — same rule as the
			// queue UI: the item is likely gone, the generalized statement
			// still replaces the pair.
			if side.id == "" || side.project == "" || side.id == genID {
				continue
			}
			if err := knowledgeInvalidate(ctx, observationURL, journalToken, side.id, side.project, genID, reason); err != nil {
				if !strings.Contains(err.Error(), "status 404") && !strings.Contains(err.Error(), "status 409") {
					return err
				}
			}
		}
		decision = "auto_generalized"
	default:
		return fmt.Errorf("unknown resolution %q", resolution)
	}
	if _, err := ws.DecideSupervisorCase(ctx, c.CaseKey, decision, conflictSweepActor); err != nil && !errors.Is(err, workspace.ErrSupervisorCaseDecided) {
		return err
	}
	log.Info("conflict sweep: applied", "case", c.CaseKey, "resolution", resolution, "decision", decision)
	return nil
}

// knowledgeInvalidate retires one knowledge item through the manual
// invalidate endpoint; a non-empty replacement turns it into a supersession
// (hints then offer the replacement in place of the retired rule).
func knowledgeInvalidate(ctx context.Context, observationURL, journalToken, knowledgeID, project, replacementID, reason string) error {
	body := map[string]any{
		"knowledge_id": knowledgeID,
		"project":      project,
		"actor":        map[string]any{"id": conflictSweepActor, "type": "system"},
		"reason":       reason,
	}
	if replacementID != "" && replacementID != knowledgeID {
		body["replacement_id"] = replacementID
	}
	// journalPost concatenates URL+path verbatim — trim here, like the
	// neighbors fetch does.
	_, err := journalPost(ctx, strings.TrimSuffix(observationURL, "/"), journalToken, "/v1/observations/knowledge/invalidate", body)
	return err
}

// proposeSweepKnowledge appends the generalized statement as knowledge.proposed
// through the same ingest the extraction pass uses (the projection builds the
// item from the event — identical to the queue UI's generalize action).
func proposeSweepKnowledge(ctx context.Context, activities *agent.Activities, knowledgeID, proposition, project string) error {
	if activities == nil || activities.Events == nil {
		return errors.New("events surface is not configured")
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("supervisor-generalize/%s/%d", knowledgeID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: conflictSweepActor, Integration: "temporality-agent-kernel", Version: "1"},
		Context:    observation.Context{Project: project, Actor: observation.Actor{ID: conflictSweepActor, Type: "system"}},
		Type:       "knowledge.proposed",
		Data: map[string]any{
			"knowledge_id": knowledgeID,
			"proposition":  proposition,
			"topics":       []string{},
			"origin":       "supervisor-conflict-sweep",
		},
	}
	if err := event.Validate(); err != nil {
		return err
	}
	return activities.Events.Enqueue(ctx, event)
}

// conflictSweepHash derives a stable generalized knowledge id from the merged
// text — the same text always proposes the same id, so idempotent re-proposals
// strengthen one node instead of duplicating it.
func conflictSweepHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:12]
}
