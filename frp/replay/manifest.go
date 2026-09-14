package replay

import "github.com/temporality-project/temporality/frp/protocol"

const Version = "1"

// Manifest captures every implementation and model input needed to explain a
// replay. Empty fields represent components not introduced by the current
// Temporality milestone yet.
type Manifest struct {
	ProtocolVersion      string            `json:"protocol_version"`
	ReplayVersion        string            `json:"replay_version"`
	RuntimeVersion       string            `json:"runtime_version"`
	PolicyVersion        string            `json:"policy_version,omitempty"`
	RendererVersion      string            `json:"renderer_version,omitempty"`
	AttentionVersion     string            `json:"attention_version,omitempty"`
	ProjectionVersions   map[string]string `json:"projection_versions,omitempty"`
	EmbeddingModel       string            `json:"embedding_model,omitempty"`
	ModelID              string            `json:"model_id,omitempty"`
	GenerationParameters map[string]any    `json:"generation_parameters,omitempty"`
	InitialFrameID       string            `json:"initial_frame_id,omitempty"`
	ObjectiveID          string            `json:"objective_id,omitempty"`
}

func DefaultManifest() Manifest {
	return Manifest{
		ProtocolVersion: protocol.Version,
		ReplayVersion:   Version,
		RuntimeVersion:  "temporality-m0",
	}
}

func (m Manifest) withDefaults() Manifest {
	defaults := DefaultManifest()
	if m.ProtocolVersion == "" {
		m.ProtocolVersion = defaults.ProtocolVersion
	}
	if m.ReplayVersion == "" {
		m.ReplayVersion = defaults.ReplayVersion
	}
	if m.RuntimeVersion == "" {
		m.RuntimeVersion = defaults.RuntimeVersion
	}
	return m
}
