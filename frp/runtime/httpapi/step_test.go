package httpapi_test

import (
	"encoding/json"
	runtimeStep "github.com/temporality-project/temporality/frp/runtime/step"
	"net/http"
	"testing"
)

func TestAtomicStepAPI(t *testing.T) {
	handler := testHandler()
	create := []byte(`{"frame":{"agent_id":"10000000-0000-4000-8000-000000000001","episode_id":"10000000-0000-4000-8000-000000000002","branch_id":"10000000-0000-4000-8000-000000000003","objective_id":"10000000-0000-4000-8000-000000000004","focus":{"type":"query","query":"work"},"attention":{"policy":"balanced","deliberate":true,"ambient":true,"max_candidates":32},"filters":{},"budget":{"tokens":1000}},"event":{"payload":{},"provenance":{"source":"test"}}}`)
	created := serve(handler, http.MethodPost, "/v1/frames", create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create frame: %s", created.Body.String())
	}
	var frameResponse struct {
		Frame struct {
			FrameID string `json:"frame_id"`
		} `json:"frame"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &frameResponse); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"frame_id":"` + frameResponse.Frame.FrameID + `","emission":{"schema":"frp.cognitive-emission.v1","emission_id":"10000000-0000-4000-8000-000000000010","frame_id":"` + frameResponse.Frame.FrameID + `","claims":[{"proposition":"atomic step works","confidence":0.9,"status":"candidate"}],"attention":[{"op":"attend","target":{"type":"query","text":"next"}}],"actions":[{"affordance":"inspect_environment","args":{"path":"/workspace"}}]},"definitions":[{"id":"inspect_environment","execution_mode":"deterministic","input_schema":{},"capabilities":["filesystem.read"],"limits":{"timeout_sec":30,"cpu":1,"memory_mb":128,"disk_mb":64},"planner":{},"failure_policy":{"retry_transient":false,"allow_strategy_change":false,"max_retries":0}}]}`)
	response := serve(handler, http.MethodPost, "/v1/step", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("step: %s", response.Body.String())
	}
	var result runtimeStep.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Claims) != 1 || len(result.Executions) != 1 || len(result.Events) != 5 || result.Frame.Revision != 1 {
		t.Fatalf("unexpected step result: %#v", result)
	}
}
