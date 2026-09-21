package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
)

// Config enables the capabilities of each experimental arm. The layer is
// constructed per arm; arm A runs with no layer attached at all.
type Config struct {
	Arm             string
	InjectTaskStart bool // task-start activation (§10.1)
	PreAction       bool // pre-action activation (§10.2)
	Guardrail       bool // repeated-failure warnings (§11)
	Feedback        bool // adaptive confidence from outcomes (§7, §12)
	TemporalRanking bool // full ranking (§4, §14); false = similarity-only
}

// ConfigForArm maps the experiment arm letters to capabilities.
func ConfigForArm(arm string) (Config, error) {
	switch arm {
	case "A":
		return Config{Arm: "A"}, nil
	case "B":
		return Config{Arm: "B", InjectTaskStart: true}, nil
	case "C":
		return Config{Arm: "C", InjectTaskStart: true, TemporalRanking: true}, nil
	case "D":
		return Config{Arm: "D", InjectTaskStart: true, TemporalRanking: true, PreAction: true, Guardrail: true, Feedback: true}, nil
	default:
		return Config{}, fmt.Errorf("unknown arm %q", arm)
	}
}

// Stats are per-task memory metrics surfaced to the benchmark driver. The
// lifecycle counters mirror the event types so aggregates never conflate
// "memory existed" with "memory was used" with "memory helped".
type Stats struct {
	Recalled           int `json:"recalled"`       // candidates above threshold at task start
	HintsInjected      int `json:"hints_injected"` // hints actually shown
	Acknowledged       int `json:"acknowledged"`   // first in-scope call after injection
	Reused             int `json:"reused"`         // recommendation applied in an action
	Validated          int `json:"validated"`      // reuse followed by success
	Contradicted       int `json:"contradicted"`   // reuse followed by matching-cause failure
	Unresolved         int `json:"unresolved"`     // reuse followed by unrelated failure
	Misleading         int `json:"misleading"`     // deprecated alias of contradicted+unresolved failures
	AssetsExtracted    int `json:"assets_extracted"`
	RepeatedFailedCall int `json:"repeated_failed_calls"` // extra repeats of an already-failing signature
	GuardrailHints     int `json:"guardrail_hints"`
}

// Layer is the external memory layer attached to a classic harness.
type Layer struct {
	store    Store
	cfg      Config
	services []string
	log      *slog.Logger

	// World-specific parsing/extraction: the defaults understand the synthetic
	// api_call world; the coding world plugs in its own (experiment 3 §3).
	parser    CallParser
	extractor ExtractFunc

	sessionID    string
	sessionIndex int

	// per-task world context (environment + service API version), set by the
	// driver before each task; environment falls back to task-text extraction.
	taskEnvironment string
	taskVersion     string

	// per-task state
	currentTask  string
	taskService  string
	taskText     string
	callLog      []CallRecord
	seq          int
	pending      map[string]pendingHint // assetID → recommendation awaiting reuse
	failedSigs   map[string]int
	guardDone    map[string]bool
	injected     map[string]bool
	acknowledged map[string]bool
	called       map[string]bool // "service|resource" already called this task
	stats        Stats
}

// pendingHint carries the recommendation plus the asset kind needed for
// cause-level attribution when the reuse outcome arrives.
type pendingHint struct {
	Recommendation
	Kind string
}

// CallParser flattens one tool call into the shape the layer reasons about.
// taskService is the scope extracted from the task text; parsers may fall back
// to it when the call itself carries no scope.
type CallParser func(call llm.ToolCall, taskService string) ParsedCall

// ExtractFunc turns a finished task's call log into asset candidates; worlds
// with different tool surfaces plug in their own extractor.
type ExtractFunc func(log []CallRecord, opt ExtractOptions) []*Asset

// NewLayer builds the layer. services is the service lexicon used for scope
// extraction.
func NewLayer(store Store, cfg Config, services []string, logger *slog.Logger) *Layer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Layer{
		store:     store,
		cfg:       cfg,
		services:  services,
		log:       logger,
		parser:    parseCall,
		extractor: ExtractExperiences,
	}
}

