package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/attention"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/procedure"
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
)

const Version = "render-0.4.0"

const (
	procedureMatchThreshold = 0.1
	maxProcedureMatches     = 8
	// maxEntityCandidates bounds how many global entities may enter one
	// render's ambient pool (M14.3): entities are world knowledge, not
	// episode state, so a large graph must not flood every frame.
	maxEntityCandidates = 16
)

// ambientHiddenEvents are attention's own bookkeeping: recording what was
// suggested or selected is an audit trail, not content. Letting them into the
// packet makes attention chase its own output — one step's suggestions spawn
// dozens of events that crowd out world.observations and claims in every
// later render (they once were 58% of an episode's events). They stay in the
// substrate for audit, replay and the M10 debugger; the model never sees
// them. Event-type regions aggregating them are filtered by label too.
var ambientHiddenEvents = map[string]struct{}{
	"attention.suggested": {},
	"attention.selected":  {},
}

func bookkeepingEvent(typeName string) bool {
	_, hidden := ambientHiddenEvents[typeName]
	return hidden
}

type Request struct {
	FrameID      string                  `json:"frame_id"`
	ObjectiveID  string                  `json:"objective_id"`
	BudgetTokens int                     `json:"budget_tokens"`
	Affordances  []affordance.Definition `json:"affordances,omitempty"`
	// AsOf is an optional memory cutoff. The agent loop renders with nil so the
	// frame sees everything committed so far — including observations produced
	// by its own previous actions after that frame was created. Time-travel
	// inspection passes an explicit cutoff to reproduce exactly what a frame
	// could see at a historical moment.
	AsOf *time.Time `json:"as_of,omitempty"`
}

type Section struct {
	Kind      string `json:"kind"`
	Attention string `json:"attention,omitempty"`
	Items     []any  `json:"items"`
}

// MapItem is the packet-facing form of an attention candidate. Ref is the
// canonical string ("event:UUID", "region:UUID", ...) the model copies
// verbatim into emissions, and Payload is compact: protocol envelopes
// (version, agent/branch ids, provenance maps, feature vectors) carry no
// cognitive signal for the model but previously cost ~a third of the packet.
// The full envelopes stay in the substrate and the M10 debugger.
type MapItem struct {
	Ref     string  `json:"ref"`
	Score   float64 `json:"score"`
	Payload any     `json:"payload"`
}
type OutsideFrame struct {
	NearbyRegions       int    `json:"nearby_regions"`
	RelatedClaims       int    `json:"related_claims"`
	AvailableExecutions int    `json:"available_executions"`
	Hint                string `json:"hint"`
}
type Provenance struct {
	ProjectionVersion string `json:"projection_version"`
	AttentionVersion  string `json:"attention_version"`
	EmbeddingModel    string `json:"embedding_model"`
	AsOf              string `json:"as_of"`
}
type TokenUsage struct {
	Estimated int `json:"estimated"`
	Budget    int `json:"budget"`
}
type ProcedureMatch struct {
	Score      float64                  `json:"score"`
	Procedure  procedure.Procedure      `json:"procedure"`
	Provenance ProcedureMatchProvenance `json:"provenance"`
}
type ProcedureMatchProvenance struct {
	ProjectionVersion string `json:"projection_version"`
}
type Packet struct {
	Protocol        string       `json:"protocol"`
	ProtocolVersion string       `json:"protocol_version"`
	RenderID        string       `json:"render_id"`
	FrameID         string       `json:"frame_id"`
	MemoryVersion   string       `json:"memory_version"`
	RendererVersion string       `json:"renderer_version"`
	Sections        []Section    `json:"sections"`
	OutsideFrame    OutsideFrame `json:"outside_frame"`
	Provenance      Provenance   `json:"provenance"`
	TokenUsage      TokenUsage   `json:"token_usage"`
}

type Stores interface {
	substrate.EventStore
	frame.Store
	objective.Store
	cognition.Store
}
type Renderer struct{ stores Stores }

