package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// Event triggers (docs/triggers-and-escalations.md §1, type=event): the kernel
// tails its own observation journal and starts a run when a matching event
// appears — "hooks around the agent cycle" (unapproved artifacts piling up,
// sandbox escape attempts, mass export, security events). The constructor is
// the event type plus context filters:
//
//	event_type  exact journal event type (artifact.review.requested, …)
//	filter      actor_id / actor_type / project — the event's context;
//	            data.<field> — exact string match on the event payload
//	prompt      the run's task, the matched event is appended as context
//
// Semantics: fires only on events observed after the trigger's activation (a
// fresh listener bootstraps to the journal tail without replaying history);
// the same journal event never fires twice (deterministic task id); events
// produced by trigger-started runs are ignored (run id prefix "trigger-") so a
// trigger can never feed itself.

const (
	eventTriggerCursorKey = "journal:events"
	eventTriggerInterval  = 15 * time.Second
	eventTriggerPageLimit = 500
)

// validateEventTriggerConfig is the save-time half of the constructor
// (docs/triggers-and-escalations.md §3): a known event type is required, and
// filter keys must be one the listener actually evaluates — a typo must fail
// at save, not silently never-match.
func validateEventTriggerConfig(config json.RawMessage) error {
	var cfg workspace.EventConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return errors.New("trigger config is not valid event config")
	}
	if cfg.EventType == "" {
		return errors.New("event_type is required for event triggers")
	}
	if cfg.Prompt == "" {
		return errors.New("prompt is required for event triggers")
	}
	for key := range cfg.Filter {
		if !eventFilterKeyKnown(key) {
			return fmt.Errorf("unknown event filter key %q (use actor_id, actor_type, project, data.<field>)", key)
		}
	}
	return nil
}

func eventFilterKeyKnown(key string) bool {
	switch {
	case key == "actor_id", key == "actor_type", key == "project":
		return true
	case strings.HasPrefix(key, "data."):
		return strings.TrimPrefix(key, "data.") != ""
	default:
		return false
	}
}

// eventTriggerMatches is the match-side half of the constructor: the event
// type compares exactly, every filter entry must hold. Unknown keys never
// match (defensive; save-time validation should have rejected them).
func eventTriggerMatches(cfg workspace.EventConfig, ev observation.Event) bool {
	if ev.Type != cfg.EventType {
		return false
	}
	for key, want := range cfg.Filter {
		var got string
		switch {
		case key == "actor_id":
			got = ev.Context.Actor.ID
		case key == "actor_type":
			got = ev.Context.Actor.Type
		case key == "project":
			got = ev.Context.Project
		case strings.HasPrefix(key, "data."):
			got = fmt.Sprint(ev.Data[strings.TrimPrefix(key, "data.")])
		default:
			return false
		}
		if got != want {
			return false
		}
	}
	return true
}

// eventTriggerOutbox is the journal publishing surface (the kernel outbox).
type eventTriggerOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// eventTriggerListener tails the journal and fires matching triggers. The
// collaborators are function fields so the cycle logic (bootstrap, cursor
// advance, feedback guard, matching) stays testable without a database or a
// temporal connection; the production wiring in main fills them with the
// store-backed implementations below.
type eventTriggerListener struct {
	// cycle collaborators
	listTriggers func(ctx context.Context) ([]parsedEventTrigger, error)
	loadCursor   func(ctx context.Context) (string, error)
	saveCursor   func(ctx context.Context, cursor string) error
	fetchPage    func(ctx context.Context, cursor string) ([]observation.Event, error)
	// fireOne starts the run for one match; log-only outcomes by design.
	fireOne func(ctx context.Context, t *parsedEventTrigger, ev observation.Event)

	// fire-path state (the production fireOne below)
	ws         *workspace.Store
	activities *agent.Activities
	prices     cost.Config
	temporal   client.Client
	taskQueue  string
	events     eventTriggerOutbox
	sourceID   string

	journalURL string
	apiToken   string
	client     *http.Client
	log        *slog.Logger
}

// newEventTriggerListener wires the production collaborators: the store for
// triggers/tasks/runs, the journal listing for the tail, the real fire path.
func newEventTriggerListener(ws *workspace.Store, activities *agent.Activities, prices cost.Config, temporal client.Client, taskQueue string, events eventTriggerOutbox, sourceID, journalURL, apiToken string, log *slog.Logger) *eventTriggerListener {
	l := &eventTriggerListener{
		ws: ws, activities: activities, prices: prices, temporal: temporal,
		taskQueue: taskQueue, events: events, sourceID: sourceID,
		journalURL: journalURL, apiToken: apiToken,
		client: &http.Client{Timeout: 20 * time.Second}, log: log,
	}
	l.listTriggers = l.listStoreTriggers
	l.loadCursor = func(ctx context.Context) (string, error) {
		stored, err := ws.ChannelCursor(ctx, eventTriggerCursorKey)
		if err != nil {
			return "", err
		}
		return stored.Cursor, nil
	}
	l.saveCursor = func(ctx context.Context, cursor string) error {
		return ws.SaveChannelCursor(ctx, eventTriggerCursorKey, workspace.ChannelCursor{Cursor: cursor, Identity: "event-triggers"})
	}
	l.fetchPage = l.fetchJournalPage
	l.fireOne = l.fire
	return l
}

