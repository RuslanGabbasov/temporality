package workspace

import (
	"context"
	"os"
	"strings"
)

// Installation-wide LLM-judge model resolution. The judge machinery of the
// old eval gate is gone (docs/agent-evolution.md §8); what remains is the
// model setting itself — the knowledge contours (conflict judging, area
// summaries) and the onboarding read it through JudgeConfig. Calibration and
// degraded states were removed with the gate.

// EnvJudgeModel is the env fallback of the judge model setting.
const EnvJudgeModel = "EVAL_JUDGE_MODEL"

// JudgeConfig is the installation-wide judge model resolution: the operator
// setting (UI) or EVAL_JUDGE_MODEL validated against the models of the
// configured providers. Empty or unknown ⇒ judge mode is off
// (configured=false). Source says which knob is in effect — "setting" (UI),
// "env" or "" (off) — so the interface can label what it edits.
type JudgeConfig struct {
	Model      string `json:"model"`
	ProviderID string `json:"provider_id,omitempty"`
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
}

// ProviderOfModel finds the provider that lists the given model. Shared by
// judge resolution and the settings endpoint that validates an operator's
// choice before saving it.
func (s *Store) ProviderOfModel(ctx context.Context, model string) (string, bool, error) {
	providers, err := s.ListProviders(ctx)
	if err != nil {
		return "", false, err
	}
	for _, p := range providers {
		for _, candidate := range p.Models {
			if strings.TrimSpace(candidate) == model {
				return p.ID, true, nil
			}
		}
	}
	return "", false, nil
}

// JudgeConfig resolves the judge model: the UI setting first, then
// EVAL_JUDGE_MODEL. A stored row wins even when empty — an operator who
// turned the judge off in the interface must not be re-enabled by the
// environment.
func (s *Store) JudgeConfig(ctx context.Context) (JudgeConfig, error) {
	model := ""
	source := ""
	if value, found, err := s.getSettingFound(ctx, SettingJudgeModel); err != nil {
		return JudgeConfig{}, err
	} else if found {
		model = strings.TrimSpace(value)
		source = "setting"
	} else {
		model = strings.TrimSpace(os.Getenv(EnvJudgeModel))
		if model != "" {
			source = "env"
		}
	}
	if model == "" {
		return JudgeConfig{}, nil
	}
	providerID, ok, err := s.ProviderOfModel(ctx, model)
	if err != nil {
		return JudgeConfig{}, err
	}
	if !ok {
		return JudgeConfig{Model: model, Source: source}, nil
	}
	return JudgeConfig{Model: model, ProviderID: providerID, Configured: true, Source: source}, nil
}
