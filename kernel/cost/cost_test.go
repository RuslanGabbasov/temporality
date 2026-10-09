package cost

import (
	"testing"
)

func TestParsePrices(t *testing.T) {
	cfg, err := ParsePrices("gpt-4o:2.50:10.00,claude-3-sonnet:3.00:15.00")
	if err != nil {
		t.Fatal(err)
	}
	prices := cfg.Prices()
	if len(prices) != 2 {
		t.Fatalf("expected 2 prices, got %d", len(prices))
	}
}

func TestParsePricesEmpty(t *testing.T) {
	cfg, err := ParsePrices("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Prices()) != 0 {
		t.Fatalf("expected 0 prices, got %d", len(cfg.Prices()))
	}
}

func TestModelCost(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	// 1000 prompt tokens * $2.50/1k = $2.50
	// 500 completion tokens * $10.00/1k = $5.00
	// Total: $7.50
	cost := cfg.ModelCost("gpt-4o", 1000, 0, 500)
	if cost != 7.5 {
		t.Fatalf("expected 7.50, got %f", cost)
	}
}

func TestModelCostCachedHalfPrice(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	// 800 cached of 1000 prompt: 200 fresh * $2.50/1k + 800 * $1.25/1k
	// = $0.50 + $1.00; 500 completion * $10.00/1k = $5.00 → $6.50
	cost := cfg.ModelCost("gpt-4o", 1000, 800, 500)
	if cost != 6.5 {
		t.Fatalf("expected 6.50, got %f", cost)
	}
	// Providers occasionally over-report cached vs prompt; clamp to prompt.
	if got := cfg.ModelCost("gpt-4o", 100, 500, 0); got != cfg.ModelCost("gpt-4o", 100, 100, 0) {
		t.Fatalf("cached must clamp to prompt: %f vs %f", got, cfg.ModelCost("gpt-4o", 100, 100, 0))
	}
}

func TestModelCostUnknown(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	cost := cfg.ModelCost("unknown", 1000, 0, 500)
	if cost != 0 {
		t.Fatalf("expected 0 for unknown model, got %f", cost)
	}
}

func TestModelCostPartialTokens(t *testing.T) {
	cfg, _ := ParsePrices("gpt-4o:2.50:10.00")
	// 100 prompt tokens * $2.50/1k = $0.25
	// 100 completion tokens * $10.00/1k = $1.00
	cost := cfg.ModelCost("gpt-4o", 100, 0, 100)
	if cost != 1.25 {
		t.Fatalf("expected 1.25, got %f", cost)
	}
}

func TestParsePricesInvalid(t *testing.T) {
	_, err := ParsePrices("gpt-4o:2.50")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	_, err = ParsePrices("gpt-4o:abc:10.00")
	if err == nil {
		t.Fatal("expected error for invalid price")
	}
}
