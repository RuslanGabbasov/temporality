package workspace

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Harness budget overrides (docs/harness-settings.md §6): per-knob values the
// operator saves from the settings UI, layered over the env-resolved harness
// budgets. Same workspace_setting KV as the rest of the settings surface; a
// missing row means "env/default", a row is the operator's explicit choice
// that survives env edits until reset. Runs pin the composed snapshot at
// start, so a change never disturbs an in-flight run.

// SettingHarnessBudgetPrefix namespaces the override rows; the suffix is the
// Budgets json tag from kernel/harness, e.g.
// "harness.budgets.compaction_tail_bytes".
const SettingHarnessBudgetPrefix = "harness.budgets."

// HarnessBudgetOverrides returns the saved overrides keyed by budget json
// tag. Rows whose values no longer parse are skipped — a broken row degrades
// to env/default instead of breaking the boot, matching the settings surface.
func (s *Store) HarnessBudgetOverrides(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key, value FROM workspace_setting WHERE key LIKE $1`, SettingHarnessBudgetPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	overrides := map[string]int{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		overrides[strings.TrimPrefix(key, SettingHarnessBudgetPrefix)] = parsed
	}
	return overrides, rows.Err()
}

// SetHarnessBudgetOverrides upserts the given overrides and deletes the
// reset keys in one pass: the UI sends only the rows it changed plus the rows
// the operator reset back to env/default.
func (s *Store) SetHarnessBudgetOverrides(ctx context.Context, sets map[string]int, resets []string) error {
	for key, value := range sets {
		if err := s.SetSetting(ctx, SettingHarnessBudgetPrefix+key, strconv.Itoa(value)); err != nil {
			return fmt.Errorf("write %s%s: %w", SettingHarnessBudgetPrefix, key, err)
		}
	}
	for _, key := range resets {
		if err := s.DeleteSetting(ctx, SettingHarnessBudgetPrefix+key); err != nil {
			return fmt.Errorf("reset %s%s: %w", SettingHarnessBudgetPrefix, key, err)
		}
	}
	return nil
}
