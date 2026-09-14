package httpapi_test

import (
	"encoding/json"
	"github.com/temporality-project/temporality/frp/execution"
	"net/http"
	"testing"
)

func TestExecutionLifecycleAPI(t *testing.T) {
	handler := testHandler()
	body := []byte(`{"definition":{"id":"inspect_environment","execution_mode":"deterministic","input_schema":{},"capabilities":["filesystem.read"],"limits":{"timeout_sec":30,"cpu":1,"memory_mb":128,"disk_mb":64},"planner":{},"failure_policy":{"retry_transient":false,"allow_strategy_change":false,"max_retries":0}},"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a6c01","arguments":{"path":"/workspace"}}`)
	created := serve(handler, http.MethodPost, "/v1/executions", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create execution: %s", created.Body.String())
	}
	var response struct {
		Execution execution.Execution `json:"execution"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Execution.Status != execution.StatusCreated {
		t.Fatal("execution was not durably created")
	}
	running := serve(handler, http.MethodPost, "/internal/v1/executions/"+response.Execution.ExecutionID+"/transitions", []byte(`{"status":"running"}`))
	if running.Code != http.StatusOK {
		t.Fatalf("start execution: %s", running.Body.String())
	}
	completed := serve(handler, http.MethodPost, "/internal/v1/executions/"+response.Execution.ExecutionID+"/transitions", []byte(`{"status":"completed"}`))
	if completed.Code != http.StatusOK {
		t.Fatalf("complete execution: %s", completed.Body.String())
	}
	got := serve(handler, http.MethodGet, "/v1/executions/"+response.Execution.ExecutionID, nil)
	var final execution.Execution
	if err := json.Unmarshal(got.Body.Bytes(), &final); err != nil {
		t.Fatal(err)
	}
	if final.Status != execution.StatusCompleted || final.StartedAt == nil || final.FinishedAt == nil {
		t.Fatalf("unexpected final execution: %#v", final)
	}
}
