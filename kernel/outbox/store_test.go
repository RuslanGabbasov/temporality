package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// makeClaimed builds n claimed events with distinct ids and payloads.
func makeClaimed(n int) []claimedEvent {
	events := make([]claimedEvent, n)
	for i := range events {
		events[i] = claimedEvent{
			SourceID: "kernel-test",
			EventID:  fmt.Sprintf("e%03d", i),
			Payload:  json.RawMessage(fmt.Sprintf(`{"event_id":"e%03d","seq":%d}`, i, i)),
		}
	}
	return events
}

func chunkIDs(chunk []claimedEvent) []string {
	ids := make([]string, len(chunk))
	for i, event := range chunk {
		ids[i] = event.EventID
	}
	return ids
}

func TestChunkEvents(t *testing.T) {
	cases := []struct {
		name      string
		events    []claimedEvent
		limit     int
		wantSizes []int
		wantFirst []string // first event id of each chunk
	}{
		{
			name:   "empty claim yields no chunks",
			events: nil,
			limit:  maxEventsPerPost,
		},
		{
			name:      "single event stays whole",
			events:    makeClaimed(1),
			limit:     maxEventsPerPost,
			wantSizes: []int{1},
			wantFirst: []string{"e000"},
		},
		{
			name:      "exactly one chunk under the limit",
			events:    makeClaimed(99),
			limit:     maxEventsPerPost,
			wantSizes: []int{99},
			wantFirst: []string{"e000"},
		},
		{
			name:      "exactly the limit is one chunk",
			events:    makeClaimed(100),
			limit:     maxEventsPerPost,
			wantSizes: []int{100},
			wantFirst: []string{"e000"},
		},
		{
			name:      "limit plus one spills into a remainder chunk",
			events:    makeClaimed(101),
			limit:     maxEventsPerPost,
			wantSizes: []int{100, 1},
			wantFirst: []string{"e000", "e100"},
		},
		{
			name:      "current claim batch of 50 stays one chunk",
			events:    makeClaimed(maxClaimBatch),
			limit:     maxEventsPerPost,
			wantSizes: []int{50},
			wantFirst: []string{"e000"},
		},
		{
			name:      "claim raised past the post limit splits evenly",
			events:    makeClaimed(250),
			limit:     maxEventsPerPost,
			wantSizes: []int{100, 100, 50},
			wantFirst: []string{"e000", "e100", "e200"},
		},
		{
			name:      "nonpositive limit degrades to one event per chunk",
			events:    makeClaimed(3),
			limit:     0,
			wantSizes: []int{1, 1, 1},
			wantFirst: []string{"e000", "e001", "e002"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			chunks := chunkEvents(tt.events, tt.limit)
			if len(chunks) != len(tt.wantSizes) {
				t.Fatalf("chunkEvents() = %d chunks, want %d", len(chunks), len(tt.wantSizes))
			}
			for i, chunk := range chunks {
				if len(chunk) != tt.wantSizes[i] {
					t.Errorf("chunk[%d] size = %d, want %d", i, len(chunk), tt.wantSizes[i])
				}
				if got := chunkIDs(chunk)[0]; got != tt.wantFirst[i] {
					t.Errorf("chunk[%d] first event = %s, want %s", i, got, tt.wantFirst[i])
				}
			}
		})
	}
}

func TestChunkEventsPreservesOrderAndCoversAll(t *testing.T) {
	events := makeClaimed(127)
	var flattened []string
	for _, chunk := range chunkEvents(events, maxEventsPerPost) {
		flattened = append(flattened, chunkIDs(chunk)...)
	}
	want := chunkIDs(events)
	if !reflect.DeepEqual(flattened, want) {
		t.Fatalf("flattened chunks %v, want original order %v", flattened, want)
	}
}

