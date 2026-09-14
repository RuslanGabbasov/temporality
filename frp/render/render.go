package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
)

const Version = "render-0.3.1"

type Request struct {
	FrameID      string `json:"frame_id"`
	ObjectiveID  string `json:"objective_id"`
	BudgetTokens int    `json:"budget_tokens"`
}
type Section struct {
	Kind      string `json:"kind"`
	Attention string `json:"attention,omitempty"`
	Items     []any  `json:"items"`
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
	events, err := r.stores.List(ctx, substrate.EventFilter{EpisodeID: current.EpisodeID, BranchID: current.BranchID, AsOf: &current.AsOf})
	if err != nil {
		return Packet{}, err
	}
	memoryVersion := "event:none"
	if len(events) > 0 {
		memoryVersion = "event:" + events[len(events)-1].EventID
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
		recent = append(recent, event)
	}
	sections := []Section{{Kind: "identity", Attention: "ambient", Items: []any{map[string]any{"agent_id": current.AgentID, "episode_id": current.EpisodeID, "branch_id": current.BranchID}}}, {Kind: "objective", Items: []any{goal}}, {Kind: "map", Attention: "ambient", Items: []any{}}, {Kind: "focus", Attention: "deliberate", Items: []any{focus}}, {Kind: "periphery", Attention: "ambient", Items: []any{}}, {Kind: "working_set", Attention: "deliberate", Items: working}, {Kind: "procedures", Attention: "ambient", Items: []any{}}, {Kind: "recent", Attention: "ambient", Items: recent}}
	packet := Packet{Protocol: protocol.Name, ProtocolVersion: protocol.Version, FrameID: current.FrameID, MemoryVersion: memoryVersion, RendererVersion: Version, Sections: sections, OutsideFrame: OutsideFrame{Hint: "No projection-backed outside-frame counts in M3"}, Provenance: Provenance{ProjectionVersion: "none", AttentionVersion: "none", EmbeddingModel: "none", AsOf: current.AsOf.Format("2006-01-02T15:04:05.999999999Z07:00")}, TokenUsage: TokenUsage{Budget: request.BudgetTokens}}
	for {
		packet.TokenUsage.Estimated = estimate(packet)
		if packet.TokenUsage.Estimated <= request.BudgetTokens {
			break
		}
		recentSection := &packet.Sections[len(packet.Sections)-1]
		if len(recentSection.Items) == 0 {
			return Packet{}, fmt.Errorf("budget %d is insufficient for required render sections", request.BudgetTokens)
		}
		recentSection.Items = recentSection.Items[1:]
	}
	packet.RenderID = contentID(packet)
	return packet, nil
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
		return r.stores.Get(ctx, ref.ID)
	default:
		return map[string]any{"type": ref.Type, "id": ref.ID}, nil
	}
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