// SetParser installs a world-specific call parser.
func (l *Layer) SetParser(p CallParser) { l.parser = p }

// SetExtractor installs a world-specific experience extractor.
func (l *Layer) SetExtractor(f ExtractFunc) { l.extractor = f }

// SetSession advances the layer to a new session.
func (l *Layer) SetSession(sessionID string, index int) {
	l.sessionID = sessionID
	l.sessionIndex = index
}

// SetTaskContext sets the environment and service API version context for the
// next task (observed world metadata used for ranking and asset provenance).
func (l *Layer) SetTaskContext(environment, version string) {
	l.taskEnvironment = environment
	l.taskVersion = version
}

// Stats returns the per-task counters (valid after TaskEnd).
func (l *Layer) Stats() Stats { return l.stats }

func (l *Layer) resetTask() {
	l.callLog = nil
	l.seq = 0
	l.pending = map[string]pendingHint{}
	l.failedSigs = map[string]int{}
	l.guardDone = map[string]bool{}
	l.injected = map[string]bool{}
	l.acknowledged = map[string]bool{}
	l.called = map[string]bool{}
	l.stats = Stats{}
}

// ------------------------------------------------------------- harness hooks

// TaskStart performs task-start activation.
func (l *Layer) TaskStart(ctx context.Context, sessionID, taskID, taskText string) []harness.Hint {
	l.resetTask()
	l.currentTask = taskID
	l.taskText = taskText
	l.taskService = l.extractService(taskText)
	if l.taskEnvironment == "" {
		l.taskEnvironment = extractEnvironment(taskText)
	}
	if !l.cfg.InjectTaskStart || l.store == nil {
		return nil
	}
	assets, err := l.store.ListAssets(ctx)
	if err != nil {
		l.log.Warn("list assets", "error", err)
		return nil
	}
	if len(assets) == 0 {
		return nil
	}
	req := RankRequest{
		TaskText:     taskText,
		Service:      l.taskService,
		Environment:  l.taskEnvironment,
		Version:      l.taskVersion,
		SessionIndex: l.sessionIndex,
	}
	scored := Rank(assets, req, DefaultWeights, !l.cfg.TemporalRanking)
	for _, candidate := range scored {
		if candidate.Score >= Threshold {
			l.stats.Recalled++
		}
	}
	l.logRecall(ctx, taskID, scored, "task_start")

	var hints []harness.Hint
	for _, candidate := range scored {
		if len(hints) >= 3 {
			break
		}
		if candidate.Score < Threshold {
			break
		}
		hints = append(hints, harness.Hint{Text: HintText(candidate.Asset)})
		l.injected[candidate.Asset.ID] = true
		l.pending[candidate.Asset.ID] = pendingHint{Recommendation: candidate.Asset.Recommendation, Kind: candidate.Asset.Kind}
		l.logInject(ctx, taskID, candidate, "task_start")
	}
	return hints
}

// BeforeToolCall performs pre-action activation and guardrail checks. Hints
// are delivered with the next context update, after the call executes.
func (l *Layer) BeforeToolCall(ctx context.Context, sessionID string, call llm.ToolCall) []harness.Hint {
	if l.store == nil {
		return nil
	}
	var hints []harness.Hint
	parsed := l.parser(call, l.taskService)

	// Pre-action activation: first attempt at this resource in the task.
	if l.cfg.PreAction && parsed.Service != "" && parsed.Resource != "" {
		key := parsed.Service + "|" + parsed.Resource
		if !l.called[key] {
			l.called[key] = true
			if hint, ok := l.preActionHint(ctx, parsed); ok {
				hints = append(hints, hint)
			}
		}
	}

	// Guardrail: soft warn on repeated identical failing calls (§11: WARN /
	// SUGGEST, never block).
	if l.cfg.Guardrail {
		sig := callSignature(parsed)
		if l.failedSigs[sig] >= 2 && !l.guardDone[sig] {
			l.guardDone[sig] = true
			l.stats.GuardrailHints++
			text := fmt.Sprintf("You have already tried this exact call %d times without success in this task; repeating identical failures is unlikely to help. Consider changing the request parameters, auth method, or consulting the service status/docs.", l.failedSigs[sig])
			if rec, ok := l.bestRecommendationFor(ctx, parsed); ok {
				text += " Prior experience suggests: " + HintText(rec)
			}
			hints = append(hints, harness.Hint{Text: text})
			l.appendEvent(ctx, Event{SessionID: sessionID, TaskID: l.currentTaskID(), Type: EventGuardrail, Payload: map[string]any{"signature": sig, "failures": l.failedSigs[sig]}})
		}
	}
	return hints
}

