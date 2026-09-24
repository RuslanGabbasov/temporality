package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/outbox"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	databaseURL := requiredEnv("DATABASE_URL")
	observationURL := env("TEMPORALITY_URL", "http://localhost:8080")
	events, err := outbox.Open(ctx, databaseURL, observationURL)
	if err != nil {
		log.Error("open kernel outbox", "error", err)
		os.Exit(1)
	}
	defer events.Close()
	if err = events.Migrate(ctx, "migrations/000019_kernel_event_outbox.up.sql"); err != nil {
		log.Error("migrate kernel outbox", "error", err)
		os.Exit(1)
	}
	activities, err := agent.NewActivities(events)
	if err != nil {
		log.Error("configure activities", "error", err)
		os.Exit(1)
	}
	temporalClient, err := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", client.DefaultHostPort)})
	if err != nil {
		log.Error("connect Temporal", "error", err)
		os.Exit(1)
	}
	defer temporalClient.Close()
	go events.RunPublisher(ctx)
	taskQueue := env("TEMPORAL_TASK_QUEUE", agent.TaskQueue)
	temporalWorker := worker.New(temporalClient, taskQueue, worker.Options{})
	temporalWorker.RegisterWorkflowWithOptions(agent.AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	temporalWorker.RegisterActivityWithOptions(activities.RecordEvent, activity.RegisterOptions{Name: agent.ActivityRecordEvent})
	temporalWorker.RegisterActivityWithOptions(activities.CallModel, activity.RegisterOptions{Name: agent.ActivityCallModel})
	temporalWorker.RegisterActivityWithOptions(activities.RunTool, activity.RegisterOptions{Name: agent.ActivityRunTool})
	temporalWorker.RegisterActivityWithOptions(activities.KnowledgeHints, activity.RegisterOptions{Name: agent.ActivityKnowledgeHints})
	registerExampleWorkflow(temporalWorker)
	workerDone := make(chan error, 1)
	go func() { workerDone <- temporalWorker.Run(worker.InterruptCh()) }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("POST /v1/agent/runs", func(w http.ResponseWriter, r *http.Request) {
		var input agent.RunInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.Project) == "" || strings.TrimSpace(input.Prompt) == "" {
			writeError(w, 422, errors.New("run_id, project and prompt are required"))
			return
		}
		if input.ActorID == "" {
			input.ActorID = "human-requester"
		}
		if err := activities.PrepareRun(&input); err != nil {
			writeError(w, 422, err)
			return
		}
		workflowID := workflowIDFor(activities.SourceID, input.Project, input.RunID)
		options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		run, err := temporalClient.ExecuteWorkflow(r.Context(), options, "AgentRun", input)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"run_id": input.RunID, "project": input.Project, "workflow_id": run.GetID(), "temporal_run_id": run.GetRunID(), "status": "started"})
	})
	mux.HandleFunc("GET /v1/agent/runs/{runID}", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		sourceID := querySourceID(r, activities.SourceID)
		response, err := temporalClient.DescribeWorkflowExecution(r.Context(), workflowIDFor(sourceID, project, r.PathValue("runID")), "")
		if err != nil {
			writeError(w, 404, err)
			return
		}
		var status string
		if response.WorkflowExecutionInfo != nil {
			status = response.WorkflowExecutionInfo.Status.String()
		}
		reply := map[string]any{"run_id": r.PathValue("runID"), "project": project, "status": status}
		if response.WorkflowExecutionInfo != nil && response.WorkflowExecutionInfo.Status == enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
			var result agent.RunResult
			if err := temporalClient.GetWorkflow(r.Context(), workflowIDFor(sourceID, project, r.PathValue("runID")), "").Get(r.Context(), &result); err != nil {
				writeError(w, 502, err)
				return
			}
			reply["result"] = result
		}
		writeJSON(w, 200, reply)
	})
	mux.HandleFunc("POST /v1/agent/runs/{runID}/approval", func(w http.ResponseWriter, r *http.Request) {
		var approval agent.Approval
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&approval); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := validateApproval(approval); err != nil {
			writeError(w, 422, err)
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		sourceID := querySourceID(r, activities.SourceID)
		childRunID, _, _ := strings.Cut(approval.OperationID, "/turn/")
		workflowID := workflowIDFor(sourceID, project, r.PathValue("runID"))
		if childRunID != r.PathValue("runID") && strings.HasPrefix(childRunID, r.PathValue("runID")+"/") {
			workflowID = "agent-child/" + agent.EventScope(sourceID, project, childRunID)
		}
		if err := temporalClient.SignalWorkflow(r.Context(), workflowID, "", agent.ApprovalSignal, approval); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	registerExampleRoutes(mux, temporalClient, taskQueue, activities)
	address := env("KERNEL_HTTP_ADDR", ":8090")
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = server.Shutdown(context.Background()) }()
	log.Info("agent kernel started", "address", address, "task_queue", taskQueue)
	serveErr := server.ListenAndServe()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Error("serve kernel API", "error", serveErr)
		stop()
		os.Exit(1)
	}
	select {
	case err := <-workerDone:
		if err != nil {
			log.Error("Temporal worker stopped", "error", err)
		}
	case <-ctx.Done():
	}
}

func validateApproval(approval agent.Approval) error {
	if approval.OperationID == "" || approval.ActorID == "" || approval.ArgumentsHash == "" {
		return errors.New("operation_id, arguments_hash and actor_id are required")
	}
	if len(approval.ArgumentsHash) != len("sha256:")+64 || !strings.HasPrefix(approval.ArgumentsHash, "sha256:") {
		return errors.New("arguments_hash must be a sha256 digest")
	}
	for _, char := range strings.TrimPrefix(approval.ArgumentsHash, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return errors.New("arguments_hash must be a lowercase sha256 digest")
		}
	}
	return nil
}

func querySourceID(r *http.Request, fallback string) string {
	if sourceID := strings.TrimSpace(r.URL.Query().Get("source_id")); sourceID != "" && len(sourceID) <= 256 {
		return sourceID
	}
	return fallback
}

func workflowIDFor(sourceID, project, runID string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + project + "\x00" + runID))
	return fmt.Sprintf("agent-run/%x", digest[:16])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		panic(name + " is required")
	}
	return value
}
