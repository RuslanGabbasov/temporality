package workspace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestParseEvalSchedule pins the schedule-value contract: empty and "0"
// disable, valid durations pass through, garbage and negatives disable — a
// broken knob must never produce a hot loop.
func TestParseEvalSchedule(t *testing.T) {
	require.Equal(t, time.Duration(0), parseEvalSchedule(""))
	require.Equal(t, time.Duration(0), parseEvalSchedule("0"))
	require.Equal(t, 6*time.Hour, parseEvalSchedule("6h"))
	require.Equal(t, 24*time.Hour, parseEvalSchedule("24h"))
	require.Equal(t, time.Duration(0), parseEvalSchedule("not-a-duration"))
	require.Equal(t, time.Duration(0), parseEvalSchedule("-1h"))
}
