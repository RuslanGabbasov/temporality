package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The depth cap behind the late-answer follow-up mechanic: resolved from the
// env through an activity (the value is recorded in history, so replays stay
// deterministic), defaulting to 3 and disabling follow-ups at 0.

func TestLateReplyMaxDepthDefaults(t *testing.T) {
	t.Setenv("KERNEL_LATE_REPLY_MAX_DEPTH", "")
	a := &Activities{}
	v, err := a.LateReplyMaxDepth(t.Context())
	require.NoError(t, err)
	require.Equal(t, 3, v, "unset env falls back to three generations")
}

func TestLateReplyMaxDepthParsesEnv(t *testing.T) {
	t.Setenv("KERNEL_LATE_REPLY_MAX_DEPTH", "5")
	a := &Activities{}
	v, err := a.LateReplyMaxDepth(t.Context())
	require.NoError(t, err)
	require.Equal(t, 5, v)
}

func TestLateReplyMaxDepthZeroDisables(t *testing.T) {
	t.Setenv("KERNEL_LATE_REPLY_MAX_DEPTH", "0")
	a := &Activities{}
	v, err := a.LateReplyMaxDepth(t.Context())
	require.NoError(t, err)
	require.Equal(t, 0, v, "0 disables follow-ups: late answers ride the starter's channels only")
}

func TestLateReplyMaxDepthGarbageFallsBack(t *testing.T) {
	t.Setenv("KERNEL_LATE_REPLY_MAX_DEPTH", "banana")
	a := &Activities{}
	v, err := a.LateReplyMaxDepth(t.Context())
	require.NoError(t, err)
	require.Equal(t, 3, v, "a non-numeric env value falls back instead of failing runs")

	t.Setenv("KERNEL_LATE_REPLY_MAX_DEPTH", "-2")
	v, err = a.LateReplyMaxDepth(t.Context())
	require.NoError(t, err)
	require.Equal(t, 3, v, "a negative env value falls back instead of failing runs")
}
