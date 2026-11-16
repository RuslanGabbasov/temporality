package main

// Organizational Activity projection (docs/temporality-organizational-
// operations-spec.md §4, этап P0). A watermark-gated background loop folds
// lifecycle journal events into the org_activity projection and the
// org_active_run snapshot. Idempotency follows the supervisor-queue pattern:
// (source_id, event_id) anchor upserts make replays no-ops, the watermark
// walks oldest→newest with 1s boundary slack, and a periodic grace-window
// re-fold catches late terminal events (the journal is append-only but its
// retention deletes history — the projection is the durable record for its
// scope).
//
// The loop never calls a model and never blocks runs: a failed pass is
// logged and retried by the next tick.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

const (
	orgActivityTick          = time.Minute
	orgActivityLookback      = 24 * time.Hour
	orgActivityPage          = 500
	orgActivityMaxPages      = 40
	orgActivitySlack         = time.Second
	orgActivityGraceWindow   = 30 * time.Minute
	orgActivityGraceEvery    = 10 // every Nth pass re-folds the grace window
	orgConsumptionTick       = 2 * time.Minute
	orgConsumptionLookback   = 15 * time.Minute
	orgPruneTick             = time.Hour
	orgFinishedRunRetention  = 7 * 24 * time.Hour
	orgAttributionMaxDepth   = 4
)

// orgActivityCategories maps journal event types to the §4.2 categories.
var orgActivityCategories = map[string]string{
	"run.started": "run", "run.restarted": "run",
	"run.completed": "run", "run.failed": "run", "run.cancelled": "run",
	"delegation.started": "progress", "delegation.completed": "progress", "delegation.failed": "progress",
	"plan.started": "plan", "plan.completed": "plan", "plan.aborted": "plan", "plan.stopped": "plan",
	"plan.task.started": "progress", "plan.task.completed": "progress",
	"plan.task.failed": "progress", "plan.task.skipped": "progress",
	"saga.registered": "task", "saga.attached": "task", "saga.renamed": "task",
	"saga.merged": "task", "saga.deleted": "task",
	"artifact.proposed": "artifact", "artifact.updated": "artifact",
	"artifact.review.requested": "artifact", "artifact.review.approved": "artifact",
	"artifact.review.rejected": "artifact", "artifact.returned_to_draft": "artifact",
	"verification.reviewed": "quality", "behavior.reviewed": "quality",
	"supervisor.verdict": "quality", "agent.version.degraded": "quality", "hitl.verdict.recorded": "quality",
	"human.requested": "human", "human.answered": "human", "human.timed_out": "human",
	"approval.requested": "human", "approval.granted": "human",
	"approval.rejected": "human", "approval.auto_granted": "human",
}

// orgActivityTypeGates keys each category to its §5 publish toggle.
var orgActivityTypeGates = map[string]func(workspace.OrgActivitySettings) bool{
	"run.started":          func(s workspace.OrgActivitySettings) bool { return s.PublishRunStarts },
	"run.restarted":        func(s workspace.OrgActivitySettings) bool { return s.PublishRunStarts },
	"run.completed":        func(s workspace.OrgActivitySettings) bool { return s.PublishRunCompletions },
	"run.failed":           func(s workspace.OrgActivitySettings) bool { return s.PublishRunFailures },
	"run.cancelled":        func(s workspace.OrgActivitySettings) bool { return s.PublishRunFailures },
	"plan.started":         publishProgressGate,
	"plan.completed":       publishProgressGate,
	"plan.aborted":         publishProgressGate,
	"plan.stopped":         publishProgressGate,
	"delegation.started":   publishProgressGate,
	"delegation.completed": publishProgressGate,
	"delegation.failed":    publishProgressGate,
	"plan.task.started":    publishProgressGate,
	"plan.task.completed":  publishProgressGate,
	"plan.task.failed":     publishProgressGate,
	"plan.task.skipped":    publishProgressGate,
	"saga.registered":      publishProgressGate,
	"saga.attached":        publishProgressGate,
	"saga.renamed":         publishProgressGate,
	"saga.merged":          publishProgressGate,
	"saga.deleted":         publishProgressGate,
	"human.requested":      publishProgressGate,
	"human.answered":       publishProgressGate,
	"human.timed_out":      publishProgressGate,
	"approval.requested":   publishProgressGate,
	"approval.granted":     publishProgressGate,
	"approval.rejected":    publishProgressGate,
	"approval.auto_granted": publishProgressGate,
}

