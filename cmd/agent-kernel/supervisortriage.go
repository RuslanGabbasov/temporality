package main

// Supervisor triage loop (docs/supervisor-model.md §3, wave A1): a background
// pass that finds recently failed runs without a supervisor verdict, reads
// their traces from the journal and asks the supervisor model for a
// structured diagnosis. The verdict is journaled as a supervisor.verdict
// event; the dashboard surfaces the newest ones in its problem queue.
// Bounded per pass (2 runs) so a burst of failures cannot burn the model
// budget.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
)

const supervisorTriageIntervalDefault = 2 * time.Minute

// Bounded per pass so a burst of failures cannot burn the model budget.
const supervisorTriagePassCap = 2

func startSupervisorTriageLoop(ctx context.Context, log *slog.Logger, activities *agent.Activities, ws supervisorCaseStore, observationURL string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	interval := supervisorTriageIntervalDefault
	if raw := strings.TrimSpace(os.Getenv("KERNEL_SUPERVISOR_TRIAGE_INTERVAL")); raw != "" {
		if d, perr := time.ParseDuration(raw); perr == nil && d > 0 {
			interval = d
		}
	}
	go func() {
		// First pass after boot: give in-flight runs a moment to terminate.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		for {
			if ctx.Err() != nil {
				return
			}
			runSupervisorTriagePass(ctx, log, activities, observationURL, modelClient)
			// Anomaly detection (wave A3) rides the same cadence; its own
			// metric dedup keeps a persisting anomaly from re-journaling.
			runSupervisorInsightPass(ctx, log, activities, observationURL, modelClient)
			// Waves B/C: knowledge triage/hygiene, friction, intake audit,
			// skill mining, narratives — one pass, per-subject dedup.
			runSupervisorCasesPass(ctx, log, activities, ws, observationURL, modelClient)
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
}

// journalEvent is one journal event reduced to what triage needs.
type journalEvent struct {
	Project string
	Run     string
	Type    string
	Data    map[string]any
}

func runSupervisorTriagePass(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	// The journal bearer token is read per pass: a boot-order race (journal
	// not yet listening) must self-heal on the next pass, not require a
	// kernel restart.
	journalToken := os.Getenv("TEMPORALITY_API_TOKEN")
	// Recently failed runs, newest first.
	failed, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "run.failed", 20)
	if err != nil {
		log.Warn("supervisor triage: list failed runs", "error", err.Error())
		return
	}
	if len(failed) == 0 {
		return
	}
	// Runs that already carry a verdict are skipped: one diagnosis per failure.
	verdicts, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "supervisor.verdict", 100)
	if err != nil {
		log.Warn("supervisor triage: list verdicts", "error", err.Error())
		return
	}
	done := map[string]bool{}
	for _, event := range verdicts {
		if runID, _ := event.Data["run_id"].(string); runID != "" {
			done[runID] = true
		}
	}

	client, err := modelClient(ctx)
	if err != nil {
		// Judge off — nothing will change until the operator acts; stay quiet.
		return
	}

	triaged := 0
	for _, event := range failed {
		if triaged >= supervisorTriagePassCap {
			break
		}
		runID := event.Run
		project := event.Project
		if runID == "" || done[runID] {
			continue
		}
		trace := buildFailedRunTrace(event)
		if err := appendRunTraceDigest(ctx, observationURL, journalToken, runID, &trace); err != nil {
			log.Warn("supervisor triage: trace digest", "run", runID, "error", err.Error())
		}
		verdict, err := agent.TriageFailedRun(ctx, client, trace)
		if err != nil {
			log.Warn("supervisor triage: model call failed", "run", runID, "error", err.Error())
			continue
		}
		verdict.ClusterKey = agent.SupervisorClusterKey(trace)
		if err := activities.PublishSupervisorVerdict(ctx, string(verdict.CauseClass), verdict, project, runID); err != nil {
			log.Warn("supervisor triage: publish", "run", runID, "error", err.Error())
			continue
		}
		triaged++
		log.Info("supervisor verdict recorded", "run", runID, "cause_class", string(verdict.CauseClass), "pattern", verdict.Pattern, "summary", verdict.Summary)
	}
}

// journalTypedEvents is one page of the journal listing.
type journalTypedEvents struct {
	Events []struct {
		Context struct {
			Project string `json:"project"`
			Run     string `json:"run"`
		} `json:"context"`
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	} `json:"events"`
}

// fetchJournalTypedEvents lists journal events of one type, newest first.
func fetchJournalTypedEvents(ctx context.Context, observationURL, journalToken, eventType string, limit int) ([]journalEvent, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(observationURL, "/")+"/v1/observations/events?type="+url.QueryEscape(eventType)+"&limit="+fmt.Sprint(limit), nil)
	if err != nil {
		return nil, err
	}
	if journalToken != "" {
		request.Header.Set("Authorization", "Bearer "+journalToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("journal events %s", journalErrorSnippet(response.StatusCode, string(body)))
	}
	var page journalTypedEvents
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	refs := make([]journalEvent, 0, len(page.Events))
	for _, event := range page.Events {
		refs = append(refs, journalEvent{Project: event.Context.Project, Run: event.Context.Run, Type: event.Type, Data: event.Data})
	}
	return refs, nil
}

// buildFailedRunTrace converts one run.failed event into the trace digest.
func buildFailedRunTrace(event journalEvent) agent.SupervisorTraceRef {
	errorType, _ := event.Data["error_type"].(string)
	errText, _ := event.Data["error"].(string)
	return agent.SupervisorTraceRef{RunID: event.Run, ErrorType: errorType, Error: errText}
}

// appendRunTraceDigest folds the run's problem events into the trace digest:
// failed tool names, the first tool failure detail, the last model failure.
func appendRunTraceDigest(ctx context.Context, observationURL, journalToken, runID string, trace *agent.SupervisorTraceRef) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(observationURL, "/")+"/v1/observations/events?run="+url.QueryEscape(runID)+"&limit=300", nil)
	if err != nil {
		return err
	}
	if journalToken != "" {
		request.Header.Set("Authorization", "Bearer "+journalToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("journal run events %s", journalErrorSnippet(response.StatusCode, string(body)))
	}
	var page journalTypedEvents
	if err := json.Unmarshal(body, &page); err != nil {
		return err
	}
	for _, event := range page.Events {
		switch event.Type {
		case "tool.failed":
			tool, _ := event.Data["tool"].(string)
			trace.Timeline = append(trace.Timeline, "tool failed: "+tool)
			detail, _ := event.Data["error"].(string)
			if detail != "" && len(trace.Timeline) < 24 {
				trace.Timeline = append(trace.Timeline, "  detail: "+detail)
			}
		case "model.failed":
			detail, _ := event.Data["error"].(string)
			if detail != "" && len(trace.Timeline) < 24 {
				trace.Timeline = append(trace.Timeline, "model failed: "+detail)
			}
		}
	}
	return nil
}

// journalErrorSnippet: status plus a short body/detail excerpt.
func journalErrorSnippet(status int, body string) string {
	snippet := strings.Join(strings.Fields(body), " ")
	if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", status, snippet)
}