func New(stores Stores) *Renderer { return &Renderer{stores: stores} }

func (r *Renderer) Render(ctx context.Context, request Request) (Packet, error) {
	if request.FrameID == "" || request.ObjectiveID == "" {
		return Packet{}, errors.New("frame_id and objective_id are required")
	}
	if request.BudgetTokens <= 0 {
		return Packet{}, errors.New("budget_tokens must be positive")
	}
	current, err := r.stores.GetFrame(ctx, request.FrameID)
	if err != nil {
		return Packet{}, err
	}
	goal, err := r.stores.GetObjective(ctx, request.ObjectiveID)
	if err != nil {
		return Packet{}, err
	}
	if current.ObjectiveID != goal.ObjectiveID || current.EpisodeID != goal.EpisodeID {
		return Packet{}, errors.New("objective does not belong to frame episode")
	}
	events, err := r.stores.List(ctx, substrate.EventFilter{EpisodeID: current.EpisodeID, BranchID: current.BranchID, AsOf: request.AsOf})
	if err != nil {
		return Packet{}, err
	}
	memoryVersion := "event:none"
	cutoff := request.AsOf
	if len(events) > 0 {
		memoryVersion = "event:" + events[len(events)-1].EventID
		if cutoff == nil {
			// The honest cutoff of a latest-memory render is the frontier of what
			// it saw: the valid time of the newest event in the packet.
			frontier := events[len(events)-1].ValidTime
			cutoff = &frontier
		}
	}
	focus, err := r.focusItem(ctx, current.Focus)
	if err != nil {
		return Packet{}, err
	}
	working := make([]any, 0, len(current.WorkingSet))
	for _, ref := range current.WorkingSet {
		item, resolveErr := r.refItem(ctx, ref)
		if resolveErr != nil {
			return Packet{}, resolveErr
		}
		working = append(working, item)
	}
	recent := make([]any, 0, len(events))
	for _, event := range events {
		// The packet is the model's view of memory; attention bookkeeping is
		// audit structure (see ambientHiddenEvents) and would dominate recent
		// as step count grows.
		if bookkeepingEvent(event.Type) {
			continue
		}
		recent = append(recent, compactEvent(event, true))
	}
	regions := []projection.Region{}
	if regionStore, ok := any(r.stores).(projection.RegionStore); ok {
		regions, err = regionStore.ListRegions(ctx, projection.RegionFilter{EpisodeID: current.EpisodeID, BranchID: current.BranchID, AsOf: request.AsOf})
		if err != nil {
			return Packet{}, err
		}
	}
	edges := []projection.Edge{}
	if edgeStore, ok := any(r.stores).(projection.EdgeStore); ok {
		edges, err = edgeStore.ListEdges(ctx, projection.EdgeFilter{EpisodeID: current.EpisodeID, BranchID: current.BranchID})
		if err != nil {
			return Packet{}, err
		}
	}
	entities := []entity.Entity{}
	entityRelations := []entity.EntityRelation{}
	if entityStore, ok := any(r.stores).(entity.Store); ok {
		if entities, err = entityStore.ListEntities(ctx, entity.Filter{}); err != nil {
			return Packet{}, err
		}
		if entityRelations, err = entityStore.ListEntityRelations(ctx, entity.RelationFilter{}); err != nil {
			return Packet{}, err
		}
	}
	ambient, err := selectAmbient(current, goal, events, regions, edges, entities, entityRelations)
	if err != nil {
		return Packet{}, err
	}
	mapItems := make([]any, 0, len(ambient.Selected))
	for _, selected := range ambient.Selected {
		mapItems = append(mapItems, mapItem(selected))
	}
	// Periphery is NOT a re-render of the map: it holds the near-miss
	// candidates attention ranked just below the cut, so the model sees what
	// sits right outside its frame without paying for the same content twice.
	periphery := make([]any, 0, len(ambient.RunnersUp))
	for _, runner := range ambient.RunnersUp {
		periphery = append(periphery, mapItem(runner))
	}
	procedureItems := []any{}
	if procedureStore, ok := any(r.stores).(procedure.Store); ok {
		values, listErr := procedureStore.ListProcedures(ctx, procedure.Filter{EpisodeID: current.EpisodeID})
		if listErr != nil {
			return Packet{}, listErr
		}
		matches := procedure.MatchProcedures(values, goal, current, procedure.MatchConfig{Threshold: procedureMatchThreshold})
		if len(matches) > maxProcedureMatches {
			matches = matches[:maxProcedureMatches]
		}
		procedureItems = make([]any, 0, len(matches))
		for _, match := range matches {
			procedureItems = append(procedureItems, ProcedureMatch{Score: match.Score, Procedure: match.Procedure, Provenance: ProcedureMatchProvenance{ProjectionVersion: match.Procedure.ProjectionVersion}})
		}
	}
	outside := ambient.Considered - len(ambient.Selected)
	hint := fmt.Sprintf("%d attention candidates are outside the frame", outside)
	sections := []Section{{Kind: "identity", Attention: "ambient", Items: []any{map[string]any{"agent_id": current.AgentID, "episode_id": current.EpisodeID, "branch_id": current.BranchID}}}, {Kind: "objective", Items: []any{goal}}, {Kind: "map", Attention: "ambient", Items: mapItems}, {Kind: "focus", Attention: "deliberate", Items: []any{focus}}, {Kind: "periphery", Attention: "ambient", Items: periphery}, {Kind: "working_set", Attention: "deliberate", Items: working}, {Kind: "procedures", Attention: "ambient", Items: procedureItems}, {Kind: "recent", Attention: "ambient", Items: recent}}
	// The affordances section closes the cognition→action loop: the model can
	// only request actions it can see, so available definitions travel with the
	// packet (deterministically ordered) instead of reaching step validation
	// unseen. Omitted entirely when the caller declares none.
	if items := affordanceItems(request.Affordances); len(items) > 0 {
		rest := append([]Section{{Kind: "affordances", Items: items}}, sections[2:]...)
		sections = append(sections[:2:2], rest...)
	}
	provenanceAsOf := current.AsOf
	if cutoff != nil {
		provenanceAsOf = *cutoff
	}
	packet := Packet{Protocol: protocol.Name, ProtocolVersion: protocol.Version, FrameID: current.FrameID, MemoryVersion: memoryVersion, RendererVersion: Version, Sections: sections, OutsideFrame: OutsideFrame{NearbyRegions: outside, Hint: hint}, Provenance: Provenance{ProjectionVersion: "event-candidates.v1", AttentionVersion: attention.Version, EmbeddingModel: "none", AsOf: provenanceAsOf.Format("2006-01-02T15:04:05.999999999Z07:00")}, TokenUsage: TokenUsage{Budget: request.BudgetTokens}}
	// Budget degradation ladder: drop the cheapest information first — old
	// recent events, then periphery near-misses, then the weakest map
	// candidates, then procedure matches (recorded patterns matter less than
	// live world data). Sections the model acts from (identity, objective,
	// focus, affordances, working set) are never trimmed.
	trimOrder := []string{"recent", "periphery", "map", "procedures"}
	for {
		packet.TokenUsage.Estimated = estimate(packet)
		if packet.TokenUsage.Estimated <= request.BudgetTokens {
			break
		}
		trimmed := false
		for _, kind := range trimOrder {
			for i := range packet.Sections {
				if packet.Sections[i].Kind != kind || len(packet.Sections[i].Items) == 0 {
					continue
				}
				if kind == "recent" {
					// Events are chronological: drop the oldest first.
					packet.Sections[i].Items = packet.Sections[i].Items[1:]
				} else {
					// Map/periphery are ranked best-first: drop the weakest.
					packet.Sections[i].Items = packet.Sections[i].Items[:len(packet.Sections[i].Items)-1]
				}
				trimmed = true
				break
			}
			if trimmed {
				break
			}
		}
		if !trimmed {
			return Packet{}, fmt.Errorf("budget %d is insufficient for required render sections", request.BudgetTokens)
		}
	}
	packet.RenderID = contentID(packet)
	return packet, nil
}
func selectAmbient(current frame.Frame, goal objective.Objective, events []protocol.Event, regions []projection.Region, edges []projection.Edge, entities []entity.Entity, entityRelations []entity.EntityRelation) (attention.Result, error) {
	if !current.Attention.Ambient {
		return attention.Result{Version: attention.Version, Selected: []attention.ScoredCandidate{}, RunnersUp: []attention.ScoredCandidate{}}, nil
	}
	pins := map[string]struct{}{}
	anchorRegions := map[string]struct{}{}
	anchorEntities := map[string]struct{}{}
	if current.Focus.Type == frame.RefRegion {
		anchorRegions[current.Focus.ID] = struct{}{}
	}
	if current.Focus.Type == frame.RefEntity {
		anchorEntities[current.Focus.ID] = struct{}{}
	}
	for _, ref := range current.WorkingSet {
		pins[string(ref.Type)+":"+ref.ID] = struct{}{}
		if ref.Type == frame.RefRegion {
			anchorRegions[ref.ID] = struct{}{}
		}
		if ref.Type == frame.RefEntity {
			anchorEntities[ref.ID] = struct{}{}
		}
	}
	graphProximity := regionGraphProximity(anchorRegions, edges)
	entityProximity := entityGraphProximity(anchorEntities, entityRelations)
	candidates := make([]attention.Candidate, 0, len(events)+len(regions))
	focusText := current.Focus.Query
	visible := make([]protocol.Event, 0, len(events))
	for _, event := range events {
		if _, hidden := ambientHiddenEvents[event.Type]; !hidden {
			visible = append(visible, event)
		}
	}
	for i, event := range visible {
		trust := .5
		if event.SourceTrust != nil {
			trust = float64(*event.SourceTrust)
		}
		if trust < float64(current.Filters.TrustMin) {
			continue
		}
		encoded, _ := json.Marshal(event.Payload)
		text := event.Type + " " + string(encoded)
		pin := 0.0
		if _, ok := pins["event:"+event.EventID]; ok {
			pin = 1
		}
		agent := 0.0
		if event.AgentID != "" && event.AgentID == current.AgentID {
			agent = 1
		}
		surprise := 0.0
		if event.Contradiction != nil {
			surprise = float64(*event.Contradiction)
		}
		recency := float64(i+1) / float64(len(events))
		candidates = append(candidates, attention.Candidate{Ref: frame.Ref{Type: frame.RefEvent, ID: event.EventID}, Features: attention.Features{SemanticRelevance: overlap(focusText, text), Recency: recency, Trust: trust, TaskRelevance: overlap(goal.Text, text), Surprise: surprise, AgentRelevance: agent, Pin: pin}, Payload: event})
	}
	for _, region := range regions {
		// Event-type regions aggregating attention bookkeeping are audit
		// structure, not content — same rule as the events themselves.
		if region.Kind == "event_type" && bookkeepingEvent(region.Label) {
			continue
		}
		trust := .5
		if trust < float64(current.Filters.TrustMin) {
			continue
		}
		pin := 0.0
		if _, ok := pins["region:"+region.RegionID]; ok {
			pin = 1
		}
		candidates = append(candidates, attention.Candidate{Ref: frame.Ref{Type: frame.RefRegion, ID: region.RegionID}, Features: attention.Features{SemanticRelevance: overlap(focusText, region.Label), GraphProximity: graphProximity[region.RegionID], Activation: region.Activation, Trust: trust, TaskRelevance: overlap(goal.Text, region.Label), Pin: pin}, Payload: region})
	}
	// M14.3: entities join the ambient candidate pool as world knowledge. They
	// are global (not episode-scoped), so an unrelated world must not flood the
	// frame: only entities with some relevance to the frame (focus overlap,
	// task overlap, graph proximity to a pinned entity, or pinned themselves)
	// compete, bounded by maxEntityCandidates strongest first.
	entityCandidates := make([]attention.Candidate, 0, len(entities))
	for _, value := range entities {
		pin := 0.0
		if _, ok := pins["entity:"+value.EntityID]; ok {
			pin = 1
		}
		label := value.Type + " " + value.Name
		semantic := overlap(focusText, label)
		task := overlap(goal.Text, label)
		proximity := entityProximity[value.EntityID]
		if pin == 0 && semantic == 0 && task == 0 && proximity == 0 {
			continue
		}
		activation := float64(value.MentionCount) / 10
		if activation > 1 {
			activation = 1
		}
		entityCandidates = append(entityCandidates, attention.Candidate{Ref: frame.Ref{Type: frame.RefEntity, ID: value.EntityID}, Features: attention.Features{SemanticRelevance: semantic, GraphProximity: proximity, Activation: activation, Trust: float64(value.Confidence), TaskRelevance: task, Pin: pin}, Payload: value})
	}
	sort.SliceStable(entityCandidates, func(i, j int) bool {
		a, b := entityCandidates[i].Features, entityCandidates[j].Features
		relevance := func(f attention.Features) float64 {
			return f.SemanticRelevance + f.TaskRelevance + f.GraphProximity + f.Pin
		}
		return relevance(a) > relevance(b)
	})
	if len(entityCandidates) > maxEntityCandidates {
		entityCandidates = entityCandidates[:maxEntityCandidates]
	}
	candidates = append(candidates, entityCandidates...)
	engine, err := attention.New(attention.DefaultPolicy())
	if err != nil {
		return attention.Result{}, err
	}
	return engine.SelectAmbient(candidates, pins, current.Attention.MaxCandidates)
}