// AfterToolCall records the observation and runs reuse/outcome detection with
// cause-level attribution (experiment 2 §8): only a failure whose cause
// matches the asset kind contradicts the memory; anything else is UNRESOLVED
// and must not move confidence.
func (l *Layer) AfterToolCall(ctx context.Context, sessionID string, call llm.ToolCall, result harness.ToolResult) {
	if l.store == nil {
		return
	}
	parsed := l.parser(call, l.taskService)
	l.seq++
	record := CallRecord{
		Seq:         l.seq,
		Tool:        call.Name,
		Service:     parsed.Service,
		Resource:    parsed.Resource,
		Auth:        parsed.Auth,
		Environment: parsed.Environment,
		Params:      parsed.Params,
		Status:      result.Status,
		OK:          result.OK,
		Cause:       result.Cause,
		Summary:     summarize(call, result),
	}
	l.callLog = append(l.callLog, record)
	if err := l.store.InsertCall(ctx, RecordedCall{
		SessionID: sessionID, TaskID: l.currentTaskID(), Step: l.seq, Seq: l.seq,
		Tool: record.Tool, Service: record.Service, Resource: record.Resource, Auth: record.Auth,
		Environment: record.Environment, Params: record.Params, Status: record.Status, OK: record.OK,
		Cause: record.Cause, Summary: record.Summary,
	}); err != nil {
		l.log.Warn("insert call", "error", err)
	}

	if !result.OK && record.Tool == "api_call" {
		sig := callSignature(parsed)
		l.failedSigs[sig]++
		if l.failedSigs[sig] > 1 {
			l.stats.RepeatedFailedCall++
		}
	}

	// Reuse detection over pending recommendations.
	for assetID, hint := range l.pending {
		if hint.Service != parsed.Service {
			continue
		}
		if hint.Resource != "" && hint.Resource != parsed.Resource {
			continue
		}
		raw, value := paramOfRaw(parsed, hint.Param)
		matched := value == hint.Value
		if hint.Value == "iterate" {
			matched = value != "" && result.OK
		}
		// Command recommendations match when every whitespace-separated token
		// of the remembered command appears in the executed one (the agent may
		// add flags or set env vars around it).
		if !matched && hint.Param == "command" && hint.Value != "" {
			matched = commandTokensCovered(value, hint.Value)
		}
		// §18.1 typed contradiction: when the string form matches but the raw
		// JSON type differs from the type the successful evidence carried (e.g.
		// version: 2 instead of "2"), the agent attempted to follow the memory
		// with a wrong type. A failure here says nothing about the memory — the
		// call shape was wrong — so it is UNRESOLVED, never a contradiction.
		typedMismatch := matched && value != "" && hint.ValueType != "" && jsonValueType(raw) != hint.ValueType
		if typedMismatch && !result.OK {
			if !l.acknowledged[assetID] {
				l.acknowledged[assetID] = true
				l.stats.Acknowledged++
			}
			l.stats.Unresolved++
			l.appendEvent(ctx, Event{AssetID: assetID, SessionID: sessionID, TaskID: l.currentTaskID(), Type: EventUnresolved, Payload: map[string]any{"status": result.Status, "cause": result.Cause, "typed_mismatch": true, "param": hint.Param, "value_sent": value}})
			continue
		}
		// ACKNOWLEDGED: the agent's first in-scope call after injection, before
		// (or without) applying the recommendation.
		if !matched && !l.acknowledged[assetID] {
			l.acknowledged[assetID] = true
			l.stats.Acknowledged++
			l.appendEvent(ctx, Event{AssetID: assetID, SessionID: sessionID, TaskID: l.currentTaskID(), Type: EventAcknowledged, Payload: map[string]any{"param": hint.Param, "value": value}})
		}
		if !matched {
			continue
		}
		delete(l.pending, assetID)
		l.stats.Reused++
		l.appendEvent(ctx, Event{AssetID: assetID, SessionID: sessionID, TaskID: l.currentTaskID(), Type: EventReuse, Payload: map[string]any{"param": hint.Param, "value": hint.Value, "status": result.Status, "cause": result.Cause}})
		if result.OK {
			l.stats.Validated++
			l.appendEvent(ctx, Event{AssetID: assetID, SessionID: sessionID, TaskID: l.currentTaskID(), Type: EventValidated, Payload: map[string]any{"status": result.Status}})
			if l.cfg.Feedback {
				l.reinforce(ctx, assetID)
			}
			continue
		}
		if contradicts(hint.Kind, result.Cause) {
			l.stats.Contradicted++
			l.stats.Misleading++
			l.appendEvent(ctx, Event{AssetID: assetID, SessionID: sessionID, TaskID: l.currentTaskID(), Type: EventContradicted, Payload: map[string]any{"status": result.Status, "cause": result.Cause}})
			if l.cfg.Feedback {
				l.weaken(ctx, assetID)
			}
			continue
		}
		// Failure cause unrelated to the memory (e.g. a parameter error while
		// testing an auth memory): UNRESOLVED, confidence unchanged.
		l.stats.Unresolved++
		l.appendEvent(ctx, Event{AssetID: assetID, SessionID: sessionID, TaskID: l.currentTaskID(), Type: EventUnresolved, Payload: map[string]any{"status": result.Status, "cause": result.Cause}})
	}
}