func TestFrameEventsPost(t *testing.T) {
	cases := []struct {
		name    string
		chunk   []claimedEvent
		want    string
		wantErr bool
	}{
		{
			name:  "single payload embedded verbatim",
			chunk: []claimedEvent{{SourceID: "s", EventID: "e1", Payload: json.RawMessage(`{"a":1}`)}},
			want:  `{"events":[{"a":1}]}`,
		},
		{
			name: "multiple payloads keep request order",
			// json.Marshal compacts insignificant whitespace inside RawMessage
			// (same as the previous single-event framing); content is verbatim.
			chunk: []claimedEvent{
				{SourceID: "s", EventID: "e1", Payload: json.RawMessage(`{"a":1}`)},
				{SourceID: "s", EventID: "e2", Payload: json.RawMessage(`{"b":2,"c":"x y"}`)},
			},
			want: `{"events":[{"a":1},{"b":2,"c":"x y"}]}`,
		},
		{
			name:  "empty chunk frames an empty events array",
			chunk: nil,
			want:  `{"events":[]}`,
		},
		{
			name:    "invalid raw payload fails marshaling",
			chunk:   []claimedEvent{{SourceID: "s", EventID: "e1", Payload: json.RawMessage(`not-json`)}},
			wantErr: true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			body, err := frameEventsPost(tt.chunk)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("frameEventsPost() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("frameEventsPost() error = %v", err)
			}
			if string(body) != tt.want {
				t.Fatalf("frameEventsPost() = %s, want %s", body, tt.want)
			}
			// And it must decode back into the ingest request contract.
			var decoded eventsPostBody
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("body does not decode: %v", err)
			}
			if len(decoded.Events) != len(tt.chunk) {
				t.Fatalf("decoded %d events, want %d", len(decoded.Events), len(tt.chunk))
			}
			for i, event := range tt.chunk {
				if string(decoded.Events[i]) != string(event.Payload) {
					t.Errorf("decoded events[%d] = %s, want %s", i, decoded.Events[i], event.Payload)
				}
			}
		})
	}
}

func TestParseIngestResponse(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus []string
		wantErrors []string
		wantErr    bool
	}{
		{
			name:       "full envelope decodes per-event results",
			body:       `{"accepted":2,"repeated":1,"results":[{"source_id":"s","event_id":"e1","status":"accepted"},{"source_id":"s","event_id":"e2","status":"repeated"},{"source_id":"s","event_id":"e3","status":"rejected","error":"storage error"}]}`,
			wantStatus: []string{"accepted", "repeated", "rejected"},
			wantErrors: []string{"", "", "storage error"},
		},
		{
			name:       "unknown fields are ignored",
			body:       `{"accepted":1,"results":[{"status":"accepted","event_id":"e1","unexpected":42}],"next_cursor":"x"}`,
			wantStatus: []string{"accepted"},
			wantErrors: []string{""},
		},
		{
			name: "missing results decodes to none",
			body: `{"accepted":0,"repeated":0}`,
		},
		{
			name:    "invalid json is an error",
			body:    `{"results":[`,
			wantErr: true,
		},
		{
			name:    "non-object body is an error",
			body:    `"accepted"`,
			wantErr: true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			results, err := parseIngestResponse([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseIngestResponse() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIngestResponse() error = %v", err)
			}
			if len(results) != len(tt.wantStatus) {
				t.Fatalf("parseIngestResponse() = %d results, want %d", len(results), len(tt.wantStatus))
			}
			for i, result := range results {
				if result.Status != tt.wantStatus[i] {
					t.Errorf("results[%d].status = %q, want %q", i, result.Status, tt.wantStatus[i])
				}
				if result.Error != tt.wantErrors[i] {
					t.Errorf("results[%d].error = %q, want %q", i, result.Error, tt.wantErrors[i])
				}
			}
		})
	}
}

