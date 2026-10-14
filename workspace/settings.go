package workspace

import (
	"context"
	"errors"
	"os"
	"time"
)

// Installation-level quality settings (docs/agent-quality-loop.md §15): the
// judge and the run frequency were env-only; these helpers read them through
// the existing workspace_setting KV (migration 000057). A present row is the
// operator's explicit UI choice; a missing row falls back to the environment
// default, so existing deployments keep working unchanged.

// Setting keys. Values are plain strings; typed helpers below parse them.
const (
	// SettingJudgeModel: the LLM-judge model (docs/agent-quality-loop.md §6.2).
	// Empty value = judge off. Falls back to EVAL_JUDGE_MODEL.
	SettingJudgeModel = "eval.judge.model"
	// SettingEvalScheduleInterval: how often evaluation suites re-run
	// (docs/agent-quality-loop.md §15, trigger=schedule). Go duration string;
	// "0" or empty = off. Falls back to EVAL_SCHEDULE_INTERVAL.
	SettingEvalScheduleInterval = "eval.schedule.interval"
)

// EnvEvalScheduleInterval seeds the schedule when no setting row exists.
const EnvEvalScheduleInterval = "EVAL_SCHEDULE_INTERVAL"

// getSettingFound distinguishes "no row" from "row with empty value": the
// first means "use the env default", the second is an explicit operator
// choice (e.g. judge off) that must override the environment.
func (s *Store) getSettingFound(ctx context.Context, key string) (value string, found bool, err error) {
	value, err = s.GetSetting(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// parseEvalSchedule parses a schedule setting/env value: empty or "0"
// disables, anything unparseable or negative also reads as off — a broken
// knob must never produce a hot loop.
func parseEvalSchedule(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// EvalScheduleInterval resolves the scheduled quality-run cadence: the setting
// row first, then EVAL_SCHEDULE_INTERVAL, then off.
func (s *Store) EvalScheduleInterval(ctx context.Context) (time.Duration, error) {
	raw := ""
	if value, found, err := s.getSettingFound(ctx, SettingEvalScheduleInterval); err != nil {
		return 0, err
	} else if found {
		raw = value
	} else {
		raw = os.Getenv(EnvEvalScheduleInterval)
	}
	return parseEvalSchedule(raw), nil
}

// LatestScheduledEvalRun returns the newest trigger=schedule run timestamp
// across all agents — the anchor the scheduler adds the interval to. Zero
// time means "never ran", so the first cycle fires immediately.
func (s *Store) LatestScheduledEvalRun(ctx context.Context) (time.Time, error) {
	var last *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT max(created_at) FROM workspace_agent_evaluation_run WHERE trigger = $1`,
		TriggerSchedule).Scan(&last); err != nil {
		return time.Time{}, err
	}
	if last == nil {
		return time.Time{}, nil
	}
	return *last, nil
}

// ListEvalSuiteAgentIDs returns agents with a non-empty evaluation suite —
// the schedule's work list. Agents without cases have nothing to re-run.
func (s *Store) ListEvalSuiteAgentIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT agent_id FROM workspace_agent_evaluation_suite
		WHERE jsonb_array_length(cases) > 0 ORDER BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
