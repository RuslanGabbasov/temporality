package workspace

import (
	"strings"
	"testing"
	"time"
)

// The live SSE loop polls the journal every 1.5s per open stream, so the
// cursor decides between an index seek and a per-poll probe of the whole
// observation_events table. The three query shapes must stay distinct: zero
// cursor reads from the start, a bare event id (legacy clients) keeps the
// historical subquery path, the full tuple takes the row-wise fast path.
func TestStreamRunEventsQueryCursorShapes(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 123456789, time.UTC)

	sql, args := streamRunEventsQuery("p1", "r1", StreamCursor{}, 50)
	if !strings.Contains(sql, "ORDER BY occurred_at, event_id LIMIT $3") {
		t.Errorf("zero cursor query must read the journal from the start: %s", sql)
	}
	if strings.Contains(sql, "(occurred_at, event_id)") {
		t.Errorf("zero cursor query must not filter by a cursor: %s", sql)
	}
	if len(args) != 3 || args[0] != "p1" || args[1] != "r1" || args[2] != 50 {
		t.Errorf("zero cursor args = %v, want [p1 r1 50]", args)
	}

	sql, args = streamRunEventsQuery("p1", "r1", StreamCursor{EventID: "ev-9"}, 25)
	if !strings.Contains(sql, "(SELECT occurred_at FROM observation_events WHERE event_id = $3 LIMIT 1)") {
		t.Errorf("legacy event_id cursor must resolve through the historical subquery: %s", sql)
	}
	if !strings.Contains(sql, "AND (occurred_at, event_id) > ((SELECT occurred_at FROM observation_events WHERE event_id = $3 LIMIT 1), $3)") {
		t.Errorf("legacy cursor comparison lost the row-wise shape: %s", sql)
	}
	if len(args) != 4 || args[2] != "ev-9" || args[3] != 25 {
		t.Errorf("legacy cursor args = %v, want [p1 r1 ev-9 25]", args)
	}

	sql, args = streamRunEventsQuery("p1", "r1", StreamCursor{OccurredAt: at, EventID: "ev-9"}, 10)
	if !strings.Contains(sql, "AND (occurred_at, event_id) > ($3, $4)") {
		t.Errorf("tuple cursor must compare row-wise against bound values: %s", sql)
	}
	if strings.Contains(sql, "SELECT occurred_at FROM observation_events WHERE event_id") {
		t.Errorf("tuple cursor must not probe the table by event_id: %s", sql)
	}
	if len(args) != 5 || args[2] != at || args[3] != "ev-9" || args[4] != 10 {
		t.Errorf("tuple cursor args = %v, want [p1 r1 <at> ev-9 10]", args)
	}
}

// A partial cursor — timestamp without event id — still has a well-defined
// position (event ids are never empty, so (ts, ”) sits after the whole
// timestamp): it must take the row-wise fast path, never the legacy subquery
// (an empty id would probe to NULL and stall the stream) and never a replay.
func TestStreamRunEventsQueryPartialTupleCursorKeepsFastPath(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	sql, args := streamRunEventsQuery("p1", "r1", StreamCursor{OccurredAt: at}, 7)
	if !strings.Contains(sql, "AND (occurred_at, event_id) > ($3, $4)") {
		t.Errorf("timestamp-only cursor must take the row-wise path, got: %s", sql)
	}
	if strings.Contains(sql, "SELECT occurred_at FROM observation_events WHERE event_id") {
		t.Errorf("timestamp-only cursor must not probe by event_id: %s", sql)
	}
	if len(args) != 5 || args[3] != "" {
		t.Errorf("row-wise path args = %v, want [p1 r1 <at> \"\" 7]", args)
	}
}

func TestParseStreamCursor(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		want     StreamCursor
		fastPath bool
	}{
		{"empty", "", StreamCursor{}, false},
		{"bare event id (historical wire format)", "0a1b-2c", StreamCursor{EventID: "0a1b-2c"}, false},
		{"full tuple", "2026-10-27T12:00:00.123456789Z|ev-9", StreamCursor{
			OccurredAt: time.Date(2026, 10, 27, 12, 0, 0, 123456789, time.UTC),
			EventID:    "ev-9",
		}, true},
		{"broken timestamp degrades to legacy", "not-a-time|ev-9", StreamCursor{EventID: "not-a-time|ev-9"}, false},
		{"whitespace only", "   ", StreamCursor{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseStreamCursor(tc.raw)
			if !got.OccurredAt.Equal(tc.want.OccurredAt) || got.EventID != tc.want.EventID {
				t.Fatalf("ParseStreamCursor(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
			if fast := !got.OccurredAt.IsZero() && got.EventID != ""; fast != tc.fastPath {
				t.Fatalf("ParseStreamCursor(%q) fast path = %v, want %v", tc.raw, fast, tc.fastPath)
			}
		})
	}
}
