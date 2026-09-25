//go:build agent_examples

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	team "github.com/temporality-project/temporality/examples/lead_coder_reviewer_qa"
	"github.com/temporality-project/temporality/kernel/agent"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func registerExampleWorkflow(w worker.Worker) {
	w.RegisterWorkflowWithOptions(team.Workflow, workflow.RegisterOptions{Name: team.WorkflowName})
}

func registerExampleRoutes(mux *http.ServeMux, temporalClient client.Client, taskQueue string, activities *agent.Activities, gate *controlplane.Gate) {
	const prefix = "/v1/agent/examples/lead-coder-reviewer-qa"
	mux.HandleFunc("POST "+prefix, func(w http.ResponseWriter, r *http.Request) {
		var input agent.RunInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.Project) == "" || strings.TrimSpace(input.Prompt) == "" {
			writeError(w, 422, errors.New("run_id, project and prompt are required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleWriter, input.Project) {
			return
		}
		if err := activities.PrepareRun(&input); err != nil {
			writeError(w, 422, err)
			return
		}
		run, err := temporalClient.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{ID: workflowIDFor(activities.SourceID, input.Project, input.RunID), TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, team.WorkflowName, input)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"run_id": input.RunID, "project": input.Project, "workflow_id": run.GetID(), "temporal_run_id": run.GetRunID(), "status": "started"})
	})
	mux.HandleFunc("GET "+prefix+"/runs/{runID}", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		id := workflowIDFor(activities.SourceID, project, r.PathValue("runID"))
		description, err := temporalClient.DescribeWorkflowExecution(r.Context(), id, "")
		if err != nil {
			writeError(w, 404, err)
			return
		}
		status := "unknown"
		if description.WorkflowExecutionInfo != nil {
			status = description.WorkflowExecutionInfo.Status.String()
		}
		reply := map[string]any{"run_id": r.PathValue("runID"), "project": project, "status": status}
		if description.WorkflowExecutionInfo != nil && description.WorkflowExecutionInfo.Status == enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
			var result team.Result
			if err := temporalClient.GetWorkflow(r.Context(), id, "").Get(r.Context(), &result); err != nil {
				writeError(w, 502, err)
				return
			}
			reply["result"] = result
		}
		writeJSON(w, 200, reply)
	})
	mux.HandleFunc("POST "+prefix+"/runs/{runID}/approval", func(w http.ResponseWriter, r *http.Request) {
		var approval agent.Approval
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&approval); err != nil {
			writeError(w, 400, err)
			return
		}
		if approval.OperationID == "" || approval.ActorID == "" {
			writeError(w, 422, errors.New("operation_id and actor_id are required"))
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleOperator, project) {
			return
		}
		childRunID, _, ok := strings.Cut(approval.OperationID, "/turn/")
		if !ok || !strings.HasPrefix(childRunID, r.PathValue("runID")+"/") {
			writeError(w, 422, errors.New("operation_id does not identify a child run of this example"))
			return
		}
		childID := "agent-child/" + agent.EventScope(activities.SourceID, project, childRunID)
		if err := temporalClient.SignalWorkflow(r.Context(), childID, "", agent.ApprovalSignal, approval); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
}
