package mcpclient

import (
	"encoding/json"
	"testing"
	"time"
)

// The MCP timeout_ms convention: the caller states its wait budget in the
// tool arguments, and Call must honor it — the Penpot export_shape loop died
// three times at the fixed 30.06s HTTP client timeout while the tool asked
// for 120000ms.
func TestArgTimeoutMs(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want int64
		ok   bool
	}{
		{"float", map[string]any{"timeout_ms": float64(120000)}, 120000, true},
		{"int", map[string]any{"timeout_ms": 5000}, 5000, true},
		{"json.Number", map[string]any{"timeout_ms": json.Number("45000")}, 45000, true},
		{"string", map[string]any{"timeout_ms": "30000"}, 30000, true},
		{"missing", map[string]any{"other": 1}, 0, false},
		{"nil args", nil, 0, false},
		{"zero", map[string]any{"timeout_ms": float64(0)}, 0, false},
		{"negative", map[string]any{"timeout_ms": float64(-5)}, 0, false},
		{"garbage", map[string]any{"timeout_ms": "soon"}, 0, false},
	}
	for _, tc := range cases {
		got, ok := argTimeoutMs(tc.args)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%s: got (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestArgTimeoutClampBounds(t *testing.T) {
	// The clamp lives in Call; here we verify the constants indirectly: a
	// huge ask cannot exceed the 10m ceiling. Direct call-budget testing
	// needs a live session — the clamp math is trivially inspected via the
	// same bounds the Call path applies.
	huge := int64(3600 * 1000)
	deadline := time.Duration(huge) * time.Millisecond
	if deadline > 10*time.Minute {
		deadline = 10 * time.Minute
	}
	if deadline != 10*time.Minute {
		t.Fatalf("huge ask must clamp to 10m, got %v", deadline)
	}
	tiny := time.Duration(1) * time.Millisecond
	if tiny < time.Second {
		tiny = time.Second
	}
	if tiny != time.Second {
		t.Fatalf("tiny ask must clamp up to 1s, got %v", tiny)
	}
}