// contradicts reports whether a failure cause refutes the asset kind.
func contradicts(kind, cause string) bool {
	if cause == "" {
		return false // unknown cause → UNRESOLVED
	}
	switch kind {
	case KindStrategySwitch, KindWhatWorked:
		return cause == CauseAuth
	case KindParamRequired:
		return cause == CauseParameter
	case KindTestCommand, KindCommandWorkaround:
		// The remembered command itself failed to compile or its tests failed:
		// the world changed under the memory (experiment 3B).
		return cause == CauseTestFail || cause == CauseBuild
	case KindNav:
		// The remembered file is gone: the repo moved it.
		return cause == CauseNotFound
	default:
		return false
	}
}

// commandTokensCovered reports whether every token of remembered appears in
// executed (as substrings, so flags and path suffixes still match).
func commandTokensCovered(executed, remembered string) bool {
	if executed == "" {
		return false
	}
	for _, token := range strings.Fields(remembered) {
		if !strings.Contains(executed, token) {
			return false
		}
	}
	return true
}

// jsonValueType maps a decoded JSON value to the type tag stored in
// Recommendation.ValueType.
func jsonValueType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case bool:
		return "bool"
	default:
		return "other"
	}
}

// TaskEnd extracts experiences and merges them into memory.
func (l *Layer) TaskEnd(ctx context.Context, sessionID, taskID string, success bool, answer string) {
	if l.store == nil {
		return
	}
	extracted := l.extractor(l.callLog, ExtractOptions{
		SessionID:   sessionID,
		TaskID:      taskID,
		TaskText:    l.taskText,
		Service:     l.taskService,
		Environment: l.taskEnvironment,
		Version:     l.taskVersion,
		Success:     success,
	})
	if len(extracted) == 0 {
		return
	}
	existing, err := l.store.ListAssets(ctx)
	if err != nil {
		l.log.Warn("list assets for merge", "error", err)
		return
	}
	byKey := map[string]*Asset{}
	for _, a := range existing {
		byKey[a.DedupKey] = a
	}
	now := timeNow()
	for _, asset := range extracted {
		l.stats.AssetsExtracted++
		if prior, ok := byKey[asset.DedupKey]; ok {
			merged := prior.Clone()
			merged.Proposition = asset.Proposition
			merged.Evidence = append(merged.Evidence, asset.Evidence...)
			// Evidence diversity (experiment 2 §4): only an independent session
			// counts as a new confirmation; same-session re-merges append
			// evidence but do not grow confidence.
			newSession := !contains(merged.SourceSessions, l.sessionID)
			if newSession {
				merged.SourceSessions = append(merged.SourceSessions, l.sessionID)
				merged.ConfirmationCount++
				if l.cfg.Feedback {
					// §18.3: what-worked assets mature on the explicit 0.40→0.55→0.67
					// schedule; failure→success assets keep the asymptote.
					merged.Confidence = MatureConfidence(merged.Kind, merged.Confidence, merged.ConfirmationCount-1)
				}
			}
			merged.LastConfirmedAt = now
			merged.LastConfirmedSession = l.sessionIndex
			merged.Environment = l.taskEnvironment
			merged.VersionContext = l.taskVersion
			if merged.Status == StatusStale && l.cfg.Feedback && newSession {
				// §18.2 resurrection: an independent-session confirmation is
				// strong validation; a stale memory returns ACTIVE directly, not
				// through four ordinary reinforcement cycles.
				merged.Status = StatusActive
				merged.Confidence = max64(merged.Confidence, 0.55)
				merged.ConsecutiveContradictions = 0
			}
			if err := l.store.UpsertAsset(ctx, merged, false); err != nil {
				l.log.Warn("merge asset", "error", err)
			}
			l.appendEvent(ctx, Event{AssetID: merged.ID, SessionID: sessionID, TaskID: taskID, Type: EventExtract, Payload: map[string]any{"dedup_key": merged.DedupKey, "merged": true, "new_session": newSession, "confidence": merged.Confidence}})
			continue
		}
		asset.ID = NewUUID()
		asset.ConfirmationCount = 1
		asset.SourceSessions = []string{l.sessionID}
		asset.LastConfirmedAt = now
		asset.LastConfirmedSession = l.sessionIndex
		if err := l.store.UpsertAsset(ctx, asset, true); err != nil {
			l.log.Warn("insert asset", "error", err)
			continue
		}
		byKey[asset.DedupKey] = asset
		l.appendEvent(ctx, Event{AssetID: asset.ID, SessionID: sessionID, TaskID: taskID, Type: EventExtract, Payload: map[string]any{"dedup_key": asset.DedupKey, "merged": false, "confidence": asset.Confidence}})
	}
}

