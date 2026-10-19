package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Golden eval-case candidates (specs/quality-loop.md §4): a production run
// whose outcome looks trustworthy is projected into a candidate row; only a
// human decision (accept/reject) lets it into the agent's eval suite.
//
// Schema guarantees (§4.3, §10.1): the single INSERT funnel
// (CreateEvalCaseCandidate) masks PII before storage, one row per run through
// the unique (run_id) index with upsert do-nothing, and rejected rows are
// reaped after 90 days (§10.2). Candidate selection reuses the M1 verdict
// projection (RunVerdicts) instead of re-deriving verdicts.

// Candidate sources (§4.2).
const (
	EvalCandidateSourceSucceededRun   = "succeeded_run"
	EvalCandidateSourceHumanCorrected = "human_corrected"
)

// Candidate review verdicts (§4.1): pending until a human decides.
const (
	EvalCandidateVerdictPending  = "pending"
	EvalCandidateVerdictAccepted = "accepted"
	EvalCandidateVerdictRejected = "rejected"
)

// evalSuiteMaxCases mirrors the suite ceiling enforced by the PUT endpoint
// (§4.4: accept returns 422 when the suite is full).
const evalSuiteMaxCases = 16

// evalCandidateMaxAnswer mirrors agentEvalMaxAnswer (kernel/agent/agenteval.go):
// a longer run answer never becomes a golden (§4.2а, "limit as in agenteval").
const evalCandidateMaxAnswer = 8192

// evalCandidateRetentionDays is the §10.2 reap policy for rejected candidates;
// the same age bounds harvesting so a reaped run is never re-collected.
const evalCandidateRetentionDays = 90

// evalProjectSharePercentMax is the §4.5 anti-contamination ceiling: at most
// 70 % of an agent's accepted goldens may come from one project.
const evalProjectSharePercentMax = 70

// Candidate review outcomes the API layer maps to §8.2 codes (409/422).
var (
	ErrCandidateDecided = errors.New("candidate already decided")
	ErrSuiteFull        = errors.New("at most 16 evaluation cases are supported")
	ErrProjectShare     = errors.New("project share of accepted goldens would exceed 70%")
)

