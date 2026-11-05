-- Per-model call caps (docs/agents.md §routing): a provider lists model
-- names, and model_limits optionally names the ceiling for one generation
-- of a SPECIFIC model — {"mimo-v2.6-pro": {"max_output_tokens": 32768}}.
-- The kernel default (TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS, 16k) is a
-- conservative floor no capable model should inherit by accident: a 44 KB
-- document edit got truncated mid tool-call by exactly that floor.
ALTER TABLE workspace_provider ADD COLUMN IF NOT EXISTS model_limits jsonb NOT NULL DEFAULT '{}'::jsonb;