func publishProgressGate(s workspace.OrgActivitySettings) bool { return s.PublishSignificantProgress }

func publishArtifactGate(s workspace.OrgActivitySettings) bool { return s.PublishArtifactEvents }

func publishQualityGate(s workspace.OrgActivitySettings) bool { return s.PublishQualityEvents }

func init() {
	for eventType, category := range orgActivityCategories {
		switch category {
		case "artifact":
			orgActivityTypeGates[eventType] = publishArtifactGate
		case "quality":
			orgActivityTypeGates[eventType] = publishQualityGate
		}
	}
}

// orgActivityTypesParam is the journal types= allow-list: lifecycle and
// progress families only. Tool calls, model calls and turn internals never
// become public events (spec §4.2).
func orgActivityTypesParam() string {
	types := make([]string, 0, len(orgActivityCategories))
	for eventType := range orgActivityCategories {
		types = append(types, eventType)
	}
	// deterministic order for stable URLs and cache-friendliness
	for i := 1; i < len(types); i++ {
		for j := i; j > 0 && types[j-1] > types[j]; j-- {
			types[j-1], types[j] = types[j], types[j-1]
		}
	}
	return strings.Join(types, ",")
}

// orgAttribution is the launch-time org position of a run: resolved ONCE per
// run and snapshotted — a later user transfer must not move history.
type orgAttribution struct {
	NodeID string
	Path   string
}

type orgFoldDeps struct {
	ws             *workspace.Store
	log            *slog.Logger
	units          []workspace.OrgUnit
	unitPaths      map[string]string
	rootUnitID     string
	settingsByUnit map[string]workspace.OrgActivitySettingsResult
	// per-pass caches
	runCache     map[string]orgAttribution // runID → attribution
	userCache    map[string]string         // actorID → unitID
	projectCache map[string]string         // projectID → first bound unitID
	artifactByID map[string]OrgArtifactRef
}

// OrgArtifactRef is the attribution input for artifact events (journal
// lifecycle events carry no project/run context — the fold joins the
// workspace row).
type OrgArtifactRef struct {
	OrgUnitID string
	OriginRun string
	SagaID    string
	Title     string
}

// startOrgActivityLoop runs the fold pass every minute (spec §14: фоновые
// процессы — проекция событий, snapshots, retention).
func startOrgActivityLoop(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL string) {
	journalToken := os.Getenv("TEMPORALITY_API_TOKEN")
	go func() {
		// First pass after boot: let the journal settle.
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		pass := 0
		ticker := time.NewTicker(orgActivityTick)
		pruneTicker := time.NewTicker(orgPruneTick)
		consumeTicker := time.NewTicker(orgConsumptionTick)
		defer ticker.Stop()
		defer pruneTicker.Stop()
		defer consumeTicker.Stop()
		runOrgActivityPass(ctx, log, ws, observationURL, journalToken, pass)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pass++
				runOrgActivityPass(ctx, log, ws, observationURL, journalToken, pass)
			case <-consumeTicker.C:
				sweepOrgAwarenessConsumption(ctx, log, ws, observationURL, journalToken)
			case <-pruneTicker.C:
				runOrgActivityPrune(ctx, log, ws)
			}
		}
	}()
}