// ------------------------------------------------------------- feedback core

func (l *Layer) reinforce(ctx context.Context, assetID string) {
	asset := l.findAsset(ctx, assetID)
	if asset == nil {
		return
	}
	before := asset.Confidence
	factor := 1.0
	if contains(asset.SourceSessions, l.sessionID) {
		factor = 0.2 // same-session repeats carry little independent evidence
	}
	if factor == 1.0 {
		// Independent-session validation.
		asset.SourceSessions = append(asset.SourceSessions, l.sessionID)
		asset.ConfirmationCount++
		asset.Confidence = MatureConfidence(asset.Kind, asset.Confidence, asset.ConfirmationCount-1)
		if asset.Status == StatusStale {
			// §18.2 resurrection: STALE + independent failure→success confirmation
			// returns ACTIVE immediately with restored confidence.
			asset.Status = StatusActive
			asset.Confidence = max64(asset.Confidence, 0.55)
			asset.ConsecutiveContradictions = 0
		}
	} else {
		asset.Confidence = min64(0.95, asset.Confidence+(0.95-asset.Confidence)*0.25*factor)
		if asset.Status == StatusStale && asset.Confidence >= 0.5 {
			asset.Status = StatusActive
		}
	}
	asset.ConsecutiveContradictions = 0
	asset.LastConfirmedAt = timeNow()
	asset.LastConfirmedSession = l.sessionIndex
	if err := l.store.UpsertAsset(ctx, asset, false); err != nil {
		l.log.Warn("reinforce asset", "error", err)
	}
	l.appendEvent(ctx, Event{AssetID: assetID, SessionID: l.sessionID, TaskID: l.currentTaskID(), Type: EventReinforced, Payload: map[string]any{"confidence_before": round3(before), "confidence_after": round3(asset.Confidence), "independent_session": factor == 1.0}})
}