// EvalCaseCandidate is one harvested production run awaiting human review
// (§4.1). Input is the task text, ExpectedOutput the run's final answer or,
// for source=human_corrected, the human's correction.
type EvalCaseCandidate struct {
	ID               int64      `json:"id"`
	AgentID          string     `json:"agent_id"`
	RunID            string     `json:"run_id"`
	TaskID           string     `json:"task_id,omitempty"`
	ProjectID        string     `json:"project_id,omitempty"`
	AgentVersion     int        `json:"agent_version,omitempty"`
	Input            string     `json:"input"`
	ExpectedOutput   string     `json:"expected_output"`
	Source           string     `json:"source"`
	Verdict          string     `json:"verdict"`
	RedactionApplied bool       `json:"redaction_applied"`
	Redactions       int        `json:"redactions"`
	ReviewedBy       string     `json:"reviewed_by,omitempty"`
	ReviewedAt       *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

const evalCaseCandidateColumns = `id, agent_id, run_id, task_id, project_id, agent_version, input,
expected_output, source, verdict, redaction_applied, redactions, reviewed_by, reviewed_at, created_at`

func scanEvalCaseCandidate(scanner interface{ Scan(dest ...any) error }) (EvalCaseCandidate, error) {
	var c EvalCaseCandidate
	var reviewedAt *time.Time
	err := scanner.Scan(&c.ID, &c.AgentID, &c.RunID, &c.TaskID, &c.ProjectID, &c.AgentVersion,
		&c.Input, &c.ExpectedOutput, &c.Source, &c.Verdict, &c.RedactionApplied, &c.Redactions,
		&c.ReviewedBy, &reviewedAt, &c.CreatedAt)
	c.ReviewedAt = reviewedAt
	return c, err
}

// ---------------------------------------------------------------------------
// Selection rules (§4.2, v1 deterministic)
// ---------------------------------------------------------------------------

// evalRunIsTerminal reports whether a run reached a harvestable final state.
// Cancelled runs never qualify (§4.2); the *_limit statuses are forced-finale
// outcomes — terminal, but not a success for rule (а).
func evalRunIsTerminal(status string) bool {
	switch status {
	case "completed", "succeeded", "failed", "turn_limit", "time_limit", "token_limit", "budget_limit":
		return true
	default:
		return false
	}
}

// evalRunSucceeded reports the §4.2а success state. The codebase records
// "completed"; "succeeded" is accepted too so the spec's literal wording and
// legacy rows match the same rule.
func evalRunSucceeded(status string) bool {
	return status == "completed" || status == "succeeded"
}

// evalCandidateSelection applies the §4.2 rules to one finished run:
//
//   - a rejected verdict, a cancelled run or an unfinished run → no candidate;
//   - any corrected verdict → source=human_corrected with the human's
//     correction as expected_output (§4.2б, "the run finished + corrected");
//   - success with an answer of at most 8192 bytes and no correction →
//     source=succeeded_run with the run's answer (§4.2а);
//   - anything else (failed run without correction, oversized or empty
//     answer, correction without text) → no candidate.
//
// Verdicts come from the M1 projection (RunVerdicts); effect=uncertain is
// checked separately by the caller (§4.2).
func evalCandidateSelection(runStatus, answer string, verdicts []HITLVerdict) (source, expected string, ok bool) {
	if !evalRunIsTerminal(runStatus) {
		return "", "", false
	}
	correction, corrected := "", false
	for _, verdict := range verdicts {
		if verdict.Type == VerdictRejected {
			return "", "", false // §4.2: rejected verdicts disqualify the run
		}
		if verdict.Type == VerdictCorrected {
			corrected = true
			if correction == "" {
				correction = verdict.Answer // first non-empty correction wins
			}
		}
	}
	if corrected {
		if strings.TrimSpace(correction) == "" {
			return "", "", false // corrected but no usable correction text
		}
		return EvalCandidateSourceHumanCorrected, correction, true
	}
	if evalRunSucceeded(runStatus) && answer != "" && len(answer) <= evalCandidateMaxAnswer {
		return EvalCandidateSourceSucceededRun, answer, true
	}
	return "", "", false
}

// evalProjectShareAllows implements the §4.5 anti-contamination rule over the
// accepted-candidate counts per project: after accepting a candidate from
// project, no single project may hold more than 70 % of the agent's accepted
// goldens. Integer arithmetic keeps the boundary exact (exactly 70 % passes);
// an agent without accepted goldens always accepts its first candidate — the
// rule must not block the bootstrap.
func evalProjectShareAllows(counts map[string]int, project string) bool {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return true
	}
	return (counts[project]+1)*100 <= evalProjectSharePercentMax*(total+1)
}

// GoldenCaseName is the suite case name of an accepted candidate (§4.4):
// golden/<run_id-prefix>, the prefix being the first 8 bytes of the run id.
func GoldenCaseName(runID string) string {
	prefix := runID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return "golden/" + prefix
}

// ---------------------------------------------------------------------------
// PII masking (§4.3): mandatory before INSERT
// ---------------------------------------------------------------------------

// candidateCredentialKeys mirror the kernel's approval-preview markers
// (kernel/agent/approval_preview.go credentialValueKeys). The workspace layer
// keeps its own copy so the storage write path never depends on kernel
// internals — the same decision workspace/hitlverdict.go documents for M1.
var candidateCredentialKeys = []string{
	"authorization:", "token=", "password=", "passwd=", "secret=",
	"api_key=", "api-key=", "credential=",
}

var candidateSensitiveNames = []string{
	"token", "password", "passwd", "secret", "api_key", "api-key",
	"authorization", "credential", "private_key", "private-key",
}

