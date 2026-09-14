package timetravel

import "errors"

const ReplayContractVersion = "1"

// ReplayContract makes purity constraints explicit at the API boundary.
// Callers cannot describe replay that performs writes or external side effects.
type ReplayContract struct {
	Version                 string `json:"version"`
	Deterministic           bool   `json:"deterministic"`
	ReadOnly                bool   `json:"read_only"`
	SideEffectsAllowed      bool   `json:"side_effects_allowed"`
	IrreversibleEffectsRun  bool   `json:"irreversible_effects_run"`
	ExternalInputsFromTrace bool   `json:"external_inputs_from_trace"`
}

func PureReplayContract() ReplayContract {
	return ReplayContract{
		Version:                 ReplayContractVersion,
		Deterministic:           true,
		ReadOnly:                true,
		SideEffectsAllowed:      false,
		IrreversibleEffectsRun:  false,
		ExternalInputsFromTrace: true,
	}
}

func (c ReplayContract) Validate() error {
	if c.Version != ReplayContractVersion {
		return errors.New("unsupported replay contract version")
	}
	if !c.Deterministic || !c.ReadOnly || c.SideEffectsAllowed || c.IrreversibleEffectsRun || !c.ExternalInputsFromTrace {
		return errors.New("replay contract violates purity invariants")
	}
	return nil
}

type ReplayRequest struct {
	EpisodeID string            `json:"episode_id"`
	Selection SnapshotSelection `json:"selection"`
	Contract  ReplayContract    `json:"contract"`
}

func (r ReplayRequest) Validate() error {
	if r.EpisodeID == "" {
		return errors.New("episode_id is required")
	}
	if err := r.Selection.Validate(); err != nil {
		return err
	}
	return r.Contract.Validate()
}
