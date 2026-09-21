// Package memory implements the Temporality adaptive experience memory layer:
// an external observer over a classic agent harness that records experience,
// retrieves it by semantic scope, ranks it by temporal relevance and
// confidence, injects small hints, and reinforces or weakens memories from
// the outcomes of subsequent actions.
package memory

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Asset statuses per the pivot document: physical deletion is avoided so the
// evolution of knowledge stays inspectable.
const (
	StatusActive   = "ACTIVE"
	StatusStale    = "STALE"
	StatusArchived = "ARCHIVED"
)

// Asset kinds produced by the experience extractor.
const (
	KindStrategySwitch = "strategy-switch" // auth/method X failed, Y succeeded
	KindParamRequired  = "param-required"  // params.X value needed to succeed
	KindPagination     = "pagination"      // iterate params.cursor via next_cursor

	// Coding-world kinds (experiment 3).
	KindWhatWorked        = "what-worked"        // first-attempt success, low confidence (§18.3)
	KindTestCommand       = "test-command"       // tests for X pass with command Y
	KindCommandWorkaround = "command-workaround" // command X fails, use Y instead
	KindNav               = "navigation"         // when investigating X, start at file F
)

// Feedback lifecycle event types (experiment 2 §10): the causal chain is
// RECALLED → INJECTED → ACKNOWLEDGED? → REUSED → VALIDATED/CONTRADICTED/UNRESOLVED
// → REINFORCED/WEAKENED. Every stage is recorded separately so aggregate
// counts never conflate "memory existed" with "memory helped".
const (
	EventRecalled     = "RECALLED"
	EventInject       = "INJECTED"
	EventAcknowledged = "ACKNOWLEDGED"
	EventReuse        = "REUSED"
	EventValidated    = "VALIDATED"
	EventContradicted = "CONTRADICTED"
	EventUnresolved   = "UNRESOLVED"
	EventReinforced   = "REINFORCED"
	EventWeakened     = "WEAKENED"
	EventExtract      = "EXTRACT"
	EventGuardrail    = "GUARDRAIL"
)

// Recommendation is the machine-readable form of an asset used for reuse
// detection: if the agent's next matching call sets Param to Value, the hint
// was followed. ValueType records the JSON type the successful evidence carried
// ("string"|"number"|"bool"); a reuse whose string form matches but whose type
// differs is a typed mismatch (§18.1) and must not be attributed to the memory.
type Recommendation struct {
	Service     string `json:"service"`
	Resource    string `json:"resource,omitempty"`
	Param       string `json:"param"`                // "auth", "params.channel", "command"
	Value       string `json:"value"`                // "oauth", "email", "iterate"
	ValueType   string `json:"value_type,omitempty"` // "string"|"number"|"bool"|"other"
	Avoid       string `json:"avoid,omitempty"`      // previous failing value
	AvoidStatus int    `json:"avoid_status,omitempty"`
}

// Failure causes (mirror opsenv causes; kept in sync for attribution).
const (
	CauseAuth      = "auth"
	CauseParameter = "parameter"
	CauseServer    = "server"
	CauseNotFound  = "not-found"

	// Coding-world causes (experiment 3, classified from shell output).
	CauseTestFail = "test-fail"
	CauseBuild    = "build"
	CauseTimeout  = "timeout"

	// CauseTypeMismatch marks a call that carried the right value in the wrong
	// JSON type (e.g. version: 2 instead of "2"); §18.1: it must never be
	// attributed to the memory as a contradiction.
	CauseTypeMismatch = "type-mismatch"
)

// Evidence points at the tool calls an asset was built from.
type Evidence struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	Seq       int    `json:"seq"`
	Tool      string `json:"tool"`
	Status    int    `json:"status"`
	Summary   string `json:"summary"`
}

// Asset is one unit of prior experience.
type Asset struct {
	ID                        string
	DedupKey                  string
	Service                   string
	Problem                   string // auth | request-params | pagination
	Kind                      string
	Proposition               string
	Recommendation            Recommendation
	Evidence                  []Evidence
	Confidence                float64
	Status                    string
	CreatedAt                 time.Time
	LastConfirmedAt           time.Time
	LastContradictedAt        time.Time
	ConfirmationCount         int
	ContradictionCount        int
	ConsecutiveContradictions int
	Environment               string
	// VersionContext is the service API version the asset was confirmed in
	// (e.g. billing-api prod "v3" vs "v5" vs staging "v4"). Empty when the
	// service is not versioned. A version mismatch halves the rank score.
	VersionContext string
	SourceSessions []string
	// LastConfirmedSession is the 1-based session index of the last
	// confirmation; 0 when never confirmed. Recency ranking is computed in
	// session units, which keeps the experiment deterministic.
	LastConfirmedSession int
}

// Text returns the text used for similarity scoring.
func (a *Asset) Text() string {
	return fmt.Sprintf("%s %s %s %s %s %s", a.Service, a.Problem, a.Kind, a.Proposition, a.Recommendation.Resource, a.Recommendation.Value)
}

// ScopeMatch compares the asset scope with the task scope.
func (a *Asset) ScopeMatch(service string) float64 {
	if service == "" || a.Service == "" {
		return 0.3
	}
	if a.Service == service {
		return 1.0
	}
	return 0.0
}

// dedupKey uniquely identifies a memory candidate. Environment and value are
// part of the identity: "prod oauth works" and "prod pat works" (after a flip)
// or "staging oauth works" must be separate assets with separate confidence
// trajectories, not merges of each other.
func dedupKey(environment, service, resource, kind, param, value string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s", environment, service, resource, kind, param, value)))
	return hex.EncodeToString(sum[:16])
}

// ComputeDedupKey exposes the asset identity function to world-specific
// extractors (coding, ...) so all assets share one dedup scheme.
func ComputeDedupKey(environment, service, resource, kind, param, value string) string {
	return dedupKey(environment, service, resource, kind, param, value)
}

// NewUUID returns a version-4 UUID.
func NewUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Clone deep-copies the mutable fields the feedback loop updates.
func (a *Asset) Clone() *Asset {
	out := *a
	out.Evidence = append([]Evidence(nil), a.Evidence...)
	out.SourceSessions = append([]string(nil), a.SourceSessions...)
	return &out
}
