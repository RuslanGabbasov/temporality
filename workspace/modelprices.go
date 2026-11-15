package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Model token prices (docs/plan-cost-accounting.md): the operator's editor
// for the KERNEL_MODEL_PRICES table lives in the UI, so the values need a
// home of their own — the workspace_setting KV, the same row family as the
// HAB coefficients and the harness budget overrides. The env value stays the
// seed; a saved list replaces it wholesale (the kernel applies it at boot and
// live through the settings route).
const SettingModelPrices = "cost.model_prices"

// maxModelPrices bounds one saved list: every provider catalog the UI can
// render fits well below it, and it keeps a corrupted or hostile row small.
const maxModelPrices = 500

// ModelPrice is one saved row: dollars per 1 000 000 tokens, the unit a
// price page quotes. The JSON shape is the API's — the settings route
// converts it to cost.Price, which owns validation.
type ModelPrice struct {
	Model       string  `json:"model"`
	PromptPer1M float64 `json:"prompt_per_1m"`
	CompPer1M   float64 `json:"completion_per_1m"`
}

// ModelPrices reads the saved list. An absent (or empty) row is not an error:
// it means "no override", and the env seed keeps serving.
func (s *Store) ModelPrices(ctx context.Context) ([]ModelPrice, error) {
	raw, err := s.GetSetting(ctx, SettingModelPrices)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var list []ModelPrice
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("decode %s: %w", SettingModelPrices, err)
	}
	if len(list) > maxModelPrices {
		return nil, fmt.Errorf("%s holds %d rows, the cap is %d", SettingModelPrices, len(list), maxModelPrices)
	}
	return list, nil
}

// SaveModelPrices replaces the stored list. An empty list clears the override
// and hands the run back to KERNEL_MODEL_PRICES.
func (s *Store) SaveModelPrices(ctx context.Context, list []ModelPrice) error {
	if len(list) > maxModelPrices {
		return fmt.Errorf("%d prices, the cap is %d", len(list), maxModelPrices)
	}
	for _, price := range list {
		if strings.TrimSpace(price.Model) == "" {
			return errors.New("every price needs a model")
		}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, SettingModelPrices, string(raw))
}