func TestResolveIngestOutcome(t *testing.T) {
	chunk := []claimedEvent{
		{SourceID: "s", EventID: "e1"},
		{SourceID: "s", EventID: "e2"},
		{SourceID: "s", EventID: "e3"},
	}
	cases := []struct {
		name          string
		chunk         []claimedEvent
		results       []ingestEventResult
		wantDelivered []string
		wantFailed    map[string]string
	}{
		{
			name:          "all accepted are delivered",
			chunk:         chunk,
			results:       []ingestEventResult{{Status: "accepted"}, {Status: "accepted"}, {Status: "accepted"}},
			wantDelivered: []string{"e1", "e2", "e3"},
		},
		{
			name:          "repeated counts as delivered",
			chunk:         chunk[:1],
			results:       []ingestEventResult{{Status: "repeated"}},
			wantDelivered: []string{"e1"},
		},
		{
			name:  "mixed outcomes split with per-event reasons",
			chunk: chunk,
			results: []ingestEventResult{
				{Status: "accepted"},
				{Status: "rejected", Error: "storage error"},
				{Status: "rejected"},
			},
			wantDelivered: []string{"e1"},
			wantFailed: map[string]string{
				"e2": "Temporality rejected event: storage error",
				"e3": "Temporality rejected event",
			},
		},
		{
			name:       "fewer results than events fails the whole chunk",
			chunk:      chunk,
			results:    []ingestEventResult{{Status: "accepted"}},
			wantFailed: map[string]string{"e1": "Temporality rejected event", "e2": "Temporality rejected event", "e3": "Temporality rejected event"},
		},
		{
			name:  "unknown status is not delivered",
			chunk: chunk[:1],
			results: []ingestEventResult{
				{Status: "deferred", Error: "come back later"},
			},
			wantFailed: map[string]string{"e1": "Temporality rejected event: come back later"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			delivered, failed := resolveIngestOutcome(tt.chunk, tt.results)
			if got := chunkIDs(delivered); len(got) != 0 || len(tt.wantDelivered) != 0 {
				if !reflect.DeepEqual(got, tt.wantDelivered) {
					t.Errorf("delivered = %v, want %v", got, tt.wantDelivered)
				}
			}
			if len(failed) != len(tt.wantFailed) {
				t.Fatalf("failed = %d events, want %d", len(failed), len(tt.wantFailed))
			}
			for _, failure := range failed {
				want, ok := tt.wantFailed[failure.Event.EventID]
				if !ok {
					t.Errorf("unexpected failure for %s", failure.Event.EventID)
					continue
				}
				if failure.Reason != want {
					t.Errorf("failure reason for %s = %q, want %q", failure.Event.EventID, failure.Reason, want)
				}
			}
		})
	}
}

func TestResolveIngestOutcomeEmptyChunk(t *testing.T) {
	delivered, failed := resolveIngestOutcome(nil, nil)
	if delivered != nil || failed != nil {
		t.Fatalf("empty chunk resolved to (%v, %v), want (nil, nil)", delivered, failed)
	}
}

func TestInvalidIngestResponseReason(t *testing.T) {
	err := errors.New("unexpected end of JSON input")
	want := "invalid Temporality ingest response: unexpected end of JSON input"
	if got := invalidIngestResponseReason(err); got != want {
		t.Fatalf("invalidIngestResponseReason() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(invalidIngestResponseReason(err), invalidIngestResponsePrefix) {
		t.Fatalf("reason must keep the historical %q prefix", invalidIngestResponsePrefix)
	}
}

func TestKeepDraining(t *testing.T) {
	cases := []struct {
		name      string
		published int
		err       error
		want      bool
	}{
		{name: "deliveries succeed, keep draining", published: 50, err: nil, want: true},
		{name: "single delivery succeeded, keep draining", published: 1, err: nil, want: true},
		{name: "empty claim, wait", published: 0, err: nil, want: false},
		{name: "failure with deliveries, wait", published: 49, err: errors.New("event ingest rejected"), want: false},
		{name: "failure without deliveries, wait", published: 0, err: errors.New("connection refused"), want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := keepDraining(tt.published, tt.err); got != tt.want {
				t.Fatalf("keepDraining(%d, %v) = %v, want %v", tt.published, tt.err, got, tt.want)
			}
		})
	}
}

func TestPublishLimitsMatchJournalContract(t *testing.T) {
	// The journal ingest accepts 1..100 events per request
	// (observation/httpapi appendObservations); a chunk must never exceed it.
	if maxEventsPerPost != 100 {
		t.Fatalf("maxEventsPerPost = %d, want 100 (journal ingest limit)", maxEventsPerPost)
	}
	if maxClaimBatch > maxEventsPerPost {
		t.Fatalf("maxClaimBatch = %d exceeds one POST limit %d", maxClaimBatch, maxEventsPerPost)
	}
}