// affordanceItems renders the compact affordance listing for the packet:
// id, execution mode, capabilities and (when declared) the input schema, so
// the model can form valid action requests without guessing.
func affordanceItems(definitions []affordance.Definition) []any {
	if len(definitions) == 0 {
		return nil
	}
	ordered := make([]affordance.Definition, len(definitions))
	copy(ordered, definitions)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	items := make([]any, 0, len(ordered))
	for _, definition := range ordered {
		item := map[string]any{"id": definition.ID, "execution_mode": definition.ExecutionMode, "capabilities": definition.Capabilities}
		if len(definition.InputSchema) > 0 {
			item["input_schema"] = definition.InputSchema
		}
		items = append(items, item)
	}
	return items
}

func regionGraphProximity(anchors map[string]struct{}, edges []projection.Edge) map[string]float64 {
	result := make(map[string]float64)
	for _, edge := range edges {
		if _, ok := anchors[edge.SourceRegionID]; ok && edge.Weight > result[edge.TargetRegionID] {
			result[edge.TargetRegionID] = edge.Weight
		}
		if _, ok := anchors[edge.TargetRegionID]; ok && edge.Weight > result[edge.SourceRegionID] {
			result[edge.SourceRegionID] = edge.Weight
		}
	}
	return result
}

// entityGraphProximity maps entity ids to their strongest relation confidence
// from any anchored entity; anchored entities themselves are excluded so the
// proximity feature rewards neighbours, not the anchors.
func entityGraphProximity(anchors map[string]struct{}, relations []entity.EntityRelation) map[string]float64 {
	result := make(map[string]float64)
	for _, relation := range relations {
		_, sourceAnchored := anchors[relation.SourceID]
		_, targetAnchored := anchors[relation.TargetID]
		if sourceAnchored && !targetAnchored && float64(relation.Confidence) > result[relation.TargetID] {
			result[relation.TargetID] = float64(relation.Confidence)
		}
		if targetAnchored && !sourceAnchored && float64(relation.Confidence) > result[relation.SourceID] {
			result[relation.SourceID] = float64(relation.Confidence)
		}
	}
	return result
}

