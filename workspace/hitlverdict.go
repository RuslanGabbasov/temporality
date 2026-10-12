package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// HITL verdicts as an eval signal (specs/quality-loop.md §3). Human decisions
// on a run are projected into typed verdicts attached to the agent definition
// version that was live when the ask was created. The delivery contract does
// not change: the projection reads fields the human_request rows and the
// observation journal already carry.

// Verdict types (§3.3).
const (
	VerdictAccepted  = "accepted"  // the human agreed with the agent's proposal
	VerdictCorrected = "corrected" // the human answered outside the options
	VerdictRejected  = "rejected"  // the human declined the proposal
)

// Verdict sources (§8.1): the ask_human row, a human decision on an
// operation, or a manual operation reconciliation.
const (
	VerdictSourceHumanRequest = "human_request"
	VerdictSourceApproval     = "approval"
	VerdictSourceReconcile    = "reconcile"
)

// Run outcome labels (§3.4): a run a human edited versus a run the human
// merely confirmed. Empty means the outcome is not settled deterministically.
const (
	RunOutcomeConfirmed      = "confirmed"
	RunOutcomeHumanCorrected = "human_corrected"
)

// Verdict projects one human_request row into its eval signal (§3.3):
//
//   - answered + the response equals one of the options exactly → accepted;
//   - answered + free text outside the options → corrected;
//   - rejected → rejected;
//   - expired → no verdict (tracked as hitl.expired-rate instead);
//   - pending | delivered | cancelled → no verdict yet.
//
// Matching is exact — no case or punctuation normalisation (v1 decision): a
// near-miss of an option still counts as a correction.
func Verdict(r HumanRequest) string {
	switch r.Status {
	case HumanStatusAnswered:
		for _, option := range r.Options {
			if option == r.Response {
				return VerdictAccepted
			}
		}
		return VerdictCorrected
	case HumanStatusRejected:
		return VerdictRejected
	default:
		return ""
	}
}

// HITLVerdict is one human verdict attached to a run (§8.1): its source, the
// operation it answers, the projected type, the author and — for ask_human
// answers — the agent version pinned at ask time. The answer is redacted
// before it leaves storage (§10.3).
type HITLVerdict struct {
	Source       string `json:"source"`
	OperationID  string `json:"operation_id"`
	Type         string `json:"type"`
	AnsweredBy   string `json:"answered_by"`
	Answer       string `json:"answer"`
	AgentVersion int    `json:"agent_version,omitempty"`
	Redacted     bool   `json:"redacted"`
}

// RunVerdicts projects every human decision attached to a run (§3, §8.1):
// ask_human rows carrying a verdict plus the terminal operation events a
// human produced (source 2: approval / manual reconciliation). The read has
// no side effects, so repeated calls return the same list (§8 idempotency).
func (s *Store) RunVerdicts(ctx context.Context, projectID, runID string) ([]HITLVerdict, error) {
	rows, err := s.ListHumanRequestsByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	verdicts := []HITLVerdict{}
	for _, row := range rows {
		verdictType := Verdict(row)
		if verdictType == "" {
			continue
		}
		answer, redacted := redactVerdictText(row.Response)
		verdicts = append(verdicts, HITLVerdict{
			Source:       VerdictSourceHumanRequest,
			OperationID:  row.ID,
			Type:         verdictType,
			AnsweredBy:   row.AnsweredBy,
			Answer:       answer,
			AgentVersion: row.AgentVersion,
			Redacted:     redacted,
		})
	}
	events, err := s.humanTerminalEvents(ctx, projectID, runID)
	if err != nil {
		return nil, err
	}
	return append(verdicts, events...), nil
}

// terminalVerdictEventTypes are the journal event types a human can settle
// one operation with (§3.4, source 2).
var terminalVerdictEventTypes = map[string]bool{
	"tool.completed":       true,
	"tool.failed":          true,
	"tool.blocked":         true,
	"operation.reconciled": true,
}

// terminalEvent is one observation-journal row feeding the verdict
// projection: type, actor and the JSONB payload.
type terminalEvent struct {
	eventType string
	actor     string
	data      map[string]any
}