func (l *Layer) weaken(ctx context.Context, assetID string) {
	asset := l.findAsset(ctx, assetID)
	if asset == nil {
		return
	}
	before := asset.Confidence
	asset.Confidence *= 0.45
	asset.ContradictionCount++
	asset.ConsecutiveContradictions++
	asset.LastContradictedAt = timeNow()
	if asset.ConsecutiveContradictions >= 2 || asset.Confidence < 0.35 {
		asset.Status = StatusStale
	}
	if asset.Confidence < 0.10 {
		asset.Status = StatusArchived
	}
	if err := l.store.UpsertAsset(ctx, asset, false); err != nil {
		l.log.Warn("weaken asset", "error", err)
	}
	l.appendEvent(ctx, Event{AssetID: assetID, SessionID: l.sessionID, TaskID: l.currentTaskID(), Type: EventWeakened, Payload: map[string]any{"confidence_before": round3(before), "confidence_after": round3(asset.Confidence), "consecutive": asset.ConsecutiveContradictions}})
}

func (l *Layer) findAsset(ctx context.Context, assetID string) *Asset {
	assets, err := l.store.ListAssets(ctx)
	if err != nil {
		l.log.Warn("list assets", "error", err)
		return nil
	}
	for _, a := range assets {
		if a.ID == assetID {
			return a
		}
	}
	return nil
}

// ------------------------------------------------------------- recall helpers

func (l *Layer) preActionHint(ctx context.Context, parsed ParsedCall) (harness.Hint, bool) {
	assets, err := l.store.ListAssets(ctx)
	if err != nil || len(assets) == 0 {
		return harness.Hint{}, false
	}
	req := RankRequest{
		TaskText:     l.taskText,
		Service:      parsed.Service,
		Resource:     parsed.Resource,
		Environment:  l.taskEnvironment,
		Version:      l.taskVersionFor(parsed.Service),
		SessionIndex: l.sessionIndex,
	}
	scored := Rank(assets, req, DefaultWeights, !l.cfg.TemporalRanking)
	for _, candidate := range scored {
		if candidate.Score < Threshold || l.injected[candidate.Asset.ID] {
			continue
		}
		l.injected[candidate.Asset.ID] = true
		l.pending[candidate.Asset.ID] = pendingHint{Recommendation: candidate.Asset.Recommendation, Kind: candidate.Asset.Kind}
		l.logInject(ctx, l.currentTaskID(), candidate, "pre_action")
		return harness.Hint{Text: HintText(candidate.Asset)}, true
	}
	return harness.Hint{}, false
}

// taskVersionFor returns the version context for a service; the per-task
// version applies only to the task's own service scope.
func (l *Layer) taskVersionFor(service string) string {
	if service == l.taskService {
		return l.taskVersion
	}
	return ""
}

func (l *Layer) bestRecommendationFor(ctx context.Context, parsed ParsedCall) (*Asset, bool) {
	assets, err := l.store.ListAssets(ctx)
	if err != nil {
		return nil, false
	}
	var best *Asset
	for _, a := range assets {
		if a.Status == StatusArchived || a.Service != parsed.Service {
			continue
		}
		if a.Environment != "" && l.taskEnvironment != "" && a.Environment != l.taskEnvironment {
			continue
		}
		if a.Recommendation.Resource != "" && a.Recommendation.Resource != parsed.Resource {
			continue
		}
		if best == nil || a.Confidence > best.Confidence {
			best = a
		}
	}
	return best, best != nil
}

