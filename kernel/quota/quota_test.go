package quota

import (
	"testing"
	"time"
)

func TestFromConfigParsesDefaultsAndPerProject(t *testing.T) {
	limiter, err := FromConfig(nil, "*: 50, forge:200, lighthouse: 0")
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if !limiter.Enabled() {
		t.Fatal("limiter with entries must be enabled")
	}
	if got := limiter.Limit("anything"); got != 50 {
		t.Fatalf("default limit = %d, want 50", got)
	}
	if got := limiter.Limit("forge"); got != 200 {
		t.Fatalf("forge limit = %d, want 200", got)
	}
	if got := limiter.Limit("lighthouse"); got != 0 {
		t.Fatalf("lighthouse limit = %d, want 0", got)
	}
}

func TestFromConfigWithoutEntriesIsUnlimited(t *testing.T) {
	limiter, err := FromConfig(nil, "")
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if limiter.Enabled() {
		t.Fatal("empty config must disable quotas")
	}
	if got := limiter.Limit("forge"); got != Unlimited {
		t.Fatalf("limit = %d, want Unlimited", got)
	}
}

func TestFromConfigPerProjectOnlyLeavesOthersUnlimited(t *testing.T) {
	limiter, err := FromConfig(nil, "forge:10")
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if !limiter.Enabled() {
		t.Fatal("a per-project entry enables the limiter")
	}
	if got := limiter.Limit("forge"); got != 10 {
		t.Fatalf("forge limit = %d, want 10", got)
	}
	if got := limiter.Limit("other"); got != Unlimited {
		t.Fatalf("uncapped project limit = %d, want Unlimited", got)
	}
}

func TestFromConfigRejectsGarbage(t *testing.T) {
	for _, config := range []string{"forge", "forge:abc", "forge:-1", "forge:"} {
		if _, err := FromConfig(nil, config); err == nil {
			t.Fatalf("config %q must be rejected", config)
		}
	}
}

func TestResetAtIsNextUTCMidnight(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 30, 0, 0, time.UTC)
	want := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	if got := resetAt(now); !got.Equal(want) {
		t.Fatalf("resetAt = %v, want %v", got, want)
	}
	// Just before midnight rolls to the next day.
	late := time.Date(2026, 9, 25, 23, 59, 59, 0, time.UTC)
	if got := resetAt(late); !got.Equal(want) {
		t.Fatalf("resetAt = %v, want %v", got, want)
	}
}
