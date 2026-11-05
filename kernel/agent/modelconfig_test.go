package agent

import (
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

// The per-model output cap (docs/agents.md §routing) overrides the shared
// kernel default: TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS (16k) is a conservative
// floor no capable model should inherit by accident — a 44 KB artifact edit
// was truncated mid tool-call by exactly that floor (finish: length @ 16384).
func TestApplyModelOutputCap(t *testing.T) {
	config := llm.Config{MaxOutputTokens: 16384}
	applyModelOutputCap(&config, modelConfigAnswer{MaxOutputTokens: 32768})
	if config.MaxOutputTokens != 32768 {
		t.Fatalf("MaxOutputTokens = %d, want the per-model 32768", config.MaxOutputTokens)
	}
	// An absent cap must not reset the configured default.
	applyModelOutputCap(&config, modelConfigAnswer{})
	if config.MaxOutputTokens != 32768 {
		t.Fatalf("an absent cap must leave the value alone, got %d", config.MaxOutputTokens)
	}
	// A zero/absent answer cap leaves the default flow intact.
	fresh := llm.Config{MaxOutputTokens: 16384}
	applyModelOutputCap(&fresh, modelConfigAnswer{MaxOutputTokens: 0})
	if fresh.MaxOutputTokens != 16384 {
		t.Fatalf("MaxOutputTokens = %d, want the untouched default", fresh.MaxOutputTokens)
	}
}

// The routing answer carries the cap end to end: pinnedConfigFrom decodes it
// into the call config.
func TestPinnedConfigFromCarriesModelCap(t *testing.T) {
	activities := &Activities{}
	config, _, err := activities.pinnedConfigFrom(
		`{"provider_id":"prov-x","provider_name":"X","base_url":"http://x","api_key":"k","model":"mimo-v2.6-pro","max_output_tokens":32768}`,
		200, "prov-x", "mimo-v2.6-pro")
	if err != nil {
		t.Fatalf("pinnedConfigFrom: %v", err)
	}
	if config.MaxOutputTokens != 32768 {
		t.Fatalf("MaxOutputTokens = %d, want the routing answer's 32768", config.MaxOutputTokens)
	}
	config, _, err = activities.pinnedConfigFrom(
		`{"provider_id":"prov-x","provider_name":"X","base_url":"http://x","api_key":"k","model":"m"}`,
		200, "prov-x", "m")
	if err != nil {
		t.Fatalf("pinnedConfigFrom: %v", err)
	}
	if config.MaxOutputTokens <= 0 {
		t.Fatalf("the shared default must still apply without a cap, got %d", config.MaxOutputTokens)
	}
}
