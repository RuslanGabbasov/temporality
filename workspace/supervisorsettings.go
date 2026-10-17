package workspace

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Supervisor settings (docs/supervisor-model.md): the installation-wide
// configuration of the model that works FOR the administrator rather than on
// tasks — judging evaluations, monitoring harness problems, and (planned)
// triage of failures and user friction. Stored in the workspace_setting KV;
// every value is validated on write and has a sane default on read, so a
// broken row degrades to defaults instead of breaking runs.
const (
	// SettingSupervisorVisionModel: the model used for image-bearing calls when
	// the run's main model is not multimodal. Empty = images go to the main
	// model as before.
	SettingSupervisorVisionModel = "supervisor.vision.model"
	// Human attention estimation coefficients (docs/supervisor-model.md §5):
	// Human Attention Spend is an ESTIMATE of the time a human spends reading
	// conclusions and writing answers — not the wall-clock wait.
	SettingHabApprovalSeconds   = "supervisor.hab.approval_seconds"    // per approval/reconcile decision
	SettingHabAnswerBaseSeconds = "supervisor.hab.answer_base_seconds" // per ask_human answer, base
	SettingHabReadingWPM        = "supervisor.hab.reading_wpm"         // reading speed, words per minute
)

// Defaults for the human-attention estimate. An approval decision (reading
// the operation summary, deciding) lands around 45 seconds; an ask_human
// answer adds a base 20 seconds for orientation before the typed answer.
const (
	DefaultHabApprovalSeconds   = 45
	DefaultHabAnswerBaseSeconds = 20
	DefaultHabReadingWPM        = 180
)

// SupervisorSettings is the full editable surface of the supervisor panel.
// Embeddings have no model setting: inference is local (rembed in-process),
// the model id travels via KERNEL_EMBEDDINGS_MODEL for operators.
type SupervisorSettings struct {
	JudgeModel  string       `json:"judge_model"`
	VisionModel string       `json:"vision_model"`
	HAB         HumanFactors `json:"hab"`
}

// HumanFactors carries the human-attention estimate coefficients (HAB) and is
// the operator-facing knob set for docs/supervisor-model.md §5.
type HumanFactors struct {
	ApprovalSeconds   int `json:"approval_seconds"`
	AnswerBaseSeconds int `json:"answer_base_seconds"`
	ReadingWPM        int `json:"reading_wpm"`
}

func (s *Store) settingInt(ctx context.Context, key string, fallback int) int {
	raw, err := s.GetSetting(ctx, key)
	if err != nil || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// HumanFactors resolves the attention-estimate coefficients: setting rows
// first, defaults otherwise.
func (s *Store) HumanFactors(ctx context.Context) (HumanFactors, error) {
	return HumanFactors{
		ApprovalSeconds:   s.settingInt(ctx, SettingHabApprovalSeconds, DefaultHabApprovalSeconds),
		AnswerBaseSeconds: s.settingInt(ctx, SettingHabAnswerBaseSeconds, DefaultHabAnswerBaseSeconds),
		ReadingWPM:        s.settingInt(ctx, SettingHabReadingWPM, DefaultHabReadingWPM),
	}, nil
}

// SupervisorSettings reads the whole panel surface in one call. Missing rows
// read as empty model (feature off) / default coefficients.
func (s *Store) SupervisorSettings(ctx context.Context) (SupervisorSettings, error) {
	judge, err := s.JudgeModelSettingValue(ctx)
	if err != nil {
		return SupervisorSettings{}, err
	}
	vision, err := s.GetSetting(ctx, SettingSupervisorVisionModel)
	if err != nil && err != ErrNotFound {
		return SupervisorSettings{}, err
	}
	hab, err := s.HumanFactors(ctx)
	if err != nil {
		return SupervisorSettings{}, err
	}
	return SupervisorSettings{
		JudgeModel:  judge,
		VisionModel: vision,
		HAB:         hab,
	}, nil
}

// SaveSupervisorSettings writes the editable values. Empty model strings mean
// "off"; zero/negative HAB values fall back to defaults. Callers validate
// model names against the provider registry before saving.
func (s *Store) SaveSupervisorSettings(ctx context.Context, settings SupervisorSettings) error {
	write := func(key, value string) error {
		if err := s.SetSetting(ctx, key, value); err != nil {
			return fmt.Errorf("write %s: %w", key, err)
		}
		return nil
	}
	if err := write(SettingJudgeModel, strings.TrimSpace(settings.JudgeModel)); err != nil {
		return err
	}
	if err := write(SettingSupervisorVisionModel, strings.TrimSpace(settings.VisionModel)); err != nil {
		return err
	}
	hab := settings.HAB
	if hab.ApprovalSeconds <= 0 {
		hab.ApprovalSeconds = DefaultHabApprovalSeconds
	}
	if hab.AnswerBaseSeconds <= 0 {
		hab.AnswerBaseSeconds = DefaultHabAnswerBaseSeconds
	}
	if hab.ReadingWPM <= 0 {
		hab.ReadingWPM = DefaultHabReadingWPM
	}
	if err := write(SettingHabApprovalSeconds, strconv.Itoa(hab.ApprovalSeconds)); err != nil {
		return err
	}
	if err := write(SettingHabAnswerBaseSeconds, strconv.Itoa(hab.AnswerBaseSeconds)); err != nil {
		return err
	}
	return write(SettingHabReadingWPM, strconv.Itoa(hab.ReadingWPM))
}

// JudgeModelSettingValue returns the stored judge model setting ("" = unset).
func (s *Store) JudgeModelSettingValue(ctx context.Context) (string, error) {
	value, err := s.GetSetting(ctx, SettingJudgeModel)
	if err != nil && err != ErrNotFound {
		return "", err
	}
	return value, nil
}