func (l *eventTriggerListener) run(ctx context.Context) {
	ticker := time.NewTicker(eventTriggerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.cycle(ctx); err != nil {
				l.log.Warn("event trigger cycle", "error", err)
			}
		}
	}
}

// cycle lists event triggers first: without any, the journal is not tailed at
// all (zero overhead on installs that never opted in). The cursor then pages
// the journal from the persisted position and fires every match.
func (l *eventTriggerListener) cycle(ctx context.Context) error {
	triggers, err := l.listTriggers(ctx)
	if err != nil {
		return err
	}
	if len(triggers) == 0 {
		return nil
	}
	cursor, err := l.loadCursor(ctx)
	if err != nil {
		return err
	}
	// Fresh listener (or first activation): page to the tail WITHOUT firing —
	// triggers react to what happens after their activation, history is not
	// replayed (a backfill would start a run per historical event).
	bootstrap := cursor == ""
	for {
		page, err := l.fetchPage(ctx, cursor)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		cursor = observation.EncodeCursor(page[len(page)-1])
		if err := l.saveCursor(ctx, cursor); err != nil {
			return err
		}
		if !bootstrap {
			for _, ev := range page {
				// Feedback guard: events of trigger-started runs never re-fire
				// triggers — a trigger listening to its own run's events
				// would otherwise loop forever.
				if strings.HasPrefix(ev.Context.Run, "trigger-") {
					continue
				}
				for i := range triggers {
					if eventTriggerMatches(triggers[i].cfg, ev) {
						l.fireOne(ctx, &triggers[i], ev)
					}
				}
			}
		}
		if len(page) < eventTriggerPageLimit {
			return nil // caught up
		}
	}
}

// parsedEventTrigger pairs a trigger row with its parsed config.
type parsedEventTrigger struct {
	trigger workspace.Trigger
	cfg     workspace.EventConfig
}

// fetchJournalPage is the production page fetch: the journal's cursor listing,
// authenticated the way every kernel journal call is.
func (l *eventTriggerListener) fetchJournalPage(ctx context.Context, cursor string) ([]observation.Event, error) {
	endpoint := fmt.Sprintf("%s/v1/observations/events?limit=%d", l.journalURL, eventTriggerPageLimit)
	if cursor != "" {
		endpoint += "&cursor=" + url.QueryEscape(cursor)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if l.apiToken != "" {
		request.Header.Set("Authorization", "Bearer "+l.apiToken)
	}
	response, err := l.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("journal listing answered HTTP %d", response.StatusCode)
	}
	var page struct {
		Events []observation.Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		return nil, err
	}
	return page.Events, nil
}

// listStoreTriggers is the production trigger listing: enabled event triggers
// with configs that parse; malformed rows are skipped, not fatal for the rest.
func (l *eventTriggerListener) listStoreTriggers(ctx context.Context) ([]parsedEventTrigger, error) {
	rows, err := l.ws.ListAllTriggers(ctx)
	if err != nil {
		return nil, err
	}
	triggers := make([]parsedEventTrigger, 0, len(rows))
	for _, t := range rows {
		if t.Type != "event" || !t.Enabled {
			continue
		}
		var cfg workspace.EventConfig
		if err := json.Unmarshal(t.Config, &cfg); err != nil || cfg.EventType == "" || cfg.Prompt == "" {
			continue
		}
		triggers = append(triggers, parsedEventTrigger{trigger: t, cfg: cfg})
	}
	sort.Slice(triggers, func(i, j int) bool { return triggers[i].trigger.ID < triggers[j].trigger.ID })
	return triggers, nil
}

