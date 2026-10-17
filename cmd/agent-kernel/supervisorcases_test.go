package main

import (
	"strings"
	"testing"
	"time"
)


func TestNormalizeFrictionText(t *testing.T) {
	a := normalizeFrictionText("Переслать отчёт 17 октября")
	b := normalizeFrictionText("  переслать отчёт  18 октября\n")
	if a != b {
		t.Fatalf("normalization must fold digits and whitespace: %q vs %q", a, b)
	}
	long := normalizeFrictionText(strings.Repeat("x", 300))
	if len(long) > 121 {
		t.Fatalf("normalization must truncate: %d", len(long))
	}
}

func TestCaseDueDedup(t *testing.T) {
	done := map[string]time.Time{}
	now := time.Now()
	done["knowledge_hygiene\x00k-1"] = now
	done["knowledge_hygiene\x00k-2"] = now.Add(-8 * 24 * time.Hour)
	if caseDue(done, "knowledge_hygiene", "k-1") {
		t.Fatal("a fresh verdict must suppress the case")
	}
	// The 7-day window is applied by the caller when building the index; the
	// index itself suppresses any known subject (older entries never land in
	// the map — the listing is bounded by the dedup horizon upstream).
	if !caseDue(done, "knowledge_hygiene", "k-3") {
		t.Fatal("unknown subject must be due")
	}
}
