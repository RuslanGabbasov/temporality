package frame

import (
	"errors"
	"fmt"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
)

const ReducerVersion = "frame-reducer.v1"

// MaxWorkingSet bounds how many deliberate anchors a frame may carry. A
// runaway emission pinning without evicting would grow the working set — and
// every later packet — without limit (context thrashing, M16). The reducer
// itself stays permissive for historical transitions; the cognitive guard in
// cognition.ReduceEmission enforces the cap on new emissions.
const MaxWorkingSet = 32

type RefType string

const (
	RefRegion    RefType = "region"
	RefClaim     RefType = "claim"
	RefEvent     RefType = "event"
	RefQuery     RefType = "query"
	RefExecution RefType = "execution"
	RefEntity    RefType = "entity"
)

type Focus struct {
	Type  RefType `json:"type"`
	ID    string  `json:"id,omitempty"`
	Query string  `json:"query,omitempty"`
}
type Ref struct {
	Type RefType `json:"type"`
	ID   string  `json:"id"`
}
type Mode string

const (
	ModeExplore Mode = "explore"
	ModeExploit Mode = "exploit"
	ModeReflect Mode = "reflect"
	ModeVerify  Mode = "verify"
)

type Attention struct {
	Policy        string `json:"policy"`
	Deliberate    bool   `json:"deliberate"`
	Ambient       bool   `json:"ambient"`
	MaxCandidates int    `json:"max_candidates"`
}
type Filters struct {
	TrustMin    float32  `json:"trust_min"`
	AgentIDs    []string `json:"agent_ids"`
	RegionKinds []string `json:"region_kinds"`
}
type Budget struct {
	Tokens int `json:"tokens"`
}

type Frame struct {
	Protocol      string    `json:"protocol"`
	Version       string    `json:"version"`
	FrameID       string    `json:"frame_id"`
	ParentFrameID string    `json:"parent_frame_id,omitempty"`
	AgentID       string    `json:"agent_id"`
	EpisodeID     string    `json:"episode_id"`
	BranchID      string    `json:"branch_id"`
	ObjectiveID   string    `json:"objective_id"`
	AsOf          time.Time `json:"as_of"`
	Focus         Focus     `json:"focus"`
	WorkingSet    []Ref     `json:"working_set"`
	Mode          Mode      `json:"mode"`
	Attention     Attention `json:"attention"`
	Zoom          int       `json:"zoom"`
	Filters       Filters   `json:"filters"`
	Budget        Budget    `json:"budget"`
	Revision      uint64    `json:"revision"`
}

func (f *Frame) ApplyDefaults() {
	if f.Protocol == "" {
		f.Protocol = protocol.Name
	}
	if f.Version == "" {
		f.Version = protocol.Version
	}
	if f.Mode == "" {
		f.Mode = ModeExplore
	}
	if f.Attention.Policy == "" {
		f.Attention.Policy = "balanced"
	}
	if f.Attention.MaxCandidates == 0 {
		f.Attention.MaxCandidates = 32
	}
	if f.Budget.Tokens == 0 {
		f.Budget.Tokens = 8000
	}
	if f.WorkingSet == nil {
		f.WorkingSet = []Ref{}
	}
	if f.Filters.AgentIDs == nil {
		f.Filters.AgentIDs = []string{}
	}
	if f.Filters.RegionKinds == nil {
		f.Filters.RegionKinds = []string{}
	}
}

func (f Frame) Validate() error {
	if f.Protocol != protocol.Name || f.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", f.Protocol, f.Version)
	}
	if f.FrameID == "" || f.AgentID == "" || f.EpisodeID == "" || f.BranchID == "" || f.ObjectiveID == "" {
		return errors.New("frame and ownership IDs are required")
	}
	if f.AsOf.IsZero() {
		return errors.New("as_of is required")
	}
	if f.Revision > 0 && f.ParentFrameID == "" {
		return errors.New("non-initial frame requires parent_frame_id")
	}
	if err := f.Focus.Validate(); err != nil {
		return err
	}
	for _, ref := range f.WorkingSet {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	switch f.Mode {
	case ModeExplore, ModeExploit, ModeReflect, ModeVerify:
	default:
		return fmt.Errorf("invalid mode %q", f.Mode)
	}
	if f.Attention.MaxCandidates < 1 {
		return errors.New("attention max_candidates must be positive")
	}
	if f.Zoom < 0 {
		return errors.New("zoom cannot be negative")
	}
	if f.Filters.TrustMin < 0 || f.Filters.TrustMin > 1 {
		return errors.New("trust_min must be between 0 and 1")
	}
	if f.Budget.Tokens < 1 {
		return errors.New("token budget must be positive")
	}
	return nil
}
func (f Focus) Validate() error {
	if f.Type == RefQuery {
		if f.Query == "" || f.ID != "" {
			return errors.New("query focus requires query and no id")
		}
		return nil
	}
	if f.Type != RefRegion && f.Type != RefClaim && f.Type != RefEvent && f.Type != RefEntity {
		return fmt.Errorf("invalid focus type %q", f.Type)
	}
	if f.ID == "" || f.Query != "" {
		return errors.New("reference focus requires id and no query")
	}
	return nil
}
func (r Ref) Validate() error {
	if r.ID == "" {
		return errors.New("working set ref id is required")
	}
	switch r.Type {
	case RefRegion, RefClaim, RefEvent, RefExecution, RefEntity:
		return nil
	default:
		return fmt.Errorf("invalid working set ref type %q", r.Type)
	}
}