// fire mirrors the schedule loop's path (resolve agent → config → policy →
// fire-time authorization → task → workflow → run row) with log-only
// outcomes: a listener problem never retries a fire, the journal cursor has
// already moved past the event.
func (l *eventTriggerListener) fire(ctx context.Context, t *parsedEventTrigger, ev observation.Event) {
	// Deterministic per (trigger, source event): redelivery of the same
	// journal event (listener restart before the cursor advanced) deduplicates
	// here, and the workflow id reuse policy backs it up.
	key := sha256.Sum256([]byte(t.trigger.ID + "\x00" + ev.EventID))
	taskID := "trigger-" + t.trigger.ID + "-" + hex.EncodeToString(key[:])[:12]
	if _, err := l.ws.GetTask(ctx, taskID); err == nil {
		return
	}
	prompt := t.cfg.Prompt + "\n\n--- Trigger event ---\n" + eventTriggerContextJSON(ev)
	input := agent.RunInput{RunID: taskID, Project: t.trigger.ProjectID, TaskID: taskID, Prompt: prompt}
	effectiveAgentID, triggerAgent, aErr := resolveRunAgent(ctx, l.ws, t.trigger.ProjectID, t.trigger.AgentID)
	if aErr != nil {
		l.log.Warn("event trigger: resolve agent", "trigger", t.trigger.ID, "error", aErr)
		return
	}
	applyAgentConfig(ctx, l.ws, triggerAgent, &input)
	applyProjectModel(ctx, l.ws, t.trigger.ProjectID, &input)
	policyErr := applyRunPolicy(ctx, l.ws, l.prices, t.trigger.ProjectID, "", t.trigger.OrgUnitID, &input)
	if err := l.activities.PrepareRun(&input); err != nil {
		l.log.Warn("event trigger: prepare run", "trigger", t.trigger.ID, "error", err)
		return
	}
	input.ActorID = "event-" + t.trigger.ID
	scope := agent.EventScope(l.sourceID, t.trigger.ProjectID, input.RunID)
	triggerData := map[string]any{
		"trigger_id": t.trigger.ID, "trigger_name": t.trigger.Name,
		"event_id": ev.EventID, "event_type": ev.Type, "source": "event",
		"event_run": ev.Context.Run,
	}
	emit := func(kind string, data map[string]any) {
		item := observation.Event{
			Schema: observation.Schema, EventID: "trigger/" + scope + "/" + kind, OccurredAt: time.Now().UTC(),
			Source:  observation.Source{ID: l.sourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
			Context: observation.Context{Project: t.trigger.ProjectID, Run: input.RunID, Task: taskID, Actor: observation.Actor{ID: input.ActorID, Type: "trigger"}},
			Type:    "trigger." + kind, Data: data,
		}
		if err := l.events.Enqueue(ctx, item); err != nil {
			l.log.Error("emit event trigger outcome", "trigger", t.trigger.ID, "kind", kind, "error", err)
		}
	}
	// Fire-time re-authorization (§21) + policy: a forbidden run is a visible
	// rejection, not a silent drop.
	identityID, authErr := authorizeTriggerRun(ctx, l.ws, &t.trigger, effectiveAgentID, triggerAgent, &input)
	if authErr == nil {
		authErr = policyErr
	}
	if authErr != nil {
		l.log.Warn("event trigger rejected", "trigger", t.trigger.ID, "name", t.trigger.Name, "error", authErr)
		emit("rejected", func() map[string]any {
			data := map[string]any{"reason": authErr.Error()}
			for k, v := range triggerData {
				data[k] = v
			}
			return data
		}())
		return
	}
	task := &workspace.Task{ID: taskID, ProjectID: t.trigger.ProjectID, AgentID: t.trigger.AgentID, Title: "Event: " + t.trigger.Name, Prompt: prompt}
	if err := l.ws.CreateTask(ctx, task); err != nil {
		l.log.Error("event trigger: create task", "trigger", t.trigger.ID, "error", err)
		return
	}
	input.Title = task.Title
	input.ExecutionIdentityID = identityID
	workflowID := workflowIDFor(l.sourceID, t.trigger.ProjectID, input.RunID)
	options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: l.taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
	if _, err := l.temporal.ExecuteWorkflow(ctx, options, "AgentRun", input); err != nil {
		if !strings.Contains(err.Error(), "already running") && !strings.Contains(err.Error(), "WorkflowExecutionAlreadyStarted") && !isWorkflowExistsError(err) {
			l.log.Error("event trigger: start run", "trigger", t.trigger.ID, "error", err)
		}
		return
	}
	wsRun := &workspace.Run{ID: input.RunID, TaskID: taskID, ProjectID: t.trigger.ProjectID, AgentID: effectiveAgentID, AgentVersion: input.AgentVersion, RunID: input.RunID, Status: "started", Model: input.Model, ExecContext: runExecContext(ctx, &input, effectiveAgentID, controlplane.RoleNone)}
	wsRun.ExecContext["execution_identity_id"] = identityID
	_ = l.ws.CreateRun(ctx, wsRun)
	triggerData["execution_identity_id"] = identityID
	emit("accepted", triggerData)
	l.log.Info("event trigger fired", "trigger", t.trigger.ID, "name", t.trigger.Name, "event", ev.EventID, "run", input.RunID)
}

// eventTriggerContextJSON renders the matched event for the agent: the whole
// context plus the payload, so the run starts from what actually happened.
func eventTriggerContextJSON(ev observation.Event) string {
	payload := map[string]any{
		"event_id":    ev.EventID,
		"type":        ev.Type,
		"occurred_at": ev.OccurredAt,
		"actor_id":    ev.Context.Actor.ID,
		"actor_type":  ev.Context.Actor.Type,
		"project":     ev.Context.Project,
		"run":         ev.Context.Run,
		"source":      ev.Source.ID,
		"data":        ev.Data,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Sprintf("event %s (%s)", ev.EventID, ev.Type)
	}
	return string(encoded)
}
