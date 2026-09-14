package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/procedure"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/runtime/httpapi"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestProcedureAPIListGetMatchAndRebuild(t *testing.T) {
	store := memory.New()
	values := []procedure.Procedure{
		testProcedure("procedure-b", "episode-a", "inspect environment", 0.9),
		testProcedure("procedure-a", "episode-a", "inspect environment", 0.9),
		testProcedure("procedure-c", "episode-b", "deploy release", 0.8),
	}
	if err := store.ReplaceProcedures(context.Background(), "episode-a", values[:2]); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceProcedures(context.Background(), "episode-b", values[2:]); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	listed := serve(handler, http.MethodGet, "/v1/procedures?episode_id=episode-a", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list procedures: status=%d body=%s", listed.Code, listed.Body.String())
	}
	var listResponse struct {
		Procedures []procedure.Procedure `json:"procedures"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listResponse); err != nil {
		t.Fatal(err)
	}
	if len(listResponse.Procedures) != 2 || listResponse.Procedures[0].ProcedureID != "procedure-a" || listResponse.Procedures[1].ProcedureID != "procedure-b" {
		t.Fatalf("unexpected stable list: %s", listed.Body.String())
	}

	got := serve(handler, http.MethodGet, "/v1/procedures/procedure-b", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get procedure: status=%d body=%s", got.Code, got.Body.String())
	}
	var gotProcedure procedure.Procedure
	if err := json.Unmarshal(got.Body.Bytes(), &gotProcedure); err != nil {
		t.Fatal(err)
	}
	if gotProcedure.ProcedureID != "procedure-b" {
		t.Fatalf("unexpected procedure: %s", got.Body.String())
	}
	if missing := serve(handler, http.MethodGet, "/v1/procedures/missing", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("missing procedure: status=%d body=%s", missing.Code, missing.Body.String())
	}

	objectiveBody := []byte(`{"objective":{"objective_id":"objective-a","episode_id":"episode-a","text":"Inspect environment","success_conditions":[],"constraints":{}},"event":{"payload":{},"provenance":{"source":"test"}}}`)
	if created := serve(handler, http.MethodPost, "/v1/objectives", objectiveBody); created.Code != http.StatusCreated {
		t.Fatalf("create objective: status=%d body=%s", created.Code, created.Body.String())
	}
	frameBody := []byte(`{"frame":{"agent_id":"agent-a","episode_id":"episode-a","branch_id":"branch-a","objective_id":"objective-a","focus":{"type":"query","query":"inspect environment"},"mode":"explore","attention":{"policy":"balanced","deliberate":true,"ambient":true,"max_candidates":32},"zoom":2,"filters":{},"budget":{"tokens":1000}},"event":{"payload":{},"provenance":{"source":"test"}}}`)
	createdFrame := serve(handler, http.MethodPost, "/v1/frames", frameBody)
	if createdFrame.Code != http.StatusCreated {
		t.Fatalf("create frame: status=%d body=%s", createdFrame.Code, createdFrame.Body.String())
	}
	var frameResponse struct {
		Frame frame.Frame `json:"frame"`
	}
	if err := json.Unmarshal(createdFrame.Body.Bytes(), &frameResponse); err != nil {
		t.Fatal(err)
	}
	matchBody := []byte(`{"frame_id":"` + frameResponse.Frame.FrameID + `","objective_id":"objective-a","limit":1,"threshold":0.1}`)
	first := serve(handler, http.MethodPost, "/v1/procedures/match", matchBody)
	second := serve(handler, http.MethodPost, "/v1/procedures/match", matchBody)
	if first.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("match is not stable: status=%d first=%s second=%s", first.Code, first.Body.String(), second.Body.String())
	}
	var matchResponse struct {
		Matches []struct {
			Procedure procedure.Procedure `json:"procedure"`
			Score     float64             `json:"score"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &matchResponse); err != nil {
		t.Fatal(err)
	}
	if len(matchResponse.Matches) != 1 || matchResponse.Matches[0].Procedure.ProcedureID != "procedure-a" || matchResponse.Matches[0].Score <= 0 {
		t.Fatalf("unexpected matches: %s", first.Body.String())
	}

	badMatch := serve(handler, http.MethodPost, "/v1/procedures/match", []byte(`{"frame_id":"missing","objective_id":"objective-a","threshold":2}`))
	if badMatch.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid match: status=%d body=%s", badMatch.Code, badMatch.Body.String())
	}
	rebuilt := serve(handler, http.MethodPost, "/v1/projections/procedures/rebuild", []byte(`{"episode_id":"episode-a","minimum_evidence":1}`))
	if rebuilt.Code != http.StatusOK {
		t.Fatalf("rebuild procedures: status=%d body=%s", rebuilt.Code, rebuilt.Body.String())
	}
	listed = serve(handler, http.MethodGet, "/v1/procedures?episode_id=episode-a", nil)
	if err := json.Unmarshal(listed.Body.Bytes(), &listResponse); err != nil {
		t.Fatal(err)
	}
	if len(listResponse.Procedures) != 0 {
		t.Fatalf("rebuild did not replace episode projection: %s", listed.Body.String())
	}
}

func testProcedure(id, episodeID, trigger string, successRate float64) procedure.Procedure {
	return procedure.Procedure{
		Protocol:             protocol.Name,
		Version:              protocol.Version,
		ProcedureID:          id,
		EpisodeID:            episodeID,
		SemanticTrigger:      trigger,
		AffordanceSequence:   []procedure.AffordanceStep{{Position: 0, AffordanceID: "inspect_environment"}},
		ExpectedOutcomes:     []procedure.ExpectedOutcome{{Status: execution.StatusCompleted}},
		EvidenceExecutionIDs: []string{"execution-" + id},
		Successes:            1,
		SuccessRate:          successRate,
		ProjectionVersion:    procedure.ProjectorVersion,
	}
}
