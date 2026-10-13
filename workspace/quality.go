package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Quality dashboard (specs/quality-loop.md §7): per-version aggregates over
// the data the quality loop already records — runs pinned to a definition
// version (000031), HITL verdicts (§3 / M1), suite evaluation runs (000048)
// and immutable version snapshots (000030). Verdict logic is never duplicated
// here: the aggregation groups the projections of hitlverdict.go (Verdict,
// projectTerminalEvents, ClassifyRunOutcome) per definition version.

// ErrVersionSnapshotMissing marks a diff request for a version without a
// stored snapshot or with an empty compiled prompt (§8.5 → 422).
var ErrVersionSnapshotMissing = errors.New("version has no snapshot or compiled prompt")

// Definition change kinds (§7.2).
const (
	DefinitionChangeAdded   = "added"
	DefinitionChangeRemoved = "removed"
	DefinitionChangeChanged = "changed"
)

// QualityEvaluation is the last suite run shown on a version row (§7.1,
// §8.4): the unified verdict (§5.2) and the judge agreement of the deciding
// calibration ride along so the dashboard shows gate-relevant status, not
// just counts. Verdict "" marks legacy rows written before migration
// 000056; judge_agreement is null when no judge calibration exists.
type QualityEvaluation struct {
	ID             int64     `json:"id"`
	Passed         int       `json:"passed"`
	Failed         int       `json:"failed"`
	PassRate       float64   `json:"pass_rate"`
	Verdict        string    `json:"verdict,omitempty"`
	JudgeAgreement *float64  `json:"judge_agreement"`
	CreatedAt      time.Time `json:"created_at"`
}

// QualityVersion is one row of the quality dashboard (§7.1, §8.4). A version
// without runs and without evaluations reports has_data=false with null
// metrics instead of a fake 0% (§7.4).
type QualityVersion struct {
	Version        int                `json:"version"`
	Runs           int                `json:"runs"`
	Confirmed      int                `json:"confirmed"`
	Corrected      int                `json:"corrected"`
	BestPassRate   *float64           `json:"best_pass_rate"`
	LastEvaluation *QualityEvaluation `json:"last_evaluation"`
	Goldens        int                `json:"goldens"`
	TTVIMinutes    *int64             `json:"ttvi_minutes"`
	HasData        bool               `json:"has_data"`
}

// QualityReport is the GET /quality payload (§8.4).
type QualityReport struct {
	AgentID  string           `json:"agent_id"`
	Versions []QualityVersion `json:"versions"`
}

// DefinitionChange is one structural difference between two snapshots (§7.2).
// Only purpose, constraints, completion and capability names are compared —
// prompt bodies never enter the structural diff; the compiled prompt is
// diffed line by line and prompt_override is covered through it.
type DefinitionChange struct {
	Field    string `json:"field"`
	Kind     string `json:"kind"`               // added | removed | changed
	Value    string `json:"value"`              // item (added/removed) or new value (changed)
	From     string `json:"from,omitempty"`     // previous value, changed only
	Redacted bool   `json:"redacted,omitempty"` // §10.3: value was masked
}

// VersionDiff is the GET /versions/diff payload (§8.5).
type VersionDiff struct {
	From              int                `json:"from"`
	To                int                `json:"to"`
	DefinitionChanges []DefinitionChange `json:"definition_changes"`
	PromptDiff        []string           `json:"prompt_diff"`
	Redacted          bool               `json:"redacted,omitempty"` // §10.3: any text was masked
}

// qualityRun is the lean workspace_run row the aggregation reads.
type qualityRun struct {
	runID        string
	status       string
	agentVersion int
}

// qualityTerminalEvent is one human terminal operation event of a run,
// ready for the shared §3.4 projection (M1 projectTerminalEvents).
type qualityTerminalEvent struct {
	runID string
	event terminalEvent
}

