package main

// Unit tests for the model-price settings surface (docs/plan-cost-accounting):
// which list the kernel actually runs on, and in which order the API renders it.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/workspace"
)

func TestActiveModelPricesFallsBackToTheEnvSeed(t *testing.T) {
	seed := []cost.Price{{Model: "gpt-4o", PromptPer1M: 2.5, CompPer1M: 10}}

	// Nothing saved yet (nil row, and an explicitly cleared list) — the
	// KERNEL_MODEL_PRICES seed keeps serving.
	for name, stored := range map[string][]workspace.ModelPrice{"nil": nil, "cleared": {}} {
		list, source := activeModelPrices(stored, seed)
		require.Equal(t, "env", source, name)
		require.Equal(t, seed, list, name)
	}
}

func TestActiveModelPricesSavedListWinsAndIsOrdered(t *testing.T) {
	seed := []cost.Price{{Model: "gpt-4o", PromptPer1M: 2.5, CompPer1M: 10}}
	stored := []workspace.ModelPrice{
		{Model: "z-ai-pro", PromptPer1M: 1, CompPer1M: 2},
		{Model: "a-mini", PromptPer1M: 0.1, CompPer1M: 0.2},
		{Model: "z-ai-pro", PromptPer1M: 3, CompPer1M: 4}, // last row of a model wins
	}

	list, source := activeModelPrices(stored, seed)
	require.Equal(t, "db", source)
	require.Len(t, list, 2, "the saved list replaces the seed wholesale")
	require.Equal(t, "a-mini", list[0].Model, "ordered by model, whatever order the payload arrived in")
	require.Equal(t, "z-ai-pro", list[1].Model)
	require.Equal(t, 3.0, list[1].PromptPer1M)
}
