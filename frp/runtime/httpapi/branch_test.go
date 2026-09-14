package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/temporality-project/temporality/frp/branch"
	"github.com/temporality-project/temporality/frp/frame"
)

func createForkSource(t *testing.T, handler http.Handler) frame.Frame {
	t.Helper()
	objective := serve(handler, http.MethodPost, "/v1/objectives", []byte(`{"objective":{"objective_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b04","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b02","text":"Compare branches","success_conditions":["deterministic"],"constraints":{}},"event":{"payload":{},"provenance":{"source":"test"}}}`))
	if objective.Code != http.StatusCreated {
		t.Fatalf("create objective status=%d body=%s", objective.Code, objective.Body.String())
	}
	created := serve(handler, http.MethodPost, "/v1/frames", []byte(`{"frame":{"agent_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b01","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b02","branch_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b03","objective_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b04","focus":{"type":"query","query":"fork source"},"mode":"explore","attention":{"policy":"balanced","deliberate":true,"ambient":true,"max_candidates":32},"zoom":2,"filters":{"trust_min":0.5},"budget":{"tokens":8000}},"event":{"payload":{},"provenance":{"source":"test"}}}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create frame status=%d body=%s", created.Code, created.Body.String())
	}
	var response struct {
		Frame frame.Frame `json:"frame"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Frame
}

func TestBranchAPIAtomicForkGetAndCAS(t *testing.T) {
	handler := testHandler()
	source := createForkSource(t, handler)
	forked := serve(handler, http.MethodPost, "/v1/fork", []byte(`{"source_frame_id":"`+source.FrameID+`","branches":[{"label":"candidate a","model_config":{"model":"a"}},{"branch_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b12","label":"candidate b","model_config":{"model":"b"}}]}`))
	if forked.Code != http.StatusCreated {
		t.Fatalf("fork status=%d body=%s", forked.Code, forked.Body.String())
	}
	var group branch.ForkGroup
	if err := json.Unmarshal(forked.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	if group.ForkGroupID == "" || len(group.Branches) != 2 || group.SourceCursor.EventSeq == 0 || group.Branches[0].BranchID == "" {
		t.Fatalf("incomplete fork group: %s", forked.Body.String())
	}
	gotBranch := serve(handler, http.MethodGet, "/v1/branches/"+group.Branches[0].BranchID, nil)
	gotGroup := serve(handler, http.MethodGet, "/v1/fork-groups/"+group.ForkGroupID, nil)
	if gotBranch.Code != http.StatusOK || gotGroup.Code != http.StatusOK {
		t.Fatalf("get branch=%d group=%d", gotBranch.Code, gotGroup.Code)
	}
	head := group.Branches[0].HeadFrameID
	updated := serve(handler, http.MethodPost, "/v1/branches/"+group.Branches[0].BranchID+"/head", []byte(`{"expected_frame_id":"`+head+`","next_frame_id":"`+head+`"}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("CAS status=%d body=%s", updated.Code, updated.Body.String())
	}
	conflict := serve(handler, http.MethodPost, "/v1/branches/"+group.Branches[0].BranchID+"/head", []byte(`{"expected_frame_id":"stale","next_frame_id":"`+head+`"}`))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("stale CAS status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestBranchComparisonAPIDeterministic(t *testing.T) {
	handler := testHandler()
	source := createForkSource(t, handler)
	forked := serve(handler, http.MethodPost, "/v1/fork", []byte(`{"source_frame_id":"`+source.FrameID+`","fork_group_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b20","branches":[{"branch_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b21","model_config":{}},{"branch_id":"018f47a7-34b2-7d10-a932-4f3ff37a5b22","model_config":{}}]}`))
	if forked.Code != http.StatusCreated {
		t.Fatalf("fork status=%d body=%s", forked.Code, forked.Body.String())
	}
	var group branch.ForkGroup
	if err := json.Unmarshal(forked.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"fork_group_id":"` + group.ForkGroupID + `","left":{"branch_id":"` + group.Branches[0].BranchID + `","episode_id":"` + group.EpisodeID + `","objective_id":"` + group.ObjectiveID + `","source_frame_id":"` + group.SourceFrameID + `","claims":["shared","left"],"cost":1},"right":{"branch_id":"` + group.Branches[1].BranchID + `","episode_id":"` + group.EpisodeID + `","objective_id":"` + group.ObjectiveID + `","source_frame_id":"` + group.SourceFrameID + `","claims":["right","shared"],"cost":2}}`)
	first := serve(handler, http.MethodPost, "/v1/branch-comparisons", body)
	second := serve(handler, http.MethodPost, "/v1/branch-comparisons", body)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("comparison statuses=%d/%d bodies=%s / %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var a, b branch.ComparisonResult
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if a.ComparisonID == "" || a.ComparisonID != b.ComparisonID {
		t.Fatalf("comparison is not deterministic: %q != %q", a.ComparisonID, b.ComparisonID)
	}
	got := serve(handler, http.MethodGet, "/v1/branch-comparisons/"+a.ComparisonID, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get comparison status=%d body=%s", got.Code, got.Body.String())
	}
}