// AgentQualityReport loads the dashboard input and groups it per definition
// version (§7.1). Suite evaluations are read per PK-backed agent index (§10.2
// — no history scan beyond this agent's own rows).
func (s *Store) AgentQualityReport(ctx context.Context, agentID string) (QualityReport, error) {
	snapshots, err := s.qualitySnapshots(ctx, agentID)
	if err != nil {
		return QualityReport{}, err
	}
	runs, err := s.qualityRuns(ctx, agentID)
	if err != nil {
		return QualityReport{}, err
	}
	requests, err := s.qualityRequests(ctx, agentID)
	if err != nil {
		return QualityReport{}, err
	}
	terminal, err := s.qualityTerminalEvents(ctx, agentID)
	if err != nil {
		return QualityReport{}, err
	}
	evaluations, err := s.qualityEvaluationRuns(ctx, agentID)
	if err != nil {
		return QualityReport{}, err
	}
	// Judge agreement of the deciding calibration per judge model (§8.4):
	// models without a calibration row stay absent — the dashboard reports
	// null, never a fake 0.
	judgeAgreement := map[string]float64{}
	for _, evaluation := range evaluations {
		if evaluation.JudgeModel == "" {
			continue
		}
		if _, seen := judgeAgreement[evaluation.JudgeModel]; seen {
			continue
		}
		calibration, err := s.latestJudgeCalibration(ctx, evaluation.JudgeModel)
		if err != nil {
			return QualityReport{}, err
		}
		if calibration.JudgeModel != "" {
			judgeAgreement[evaluation.JudgeModel] = calibration.AgreementRate
		}
	}
	suite, err := s.GetAgentEvaluationSuite(ctx, agentID)
	if err != nil {
		return QualityReport{}, err
	}
	return buildQualityReport(agentID, snapshots, runs, requests, terminal, evaluations, judgeAgreement, countGoldenCases(suite.Cases)), nil
}

