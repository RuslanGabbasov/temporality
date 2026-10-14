package llm

import (
	"testing"
	"time"
)

// A clean install boots without the default model endpoint: providers
// configured through onboarding make runs work without TEMPORALITY_MODEL_*.
func TestOptionalConfigFromEnvAllowsMissingDefault(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "")
	t.Setenv("TEMPORALITY_MODEL_ID", "")
	t.Setenv("TEMPORALITY_MODEL_API_KEY", "")

	cfg, err := OptionalConfigFromEnv()
	if err != nil {
		t.Fatalf("OptionalConfigFromEnv: %v", err)
	}
	if cfg.BaseURL != "" || cfg.Model != "" || cfg.APIKey != "" {
		t.Fatalf("empty env leaked into config: %+v", cfg)
	}
	// Sensible transport defaults survive — other subsystems (provider
	// routing overlays, HTTP timeouts) read them.
	if cfg.Timeout != 180*time.Second || cfg.StreamIdleTimeout != 90*time.Second {
		t.Fatalf("defaults lost: timeout=%v idle=%v", cfg.Timeout, cfg.StreamIdleTimeout)
	}

	// The strict form still refuses: hot-path callers rely on the contract.
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted a missing default model")
	}

	// Malformed optional values still fail both forms.
	t.Setenv("TEMPORALITY_MODEL_TIMEOUT", "nope")
	if _, err := OptionalConfigFromEnv(); err == nil {
		t.Fatal("OptionalConfigFromEnv accepted a malformed timeout")
	}
}

// With the endpoint present both forms behave identically.
func TestOptionalConfigFromEnvParsesConfiguredDefault(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "http://localhost:1")
	t.Setenv("TEMPORALITY_MODEL_ID", "mimo-x")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.BaseURL != "http://localhost:1" || cfg.Model != "mimo-x" {
		t.Fatalf("config = %+v", cfg)
	}
	loose, err := OptionalConfigFromEnv()
	if err != nil {
		t.Fatalf("OptionalConfigFromEnv: %v", err)
	}
	if loose != cfg {
		t.Fatalf("optional form diverged: %+v vs %+v", loose, cfg)
	}
}
