package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Immutable agent versions and their diffs (docs/agent-evolution.md §2.1, §4):
// a version is a release — created by a manual edit, an applied proposal or a
// rollback — and history is never rewritten, only extended. The structural
// and prompt diffs of two snapshots back the unchanged versions/diff and
// proposals/{id}/diff endpoints; they moved here from the retired quality
// dashboard (docs/agent-quality-loop.md).

// ErrVersionSnapshotMissing marks a diff request for a version without a
// stored snapshot or with an empty compiled prompt (§8.5 → 422).
var ErrVersionSnapshotMissing = errors.New("version has no snapshot or compiled prompt")

// Definition change kinds (§7.2).
const (
	DefinitionChangeAdded   = "added"
	DefinitionChangeRemoved = "removed"
	DefinitionChangeChanged = "changed"
)

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

// agentVersionColumns is the projection every version read scans — the column
// order and scanAgentVersion are position-sensitive. The lineage columns are
// nullable for pre-v2 rows (docs/agent-evolution.md §2.1), so reads fold them
// to zero values.
const agentVersionColumns = `agent_id, version, definition, description, compiled_prompt, prompt_source,
	generator_model, author, created_at, kind, COALESCE(baseline_version, 0), COALESCE(rollback_of, 0), change_summary`

func scanAgentVersion(row interface{ Scan(dest ...any) error }) (AgentVersion, error) {
	var v AgentVersion
	var definition []byte
	if err := row.Scan(&v.AgentID, &v.Version, &definition, &v.Description, &v.CompiledPrompt,
		&v.PromptSource, &v.GeneratorModel, &v.Author, &v.CreatedAt,
		&v.Kind, &v.BaselineVersion, &v.RollbackOf, &v.ChangeSummary); err != nil {
		return AgentVersion{}, err
	}
	if def := decodeAgentDefinition(definition); def != nil {
		v.Definition = *def
	}
	return v, nil
}

// GetAgentVersion loads one immutable snapshot (§7.2 diff input).
func (s *Store) GetAgentVersion(ctx context.Context, agentID string, version int) (AgentVersion, error) {
	snapshot, err := scanAgentVersion(s.pool.QueryRow(ctx,
		`SELECT `+agentVersionColumns+` FROM workspace_agent_version WHERE agent_id = $1 AND version = $2`,
		agentID, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentVersion{}, ErrNotFound
	}
	if err != nil {
		return AgentVersion{}, err
	}
	return snapshot, nil
}

// rollbackVersionOf builds the row RollbackAgentVersion inserts: a NEW version
// carrying the target snapshot's content — history is extended, never rewritten
// (docs/agent-evolution.md §1, §3.2). The rollback itself goes under
// observation on the common terms, with the version it replaced as baseline.
func rollbackVersionOf(snapshot AgentVersion, latest int, author string, now time.Time) AgentVersion {
	return AgentVersion{
		AgentID:         snapshot.AgentID,
		Version:         latest + 1,
		Definition:      snapshot.Definition,
		Description:     snapshot.Description,
		CompiledPrompt:  snapshot.CompiledPrompt,
		PromptSource:    snapshot.PromptSource,
		Kind:            "rollback",
		BaselineVersion: latest,
		RollbackOf:      snapshot.Version,
		ChangeSummary:   fmt.Sprintf("Откат к версии %d", snapshot.Version),
		Author:          author,
		CreatedAt:       now,
	}
}

// RollbackAgentVersion creates a new version with the content of toVersion
// (docs/agent-evolution.md §3.2 «Откатить»): the snapshot of toVersion is
// re-inserted as the NEXT version number with kind=rollback, rollback_of=to
// and baseline_version=current head. The read of the head and the insert ride
// one transaction, so two concurrent rollbacks cannot both claim the same
// next number. The agent row is left to the caller — the kernel applies the
// rolled-back definition through its regular update path.
func (s *Store) RollbackAgentVersion(ctx context.Context, agentID string, toVersion int, author string) (AgentVersion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AgentVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the agent row first: the head read must not race an apply of the
	// same agent (the apply path locks this row too).
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT true FROM workspace_agent WHERE id = $1 FOR UPDATE`, agentID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentVersion{}, ErrNotFound
		}
		return AgentVersion{}, err
	}

	snapshot, err := scanAgentVersion(tx.QueryRow(ctx,
		`SELECT `+agentVersionColumns+` FROM workspace_agent_version WHERE agent_id = $1 AND version = $2`,
		agentID, toVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentVersion{}, ErrNotFound
	}
	if err != nil {
		return AgentVersion{}, err
	}

	var latest int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM workspace_agent_version WHERE agent_id = $1`,
		agentID).Scan(&latest); err != nil {
		return AgentVersion{}, err
	}

	created := rollbackVersionOf(snapshot, latest, author, time.Now().UTC())
	if err := insertAgentVersionRow(ctx, tx, created); err != nil {
		return AgentVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentVersion{}, err
	}
	return created, nil
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
