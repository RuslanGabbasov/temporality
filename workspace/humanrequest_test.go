package workspace

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestScanHumanRequestIncludesThreadRef round-trips the conversation thread
// pointer (docs/channels.md §9) through the shared column list and scan —
// the position-sensitive wiring every human_request query goes through.
func TestScanHumanRequestIncludesThreadRef(t *testing.T) {
	now := time.Now()
	values := sliceRow{"op-2", "run-2", "proj", "agent-a", 3, "user:ruslan", "ruslan",
		"Ship?", "", []byte(`[]`), nil, "delivered", "slackbot", "1697000000.000100", "", "",
		0, "fallback", "", now, nil, nil, nil}
	request, err := scanHumanRequest(values)
	require.NoError(t, err)
	require.Equal(t, "1697000000.000100", request.ThreadRef)
	require.Equal(t, "slackbot", request.Channel)
}

// TestThreadRefIsWrittenOnlyWhenEmpty guards the fill-when-empty contract of
// SetHumanRequestThreadRef at the source level (unit tests here have no
// database): thread_ref appears in the shared column list, the scan and the
// INSERT of CreateHumanRequest, its only UPDATE writes it when empty — the
// first thread (question post or user reply) stays the conversation anchor.
func TestThreadRefIsWrittenOnlyWhenEmpty(t *testing.T) {
	raw, err := os.ReadFile("humanrequest.go")
	require.NoError(t, err)
	source := string(raw)

	require.Contains(t, humanRequestColumns, "status, channel, thread_ref, response")
	require.Contains(t, source, "&r.Status, &r.Channel, &r.ThreadRef, &r.Response")
	require.Contains(t, source, "status, channel, thread_ref, timeout_seconds")

	update := "UPDATE human_request SET thread_ref = $2 WHERE id = $1 AND thread_ref = ''"
	require.Contains(t, source, update)
	// No other UPDATE may reassign the thread anchor.
	parts := strings.Split(source, "UPDATE human_request")
	require.Greater(t, len(parts), 1, "expected the known UPDATE statements to stay in place")
	for _, part := range parts[1:] {
		if where := strings.Index(part, "WHERE"); where > 0 {
			part = part[:where]
		}
		if strings.Contains(part, "thread_ref") {
			require.Contains(t, part, "SET thread_ref = $2",
				"the only thread_ref UPDATE must be the fill-when-empty pin")
		}
	}
}