func candidateSensitiveName(value string) bool {
	for _, marker := range candidateSensitiveNames {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// candidateRedactOnce is a copy of redactApprovalText
// (kernel/agent/approval_preview.go) adapted for free-form candidate text:
// it reports where the inserted [REDACTED] mask ends so the caller can resume
// scanning after it (the kernel helper stops at the first match of the whole
// string), and it keeps the text following a leading "Bearer …" (the kernel
// truncates the tail, which would corrupt a task or answer starting with a
// pasted credential line).
func candidateRedactOnce(value string) (next string, consumed int, changed bool) {
	const mask = "[REDACTED]"
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "bearer ") {
		rest := value[len("bearer "):]
		if space := strings.IndexAny(rest, " \t\r\n"); space >= 0 {
			rest = rest[space:]
		} else {
			rest = ""
		}
		masked := "Bearer " + mask + rest
		return masked, len("Bearer ") + len(mask), true
	}
	if scheme := strings.Index(lower, "://"); scheme >= 0 {
		start := scheme + 3
		if at := strings.Index(value[start:], "@"); at >= 0 {
			return value[:start] + mask + value[start+at:], start + len(mask), true
		}
	}
	for _, key := range candidateCredentialKeys {
		if at := strings.Index(lower, key); at >= 0 {
			start := at + len(key)
			end := start
			if start < len(value) && (value[start] == '\'' || value[start] == '"') {
				quote := value[start]
				end = start + 1
				for end < len(value) && value[end] != quote {
					end++
				}
				if end < len(value) {
					end++
				}
			} else {
				for end < len(value) && !strings.ContainsRune(" \t;|&,", rune(value[end])) {
					end++
				}
			}
			return value[:start] + mask + value[end:], start + len(mask), true
		}
	}
	if at := strings.IndexAny(value, "=:"); at > 0 && candidateSensitiveName(strings.TrimLeft(strings.ToLower(value[:at]), "-")) {
		start := at + 1
		for start < len(value) && (value[start] == ' ' || value[start] == '\t') {
			start++
		}
		end := start
		for end < len(value) && !strings.ContainsRune(" \t\r\n;|&,", rune(value[end])) {
			end++
		}
		if end == start {
			return value, len(value), false // marker with no value: nothing to mask
		}
		return value[:start] + mask + value[end:], start + len(mask), true
	}
	return value, len(value), false
}

// redactCandidateText masks every credential-looking fragment in one candidate
// field and counts the masked fragments (§4.3). The scan resumes after each
// inserted [REDACTED], so repeated occurrences of one marker
// ("token=a … token=b") are all masked — the kernel helper, applied to the
// whole string, would only ever reach the first. Each pass consumes at least
// one mask (≥10 bytes), so the loop always terminates.
func redactCandidateText(value string) (string, int) {
	redactions := 0
	from := 0
	for from < len(value) {
		next, consumed, changed := candidateRedactOnce(value[from:])
		if !changed {
			break
		}
		value = value[:from] + next
		from += consumed
		redactions++
	}
	return value, redactions
}

// prepareEvalCaseCandidateForInsert is the single pre-INSERT funnel (§4.3):
// masking of input and expected_output is unconditional, and the flag/count
// columns record what masking did. CreateEvalCaseCandidate calls it first, so
// no row can reach the table without passing through the primitive — a clean
// text stores redaction_applied=false with zero redactions, a text carrying
// secrets stores masked fragments and redaction_applied=true.
func prepareEvalCaseCandidateForInsert(c *EvalCaseCandidate) {
	input, inputRedactions := redactCandidateText(c.Input)
	expected, expectedRedactions := redactCandidateText(c.ExpectedOutput)
	c.Input = input
	c.ExpectedOutput = expected
	c.Redactions = inputRedactions + expectedRedactions
	c.RedactionApplied = c.Redactions > 0
	if c.Verdict == "" {
		c.Verdict = EvalCandidateVerdictPending
	}
}

// CreateEvalCaseCandidate stores one candidate with mandatory masking.
// Idempotent by run_id (§10.1г): a duplicate run reports created=false and
// updates nothing, so retries never duplicate rows or re-emit the created
// event (the caller emits only when created is true).
func (s *Store) CreateEvalCaseCandidate(ctx context.Context, c *EvalCaseCandidate) (bool, error) {
	if c.AgentID == "" || c.RunID == "" || c.Source == "" {
		return false, errors.New("agent_id, run_id and source are required")
	}
	if c.Source != EvalCandidateSourceSucceededRun && c.Source != EvalCandidateSourceHumanCorrected {
		return false, fmt.Errorf("unknown candidate source %q", c.Source)
	}
	prepareEvalCaseCandidateForInsert(c)
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO workspace_eval_case_candidate
		 (agent_id, run_id, task_id, project_id, agent_version, input, expected_output,
		  source, verdict, redaction_applied, redactions)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 ON CONFLICT (run_id) DO NOTHING
		 RETURNING id, created_at`,
		c.AgentID, c.RunID, c.TaskID, c.ProjectID, c.AgentVersion, c.Input, c.ExpectedOutput,
		c.Source, c.Verdict, c.RedactionApplied, c.Redactions).
		Scan(&id, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // run already harvested — idempotent do-nothing
	}
	if err != nil {
		return false, err
	}
	c.ID = id
	return true, nil
}

// ListEvalCaseCandidates returns candidates for the review queue (§4.1),
// newest first; an empty verdict filter returns every verdict so decided
// history stays visible. limit 0 selects the default of 100 (cap 200).
func (s *Store) ListEvalCaseCandidates(ctx context.Context, agentID, verdict string, limit int) ([]EvalCaseCandidate, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT ` + evalCaseCandidateColumns + ` FROM workspace_eval_case_candidate WHERE agent_id = $1`
	args := []any{agentID}
	if verdict != "" {
		query += ` AND verdict = $2`
		args = append(args, verdict)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EvalCaseCandidate{}
	for rows.Next() {
		c, err := scanEvalCaseCandidate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// GetEvalCaseCandidate loads one candidate scoped to its agent.
func (s *Store) GetEvalCaseCandidate(ctx context.Context, agentID string, id int64) (EvalCaseCandidate, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+evalCaseCandidateColumns+` FROM workspace_eval_case_candidate WHERE id = $1 AND agent_id = $2`,
		id, agentID)
	c, err := scanEvalCaseCandidate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return EvalCaseCandidate{}, ErrNotFound
	}
	return c, err
}

// AcceptEvalCaseCandidate decides a pending candidate into the suite (§4.4).
// One transaction under a per-agent advisory lock makes the §4.4 suite limit
// (16) and the §4.5 70 % rule hold under concurrent accepts; the verdict flip
// is guarded by verdict='pending' so a repeated accept is a conflict, never a
// second suite entry (§8.2 idempotency).
func (s *Store) AcceptEvalCaseCandidate(ctx context.Context, agentID string, id int64, reviewer string) (EvalCaseCandidate, AgentEvaluationSuite, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize reviewers of one agent for the duration of the decision.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		"eval-case-candidate:"+agentID); err != nil {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}

	row := tx.QueryRow(ctx,
		`SELECT `+evalCaseCandidateColumns+` FROM workspace_eval_case_candidate WHERE id = $1 AND agent_id = $2`,
		id, agentID)
	candidate, err := scanEvalCaseCandidate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, ErrNotFound
	}
	if err != nil {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	if candidate.Verdict != EvalCandidateVerdictPending {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, ErrCandidateDecided
	}

	suite := AgentEvaluationSuite{AgentID: agentID, Cases: []EvaluationCase{}}
	var raw []byte
	err = tx.QueryRow(ctx,
		`SELECT cases, updated_at FROM workspace_agent_evaluation_suite WHERE agent_id = $1`, agentID).
		Scan(&raw, &suite.UpdatedAt)
	switch {
	case err == nil:
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &suite.Cases); err != nil {
				return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// No suite yet — the first accept creates the row.
	default:
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	if len(suite.Cases) >= evalSuiteMaxCases {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, ErrSuiteFull
	}

	// Anti-contamination (§4.5): counts are taken before this candidate flips
	// to accepted, so the candidate never counts as its own predecessor.
	counts, err := evalAcceptedProjectCounts(ctx, tx, agentID)
	if err != nil {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	if !evalProjectShareAllows(counts, candidate.ProjectID) {
		total, same := 0, 0
		for _, n := range counts {
			total += n
		}
		same = counts[candidate.ProjectID]
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, fmt.Errorf(
			"%w: accepting this candidate would put %d of %d goldens in project %q",
			ErrProjectShare, same+1, total+1, candidate.ProjectID)
	}

	flip := tx.QueryRow(ctx,
		`UPDATE workspace_eval_case_candidate
		 SET verdict = $3, reviewed_by = $4, reviewed_at = now()
		 WHERE id = $1 AND agent_id = $2 AND verdict = 'pending'
		 RETURNING reviewed_at`,
		id, agentID, EvalCandidateVerdictAccepted, reviewer)
	var reviewedAt time.Time
	if err := flip.Scan(&reviewedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EvalCaseCandidate{}, AgentEvaluationSuite{}, ErrCandidateDecided
		}
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	candidate.Verdict = EvalCandidateVerdictAccepted
	candidate.ReviewedBy = reviewer
	candidate.ReviewedAt = &reviewedAt

	// §13 option B: the golden's expected output freezes the business rules
	// of the day it was reviewed — after DefaultSuiteStaleDays it turns
	// stale and the apply gate refuses with suite_stale instead of
	// masquerading drift as an agent regression.
	staleAfter := reviewedAt.AddDate(0, 0, DefaultSuiteStaleDays)
	suite.Cases = append(suite.Cases, EvaluationCase{
		Name:           GoldenCaseName(candidate.RunID),
		Input:          candidate.Input,
		ExpectedOutput: candidate.ExpectedOutput,
		StaleAfter:     &staleAfter,
	})
	encoded, err := json.Marshal(suite.Cases)
	if err != nil {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO workspace_agent_evaluation_suite (agent_id, cases, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (agent_id) DO UPDATE SET cases = EXCLUDED.cases, updated_at = now()
		 RETURNING updated_at`,
		agentID, encoded).Scan(&suite.UpdatedAt)
	if err != nil {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EvalCaseCandidate{}, AgentEvaluationSuite{}, err
	}
	return candidate, suite, nil
}

// RejectEvalCaseCandidate decides a pending candidate out of the suite path
// (§4.4): rejected candidates stay in history for the retention period and a
// repeated reject is a conflict, not a second decision.
func (s *Store) RejectEvalCaseCandidate(ctx context.Context, agentID string, id int64, reviewer string) (EvalCaseCandidate, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE workspace_eval_case_candidate
		 SET verdict = $3, reviewed_by = $4, reviewed_at = now()
		 WHERE id = $1 AND agent_id = $2 AND verdict = 'pending'
		 RETURNING `+evalCaseCandidateColumns,
		id, agentID, EvalCandidateVerdictRejected, reviewer)
	c, err := scanEvalCaseCandidate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.GetEvalCaseCandidate(ctx, agentID, id); getErr != nil {
			return EvalCaseCandidate{}, getErr
		}
		return EvalCaseCandidate{}, ErrCandidateDecided
	}
	return c, err
}

// evalAcceptedProjectCounts aggregates accepted candidates per project for
// the §4.5 rule.
func evalAcceptedProjectCounts(ctx context.Context, tx pgx.Tx, agentID string) (map[string]int, error) {
	rows, err := tx.Query(ctx,
		`SELECT COALESCE(NULLIF(project_id, ''), '(none)'), COUNT(*)::int
		 FROM workspace_eval_case_candidate
		 WHERE agent_id = $1 AND verdict = 'accepted'
		 GROUP BY project_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var project string
		var n int
		if err := rows.Scan(&project, &n); err != nil {
			return nil, err
		}
		counts[project] = n
	}
	return counts, rows.Err()
}

// PurgeRejectedEvalCaseCandidates enforces the §10.2 retention policy:
// rejected candidates older than 90 days are deleted (decided history of
// recent reviews stays visible). Returns the number of reaped rows.
func (s *Store) PurgeRejectedEvalCaseCandidates(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM workspace_eval_case_candidate
		 WHERE verdict = 'rejected' AND created_at < now() - make_interval(days => $1)`,
		evalCandidateRetentionDays)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Harvesting (§4.2): finished runs → candidates
// ---------------------------------------------------------------------------

// evalHarvestBatch bounds one harvesting tick; evalHarvestSettleMinutes gives
// the journal publisher time to land the run's terminal tool events before
// the effect=uncertain check runs (a premature check would only defer the
// candidate to a later tick — the unique run_id index keeps it idempotent).
const (
	evalHarvestBatch          = 200
	evalHarvestSettleMinutes  = 2
	evalHarvestTerminalStatus = `'completed','succeeded','failed','turn_limit','time_limit','token_limit','budget_limit'`
)

// HarvestableRun is the finished-run slice the harvester judges: run identity
// plus the task text that becomes the candidate input (§4.1 "input — текст
// задачи").
type HarvestableRun struct {
	RunID        string
	TaskID       string
	ProjectID    string
	AgentID      string
	AgentVersion int
	Status       string
	Answer       string
	TaskPrompt   string
}

// harvestableEvalRuns lists finished runs without a candidate yet: terminal
// status, inside the retention window (so a reaped run is never re-collected)
// and past the journal settle window.
func (s *Store) harvestableEvalRuns(ctx context.Context) ([]HarvestableRun, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.run_id, r.task_id, r.project_id, r.agent_id, r.agent_version, r.status, r.answer, t.prompt
		 FROM workspace_run r
		 JOIN workspace_task t ON t.id = r.task_id
		 LEFT JOIN workspace_eval_case_candidate c ON c.run_id = r.run_id
		 WHERE c.id IS NULL
		   AND r.agent_id IS NOT NULL
		   AND r.status IN (`+evalHarvestTerminalStatus+`)
		   AND r.created_at > now() - make_interval(days => $1)
		   AND r.updated_at < now() - make_interval(mins => $2)
		 ORDER BY r.updated_at DESC
		 LIMIT $3`,
		evalCandidateRetentionDays, evalHarvestSettleMinutes, evalHarvestBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []HarvestableRun
	for rows.Next() {
		var run HarvestableRun
		if err := rows.Scan(&run.RunID, &run.TaskID, &run.ProjectID, &run.AgentID,
			&run.AgentVersion, &run.Status, &run.Answer, &run.TaskPrompt); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

// HarvestEvalCaseCandidates is the §4.2 selection pass: for every eligible
// finished run it loads the M1 verdicts, checks the effect=uncertain
// exclusion and, when the rules select the run, inserts one candidate
// (redacted, idempotent by run_id). Returns the candidates actually created
// so the caller emits eval.case_candidate.created exactly once per candidate;
// on a mid-batch error the already-created slice is returned together with
// the error so their events are not lost.
func (s *Store) HarvestEvalCaseCandidates(ctx context.Context) ([]EvalCaseCandidate, error) {
	runs, err := s.harvestableEvalRuns(ctx)
	if err != nil {
		return nil, err
	}
	created := []EvalCaseCandidate{}
	for _, run := range runs {
		if strings.TrimSpace(run.TaskPrompt) == "" {
			continue
		}
		verdicts, err := s.RunVerdicts(ctx, run.ProjectID, run.RunID)
		if err != nil {
			return created, err
		}
		uncertain, err := s.runHasUncertainEffect(ctx, run.ProjectID, run.RunID)
		if err != nil {
			return created, err
		}
		if uncertain {
			continue // §4.2: unreconciled operations never become goldens
		}
		source, expected, ok := evalCandidateSelection(run.Status, run.Answer, verdicts)
		if !ok {
			continue
		}
		candidate := EvalCaseCandidate{
			AgentID: run.AgentID, RunID: run.RunID, TaskID: run.TaskID,
			ProjectID: run.ProjectID, AgentVersion: run.AgentVersion,
			Input: run.TaskPrompt, ExpectedOutput: expected, Source: source,
		}
		inserted, err := s.CreateEvalCaseCandidate(ctx, &candidate)
		if err != nil {
			return created, err
		}
		if inserted {
			created = append(created, candidate)
		}
	}
	return created, nil
}

// harvestToolEvent is the journal slice the effect check needs (§4.2:
// effect=uncertain / never-settled operations).
type harvestToolEvent struct {
	Type        string
	OperationID string
	Effect      string
	ErrorType   string
}

// runHasUncertainEffect mirrors the kernel's effect=uncertain rule
// (kernel/agent/reconcile.go UncertainOperations) scoped to one run: a
// tool.failed marked effect=uncertain (legacy: error_type=activity_failed)
// or a tool.started whose terminal event never landed means the operation's
// effect is unreconciled — such runs are excluded from harvesting (§4.2).
func (s *Store) runHasUncertainEffect(ctx context.Context, projectID, runID string) (bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT type, data FROM observation_events
		 WHERE project_id = $1 AND run_id = $2
		   AND type IN ('tool.started','tool.completed','tool.failed','tool.blocked','operation.reconciled')
		 ORDER BY occurred_at, event_id`, projectID, runID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	events := []harvestToolEvent{}
	for rows.Next() {
		var eventType string
		var raw []byte
		if err := rows.Scan(&eventType, &raw); err != nil {
			return false, err
		}
		event := harvestToolEvent{Type: eventType}
		if len(raw) > 0 {
			data := map[string]any{}
			_ = json.Unmarshal(raw, &data)
			event.OperationID, _ = data["operation_id"].(string)
			event.Effect, _ = data["effect"].(string)
			event.ErrorType, _ = data["error_type"].(string)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return uncertainEffectFromEvents(events), nil
}

// uncertainEffectFromEvents folds the run's tool lifecycle events into the
// §4.2 exclusion verdict: any operation opened by tool.started but never
// settled (completed/blocked/reconciled/failed), or any failure whose effect
// the journal could not prove, leaves the run unreconciled. Events without an
// operation id are ignored, as in the kernel projection.
func uncertainEffectFromEvents(events []harvestToolEvent) bool {
	open := map[string]bool{}
	for _, event := range events {
		if event.OperationID == "" && event.Type == "tool.started" {
			continue
		}
		switch event.Type {
		case "tool.started":
			open[event.OperationID] = true
		case "tool.completed", "tool.blocked", "operation.reconciled", "tool.failed":
			delete(open, event.OperationID)
			if event.Type == "tool.failed" && failedEffectUncertain(event) {
				return true
			}
		}
	}
	return len(open) > 0
}

// failedEffectUncertain copies the kernel classification
// (kernel/agent/reconcile.go failedUncertain): effect "none" settles the
// operation, "uncertain" unsettles it, unmarked legacy failures count when
// they happened at/after the execution boundary (activity_failed).
func failedEffectUncertain(event harvestToolEvent) bool {
	switch event.Effect {
	case "none":
		return false
	case "uncertain":
		return true
	}
	return event.ErrorType == "activity_failed"
}

// PendingRunRefresh lists runs whose row is still non-terminal and recent
// enough to ask Temporal about: workspace_run.status only advances when the
// run row is refreshed (GET /v1/workspace/runs/{runID} does it on demand),
// and the golden harvester needs the terminal status plus the answer. Bounded
// newest-first so stale undescribable rows never starve fresh ones.
func (s *Store) PendingRunRefresh(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, COALESCE(task_id, '') AS task_id, project_id, agent_id, agent_version, exec_context, run_id, status,
		        model, answer, turns, error, created_at, updated_at
		 FROM workspace_run
		 WHERE status NOT IN ('completed','succeeded','failed','turn_limit','time_limit',
		                      'token_limit','budget_limit','cancelled','canceled')
		   AND created_at > now() - interval '7 days'
		 ORDER BY updated_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Run
	for rows.Next() {
		var r Run
		var agentID *string
		var execContext []byte
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ProjectID, &agentID, &r.AgentVersion, &execContext,
			&r.RunID, &r.Status, &r.Model, &r.Answer, &r.Turns, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.AgentID = derefPtr(agentID)
		r.ExecContext = decodeExecContext(execContext)
		result = append(result, r)
	}
	return result, rows.Err()
}
