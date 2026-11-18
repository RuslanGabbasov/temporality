package sandbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The progress parser keys the status endpoint's phases and step counters:
// buildkit's "#12 [2/4] RUN …" lines carry the dockerfile step position,
// pulling/downloading lines name the slow base-image phase, exporting/naming
// close it out. The registry is one slot: begin → feed → finish, and the
// second begin while running is refused.
func TestBuildProgressLifecycle(t *testing.T) {
	// A failed assert mid-test leaves the slot claimed — the cleanup keeps
	// the two tests independent.
	t.Cleanup(func() { FinishBuild("", nil) })
	require.True(t, TryBeginBuild(), "the slot must be free before the first build")
	require.False(t, TryBeginBuild(), "a second concurrent build is refused")

	recordBuildLine("#5 pulling golang:1.27-alpine")
	recordBuildLine("#8 downloading from docker.io/library/golang")
	recordBuildLine("#12 [2/4] RUN apk add --no-cache git curl")
	recordBuildLine("#13 [3/4] COPY config /")
	recordBuildLine("#20 exporting layers")
	recordBuildLine("#21 naming to docker.io/library/temporality-sandbox:latest done")

	// The step lines drive the counter (2/4 → 3/4); the final naming line
	// flips the phase to export — the build steps are done.
	snap := BuildProgressSnapshot()
	require.True(t, snap.Running)
	require.Equal(t, "export", snap.Phase)
	require.Equal(t, 3, snap.Step)
	require.Equal(t, 4, snap.Total)
	require.Contains(t, snap.LastLine, "naming to")
	require.GreaterOrEqual(t, snap.ElapsedMS, int64(0))

	FinishBuild("temporality-sandbox:latest@sha256:ab", nil)
	snap = BuildProgressSnapshot()
	require.False(t, snap.Running)
	require.True(t, snap.Done)
	require.False(t, snap.Failed)
	require.Equal(t, "temporality-sandbox:latest@sha256:ab", snap.Image)

	require.True(t, TryBeginBuild(), "a finished build frees the slot")
	FinishBuild("", errFakeBuild{})
}

func TestBuildProgressFailure(t *testing.T) {
	t.Cleanup(func() { FinishBuild("", nil) })
	require.True(t, TryBeginBuild())
	recordBuildLine("#4 ERROR: failed to solve: apk add broke")
	FinishBuild("", errFakeBuild{})
	snap := BuildProgressSnapshot()
	require.True(t, snap.Done)
	require.True(t, snap.Failed)
	require.Contains(t, snap.Error, "fake build failure")
	require.False(t, snap.Running)
}

type errFakeBuild struct{}

func (e errFakeBuild) Error() string { return "fake build failure" }
