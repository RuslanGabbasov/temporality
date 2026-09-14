package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/runtime/httpapi"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func testHandler() http.Handler {
	return httpapi.New(memory.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestAppendGetAndReplay(t *testing.T) {
	handler := testHandler()
	body := []byte(`{"event_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a01","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02","type":"episode.started","payload":{},"provenance":{"source":"test"}}`)
	created := serve(handler, http.MethodPost, "/v1/events", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("append status=%d body=%s", created.Code, created.Body.String())
	}
	got := serve(handler, http.MethodGet, "/v1/events/018f47a7-34b2-7d10-a932-4f3ff37a4a01", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", got.Code, got.Body.String())
	}
	replayed := serve(handler, http.MethodPost, "/v1/replay", []byte(`{"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02"}`))
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	var response struct {
		Events []json.RawMessage `json:"events"`
		Digest string            `json:"digest"`
	}
	if err := json.Unmarshal(replayed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Events) != 1 || response.Digest == "" {
		t.Fatalf("unexpected replay: %s", replayed.Body.String())
	}
}

func TestEventPaginationAndMetrics(t *testing.T) {
	handler := testHandler()
	for _, id := range []string{"018f47a7-34b2-7d10-a932-4f3ff37a4a11", "018f47a7-34b2-7d10-a932-4f3ff37a4a12", "018f47a7-34b2-7d10-a932-4f3ff37a4a13"} {
		body := []byte(`{"event_id":"` + id + `","type":"metric.recorded","payload":{},"provenance":{}}`)
		if response := serve(handler, http.MethodPost, "/v1/events", body); response.Code != http.StatusCreated {
			t.Fatalf("append failed: %s", response.Body.String())
		}
	}
	first := serve(handler, http.MethodGet, "/v1/events?limit=2", nil)
	var page struct {
		Events     []json.RawMessage `json:"events"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.NextCursor == "" {
		t.Fatalf("unexpected first page: %s", first.Body.String())
	}
	second := serve(handler, http.MethodGet, "/v1/events?limit=2&cursor="+page.NextCursor, nil)
	page.Events = nil
	page.NextCursor = ""
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.NextCursor != "" {
		t.Fatalf("unexpected second page: %s", second.Body.String())
	}
	metrics := serve(handler, http.MethodGet, "/metrics", nil)
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "temporality_events_appended_total 3") {
		t.Fatalf("unexpected metrics: %s", metrics.Body.String())
	}
}

func TestCommitAndTransitionClaim(t *testing.T) {
	handler := testHandler()
	created := serve(handler, http.MethodPost, "/v1/claims", []byte(`{"event":{"payload":{},"provenance":{"source":"test"}},"claim":{"proposition":"The build is reproducible","confidence":0.9}}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create claim: %s", created.Body.String())
	}
	var commit cognition.Commit
	if err := json.Unmarshal(created.Body.Bytes(), &commit); err != nil {
		t.Fatal(err)
	}
	transitioned := serve(handler, http.MethodPost, "/v1/claims/"+commit.Claim.ClaimID+"/transitions", []byte(`{"event":{"payload":{},"provenance":{"source":"test"}},"to_status":"supported","confidence":0.98}`))
	if transitioned.Code != http.StatusOK {
		t.Fatalf("transition: %s", transitioned.Body.String())
	}
	var result struct {
		Claim cognition.Claim `json:"claim"`
	}
	if err := json.Unmarshal(transitioned.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Claim.Status != cognition.ClaimSupported {
		t.Fatalf("unexpected transition: %s", transitioned.Body.String())
	}
}

func TestFrameCreateTransitionGetAndReplay(t *testing.T) {
	handler := testHandler()
	objectiveCreated := serve(handler, http.MethodPost, "/v1/objectives", []byte(`{"objective":{"objective_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b04","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b02","text":"Verify the runtime","success_conditions":["render_is_deterministic"],"constraints":{}},"event":{"payload":{},"provenance":{"source":"test"}}}`))
	if objectiveCreated.Code != http.StatusCreated {
		t.Fatalf("create objective: %s", objectiveCreated.Body.String())
	}
	createBody := []byte(`{"frame":{"agent_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b01","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b02","branch_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b03","objective_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b04","focus":{"type":"query","query":"initial investigation"},"mode":"explore","attention":{"policy":"balanced","deliberate":true,"ambient":true,"max_candidates":32},"zoom":2,"filters":{"trust_min":0.5},"budget":{"tokens":8000}},"event":{"payload":{},"provenance":{"source":"test"}}}`)
	created := serve(handler, http.MethodPost, "/v1/frames", createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("create frame status=%d body=%s", created.Code, created.Body.String())
	}
	var initial struct {
		Frame frame.Frame `json:"frame"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	renderBody := []byte(`{"frame_id":"` + initial.Frame.FrameID + `","objective_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b04","budget_tokens":2000}`)
	firstRender := serve(handler, http.MethodPost, "/v1/render", renderBody)
	secondRender := serve(handler, http.MethodPost, "/v1/render", renderBody)
	if firstRender.Code != http.StatusOK || firstRender.Body.String() != secondRender.Body.String() {
		t.Fatalf("render is not deterministic: %s / %s", firstRender.Body.String(), secondRender.Body.String())
	}
	transitionBody := []byte(`{"transition":{"as_of":"2026-09-14T12:00:00Z","operations":[{"op":"pin","ref":{"type":"claim","id":"018f47a7-34b2-7d10-a932-4f3ff37a4b10"}},{"op":"set_mode","mode":"verify"}]},"event":{"payload":{},"provenance":{"source":"test"}}}`)
	transitioned := serve(handler, http.MethodPost, "/v1/frames/"+initial.Frame.FrameID+"/transitions", transitionBody)
	if transitioned.Code != http.StatusCreated {
		t.Fatalf("transition status=%d body=%s", transitioned.Code, transitioned.Body.String())
	}
	var result frame.TransitionResult
	if err := json.Unmarshal(transitioned.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Frame.ParentFrameID != initial.Frame.FrameID || result.Frame.Revision != 1 || result.Frame.Mode != frame.ModeVerify {
		t.Fatalf("unexpected transition: %s", transitioned.Body.String())
	}
	emissionBody := []byte(`{"schema":"frp.cognitive-emission.v1","emission_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b20","frame_id":"` + result.Frame.FrameID + `","attention":[{"op":"attend","target":{"type":"query","text":"evidence against current hypothesis"}}],"frame_ops":[{"op":"unpin","ref":"claim:018f47a7-34b2-7d10-a932-4f3ff37a4b10"}]}`)
	reduced := serve(handler, http.MethodPost, "/v1/frames/"+result.Frame.FrameID+"/emissions", emissionBody)
	if reduced.Code != http.StatusCreated {
		t.Fatalf("emission status=%d body=%s", reduced.Code, reduced.Body.String())
	}
	var emissionResult struct {
		Decision cognition.Decision `json:"decision"`
	}
	if err := json.Unmarshal(reduced.Body.Bytes(), &emissionResult); err != nil {
		t.Fatal(err)
	}
	if emissionResult.Decision.Frame.Revision != 2 || emissionResult.Decision.Frame.Focus.Query != "evidence against current hypothesis" {
		t.Fatalf("unexpected emission decision: %s", reduced.Body.String())
	}
	gotInitial := serve(handler, http.MethodGet, "/v1/frames/"+initial.Frame.FrameID, nil)
	var unchanged frame.Frame
	if err := json.Unmarshal(gotInitial.Body.Bytes(), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 0 || len(unchanged.WorkingSet) != 0 {
		t.Fatal("initial frame was mutated")
	}
	replayed := serve(handler, http.MethodPost, "/v1/replay", []byte(`{"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4b02"}`))
	var replayResult struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(replayed.Body.Bytes(), &replayResult); err != nil {
		t.Fatal(err)
	}
	if len(replayResult.Events) != 4 {
		t.Fatalf("expected objective, frame create, direct transition, and emission transition events: %s", replayed.Body.String())
	}
}

func serve(handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("content-type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