func overlap(left, right string) float64 {
	terms := func(value string) map[string]struct{} {
		result := map[string]struct{}{}
		for _, term := range strings.Fields(strings.ToLower(value)) {
			term = strings.Trim(term, ".,:;!?()[]{}\"")
			if len(term) > 2 {
				result[term] = struct{}{}
			}
		}
		return result
	}
	a, b := terms(left), terms(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	matches := 0
	for term := range a {
		if _, ok := b[term]; ok {
			matches++
		}
	}
	denominator := len(a)
	if len(b) < denominator {
		denominator = len(b)
	}
	return float64(matches) / float64(denominator)
}

func (r *Renderer) focusItem(ctx context.Context, focus frame.Focus) (any, error) {
	if focus.Type == frame.RefQuery {
		return map[string]any{"type": focus.Type, "query": focus.Query}, nil
	}
	return r.refItem(ctx, frame.Ref{Type: focus.Type, ID: focus.ID})
}
func (r *Renderer) refItem(ctx context.Context, ref frame.Ref) (any, error) {
	switch ref.Type {
	case frame.RefClaim:
		return r.stores.GetClaim(ctx, ref.ID)
	case frame.RefEvent:
		event, err := r.stores.Get(ctx, ref.ID)
		if err != nil {
			return nil, err
		}
		return compactEvent(event, false), nil
	case frame.RefEntity:
		if entityStore, ok := any(r.stores).(entity.Store); ok {
			value, err := entityStore.GetEntity(ctx, ref.ID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ref": canonicalRef(ref), "type": value.Type, "name": value.Name}, nil
		}
		return map[string]any{"type": ref.Type, "id": ref.ID}, nil
	default:
		return map[string]any{"type": ref.Type, "id": ref.ID}, nil
	}
}

// canonicalRef is the string form the model copies into emission refs.
func canonicalRef(ref frame.Ref) string { return string(ref.Type) + ":" + ref.ID }

// mapItem compacts an attention candidate for the packet: canonical string
// ref, the score, and the content itself. Region and entity projections shed
// their bookkeeping fields; events shed the protocol envelope entirely.
func mapItem(candidate attention.ScoredCandidate) MapItem {
	var payload any
	switch value := candidate.Candidate.Payload.(type) {
	case protocol.Event:
		payload = map[string]any{"type": value.Type, "payload": value.Payload}
	case projection.Region:
		payload = map[string]any{"kind": value.Kind, "label": value.Label, "activation": value.Activation}
	case entity.Entity:
		payload = map[string]any{"type": value.Type, "name": value.Name}
	default:
		payload = candidate.Candidate.Payload
	}
	return MapItem{Ref: canonicalRef(candidate.Candidate.Ref), Score: candidate.Score, Payload: payload}
}

// compactEvent strips the protocol envelope (version, agent/branch/episode
// ids, provenance) down to what cognition needs: the canonical ref, the type
// and the content. The envelope remains in the substrate for audit and
// time-travel. withTime keeps valid_time where chronology is the point of the
// section (recent); map items already carry attention's recency score.
func compactEvent(event protocol.Event, withTime bool) map[string]any {
	item := map[string]any{"ref": canonicalRef(frame.Ref{Type: frame.RefEvent, ID: event.EventID}), "type": event.Type, "payload": event.Payload}
	if withTime {
		item["valid_time"] = event.ValidTime
	}
	return item
}
func estimate(value any) int {
	data, _ := json.Marshal(value)
	tokens := (len(data) + 3) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}
func contentID(packet Packet) string {
	packet.RenderID = ""
	data, _ := json.Marshal(packet)
	sum := sha256.Sum256(data)
	raw := sum[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := hex.EncodeToString(raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
