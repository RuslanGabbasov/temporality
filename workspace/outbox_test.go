package workspace

import (
	"testing"
	"time"
)

// TestOutboxBackoffLadder pins the redelivery ladder: first retry after 30s,
// doubling per attempt, capped at an hour so a poison event never hammers
// the transport (docs/conversational-runtime.md §13.3).
func TestOutboxBackoffLadder(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 30 * time.Second},
		{2, time.Minute},
		{3, 2 * time.Minute},
		{4, 4 * time.Minute},
		{6, 16 * time.Minute},
		{7, 32 * time.Minute},
		{8, time.Hour},
		{50, time.Hour},
	}
	for _, tc := range cases {
		if got := outboxBackoff(tc.attempt); got != tc.want {
			t.Errorf("outboxBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