// humanTerminalEvents loads the source-2 candidates: terminal operation
// events with a human actor (the run's project scopes the journal lookup;
// idx observation_events_run_time_idx serves it).
func (s *Store) humanTerminalEvents(ctx context.Context, projectID, runID string) ([]HITLVerdict, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT type, actor_id, data FROM observation_events
		 WHERE project_id = $1 AND run_id = $2 AND actor_type = 'human'
		   AND type IN ('tool.completed','tool.failed','tool.blocked','operation.reconciled')
		 ORDER BY occurred_at, event_id`, projectID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []terminalEvent{}
	for rows.Next() {
		var eventType, actor string
		var raw []byte
		if err := rows.Scan(&eventType, &actor, &raw); err != nil {
			return nil, err
		}
		data := map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &data)
		}
		events = append(events, terminalEvent{eventType: eventType, actor: actor, data: data})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return projectTerminalEvents(events), nil
}

// projectTerminalEvents maps source-2 events onto verdicts (§3.4):
// a human-completed operation is an acceptance, a human-blocked or
// human-failed one a rejection, a manual reconciliation a correction whose
// answer is the operator's note. Non-human actors and foreign event types
// are dropped defensively — the SQL filter already excludes them.
func projectTerminalEvents(events []terminalEvent) []HITLVerdict {
	verdicts := []HITLVerdict{}
	for _, event := range events {
		if event.actor != "human" || !terminalVerdictEventTypes[event.eventType] {
			continue
		}
		operationID, _ := event.data["operation_id"].(string)
		verdict := HITLVerdict{OperationID: operationID, AnsweredBy: event.actor}
		switch event.eventType {
		case "tool.completed":
			verdict.Source, verdict.Type = VerdictSourceApproval, VerdictAccepted
		case "tool.failed", "tool.blocked":
			verdict.Source, verdict.Type = VerdictSourceApproval, VerdictRejected
		case "operation.reconciled":
			verdict.Source, verdict.Type = VerdictSourceReconcile, VerdictCorrected
			note, _ := event.data["note"].(string)
			verdict.Answer, verdict.Redacted = redactVerdictText(note)
		}
		verdicts = append(verdicts, verdict)
	}
	return verdicts
}

// runSucceeded matches the §3.4 wording «ран завершился succeeded» onto the
// status vocabulary workspace_run actually records: the kernel's RunResult
// writes completed | failed | cancelled and legacy rows may carry other
// spellings, so both «succeeded» (spec) and «completed» (storage) count as
// the successful terminal state.
func runSucceeded(runStatus string) bool {
	return runStatus == "succeeded" || runStatus == "completed"
}

// ClassifyRunOutcome applies the §3.4 rule deterministically: a run is
// human-corrected when any verdict is a correction/rejection or a human
// intervened in its operations (approval/reconcile verdicts); it is confirmed
// only when it succeeded, every verdict is an acceptance and no human touched
// an operation. Anything else — unfinished, failed, cancelled, or an expired
// ask without a verdict — stays unsettled (empty string).
func ClassifyRunOutcome(runStatus string, verdicts []HITLVerdict) string {
	for _, verdict := range verdicts {
		if verdict.Type == VerdictCorrected || verdict.Type == VerdictRejected {
			return RunOutcomeHumanCorrected
		}
	}
	for _, verdict := range verdicts {
		if verdict.Source == VerdictSourceApproval || verdict.Source == VerdictSourceReconcile {
			return RunOutcomeHumanCorrected
		}
	}
	if !runSucceeded(runStatus) {
		return ""
	}
	return RunOutcomeConfirmed
}

// HitlExpiredRate is the §9 system-health aggregate: how many of the asks
// closed inside the window timed out instead of being answered. The rate is
// the expired share of terminal requests (answered | rejected | expired);
// open rows (pending/delivered) are not part of the denominator.
type HitlExpiredRate struct {
	WindowHours int     `json:"window_hours"`
	Total       int     `json:"total"`
	Expired     int     `json:"expired"`
	Rate        float64 `json:"rate"`
}

// hitlExpiredRate projects the counters into the aggregate (§9); the input
// window must be positive and the counters satisfy expired ≤ total.
func hitlExpiredRate(window time.Duration, total, expired int) HitlExpiredRate {
	hours := int(window.Hours())
	if hours < 1 {
		hours = 1
	}
	rate := 0.0
	if total > 0 {
		rate = float64(expired) / float64(total)
	}
	return HitlExpiredRate{WindowHours: hours, Total: total, Expired: expired, Rate: rate}
}

// HitlExpiredRate computes the hitl.expired.rate aggregate over requests
// created inside the window (§9 periodic system event).
func (s *Store) HitlExpiredRate(ctx context.Context, window time.Duration) (HitlExpiredRate, error) {
	var total, expired int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE status IN ('answered','rejected','expired')),
		        count(*) FILTER (WHERE status = 'expired')
		 FROM human_request
		 WHERE created_at >= now() - make_interval(secs => $1)`,
		window.Seconds()).Scan(&total, &expired)
	if err != nil {
		return HitlExpiredRate{}, err
	}
	return hitlExpiredRate(window, total, expired), nil
}

