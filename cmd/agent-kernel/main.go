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
	"strconv"
	"strings"
	"syscall"
	"time"

	"crypto/rand"
	"encoding/hex"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/outbox"
	"github.com/temporality-project/temporality/kernel/priming"
	"github.com/temporality-project/temporality/kernel/quota"
	"github.com/temporality-project/temporality/workspace"
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
	if err := controlplane.LoadFileSecrets("DATABASE_URL", "TEMPORALITY_MODEL_API_KEY", "TEMPORALITY_API_TOKEN", "KERNEL_AUTH_TOKENS"); err != nil {
		log.Error("load file secrets", "error", err)
		os.Exit(1)
	}
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
	quotas, err := quota.Open(ctx, databaseURL, os.Getenv("KERNEL_RUN_QUOTAS"))
	if err != nil {
		log.Error("open run quotas", "error", err)
		os.Exit(1)
	}
	defer quotas.Close()
	if err = quotas.Migrate(ctx, "migrations/000020_kernel_run_quota.up.sql"); err != nil {
		log.Error("migrate run quotas", "error", err)
		os.Exit(1)
	}
	if quotas.Enabled() {
		log.Info("run quotas enabled", "default_per_day", quotas.Default())
	}
	costPrices, err := cost.ParsePrices(os.Getenv("KERNEL_MODEL_PRICES"))
	if err != nil {
		log.Error("parse model prices", "error", err)
		os.Exit(1)
	}
	costAPI := cost.NewCostAPI(costPrices, observationURL, os.Getenv("TEMPORALITY_API_TOKEN"))
	if len(costPrices.Prices()) > 0 {
		log.Info("model cost accounting enabled", "models", len(costPrices.Prices()))
	}
	primer := priming.NewPrimer(priming.DefaultConfig(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"))
	ws, err := workspace.Open(ctx, databaseURL)
	if err != nil {
		log.Error("open workspace store", "error", err)
		os.Exit(1)
	}
	defer ws.Close()
	if err = ws.Migrate(ctx, "migrations/000021_workspace.up.sql"); err != nil {
		log.Error("migrate workspace", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000022_workspace_agents_top_level.up.sql"); err != nil {
		log.Error("migrate workspace v22", "error", err)
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
	temporalWorker.RegisterActivityWithOptions(activities.KnowledgeLookup, activity.RegisterOptions{Name: agent.ActivityKnowledgeLookup})
	registerExampleWorkflow(temporalWorker)
	workerDone := make(chan error, 1)
	go func() { workerDone <- temporalWorker.Run(worker.InterruptCh()) }()
	gate, err := controlplane.NewGate(os.Getenv("KERNEL_AUTH_TOKENS"))
	if err != nil {
		log.Error("parse KERNEL_AUTH_TOKENS", "error", err)
		os.Exit(1)
	}
	// Merge database-stored user tokens with .env tokens. DB tokens added
	// via the UI take effect after kernel restart.
	if dbUsers, err := ws.ListUsers(ctx); err == nil {
		var extra []string
		for _, u := range dbUsers {
			if u.Token != "" && u.Active {
				projects := "*"
				if len(u.Projects) > 0 {
					projects = strings.Join(u.Projects, ",")
				}
				extra = append(extra, u.Token+":"+u.Name+":"+u.Role+":"+projects)
			}
		}
		if len(extra) > 0 {
			dbGate, err := controlplane.NewGate(strings.Join(extra, ";"))
			if err == nil && dbGate != nil && dbGate.Enabled() {
				gate = gate.Merge(dbGate)
				log.Info("merged database user tokens", "count", len(extra))
			}
		}
	}
	if !gate.Enabled() {
		log.Warn("KERNEL_AUTH_TOKENS is empty: authentication disabled; configure tokens before sharing this instance")
	} else {
		log.Info("kernel authentication enabled", "tokens", gate.TokenCount())
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /v1/agent/whoami", func(w http.ResponseWriter, r *http.Request) {
		// Identity for UI clients: the token's subject, role and project scope.
		// Any authenticated role may see its own identity.
		principal, ok := controlplane.FromContext(r.Context())
		if !ok {
			writeJSON(w, 200, map[string]any{"subject": "anonymous", "role": "admin", "projects": []string{"*"}, "auth_enabled": false})
			return
		}
		writeJSON(w, 200, map[string]any{"subject": principal.Subject, "role": principal.Role.String(), "projects": principal.Projects, "auth_enabled": true})
	})
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
		if !gate.Allow(w, r, controlplane.RoleWriter, input.Project) {
			return
		}
		if err := activities.PrepareRun(&input); err != nil {
			writeError(w, 422, err)
			return
		}
		// Consume the quota slot only when everything else validated: an
		// invalid request must never burn the project's daily budget.
		if quotas.Enabled() {
			verdict, err := quotas.Allow(r.Context(), input.Project)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			if !verdict.Allowed {
				retryAfter := int64(time.Until(verdict.ResetAt).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "daily run quota exceeded for project " + input.Project, "quota": verdict})
				return
			}
		}
		if input.ActorID == "" {
			input.ActorID = "human-requester"
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
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
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
		if !gate.Allow(w, r, controlplane.RoleOperator, project) {
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
	mux.HandleFunc("GET /v1/agent/outbox", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		stats, err := events.Stats(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"pending": stats.Pending, "oldest_pending_age_seconds": int64(stats.OldestPendingAge.Seconds()), "delivered": stats.Delivered, "last_error": stats.FailedAttemptsLast})
	})
	mux.HandleFunc("GET /v1/agent/quotas", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		verdict, err := quotas.Usage(r.Context(), project)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"project": project, "used": verdict.Used, "limit": verdict.Limit, "reset_at": verdict.ResetAt})
	})
	mux.HandleFunc("GET /v1/agent/cost/prices", func(w http.ResponseWriter, r *http.Request) {
		costAPI.PricesHandler(w, r)
	})
	mux.HandleFunc("GET /v1/agent/cost/run", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		costAPI.RunCostHandler(w, r)
	})
	mux.HandleFunc("GET /v1/agent/cost/project", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		costAPI.ProjectCostHandler(w, r)
	})
	mux.HandleFunc("GET /v1/agent/priming", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		primer.Handler(w, r)
	})
	liveness := &temporalLiveness{client: temporalClient}
	mux.HandleFunc("GET /v1/agent/operations", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		operations, err := activities.UncertainOperations(r.Context(), project, liveness)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"operations": operations, "count": len(operations)})
	})
	mux.HandleFunc("POST /v1/agent/operations/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var request agent.ReconcileRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
			writeError(w, 400, err)
			return
		}
		if !gate.Allow(w, r, controlplane.RoleOperator, request.Project) {
			return
		}
		event, err := activities.RecordReconciliation(r.Context(), request)
		if err != nil {
			writeError(w, 422, err)
			return
		}
		writeJSON(w, 201, map[string]any{"event_id": event.EventID, "operation_id": request.OperationID, "effect": request.Effect, "recorded": true})
	})
	// Workspace CRUD endpoints
	mux.HandleFunc("GET /v1/workspace/projects", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		projects, err := ws.ListAllProjects(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"projects": projects})
	})
	mux.HandleFunc("POST /v1/workspace/projects", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req workspace.CreateProjectRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, 422, errors.New("name is required"))
			return
		}
		if req.ID == "" {
			req.ID = slugify(req.Name)
		}
		project := &workspace.Project{ID: req.ID, Name: req.Name, Description: req.Description}
		if err := ws.CreateProject(r.Context(), project); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 201, project)
	})
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		project, err := ws.GetProject(r.Context(), r.PathValue("projectID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, project)
	})
	mux.HandleFunc("PUT /v1/workspace/projects/{projectID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req workspace.CreateProjectRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		project := workspace.Project{ID: r.PathValue("projectID"), Name: req.Name, Description: req.Description}
		if err := ws.UpdateProject(r.Context(), project); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, project)
	})
	mux.HandleFunc("DELETE /v1/workspace/projects/{projectID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if err := ws.DeleteProject(r.Context(), r.PathValue("projectID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/agents", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		agents, err := ws.ListAgentsByProject(r.Context(), r.PathValue("projectID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"agents": agents})
	})
	mux.HandleFunc("GET /v1/workspace/agents", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		agents, err := ws.ListAllAgents(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"agents": agents})
	})
	mux.HandleFunc("POST /v1/workspace/agents", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req workspace.CreateAgentRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, 422, errors.New("name is required"))
			return
		}
		if req.ID == "" {
			req.ID = slugify(req.Name)
		}
		a := &workspace.Agent{
			ID: req.ID, ProjectID: req.ProjectID, Name: req.Name,
			Model: req.Model, SystemPrompt: req.SystemPrompt,
			Skills: req.Skills, MCPServers: req.MCPServers,
			SandboxProfile: req.SandboxProfile,
		}
		if err := ws.CreateAgent(r.Context(), a); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 201, a)
	})
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		a, err := ws.GetAgent(r.Context(), r.PathValue("agentID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, a)
	})
	mux.HandleFunc("PUT /v1/workspace/agents/{agentID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req workspace.CreateAgentRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		a := workspace.Agent{
			ID: r.PathValue("agentID"), ProjectID: req.ProjectID, Name: req.Name,
			Model: req.Model, SystemPrompt: req.SystemPrompt,
			Skills: req.Skills, MCPServers: req.MCPServers,
			SandboxProfile: req.SandboxProfile,
		}
		if err := ws.UpdateAgent(r.Context(), a); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, a)
	})
	mux.HandleFunc("DELETE /v1/workspace/agents/{agentID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if err := ws.DeleteAgent(r.Context(), r.PathValue("agentID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/tasks", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		tasks, err := ws.ListTasks(r.Context(), r.PathValue("projectID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"tasks": tasks})
	})
	mux.HandleFunc("POST /v1/workspace/tasks", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req workspace.CreateTaskRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" || strings.TrimSpace(req.ProjectID) == "" {
			writeError(w, 422, errors.New("title, prompt and project_id are required"))
			return
		}
		if req.ID == "" {
			req.ID = slugify(req.Title)
		}
		task := &workspace.Task{ID: req.ID, ProjectID: req.ProjectID, AgentID: req.AgentID, Title: req.Title, Prompt: req.Prompt}
		if err := ws.CreateTask(r.Context(), task); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 201, task)
	})
	mux.HandleFunc("GET /v1/workspace/tasks/{taskID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		task, err := ws.GetTask(r.Context(), r.PathValue("taskID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, task)
	})
	mux.HandleFunc("DELETE /v1/workspace/tasks/{taskID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if err := ws.DeleteTask(r.Context(), r.PathValue("taskID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("POST /v1/workspace/tasks/{taskID}/runs", func(w http.ResponseWriter, r *http.Request) {
		// Start a run for a workspace task, using the assigned agent's config.
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		task, err := ws.GetTask(r.Context(), r.PathValue("taskID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		var req workspace.StartRunRequest
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
		// Resolve agent: explicit override > task default > none
		agentID := req.AgentID
		if agentID == "" {
			agentID = task.AgentID
		}
		var agentCfg *workspace.Agent
		if agentID != "" {
			a, aErr := ws.GetAgent(r.Context(), agentID)
			if aErr != nil {
				if errors.Is(aErr, workspace.ErrNotFound) {
					writeError(w, 404, errors.New("agent not found"))
					return
				}
				writeError(w, 500, aErr)
				return
			}
			agentCfg = &a
		}
		runID := task.ID + "-" + time.Now().UTC().Format("20060102-150405")
		input := agent.RunInput{
			RunID:   runID,
			Project: task.ProjectID,
			TaskID:  task.ID,
			Prompt:  task.Prompt,
		}
		if agentCfg != nil {
			if agentCfg.Model != "" {
				input.Model = agentCfg.Model
			}
			if agentCfg.SystemPrompt != "" {
				input.SystemPrompt = agentCfg.SystemPrompt
			}
		}
		if req.Model != "" {
			input.Model = req.Model
		}
		if err := activities.PrepareRun(&input); err != nil {
			writeError(w, 422, err)
			return
		}
		if quotas.Enabled() {
			verdict, qErr := quotas.Allow(r.Context(), task.ProjectID)
			if qErr != nil {
				writeError(w, 500, qErr)
				return
			}
			if !verdict.Allowed {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "daily quota exceeded"})
				return
			}
		}
		input.ActorID = "human-requester"
		workflowID := workflowIDFor(activities.SourceID, task.ProjectID, runID)
		options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		run, wfErr := temporalClient.ExecuteWorkflow(r.Context(), options, "AgentRun", input)
		if wfErr != nil {
			writeError(w, 409, wfErr)
			return
		}
		wsRun := &workspace.Run{
			ID: runID, TaskID: task.ID, ProjectID: task.ProjectID,
			AgentID: agentID, RunID: runID, Status: "started", Model: input.Model,
		}
		_ = ws.CreateRun(r.Context(), wsRun)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"run_id": runID, "task_id": task.ID, "project": task.ProjectID,
			"workflow_id": run.GetID(), "temporal_run_id": run.GetRunID(), "status": "started",
		})
	})
	mux.HandleFunc("GET /v1/workspace/runs/{runID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		run, err := ws.GetRun(r.Context(), r.PathValue("runID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// Enrich with live Temporal status
		response, tErr := temporalClient.DescribeWorkflowExecution(r.Context(), workflowIDFor(activities.SourceID, run.ProjectID, run.RunID), "")
		if tErr == nil && response.WorkflowExecutionInfo != nil {
			run.Status = strings.ToLower(response.WorkflowExecutionInfo.Status.String())
			if response.WorkflowExecutionInfo.Status == enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
				var result agent.RunResult
				if gErr := temporalClient.GetWorkflow(r.Context(), workflowIDFor(activities.SourceID, run.ProjectID, run.RunID), "").Get(r.Context(), &result); gErr == nil {
					run.Answer = result.Answer
					run.Turns = result.Turns
					run.Status = result.Status
					_ = ws.UpdateRunStatus(r.Context(), run.ID, run.Status, run.Answer, run.Turns, "")
				}
			}
		}
		writeJSON(w, 200, run)
	})
	mux.HandleFunc("GET /v1/workspace/tasks/{taskID}/runs", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		runs, err := ws.ListRuns(r.Context(), r.PathValue("taskID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"runs": runs})
	})

	// Providers
	mux.HandleFunc("GET /v1/workspace/providers", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		providers, err := ws.ListProviders(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"providers": providers})
	})
	mux.HandleFunc("POST /v1/workspace/providers", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req workspace.CreateProviderRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.BaseURL) == "" {
			writeError(w, 422, errors.New("name and base_url are required"))
			return
		}
		if req.ID == "" {
			req.ID = "prov-" + shortID()
		}
		provider := &workspace.Provider{ID: req.ID, Name: req.Name, BaseURL: req.BaseURL, APIKeyRef: req.APIKeyRef, Models: req.Models, Labels: req.Labels}
		if err := ws.CreateProvider(r.Context(), provider); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, provider)
	})
	mux.HandleFunc("GET /v1/workspace/providers/{providerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		provider, err := ws.GetProvider(r.Context(), r.PathValue("providerID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, provider)
	})
	mux.HandleFunc("PUT /v1/workspace/providers/{providerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req workspace.CreateProviderRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		provider := workspace.Provider{ID: r.PathValue("providerID"), Name: req.Name, BaseURL: req.BaseURL, APIKeyRef: req.APIKeyRef, Models: req.Models, Labels: req.Labels}
		if err := ws.UpdateProvider(r.Context(), provider); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, provider)
	})
	mux.HandleFunc("DELETE /v1/workspace/providers/{providerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if err := ws.DeleteProvider(r.Context(), r.PathValue("providerID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
	})

	// Users
	mux.HandleFunc("GET /v1/workspace/users", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		users, err := ws.ListUsers(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"users": users})
	})
	mux.HandleFunc("POST /v1/workspace/users", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var req workspace.CreateUserRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Role) == "" {
			writeError(w, 422, errors.New("name and role are required"))
			return
		}
		if req.ID == "" {
			req.ID = "user-" + shortID()
		}
		token := strings.TrimSpace(req.Token)
		if token == "" {
			token = generateToken()
		}
		active := req.Active != nil && *req.Active
		user := &workspace.User{ID: req.ID, Name: req.Name, Email: req.Email, Role: req.Role, Token: token, Projects: req.Projects, Active: active}
		if err := ws.CreateUser(r.Context(), user); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, user)
	})
	mux.HandleFunc("PUT /v1/workspace/users/{userID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var req workspace.CreateUserRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		active := req.Active != nil && *req.Active
		user := workspace.User{ID: r.PathValue("userID"), Name: req.Name, Email: req.Email, Role: req.Role, Projects: req.Projects, Active: active}
		if err := ws.UpdateUser(r.Context(), user); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, user)
	})
	mux.HandleFunc("DELETE /v1/workspace/users/{userID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		if err := ws.DeleteUser(r.Context(), r.PathValue("userID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
	})

	registerExampleRoutes(mux, temporalClient, taskQueue, activities, gate)
	address := env("KERNEL_HTTP_ADDR", ":8090")
	server := &http.Server{Addr: address, Handler: gate.Authenticate(mux), ReadHeaderTimeout: 5 * time.Second}
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

// temporalLiveness reports whether the run's workflow is still executing.
// A missing workflow counts as not running: its events can never complete.
type temporalLiveness struct {
	client client.Client
}

func (t *temporalLiveness) IsWorkflowRunning(ctx context.Context, sourceID, project, runID string) (bool, error) {
	description, err := t.client.DescribeWorkflowExecution(ctx, workflowIDFor(sourceID, project, runID), "")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}
	status := description.GetWorkflowExecutionInfo().GetStatus()
	return status == enums.WORKFLOW_EXECUTION_STATUS_RUNNING, nil
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

func slugify(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && (r == ' ' || r == '-' || r == '_') {
			b.WriteByte('-')
			prevDash = true
		}
	}
	result := strings.TrimRight(b.String(), "-")
	if result == "" {
		return "untitled"
	}
	return result
}

func shortID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
