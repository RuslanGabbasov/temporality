package procedure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
)

const ProjectorVersion = "procedure-projector.v1"

var ErrNotFound = errors.New("procedure not found")

type Preconditions struct {
	RequiredArguments []string `json:"required_arguments"`
}

type AffordanceStep struct {
	Position     int    `json:"position"`
	AffordanceID string `json:"affordance_id"`
}

type ExpectedOutcome struct {
	Status execution.Status `json:"status"`
}

type Procedure struct {
	Protocol             string            `json:"protocol"`
	Version              string            `json:"version"`
	ProcedureID          string            `json:"procedure_id"`
	EpisodeID            string            `json:"episode_id"`
	SemanticTrigger      string            `json:"semantic_trigger"`
	Preconditions        Preconditions     `json:"preconditions"`
	AffordanceSequence   []AffordanceStep  `json:"affordance_sequence"`
	ExpectedOutcomes     []ExpectedOutcome `json:"expected_outcomes"`
	EvidenceExecutionIDs []string          `json:"evidence_execution_ids"`
	Successes            int               `json:"successes"`
	Failures             int               `json:"failures"`
	SuccessRate          float64           `json:"success_rate"`
	ProjectionVersion    string            `json:"projection_version"`
}

type Evidence struct {
	Execution execution.Execution
	Request   affordance.Request
}

type BuildConfig struct {
	MinimumEvidence int
}

func (c BuildConfig) minimumEvidence() int {
	if c.MinimumEvidence < 1 {
		return 1
	}
	return c.MinimumEvidence
}

// Build is pure: only terminal executions contribute, and input order cannot affect output.
func Build(episodeID string, evidence []Evidence, config BuildConfig) []Procedure {
	type group struct {
		ids       []string
		successes int
		failures  int
		keys      map[string]int
	}
	groups := make(map[string]*group)
	seen := make(map[string]struct{})
	for _, item := range evidence {
		e := item.Execution
		r := item.Request
		if e.EpisodeID != episodeID || !e.Status.Terminal() || e.RequestID != r.RequestID || e.AffordanceID != r.AffordanceID || r.EpisodeID != episodeID {
			continue
		}
		if _, duplicate := seen[e.ExecutionID]; duplicate {
			continue
		}
		seen[e.ExecutionID] = struct{}{}
		g := groups[e.AffordanceID]
		if g == nil {
			g = &group{keys: make(map[string]int)}
			groups[e.AffordanceID] = g
		}
		g.ids = append(g.ids, e.ExecutionID)
		if e.Status == execution.StatusCompleted {
			g.successes++
		} else {
			g.failures++
		}
		for key := range r.Arguments {
			g.keys[key]++
		}
	}
	affordanceIDs := make([]string, 0, len(groups))
	for id, g := range groups {
		if len(g.ids) >= config.minimumEvidence() {
			affordanceIDs = append(affordanceIDs, id)
		}
	}
	sort.Strings(affordanceIDs)
	result := make([]Procedure, 0, len(affordanceIDs))
	for _, affordanceID := range affordanceIDs {
		g := groups[affordanceID]
		sort.Strings(g.ids)
		required := make([]string, 0)
		for key, count := range g.keys {
			if count == len(g.ids) {
				required = append(required, key)
			}
		}
		sort.Strings(required)
		trigger := strings.Join(tokenize(affordanceID), " ")
		result = append(result, Procedure{
			Protocol: protocol.Name, Version: protocol.Version,
			ProcedureID: procedureID(episodeID, affordanceID, required), EpisodeID: episodeID,
			SemanticTrigger: trigger, Preconditions: Preconditions{RequiredArguments: required},
			AffordanceSequence:   []AffordanceStep{{Position: 0, AffordanceID: affordanceID}},
			ExpectedOutcomes:     []ExpectedOutcome{{Status: execution.StatusCompleted}},
			EvidenceExecutionIDs: append([]string(nil), g.ids...), Successes: g.successes, Failures: g.failures,
			SuccessRate: float64(g.successes+1) / float64(g.successes+g.failures+2), ProjectionVersion: ProjectorVersion,
		})
	}
	return result
}

func procedureID(episodeID, affordanceID string, required []string) string {
	content := episodeID + "\x00" + affordanceID + "\x00" + strings.Join(required, "\x00")
	sum := sha256.Sum256([]byte(ProjectorVersion + "\x00" + content))
	raw := append([]byte(nil), sum[:16]...)
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := hex.EncodeToString(raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (p Procedure) Validate() error {
	if p.Protocol != protocol.Name || p.Version != protocol.Version || p.ProjectionVersion != ProjectorVersion {
		return errors.New("unsupported procedure version")
	}
	if p.ProcedureID == "" || p.EpisodeID == "" || p.SemanticTrigger == "" || len(p.AffordanceSequence) == 0 {
		return errors.New("procedure identity, trigger, and sequence are required")
	}
	if p.Successes < 0 || p.Failures < 0 || p.Successes+p.Failures != len(p.EvidenceExecutionIDs) || p.SuccessRate < 0 || p.SuccessRate > 1 {
		return errors.New("invalid procedure evidence")
	}
	return nil
}

type Filter struct{ EpisodeID string }

type Store interface {
	ReplaceProcedures(context.Context, string, []Procedure) error
	ListProcedures(context.Context, Filter) ([]Procedure, error)
	GetProcedure(context.Context, string) (Procedure, error)
}

type Source interface {
	ListProcedureEvidence(context.Context, string) ([]Evidence, error)
}

type Projector struct{ Config BuildConfig }

func (p Projector) Version() string { return ProjectorVersion }

// Rebuild replaces only the episode projection and deliberately emits no Event.
func (p Projector) Rebuild(ctx context.Context, episodeID string, source Source, target Store) error {
	if strings.TrimSpace(episodeID) == "" {
		return errors.New("episode_id is required")
	}
	evidence, err := source.ListProcedureEvidence(ctx, episodeID)
	if err != nil {
		return err
	}
	procedures := Build(episodeID, evidence, p.Config)
	for _, value := range procedures {
		if err = value.Validate(); err != nil {
			return fmt.Errorf("build procedure: %w", err)
		}
	}
	return target.ReplaceProcedures(ctx, episodeID, procedures)
}

type MatchConfig struct{ Threshold float64 }
type Match struct {
	Procedure Procedure
	Score     float64
}

func MatchProcedures(procedures []Procedure, goal objective.Objective, current frame.Frame, config MatchConfig) []Match {
	query := goal.Text + " " + strings.Join(goal.SuccessConditions, " ")
	if current.Focus.Type == frame.RefQuery {
		query += " " + current.Focus.Query
	} else {
		query += " " + current.Focus.ID
	}
	queryTokens := tokenSet(query)
	matches := make([]Match, 0)
	for _, candidate := range procedures {
		terms := tokenSet(candidate.SemanticTrigger + " " + strings.Join(candidate.Preconditions.RequiredArguments, " "))
		shared := 0
		for term := range terms {
			if _, ok := queryTokens[term]; ok {
				shared++
			}
		}
		semantic := 0.0
		if len(terms) > 0 {
			semantic = float64(shared) / float64(len(terms))
		}
		// Confidence is multiplicative so failures poison neither matching nor the corpus, but always lower rank.
		score := semantic * candidate.SuccessRate
		if score >= config.Threshold {
			matches = append(matches, Match{Procedure: candidate, Score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Procedure.ProcedureID < matches[j].Procedure.ProcedureID
	})
	return matches
}

func tokenSet(value string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, token := range tokenize(value) {
		result[token] = struct{}{}
	}
	return result
}
func tokenize(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