// runOrgActivityPass folds one page-bounded batch of journal events.
func runOrgActivityPass(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL, journalToken string, pass int) {
	watermark, err := ws.OrgActivityWatermark(ctx)
	if err != nil {
		log.Warn("org activity: watermark", "error", err)
		return
	}
	since := watermark
	if since.IsZero() {
		since = time.Now().UTC().Add(-orgActivityLookback)
	} else {
		since = since.Add(-orgActivitySlack)
	}
	// The grace-window re-fold catches events that land after the watermark
	// passed them (late terminal events from sweeps) — safe: the anchor
	// upserts dedupe every reprocessed event.
	if pass%orgActivityGraceEvery == 0 {
		if grace := time.Now().UTC().Add(-orgActivityGraceWindow); grace.Before(since) {
			since = grace
		}
	}
	deps, err := loadOrgFoldDeps(ctx, ws, log)
	if err != nil {
		log.Warn("org activity: load deps", "error", err)
		return
	}
	cursor := ""
	newest := watermark
	folded := 0
	for page := 0; page < orgActivityMaxPages; page++ {
		params := url.Values{
			"types": {orgActivityTypesParam()}, "order": {"asc"},
			"since": {since.Format(time.RFC3339Nano)}, "limit": {strconv.Itoa(orgActivityPage)},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		events, next, err := supervisorTypedEventPage(ctx, observationURL, journalToken, params)
		if err != nil {
			log.Warn("org activity: journal page", "error", err)
			return
		}
		for i := range events {
			inserted, err := foldOrgEvent(ctx, deps, &events[i])
			if err != nil {
				log.Warn("org activity: fold event", "event", events[i].EventID, "error", err)
				continue // one bad event never stalls the projection
			}
			if inserted {
				folded++
			}
			if events[i].OccurredAt.After(newest) {
				newest = events[i].OccurredAt
			}
		}
		if next == "" || len(events) == 0 {
			break
		}
		cursor = next
	}
	if newest.After(watermark) {
		if err := ws.AdvanceOrgActivitySync(ctx, newest); err != nil {
			log.Warn("org activity: advance watermark", "error", err)
		}
	}
	if folded > 0 {
		log.Info("org activity folded", "events", folded)
	}
}

// loadOrgFoldDeps loads the units, settings and attribution inputs once per
// pass — the fold resolves every node from two queries, not per event.
func loadOrgFoldDeps(ctx context.Context, ws *workspace.Store, log *slog.Logger) (*orgFoldDeps, error) {
	units, err := ws.ListOrgUnits(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := ws.AllOrgActivitySettingRows(ctx)
	if err != nil {
		return nil, err
	}
	deps := &orgFoldDeps{
		ws:             ws,
		log:            log,
		units:          units,
		unitPaths:      map[string]string{},
		settingsByUnit: workspace.ResolveOrgActivitySettingsForUnits(units, rows),
		runCache:       map[string]orgAttribution{},
		userCache:      map[string]string{},
		projectCache:   map[string]string{},
		artifactByID:   map[string]OrgArtifactRef{},
	}
	for _, unit := range units {
		deps.unitPaths[unit.ID] = unit.SelfPath()
		if deps.rootUnitID == "" && unit.ParentID == "" {
			deps.rootUnitID = unit.ID
		}
	}
	return deps, nil
}

// foldOrgEvent maps one journal event onto the projection. Returns whether a
// NEW projection row was written.
func foldOrgEvent(ctx context.Context, deps *orgFoldDeps, event *observation.Event) (bool, error) {
	category, ok := orgActivityCategories[event.Type]
	if !ok {
		return false, nil // not ours (defensive: the fetch is already filtered)
	}
	project := event.Context.Project
	runID := event.Context.Run
	actorID := event.Context.Actor.ID

	// Artifact events carry no project/run context: join the artifact row.
	if strings.HasPrefix(event.Type, "artifact.") {
		artifactID, _ := event.Data["artifact_id"].(string)
		if artifactID == "" {
			return false, nil
		}
		ref, err := deps.artifactRef(ctx, artifactID)
		if err != nil {
			return false, err
		}
		if runID == "" {
			runID = ref.OriginRun
		}
	}

	attrib := deps.resolveAttribution(ctx, project, runID, actorID, orgString(event.Data, "parent_run_id"), 0)
	settings := deps.settings(attrib.NodeID)

	// Maintain the active-run snapshot ALWAYS (Work Board and the supervisor
	// read it even when activity collection is off for the node).
	if runID != "" && project != "" {
		if err := updateOrgActiveRun(ctx, deps, event, category, project, runID, actorID, attrib); err != nil {
			return false, err
		}
	}

	// The §5 publish gates decide whether the event becomes a public
	// projection row at all.
	if !settings.ActivityEnabled {
		return false, nil
	}
	gate, ok := orgActivityTypeGates[event.Type]
	if !ok {
		return false, nil
	}
	if !gate(settings) {
		return false, nil
	}

	refs := orgEventRefs(event.Data)
	row := workspace.OrgActivityEvent{
		SourceID:      event.Source.ID,
		EventID:       event.EventID,
		EventType:     event.Type,
		Category:      category,
		OccurredAt:    event.OccurredAt,
		ReceivedAt:    receivedAtPtr(event.ReceivedAt),
		ProjectID:     project,
		RunID:         runID,
		TaskID:        orgTaskID(event, event.Data),
		PlanID:        orgString(event.Data, "operation_id"),
		ActorID:       actorID,
		OrgNodeID:     attrib.NodeID,
		OrgNodePath:   attrib.Path,
		Summary:       orgEventSummary(event.Type, event.Data),
		Visibility:    "org-unit",
		Refs:          refs,
		SourceRef:     orgSourceRef(event),
		SchemaVersion: 1,
	}
	if row.Refs == nil {
		row.Refs = json.RawMessage("{}")
	}
	return deps.ws.UpsertOrgActivity(ctx, row)
}

// orgSourceRef keeps provenance pointers only — never event content (spec
// §16: no tool arguments/results in the projection).
func orgSourceRef(event *observation.Event) json.RawMessage {
	ref := map[string]any{
		"source_id":   event.Source.ID,
		"integration": event.Source.Integration,
	}
	if event.Context.ParentEventID != "" {
		ref["parent_event_id"] = event.Context.ParentEventID
	}
	if len(event.Evidence) > 0 {
		evidence := make([]map[string]string, 0, len(event.Evidence))
		for _, e := range event.Evidence {
			evidence = append(evidence, map[string]string{"ref": e.Ref, "type": e.Type})
		}
		ref["evidence"] = evidence
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

// orgEventRefs extracts the whitelisted reference ids from event data —
// component/repository/artifact pointers for the boards and the supervisor
// batch (spec §4.3 refs).
func orgEventRefs(data map[string]any) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage("{}")
	}
	keys := []string{
		"artifact_id", "artifact_version", "saga_id", "child_run_id", "parent_run_id",
		"operation_id", "request_id", "task_id", "agent_id", "review_verdict",
		"cause_class", "status", "verdict", "degraded",
	}
	refs := map[string]any{}
	for _, key := range keys {
		if value, ok := data[key]; ok && value != nil && value != "" {
			switch value.(type) {
			case string, bool, float64, int64, int:
				refs[key] = value
			}
		}
	}
	if len(refs) == 0 {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

// updateOrgActiveRun maintains the snapshot: status transitions, title/task
// attribution on start, urgent latch for critical events.
func updateOrgActiveRun(ctx context.Context, deps *orgFoldDeps, event *observation.Event, category, project, runID, actorID string, attrib orgAttribution) error {
	status := ""
	var startedAt, finishedAt, lastEvent *time.Time
	occurred := event.OccurredAt
	lastEvent = &occurred
	urgentSet := false
	urgentClear := false
	switch event.Type {
	case "run.started", "run.restarted":
		status = "running"
		startedAt = &occurred
	case "run.completed":
		status = "completed"
		finishedAt = &occurred
	case "run.failed":
		status = "failed"
		finishedAt = &occurred
		urgentSet = true
	case "run.cancelled":
		status = "cancelled"
		finishedAt = &occurred
		urgentClear = true
	case "human.requested", "approval.requested":
		urgentSet = true // waiting on a human is critical for the supervisor
	case "human.answered", "human.timed_out", "approval.granted", "approval.rejected", "approval.auto_granted":
		urgentClear = true
	}
	row := workspace.OrgActiveRun{
		ProjectID:   project,
		RunID:       runID,
		OrgNodeID:   attrib.NodeID,
		OrgNodePath: attrib.Path,
		ActorID:     actorID,
		TaskID:      orgString(event.Data, "task_id"),
		AgentID:     orgString(event.Data, "agent_id"),
		Title:       orgString(event.Data, "title"),
		Status:      status,
	}
	if row.Status == "" {
		row.Status = "running" // non-terminal events keep the run live
	}
	row.StartedAt = startedAt
	row.LastEventAt = lastEvent
	row.FinishedAt = finishedAt
	if err := deps.ws.UpsertOrgActiveRun(ctx, row); err != nil {
		return err
	}
	if urgentSet {
		return deps.ws.SetOrgActiveRunUrgent(ctx, project, runID, true)
	}
	if urgentClear {
		return deps.ws.SetOrgActiveRunUrgent(ctx, project, runID, false)
	}
	// A source_id observed on any run event feeds the steer worker's
	// workflow-id resolution.
	if event.Source.ID != "" {
		return deps.ws.SetOrgActiveRunSource(ctx, project, runID, event.Source.ID)
	}
	return nil
}
// resolveAttribution returns the launch-time org position for a run (or an
// actor-scoped event), caching per pass. Resolution order: existing snapshot
// → parent run → exec_context → user unit → project binding → root. The
// snapshot-first order is what makes «перевод не перемещает историю» hold:
// once attributed, a run never re-resolves.
func (d *orgFoldDeps) resolveAttribution(ctx context.Context, project, runID, actorID, parentRunID string, depth int) orgAttribution {
	if runID != "" {
		if cached, ok := d.runCache[runID]; ok {
			return cached
		}
	}
	attrib := d.attribFor(ctx, project, runID, actorID, parentRunID, depth)
	if runID != "" {
		d.runCache[runID] = attrib
	}
	return attrib
}

func (d *orgFoldDeps) attribFor(ctx context.Context, project, runID, actorID, parentRunID string, depth int) orgAttribution {
	if depth > orgAttributionMaxDepth {
		return orgAttribution{}
	}
	// 1. Existing snapshot (the historical anchor).
	if project != "" && runID != "" {
		if row, err := d.ws.GetOrgActiveRun(ctx, project, runID); err == nil && row.OrgNodeID != "" {
			return orgAttribution{NodeID: row.OrgNodeID, Path: row.OrgNodePath}
		}
	}
	// 2. Parent run (delegation children inherit the initiator's node).
	if parentRunID != "" && parentRunID != runID {
		if parent := d.resolveAttribution(ctx, project, parentRunID, "", "", depth+1); parent.NodeID != "" {
			return parent
		}
	}
	// 3. Launch-time snapshot in exec_context.
	if runID != "" {
		if exec, err := d.ws.RunExecContextByRunID(ctx, runID); err == nil {
			if unit := orgExecContextUnit(exec); unit != "" {
				return d.attribWithUnit(unit)
			}
			if actor, _ := exec["actor"].(string); actor != "" && actorID == "" {
				actorID = actor
			}
		}
	}
	// 4. The actor's current unit (best effort for non-run events).
	if unit, ok := d.userUnit(ctx, actorID); ok && unit != "" {
		return d.attribWithUnit(unit)
	}
	// 5. The project's org binding.
	if project != "" {
		if unit, ok := d.projectUnit(ctx, project); ok && unit != "" {
			return d.attribWithUnit(unit)
		}
	}
	// 6. The root unit — never drop an event for lack of attribution.
	if d.rootUnitID != "" {
		return d.attribWithUnit(d.rootUnitID)
	}
	return orgAttribution{}
}

func (d *orgFoldDeps) attribWithUnit(unitID string) orgAttribution {
	return orgAttribution{NodeID: unitID, Path: d.unitPaths[unitID]}
}

func (d *orgFoldDeps) userUnit(ctx context.Context, userID string) (string, bool) {
	if userID == "" {
		return "", false
	}
	if unit, ok := d.userCache[userID]; ok {
		return unit, true
	}
	unit := ""
	if user, err := d.ws.GetUser(ctx, userID); err == nil {
		unit = user.OrgUnitID
	}
	d.userCache[userID] = unit
	return unit, true
}

func (d *orgFoldDeps) projectUnit(ctx context.Context, projectID string) (string, bool) {
	if unit, ok := d.projectCache[projectID]; ok {
		return unit, true
	}
	unit := ""
	if units, err := d.ws.ProjectOrgUnits(ctx, projectID); err == nil && len(units) > 0 {
		unit = units[0]
	}
	d.projectCache[projectID] = unit
	return unit, true
}

func (d *orgFoldDeps) artifactRef(ctx context.Context, artifactID string) (OrgArtifactRef, error) {
	if ref, ok := d.artifactByID[artifactID]; ok {
		return ref, nil
	}
	unit, originRun, _, sagaID, title, err := d.ws.OrgArtifactContext(ctx, artifactID)
	if err != nil {
		return OrgArtifactRef{}, err
	}
	ref := OrgArtifactRef{OrgUnitID: unit, OriginRun: originRun, SagaID: sagaID, Title: title}
	d.artifactByID[artifactID] = ref
	return ref, nil
}

func (d *orgFoldDeps) settings(nodeID string) workspace.OrgActivitySettings {
	if result, ok := d.settingsByUnit[nodeID]; ok {
		return result.Settings
	}
	// Unknown node (deleted between load and fold): system defaults.
	return workspace.OrgActivitySettingsFor(d.settingsByUnit[""].Values)
}

// orgExecContextUnit extracts the launch-time org unit: authorized_by
// .org_unit_id is stamped by runExecContextFor at every entry point.
func orgExecContextUnit(exec map[string]any) string {
	authorized, ok := exec["authorized_by"].(map[string]any)
	if !ok {
		return ""
	}
	unit, _ := authorized["org_unit_id"].(string)
	return unit
}

// sweepOrgAwarenessConsumption correlates run.user_note journal events (the
// workflow's own proof that the note reached the conversation) with delivered
// awareness rows via the [org-awareness:<id>] marker: consumed means
// placement in the model context, nothing more (spec §8).
func sweepOrgAwarenessConsumption(ctx context.Context, log *slog.Logger, ws *workspace.Store, observationURL, journalToken string) {
	since := time.Now().UTC().Add(-orgConsumptionLookback)
	cursor := ""
	for page := 0; page < 10; page++ {
		params := url.Values{
			"type":  {"run.user_note"}, "order": {"asc"},
			"since": {since.Format(time.RFC3339Nano)}, "limit": {"500"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		events, next, err := supervisorTypedEventPage(ctx, observationURL, journalToken, params)
		if err != nil {
			log.Warn("org awareness consumption: journal page", "error", err)
			return
		}
		for i := range events {
			text, _ := events[i].Data["text"].(string)
			for _, id := range orgAwarenessMarkerIDs(text) {
				if err := ws.MarkOrgAwarenessDeliveryConsumed(ctx, id, events[i].OccurredAt); err != nil {
					log.Warn("org awareness consumption: mark", "delivery", id, "error", err)
				}
			}
		}
		if next == "" || len(events) == 0 {
			return
		}
		cursor = next
	}
}

const orgAwarenessMarkerPrefix = "[org-awareness:"

// orgAwarenessMarkerIDs extracts the delivery ids the steer worker embeds in
// its message framing.
func orgAwarenessMarkerIDs(text string) []string {
	if !strings.Contains(text, orgAwarenessMarkerPrefix) {
		return nil
	}
	var ids []string
	rest := text
	for {
		start := strings.Index(rest, orgAwarenessMarkerPrefix)
		if start < 0 {
			return ids
		}
		rest = rest[start+len(orgAwarenessMarkerPrefix):]
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return ids
		}
		id := strings.TrimSpace(rest[:end])
		if id != "" {
			ids = append(ids, id)
		}
		rest = rest[end+1:]
	}
}

// runOrgActivityPrune enforces the retention settings (the smallest among the
// nodes wins — the strictest scope governs the shared projection) and drops
// terminal snapshot rows.
func runOrgActivityPrune(ctx context.Context, log *slog.Logger, ws *workspace.Store) {
	units, err := ws.ListOrgUnits(ctx)
	if err != nil {
		log.Warn("org activity prune: units", "error", err)
		return
	}
	rows, err := ws.AllOrgActivitySettingRows(ctx)
	if err != nil {
		log.Warn("org activity prune: settings", "error", err)
		return
	}
	settingsByUnit := workspace.ResolveOrgActivitySettingsForUnits(units, rows)
	minActivity, minDelivery := time.Duration(0), time.Duration(0)
	for _, result := range settingsByUnit {
		s := result.Settings
		if s.ActivityRetention > 0 && (minActivity == 0 || s.ActivityRetention < minActivity) {
			minActivity = s.ActivityRetention
		}
		if s.DeliveryRetention > 0 && (minDelivery == 0 || s.DeliveryRetention < minDelivery) {
			minDelivery = s.DeliveryRetention
		}
	}
	if minActivity > 0 {
		if _, err := ws.PruneOrgActivity(ctx, time.Now().UTC().Add(-minActivity)); err != nil {
			log.Warn("org activity prune: rows", "error", err)
		}
	}
	if minDelivery > 0 {
		if _, err := ws.PruneOrgAwarenessDeliveries(ctx, time.Now().UTC().Add(-minDelivery)); err != nil {
			log.Warn("org activity prune: deliveries", "error", err)
		}
	}
	if _, err := ws.PruneOrgActiveRunsFinished(ctx, time.Now().UTC().Add(-orgFinishedRunRetention)); err != nil {
		log.Warn("org activity prune: snapshot", "error", err)
	}
}

// --- small helpers ---

func orgString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func orgTaskID(event *observation.Event, data map[string]any) string {
	if event.Context.Task != "" {
		return event.Context.Task
	}
	return orgString(data, "task_id")
}

func receivedAtPtr(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

// orgEventSummary builds the safe short summary (spec §4.3: «Safe short
// description»): whitelisted fields only, control characters collapsed,
// hard length cap. Never raw tool arguments or file content.
func orgEventSummary(eventType string, data map[string]any) string {
	label := strings.ReplaceAll(eventType, ".", " ")
	text := ""
	switch eventType {
	case "run.started", "run.restarted":
		text = orgFirstNonEmpty(orgString(data, "title"), label)
	case "run.completed":
		text = label
		if verdict := orgString(data, "review_verdict"); verdict != "" {
			text += " (review: " + verdict + ")"
		}
		if status := orgString(data, "status"); status != "" && status != "completed" {
			text += " (" + status + ")"
		}
	case "run.failed":
		if errType := orgString(data, "error_type"); errType != "" {
			text = label + ": " + errType
		} else {
			text = label
		}
	case "plan.started", "plan.completed", "plan.aborted", "plan.stopped":
		if goal := orgString(data, "goal"); goal != "" {
			text = label + ": " + goal
		} else {
			text = label
		}
	case "artifact.proposed", "artifact.updated",
		"artifact.review.requested", "artifact.review.approved",
		"artifact.review.rejected", "artifact.returned_to_draft":
		if title := orgString(data, "title"); title != "" {
			text = label + ": " + title
		} else {
			text = label
		}
	case "human.requested":
		text = "Waiting for human input"
	case "human.answered":
		text = "Human input received"
	case "human.timed_out":
		text = "Human input timed out"
	case "approval.requested":
		text = "Waiting for approval"
	case "approval.granted", "approval.auto_granted":
		text = "Approval granted"
	case "approval.rejected":
		text = "Approval rejected"
	case "supervisor.verdict":
		text = orgFirstNonEmpty(orgString(data, "summary"), label)
	case "agent.version.degraded":
		text = "Agent version degraded"
	default:
		for _, key := range []string{"summary", "title", "name", "goal", "status", "verdict"} {
			if value := orgString(data, key); value != "" {
				text = label + ": " + value
				break
			}
		}
		if text == "" {
			text = label
		}
	}
	return truncateOrgSummary(text)
}

func orgFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

const orgSummaryMaxRunes = 200

// truncateOrgSummary caps the summary and collapses control characters — a
// model-written summary field must never break the board layout or carry
// escape payloads.
func truncateOrgSummary(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > orgSummaryMaxRunes {
		return strings.TrimSpace(string(runes[:orgSummaryMaxRunes])) + "…"
	}
	return text
}

// fmt is referenced by the doc comment above; keep the import honest.