// verdictCredentialMarkers mirror the kernel's approval-preview markers
// (kernel/agent/approval_preview.go): a verdict answer must never leak a
// pasted credential. The workspace layer keeps its own copy because
// kernel/agent already depends on workspace — importing back would cycle.
var verdictCredentialMarkers = []string{
	"authorization:", "token=", "password=", "passwd=", "secret=",
	"api_key=", "api-key=", "credential=",
}

var verdictSensitiveNames = []string{
	"token", "password", "passwd", "secret", "api_key", "api-key",
	"authorization", "credential", "private_key", "private-key",
}

func verdictSensitiveName(value string) bool {
	for _, marker := range verdictSensitiveNames {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// verdictValueEnd scans past a credential value: a quoted value ends after
// its closing quote, a bare one at the first separator or whitespace.
func verdictValueEnd(value string, start int) int {
	if start < len(value) && (value[start] == '\'' || value[start] == '"') {
		quote := value[start]
		end := start + 1
		for end < len(value) && value[end] != quote {
			end++
		}
		if end < len(value) {
			end++
		}
		return end
	}
	end := start
	for end < len(value) && !strings.ContainsRune(" \t;|&,", rune(value[end])) {
		end++
	}
	return end
}

// redactVerdictText masks credential-looking fragments in a verdict answer
// (§10.3, «как approval preview») and reports whether anything was masked.
// Only the credential value is replaced — the rest of the sentence survives,
// so the answer stays readable as an eval signal. Every occurrence of every
// marker is masked in a single forward pass.
func redactVerdictText(value string) (string, bool) {
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "bearer ") {
		return "Bearer [REDACTED]", true
	}
	redacted := false
	// URL credentials: scheme://user:pass@host keeps only the host part.
	if scheme := strings.Index(lower, "://"); scheme >= 0 {
		start := scheme + 3
		if at := strings.Index(value[start:], "@"); at >= 0 {
			value = value[:start] + "[REDACTED]" + value[start+at:]
			lower = strings.ToLower(value)
			redacted = true
		}
	}
	pos := 0
	for pos < len(value) {
		at, marker := -1, ""
		for _, candidate := range verdictCredentialMarkers {
			idx := strings.Index(lower[pos:], candidate)
			if idx >= 0 && (at < 0 || pos+idx < at) {
				at, marker = pos+idx, candidate
			}
		}
		if at < 0 {
			break
		}
		start := at + len(marker)
		end := verdictValueEnd(value, start)
		value = value[:start] + "[REDACTED]" + value[end:]
		lower = strings.ToLower(value)
		redacted = true
		pos = start + len("[REDACTED]")
	}
	if redacted {
		return value, true
	}
	// Bare sensitive key=value / key: value without a known marker.
	if at := strings.IndexAny(value, "=:"); at > 0 && verdictSensitiveName(strings.TrimLeft(strings.ToLower(value[:at]), "-")) {
		start := at + 1
		return value[:start] + "[REDACTED]" + value[verdictValueEnd(value, start):], true
	}
	return value, false
}