func (l *Layer) logRecall(ctx context.Context, taskID string, scored []Scored, point string) {
	if len(scored) == 0 {
		return
	}
	candidates := make([]map[string]any, 0, len(scored))
	for i, s := range scored {
		if i >= 8 {
			break
		}
		candidates = append(candidates, map[string]any{"asset": s.Asset.ID, "score": s.Score, "why": s.Why})
	}
	l.appendEvent(ctx, Event{SessionID: l.sessionID, TaskID: taskID, Type: EventRecalled, Payload: map[string]any{"point": point, "candidates": candidates}})
}

func (l *Layer) logInject(ctx context.Context, taskID string, candidate Scored, point string) {
	l.stats.HintsInjected++
	l.appendEvent(ctx, Event{AssetID: candidate.Asset.ID, SessionID: l.sessionID, TaskID: taskID, Type: EventInject, Payload: map[string]any{"point": point, "score": candidate.Score, "why": candidate.Why}})
}

func (l *Layer) appendEvent(ctx context.Context, event Event) {
	event.At = timeNow()
	if event.SessionID == "" {
		event.SessionID = l.sessionID
	}
	if err := l.store.AppendEvent(ctx, event); err != nil {
		l.log.Warn("append event", "error", err)
	}
}

// ---------------------------------------------------------------- utilities

type ParsedCall struct {
	Name        string
	Service     string
	Resource    string
	Auth        string
	Environment string
	Params      map[string]any
}

func parseCall(call llm.ToolCall, taskService string) ParsedCall {
	parsed := ParsedCall{Name: call.Name, Params: map[string]any{}}
	parsed.Service, _ = call.Args["service"].(string)
	parsed.Resource, _ = call.Args["resource"].(string)
	parsed.Auth, _ = call.Args["auth"].(string)
	parsed.Environment, _ = call.Args["environment"].(string)
	if parsed.Environment == "" {
		parsed.Environment = "prod"
	}
	if raw, ok := call.Args["params"].(map[string]any); ok {
		parsed.Params = raw
	}
	return parsed
}

func paramOf(call ParsedCall, param string) string {
	raw, value := paramOfRaw(call, param)
	_ = raw
	return value
}

// paramOfRaw returns both the raw decoded JSON value (for typed-reuse checks)
// and its canonical string form.
func paramOfRaw(call ParsedCall, param string) (any, string) {
	if param == "auth" {
		if call.Auth == "" {
			return nil, ""
		}
		return call.Auth, call.Auth
	}
	key := param
	if strings.HasPrefix(param, "params.") {
		key = strings.TrimPrefix(param, "params.")
	}
	if v, ok := call.Params[key]; ok {
		return v, fmt.Sprintf("%v", v)
	}
	return nil, ""
}

func callSignature(call ParsedCall) string {
	keys := make([]string, 0, len(call.Params))
	for k := range call.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, call.Params[k]))
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", call.Name, call.Service, call.Resource, call.Auth, call.Environment, strings.Join(parts, ","))
}

func summarize(call llm.ToolCall, result harness.ToolResult) string {
	args, _ := json.Marshal(call.Args)
	snippet := string(args)
	if len(snippet) > 160 {
		snippet = snippet[:160]
	}
	return fmt.Sprintf("%s %s → %d", call.Name, snippet, result.Status)
}

func (l *Layer) extractService(text string) string {
	lowered := strings.ToLower(text)
	for _, svc := range l.services {
		if strings.Contains(lowered, strings.ToLower(svc)) {
			return svc
		}
	}
	return ""
}

// extractEnvironment infers the deployment environment from task text when no
// explicit task context was set.
func extractEnvironment(text string) string {
	if strings.Contains(strings.ToLower(text), "staging") {
		return "staging"
	}
	return "prod"
}

// currentTaskID is tracked implicitly via the last reset; the benchmark calls
// hooks sequentially per task so this is unambiguous.
func (l *Layer) currentTaskID() string { return l.currentTask }

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func round3(v float64) float64 { return float64(int(v*1000)) / 1000 }