// qualitySnapshots reads version + creation time only: the report needs the
// snapshot list and the TTVI start, not the compiled prompts.
func (s *Store) qualitySnapshots(ctx context.Context, agentID string) ([]AgentVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT version, created_at FROM workspace_agent_version
		 WHERE agent_id = $1 ORDER BY version`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snapshots := []AgentVersion{}
	for rows.Next() {
		var version AgentVersion
		if err := rows.Scan(&version.Version, &version.CreatedAt); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, version)
	}
	return snapshots, rows.Err()
}

// qualityRuns reads every run of the agent with the version pin (000031) and
// the terminal status the §3.4 classification needs.
func (s *Store) qualityRuns(ctx context.Context, agentID string) ([]qualityRun, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT run_id, status, agent_version FROM workspace_run
		 WHERE agent_id = $1 ORDER BY created_at, id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []qualityRun{}
	for rows.Next() {
		var run qualityRun
		if err := rows.Scan(&run.runID, &run.status, &run.agentVersion); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// qualityRequests loads the ask_human rows of the agent's runs for the §3.3
// verdict projection. Rows are attributed through their run's version pin;
// legacy rows (agent_version=0) still classify the run they belong to.
func (s *Store) qualityRequests(ctx context.Context, agentID string) ([]HumanRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT hr.run_id, hr.status, hr.options, hr.response
		 FROM human_request hr
		 JOIN workspace_run r ON r.project_id = hr.project_id AND r.run_id = hr.run_id
		 WHERE r.agent_id = $1
		 ORDER BY hr.created_at, hr.id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := []HumanRequest{}
	for rows.Next() {
		var row HumanRequest
		var options []byte
		if err := rows.Scan(&row.RunID, &row.Status, &options, &row.Response); err != nil {
			return nil, err
		}
		if len(options) > 0 {
			_ = json.Unmarshal(options, &row.Options)
		}
		requests = append(requests, row)
	}
	return requests, rows.Err()
}

// qualityTerminalEvents loads source-2 verdict candidates (§3.4): terminal
// operation events a human produced, joined through the agent's runs. The
// actor *type* is scanned because the shared projection filters on it; the
// SQL predicate already restricts rows to human actors.
func (s *Store) qualityTerminalEvents(ctx context.Context, agentID string) ([]qualityTerminalEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT e.run_id, e.type, e.actor_type, e.data
		 FROM observation_events e
		 JOIN workspace_run r ON r.project_id = e.project_id AND r.run_id = e.run_id
		 WHERE r.agent_id = $1 AND e.actor_type = 'human'
		   AND e.type IN ('tool.completed','tool.failed','tool.blocked','operation.reconciled')
		 ORDER BY e.occurred_at, e.event_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []qualityTerminalEvent{}
	for rows.Next() {
		var item qualityTerminalEvent
		var eventType, actor string
		var raw []byte
		if err := rows.Scan(&item.runID, &eventType, &actor, &raw); err != nil {
			return nil, err
		}
		data := map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &data)
		}
		item.event = terminalEvent{eventType: eventType, actor: actor, data: data}
		events = append(events, item)
	}
	return events, rows.Err()
}

// qualityEvaluationRuns reads this agent's suite runs (000048), newest
// first, with the unified verdict and judge provenance (000056, §5.1, §6.2)
// the dashboard surfaces (§8.4).
func (s *Store) qualityEvaluationRuns(ctx context.Context, agentID string) ([]AgentEvaluationRun, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, agent_version, passed, failed, verdict, judge_model, created_at
		 FROM workspace_agent_evaluation_run
		 WHERE agent_id = $1
		 ORDER BY created_at DESC, id DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evaluations := []AgentEvaluationRun{}
	for rows.Next() {
		var evaluation AgentEvaluationRun
		if err := rows.Scan(&evaluation.ID, &evaluation.AgentVersion, &evaluation.Passed, &evaluation.Failed,
			&evaluation.Verdict, &evaluation.JudgeModel, &evaluation.CreatedAt); err != nil {
			return nil, err
		}
		evaluations = append(evaluations, evaluation)
	}
	return evaluations, rows.Err()
}

// versionBucket accumulates one version's run and evaluation counters.
type versionBucket struct {
	runs, confirmed, corrected int
	evaluations                []AgentEvaluationRun
}

// buildQualityReport groups runs, verdicts and evaluations per definition
// version (§7.1). Pure function over already-loaded rows so the dashboard
// math is unit-testable on a fixed dataset (§7 done criteria). Version rows
// follow the snapshot list; versions observed only in runs or evaluations are
// appended so no data disappears. Version 0 (unknown pin, §3.1) never enters
// the metrics. judgeAgreement maps a judge model to its latest calibration
// agreement (§6.4); models without calibration stay absent → null in §8.4.
func buildQualityReport(agentID string, snapshots []AgentVersion, runs []qualityRun, requests []HumanRequest, terminal []qualityTerminalEvent, evaluations []AgentEvaluationRun, judgeAgreement map[string]float64, goldens int) QualityReport {
	// Verdicts per run: ask_human projections (§3.3) plus human terminal
	// operation events (§3.4, source 2) through the shared M1 projection.
	verdictsByRun := map[string][]HITLVerdict{}
	for _, row := range requests {
		verdictType := Verdict(row)
		if verdictType == "" {
			continue
		}
		verdictsByRun[row.RunID] = append(verdictsByRun[row.RunID], HITLVerdict{
			Source:       VerdictSourceHumanRequest,
			Type:         verdictType,
			AgentVersion: row.AgentVersion,
		})
	}
	eventsByRun := map[string][]terminalEvent{}
	for _, item := range terminal {
		eventsByRun[item.runID] = append(eventsByRun[item.runID], item.event)
	}
	for runID, events := range eventsByRun {
		verdictsByRun[runID] = append(verdictsByRun[runID], projectTerminalEvents(events)...)
	}

	ordered := []int{}
	seen := map[int]bool{}
	snapshotAt := map[int]time.Time{}
	for _, snapshot := range snapshots {
		if !seen[snapshot.Version] {
			seen[snapshot.Version] = true
			ordered = append(ordered, snapshot.Version)
		}
		snapshotAt[snapshot.Version] = snapshot.CreatedAt
	}
	buckets := map[int]*versionBucket{}
	ensure := func(version int) *versionBucket {
		if buckets[version] == nil {
			buckets[version] = &versionBucket{}
		}
		if !seen[version] {
			seen[version] = true
			ordered = append(ordered, version)
		}
		return buckets[version]
	}
	for _, run := range runs {
		if run.agentVersion <= 0 {
			continue // version unknown: out of per-version metrics (§3.1)
		}
		bucket := ensure(run.agentVersion)
		bucket.runs++
		switch ClassifyRunOutcome(run.status, verdictsByRun[run.runID]) {
		case RunOutcomeConfirmed:
			bucket.confirmed++
		case RunOutcomeHumanCorrected:
			bucket.corrected++
		}
	}
	for _, evaluation := range evaluations {
		if evaluation.AgentVersion <= 0 {
			continue
		}
		bucket := ensure(evaluation.AgentVersion)
		bucket.evaluations = append(bucket.evaluations, evaluation)
	}
	sort.Ints(ordered)

	report := QualityReport{AgentID: agentID, Versions: []QualityVersion{}}
	for _, version := range ordered {
		bucket := buckets[version]
		if bucket == nil {
			bucket = &versionBucket{}
		}
		row := QualityVersion{
			Version:   version,
			Runs:      bucket.runs,
			Confirmed: bucket.confirmed,
			Corrected: bucket.corrected,
			Goldens:   goldens,
			HasData:   bucket.runs > 0 || len(bucket.evaluations) > 0,
		}
		// Newest first, then pick the last run, the best pass rate and the
		// earliest green run (§7.1 time-to-verified-improvement).
		sort.SliceStable(bucket.evaluations, func(i, j int) bool {
			return bucket.evaluations[i].CreatedAt.After(bucket.evaluations[j].CreatedAt)
		})
		foundBest := false
		bestRaw := 0.0
		var green *time.Time
		for i, evaluation := range bucket.evaluations {
			total := evaluation.Passed + evaluation.Failed
			rate := 0.0
			if total > 0 {
				rate = float64(evaluation.Passed) / float64(total)
			}
			if i == 0 {
				row.LastEvaluation = &QualityEvaluation{
					ID:        evaluation.ID,
					Passed:    evaluation.Passed,
					Failed:    evaluation.Failed,
					PassRate:  roundQualityRate(rate),
					Verdict:   evaluation.Verdict,
					CreatedAt: evaluation.CreatedAt,
				}
				// §8.4: the judge agreement rides along when the run was
				// scored by a judge model that has a calibration history.
				if agreement, ok := judgeAgreement[evaluation.JudgeModel]; ok {
					value := roundQualityRate(agreement)
					row.LastEvaluation.JudgeAgreement = &value
				}
			}
			if total > 0 && (!foundBest || rate > bestRaw) {
				foundBest, bestRaw = true, rate
			}
			if evaluation.Failed == 0 && evaluation.Passed > 0 {
				if green == nil || evaluation.CreatedAt.Before(*green) {
					at := evaluation.CreatedAt
					green = &at
				}
			}
		}
		if foundBest {
			best := roundQualityRate(bestRaw)
			row.BestPassRate = &best
		}
		if green != nil {
			if created, ok := snapshotAt[version]; ok {
				minutes := int64(green.Sub(created) / time.Minute)
				row.TTVIMinutes = &minutes
			}
		}
		report.Versions = append(report.Versions, row)
	}
	return report
}

// roundQualityRate keeps pass rates readable in JSON (0.6667, not
// 0.6666666666666666) without changing comparisons — the maximum is taken on
// raw rates first.
func roundQualityRate(value float64) float64 {
	return math.Round(value*10000) / 10000
}

// countGoldenCases counts suite cases harvested from production runs (§4.4:
// M2 stores them as golden/<run-id prefix>).
func countGoldenCases(cases []EvaluationCase) int {
	count := 0
	for _, testCase := range cases {
		if strings.HasPrefix(testCase.Name, "golden/") {
			count++
		}
	}
	return count
}

// GetAgentVersion loads one immutable snapshot (§7.2 diff input).
func (s *Store) GetAgentVersion(ctx context.Context, agentID string, version int) (AgentVersion, error) {
	var snapshot AgentVersion
	var definition []byte
	err := s.pool.QueryRow(ctx,
		`SELECT agent_id, version, definition, description, compiled_prompt, prompt_source, generator_model, author, created_at
		 FROM workspace_agent_version WHERE agent_id = $1 AND version = $2`, agentID, version).
		Scan(&snapshot.AgentID, &snapshot.Version, &definition, &snapshot.Description,
			&snapshot.CompiledPrompt, &snapshot.PromptSource, &snapshot.GeneratorModel,
			&snapshot.Author, &snapshot.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentVersion{}, ErrNotFound
	}
	if err != nil {
		return AgentVersion{}, err
	}
	if decoded := decodeAgentDefinition(definition); decoded != nil {
		snapshot.Definition = *decoded
	}
	return snapshot, nil
}

// AgentVersionsDiff compares two snapshots: structural definition changes
// plus the line-by-line diff of the compiled prompts (§7.2). A missing
// snapshot or an empty compiled prompt is ErrVersionSnapshotMissing (§8.5 →
// 422); texts are redacted before they leave storage (§10.3).
func (s *Store) AgentVersionsDiff(ctx context.Context, agentID string, from, to int) (VersionDiff, error) {
	fromSnapshot, err := s.GetAgentVersion(ctx, agentID, from)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return VersionDiff{}, fmt.Errorf("version %d: %w", from, ErrVersionSnapshotMissing)
		}
		return VersionDiff{}, err
	}
	toSnapshot, err := s.GetAgentVersion(ctx, agentID, to)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return VersionDiff{}, fmt.Errorf("version %d: %w", to, ErrVersionSnapshotMissing)
		}
		return VersionDiff{}, err
	}
	if fromSnapshot.CompiledPrompt == "" || toSnapshot.CompiledPrompt == "" {
		return VersionDiff{}, fmt.Errorf("versions %d and %d: %w", from, to, ErrVersionSnapshotMissing)
	}
	return VersionDiffOf(fromSnapshot, toSnapshot), nil
}

// VersionDiffOf builds the §8.5 payload from two snapshots and applies the
// §10.3 redaction: credential-looking fragments in definition changes and in
// changed prompt lines are masked before the response is written.
func VersionDiffOf(from, to AgentVersion) VersionDiff {
	diff := VersionDiff{
		From:              from.Version,
		To:                to.Version,
		DefinitionChanges: DefinitionDiff(from, to),
		PromptDiff:        LineDiff(from.CompiledPrompt, to.CompiledPrompt),
	}
	redacted := false
	for i := range diff.DefinitionChanges {
		if diff.DefinitionChanges[i].Redacted {
			redacted = true
		}
	}
	for i, line := range diff.PromptDiff {
		clean, hit := redactVerdictText(line)
		if hit {
			diff.PromptDiff[i] = clean
			redacted = true
		}
	}
	diff.Redacted = redacted
	return diff
}

// DefinitionDiff compares the structured fields of two snapshots (§7.2):
// purpose, constraints, completion and capability names — prompt bodies are
// excluded on purpose so the response never carries prompt_override text.
func DefinitionDiff(from, to AgentVersion) []DefinitionChange {
	changes := []DefinitionChange{}
	if from.Description != to.Description {
		changes = append(changes, DefinitionChange{
			Field: "purpose", Kind: DefinitionChangeChanged,
			Value: to.Description, From: from.Description,
		})
	}
	changes = append(changes, diffDefinitionList("constraints", from.Definition.Constraints, to.Definition.Constraints)...)
	changes = append(changes, diffDefinitionList("completion", from.Definition.Completion, to.Definition.Completion)...)
	changes = append(changes, diffDefinitionCapabilities(from.Definition.Capabilities, to.Definition.Capabilities)...)
	for i := range changes {
		value, redactedValue := redactVerdictText(changes[i].Value)
		changes[i].Value = value
		changes[i].Redacted = redactedValue
		if changes[i].Kind == DefinitionChangeChanged {
			previous, redactedFrom := redactVerdictText(changes[i].From)
			changes[i].From = previous
			changes[i].Redacted = changes[i].Redacted || redactedFrom
		}
	}
	return changes
}

// diffDefinitionList reports set-style membership changes of a string list:
// every item that appeared or disappeared becomes one entry (§7.2 example).
func diffDefinitionList(field string, from, to []string) []DefinitionChange {
	fromSet := make(map[string]bool, len(from))
	for _, item := range from {
		fromSet[item] = true
	}
	toSet := make(map[string]bool, len(to))
	for _, item := range to {
		toSet[item] = true
	}
	changes := []DefinitionChange{}
	for _, item := range to {
		if !fromSet[item] {
			changes = append(changes, DefinitionChange{Field: field, Kind: DefinitionChangeAdded, Value: item})
		}
	}
	for _, item := range from {
		if !toSet[item] {
			changes = append(changes, DefinitionChange{Field: field, Kind: DefinitionChangeRemoved, Value: item})
		}
	}
	return changes
}

// diffDefinitionCapabilities compares effective capability states by name
// (§7.2 «только имена/счётчики»): nil reads as allowed (AgentCapabilities.Cap),
// so only real on/off flips are reported.
func diffDefinitionCapabilities(from, to AgentCapabilities) []DefinitionChange {
	changes := []DefinitionChange{}
	report := func(name string, fromEnabled, toEnabled bool) {
		if fromEnabled == toEnabled {
			return
		}
		kind := DefinitionChangeAdded
		if !toEnabled {
			kind = DefinitionChangeRemoved
		}
		changes = append(changes, DefinitionChange{Field: "capabilities", Kind: kind, Value: name})
	}
	report("read_files", from.Cap(from.ReadFiles), to.Cap(to.ReadFiles))
	report("modify_files", from.Cap(from.ModifyFiles), to.Cap(to.ModifyFiles))
	report("run_commands", from.Cap(from.RunCommands), to.Cap(to.RunCommands))
	report("network", from.Cap(from.Network), to.Cap(to.Network))
	report("skills", from.Cap(from.Skills), to.Cap(to.Skills))
	report("knowledge", from.Cap(from.Knowledge), to.Cap(to.Knowledge))
	report("delegation", from.Cap(from.Delegation), to.Cap(to.Delegation))
	return changes
}

// maxLineDiffCells bounds the LCS table behind LineDiff; longer prompts fall
// back to the order-insensitive multiset comparison so a pathological prompt
// cannot blow up memory (§10.2).
const maxLineDiffCells = 4 << 20

// LineDiff renders the changed lines of two multiline texts as a compact
// "-"/"+" list without context lines — the line-oriented representation the
// skill_diff tool uses for SKILL.md changes (skills/diff.go), applied to the
// compiled prompt snapshots (§7.2).
func LineDiff(from, to string) []string {
	fromLines := strings.Split(from, "\n")
	toLines := strings.Split(to, "\n")
	n, m := len(fromLines), len(toLines)
	if n*m > maxLineDiffCells {
		return multisetLineDiff(fromLines, toLines)
	}
	// Longest common subsequence table: walks both texts and emits only the
	// lines that changed.
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case fromLines[i] == toLines[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	diff := []string{}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case fromLines[i] == toLines[j]:
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			diff = append(diff, "- "+fromLines[i])
			i++
		default:
			diff = append(diff, "+ "+toLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		diff = append(diff, "- "+fromLines[i])
	}
	for ; j < m; j++ {
		diff = append(diff, "+ "+toLines[j])
	}
	return diff
}

// multisetLineDiff is the order-insensitive fallback: removed lines first,
// then added ones (counts match the skills.DiffMarkdown approach).
func multisetLineDiff(fromLines, toLines []string) []string {
	remaining := map[string]int{}
	for _, line := range toLines {
		remaining[line]++
	}
	diff := []string{}
	for _, line := range fromLines {
		if remaining[line] > 0 {
			remaining[line]--
			continue
		}
		diff = append(diff, "- "+line)
	}
	original := map[string]int{}
	for _, line := range fromLines {
		original[line]++
	}
	for _, line := range toLines {
		if original[line] > 0 {
			original[line]--
			continue
		}
		diff = append(diff, "+ "+line)
	}
	return diff
}
