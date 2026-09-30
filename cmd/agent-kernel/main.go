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
	if err = ws.Migrate(ctx, "migrations/000023_user_tokens.up.sql"); err != nil {
		log.Error("migrate workspace v23", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000024_project_defaults.up.sql"); err != nil {
		log.Error("migrate workspace v24", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000025_triggers.up.sql"); err != nil {
		log.Error("migrate workspace v25", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000026_project_archive.up.sql"); err != nil {
		log.Error("migrate workspace v26", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000027_project_allowed_users.up.sql"); err != nil {
		log.Error("migrate workspace v27", "error", err)
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
	mux.HandleFunc("POST /v1/agent/runs/{runID}/cancel", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleWriter, project) {
			return
		}
		sourceID := querySourceID(r, activities.SourceID)
		workflowID := workflowIDFor(sourceID, project, r.PathValue("runID"))
		if err := temporalClient.CancelWorkflow(r.Context(), workflowID, ""); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]string{"run_id": r.PathValue("runID"), "status": "cancel_requested"})
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
		// Get user from token for project filtering
		var userID string
		var isAdmin bool
		if principal, ok := controlplane.FromContext(r.Context()); ok {
			isAdmin = principal.Role >= controlplane.RoleAdmin
			// Try to find user by token to get their ID
			token, _ := controlplane.BearerToken(r)
			if token != "" {
				if user, err := ws.GetUserByToken(r.Context(), token); err == nil {
					userID = user.ID
				}
			}
		}
		projects, err := ws.ListProjectsForUser(r.Context(), userID, isAdmin)
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
		project := &workspace.Project{ID: req.ID, Name: req.Name, Description: req.Description, DefaultAgentID: req.DefaultAgentID, DefaultModel: req.DefaultModel, AllowedUsers: req.AllowedUsers}
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
		project := workspace.Project{ID: r.PathValue("projectID"), Name: req.Name, Description: req.Description, DefaultAgentID: req.DefaultAgentID, DefaultModel: req.DefaultModel, AllowedUsers: req.AllowedUsers}
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
			ID: req.ID, ProjectID: req.ProjectID, Name: req.Name, Description: req.Description,
			Model: req.Model, Provider: req.Provider, SystemPrompt: req.SystemPrompt,
			Temperature: req.Temperature, MaxTokens: req.MaxTokens,
			Skills: req.Skills, MCPServers: req.MCPServers,
			SandboxProfile: req.SandboxProfile, NetworkAccess: req.NetworkAccess, ReadOnly: req.ReadOnly,
			MaxTurns: req.MaxTurns, ApprovalMode: req.ApprovalMode, Labels: req.Labels,
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
			ID: r.PathValue("agentID"), ProjectID: req.ProjectID, Name: req.Name, Description: req.Description,
			Model: req.Model, Provider: req.Provider, SystemPrompt: req.SystemPrompt,
			Temperature: req.Temperature, MaxTokens: req.MaxTokens,
			Skills: req.Skills, MCPServers: req.MCPServers,
			SandboxProfile: req.SandboxProfile, NetworkAccess: req.NetworkAccess, ReadOnly: req.ReadOnly,
			MaxTurns: req.MaxTurns, ApprovalMode: req.ApprovalMode, Labels: req.Labels,
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
			req.ID = slugify(req.Title) + "-" + time.Now().UTC().Format("20060102-150405")
		}
		// Ensure project exists in workspace (auto-create from journal scope).
		if _, err := ws.GetProject(r.Context(), req.ProjectID); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				if err := ws.CreateProject(r.Context(), &workspace.Project{ID: req.ProjectID, Name: req.ProjectID}); err != nil {
					writeError(w, 500, err)
					return
				}
			} else {
				writeError(w, 500, err)
				return
			}
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
	mux.HandleFunc("PATCH /v1/workspace/tasks/{taskID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Prompt string `json:"prompt"`
			Title  string `json:"title"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := ws.UpdateTaskPrompt(r.Context(), r.PathValue("taskID"), req.Prompt, req.Title); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"updated": true})
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
			if agentCfg.NetworkAccess != nil && *agentCfg.NetworkAccess {
				input.NetworkAccess = true
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
	mux.HandleFunc("POST /v1/workspace/runs/{runID}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
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
		workflowID := workflowIDFor(activities.SourceID, run.ProjectID, run.RunID)
		if err := temporalClient.CancelWorkflow(r.Context(), workflowID, ""); err != nil {
			writeError(w, 409, err)
			return
		}
		_ = ws.UpdateRunStatus(r.Context(), run.ID, "cancelled", "", 0, "")
		writeJSON(w, 200, map[string]string{"run_id": run.RunID, "status": "cancel_requested"})
	})
	// SSE stream for a workspace run — pushes model.completed, turn.completed,
	// tool.* and run.completed events as they land in the journal.
	mux.HandleFunc("GET /v1/workspace/runs/{runID}/stream", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		runID := r.PathValue("runID")
		run, err := ws.GetRun(r.Context(), runID)
		if err != nil {
			writeError(w, 404, err)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, 500, errors.New("streaming not supported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		ctx := r.Context()
		var cursor string
		done := false
		for !done {
			select {
			case <-ctx.Done():
				done = true
			default:
			}
			if done {
				break
			}
			events, nextCursor, eErr := ws.StreamRunEvents(r.Context(), run.ProjectID, runID, cursor, 50)
			if eErr == nil {
				for _, ev := range events {
					data, _ := json.Marshal(ev)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
					flusher.Flush()
					if ev.Type == "run.completed" || ev.Type == "run.failed" {
						done = true
					}
				}
				if nextCursor != "" {
					cursor = nextCursor
				}
			}
			if !done {
				time.Sleep(1500 * time.Millisecond)
			}
		}
		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	})

	// Trajectory extraction: deterministic structures from a run's event stream.
	mux.HandleFunc("GET /v1/workspace/runs/{runID}/trajectory", func(w http.ResponseWriter, r *http.Request) {
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
		// Fetch all events for this run from the journal.
		events, _, eErr := ws.StreamRunEvents(r.Context(), run.ProjectID, run.RunID, "", 500)
		if eErr != nil {
			writeError(w, 500, eErr)
			return
		}
		// Convert to EventLike for the extraction module.
		var evLikes []agent.EventLike
		for _, ev := range events {
			var occurredAt time.Time
			if ev.OccurredAt != "" {
				occurredAt, _ = time.Parse(time.RFC3339Nano, ev.OccurredAt)
			}
			el := agent.EventLike{
				EventID:    ev.EventID,
				OccurredAt: occurredAt,
				Type:       ev.Type,
				Context:    struct{ Run, Project string }{Run: run.RunID, Project: run.ProjectID},
			}
			// Parse the data JSON.
			if ev.Data != nil {
				_ = json.Unmarshal(ev.Data, &el.Data)
			}
			if el.Data == nil {
				el.Data = make(map[string]any)
			}
			evLikes = append(evLikes, el)
		}
		trajectory := agent.ExtractTrajectory(evLikes)
		// Debug: log event types and turn count
		slog.Info("trajectory extraction", "events", len(evLikes), "turns", len(trajectory.Turns), "tools", len(trajectory.Tools), "tokens", trajectory.Summary.TotalTokens)
		for i, ev := range evLikes {
			if i < 5 || ev.Type == "model.completed" || ev.Type == "turn.started" {
				slog.Info("  event", "type", ev.Type, "id", ev.EventID[len(ev.EventID)-10:], "data_keys", len(ev.Data))
			}
		}
		writeJSON(w, 200, trajectory)
	})

	// Experience projection: aggregate trajectories across all runs in a project.
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/projection", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		projectID := r.PathValue("projectID")
		runs, err := ws.ListRunsByProject(r.Context(), projectID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		var trajectories []agent.Trajectory
		for _, run := range runs {
			events, _, eErr := ws.StreamRunEvents(r.Context(), run.ProjectID, run.RunID, "", 500)
			if eErr != nil {
				continue
			}
			var evLikes []agent.EventLike
			for _, ev := range events {
				var occurredAt time.Time
				if ev.OccurredAt != "" {
					occurredAt, _ = time.Parse(time.RFC3339Nano, ev.OccurredAt)
				}
				el := agent.EventLike{
					EventID: ev.EventID, OccurredAt: occurredAt, Type: ev.Type,
					Context: struct{ Run, Project string }{Run: run.RunID, Project: run.ProjectID},
				}
				if ev.Data != nil {
					_ = json.Unmarshal(ev.Data, &el.Data)
				}
				if el.Data == nil {
					el.Data = make(map[string]any)
				}
				evLikes = append(evLikes, el)
			}
			trajectories = append(trajectories, agent.ExtractTrajectory(evLikes))
		}
		projection := agent.ProjectExperience(trajectories)
		slog.Info("experience projection", "project", projectID, "runs", len(trajectories), "tool_patterns", len(projection.ToolPatterns), "knowledge", len(projection.KnowledgeLife))
		writeJSON(w, 200, projection)
	})

	// Trajectory comparison: extract and diff two runs side-by-side.
	mux.HandleFunc("GET /v1/workspace/runs/compare", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		runA := r.URL.Query().Get("a")
		runB := r.URL.Query().Get("b")
		if runA == "" || runB == "" {
			writeError(w, 422, errors.New("query params a and b (run IDs) are required"))
			return
		}
		extractRun := func(runID string) (*agent.Trajectory, error) {
			run, err := ws.GetRun(r.Context(), runID)
			if err != nil {
				return nil, err
			}
			events, _, eErr := ws.StreamRunEvents(r.Context(), run.ProjectID, run.RunID, "", 500)
			if eErr != nil {
				return nil, eErr
			}
			var evLikes []agent.EventLike
			for _, ev := range events {
				var occurredAt time.Time
				if ev.OccurredAt != "" {
					occurredAt, _ = time.Parse(time.RFC3339Nano, ev.OccurredAt)
				}
				el := agent.EventLike{
					EventID: ev.EventID, OccurredAt: occurredAt, Type: ev.Type,
					Context: struct{ Run, Project string }{Run: run.RunID, Project: run.ProjectID},
				}
				if ev.Data != nil {
					_ = json.Unmarshal(ev.Data, &el.Data)
				}
				if el.Data == nil {
					el.Data = make(map[string]any)
				}
				evLikes = append(evLikes, el)
			}
			t := agent.ExtractTrajectory(evLikes)
			return &t, nil
		}
		trajA, errA := extractRun(runA)
		trajB, errB := extractRun(runB)
		if errA != nil {
			writeError(w, 404, fmt.Errorf("run A: %w", errA))
			return
		}
		if errB != nil {
			writeError(w, 404, fmt.Errorf("run B: %w", errB))
			return
		}
		// Build comparison
		comparison := map[string]any{
			"run_a": trajA,
			"run_b": trajB,
			"diff": map[string]any{
				"turns":     trajA.Summary.TotalTurns - trajB.Summary.TotalTurns,
				"tools":     trajA.Summary.TotalTools - trajB.Summary.TotalTools,
				"tokens":    trajA.Summary.TotalTokens - trajB.Summary.TotalTokens,
				"knowledge": trajA.Summary.KnowledgeFormed - trajB.Summary.KnowledgeFormed,
				"failed":    trajA.Summary.FailedTools - trajB.Summary.FailedTools,
				"tools_a":   trajA.Summary.ToolsUsed,
				"tools_b":   trajB.Summary.ToolsUsed,
			},
		}
		writeJSON(w, 200, comparison)
	})

	// Artifact extraction: extract reusable structures from a trajectory.
	mux.HandleFunc("GET /v1/workspace/runs/{runID}/artifacts", func(w http.ResponseWriter, r *http.Request) {
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
		events, _, eErr := ws.StreamRunEvents(r.Context(), run.ProjectID, run.RunID, "", 500)
		if eErr != nil {
			writeError(w, 500, eErr)
			return
		}
		var evLikes []agent.EventLike
		for _, ev := range events {
			var occurredAt time.Time
			if ev.OccurredAt != "" {
				occurredAt, _ = time.Parse(time.RFC3339Nano, ev.OccurredAt)
			}
			el := agent.EventLike{
				EventID: ev.EventID, OccurredAt: occurredAt, Type: ev.Type,
				Context: struct{ Run, Project string }{Run: run.RunID, Project: run.ProjectID},
			}
			if ev.Data != nil {
				_ = json.Unmarshal(ev.Data, &el.Data)
			}
			if el.Data == nil {
				el.Data = make(map[string]any)
			}
			evLikes = append(evLikes, el)
		}
		traj := agent.ExtractTrajectory(evLikes)
		artifacts := agent.ExtractArtifacts(traj)
		writeJSON(w, 200, map[string]any{"artifacts": artifacts, "count": len(artifacts)})
	})

	// Activation chain: trace how a specific knowledge item influenced agent decisions.
	mux.HandleFunc("GET /v1/workspace/knowledge/{knowledgeID}/chain", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		knowledgeID := r.PathValue("knowledgeID")
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 422, errors.New("project query param is required"))
			return
		}
		// Get all completed runs for this project
		runs, err := ws.ListRunsByProject(r.Context(), project)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		// Extract trajectories
		var trajectories []agent.Trajectory
		for _, run := range runs {
			events, _, eErr := ws.StreamRunEvents(r.Context(), run.ProjectID, run.RunID, "", 500)
			if eErr != nil {
				continue
			}
			var evLikes []agent.EventLike
			for _, ev := range events {
				var occurredAt time.Time
				if ev.OccurredAt != "" {
					occurredAt, _ = time.Parse(time.RFC3339Nano, ev.OccurredAt)
				}
				el := agent.EventLike{
					EventID: ev.EventID, OccurredAt: occurredAt, Type: ev.Type,
					Context: struct{ Run, Project string }{Run: run.RunID, Project: run.ProjectID},
				}
				if ev.Data != nil {
					_ = json.Unmarshal(ev.Data, &el.Data)
				}
				if el.Data == nil {
					el.Data = make(map[string]any)
				}
				evLikes = append(evLikes, el)
			}
			trajectories = append(trajectories, agent.ExtractTrajectory(evLikes))
		}
		chain := agent.ExtractActivationChain(knowledgeID, trajectories)
		writeJSON(w, 200, chain)
	})

	// All activation chains for a project.
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/chains", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		projectID := r.PathValue("projectID")
		runs, err := ws.ListRunsByProject(r.Context(), projectID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		var trajectories []agent.Trajectory
		for _, run := range runs {
			events, _, eErr := ws.StreamRunEvents(r.Context(), run.ProjectID, run.RunID, "", 500)
			if eErr != nil {
				continue
			}
			var evLikes []agent.EventLike
			for _, ev := range events {
				var occurredAt time.Time
				if ev.OccurredAt != "" {
					occurredAt, _ = time.Parse(time.RFC3339Nano, ev.OccurredAt)
				}
				el := agent.EventLike{
					EventID: ev.EventID, OccurredAt: occurredAt, Type: ev.Type,
					Context: struct{ Run, Project string }{Run: run.RunID, Project: run.ProjectID},
				}
				if ev.Data != nil {
					_ = json.Unmarshal(ev.Data, &el.Data)
				}
				if el.Data == nil {
					el.Data = make(map[string]any)
				}
				evLikes = append(evLikes, el)
			}
			trajectories = append(trajectories, agent.ExtractTrajectory(evLikes))
		}
		chains := agent.ExtractAllActivationChains(trajectories)
		slog.Info("activation chains", "project", projectID, "runs", len(trajectories), "chains", len(chains))
		writeJSON(w, 200, map[string]any{"chains": chains, "count": len(chains)})
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

	// Triggers
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/triggers", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		triggers, err := ws.ListTriggers(r.Context(), r.PathValue("projectID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"triggers": triggers})
	})
	mux.HandleFunc("POST /v1/workspace/triggers", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req workspace.Trigger
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if req.ID == "" {
			req.ID = slugify(req.Name) + "-" + time.Now().UTC().Format("20060102-150405")
		}
		if req.Type == "" || req.ProjectID == "" {
			writeError(w, 422, errors.New("type and project_id are required"))
			return
		}
		if err := ws.CreateTrigger(r.Context(), &req); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 201, req)
	})
	mux.HandleFunc("GET /v1/workspace/triggers/{triggerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		trigger, err := ws.GetTrigger(r.Context(), r.PathValue("triggerID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, trigger)
	})
	mux.HandleFunc("PUT /v1/workspace/triggers/{triggerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req workspace.Trigger
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		req.ID = r.PathValue("triggerID")
		if err := ws.UpdateTrigger(r.Context(), req); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, req)
	})
	mux.HandleFunc("DELETE /v1/workspace/triggers/{triggerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if err := ws.DeleteTrigger(r.Context(), r.PathValue("triggerID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})

	// Webhook triggers: POST /v1/workspace/webhook/{projectID}/{path}
	// Matches webhook triggers by projectID and config.path.
	mux.HandleFunc("POST /v1/workspace/webhook/{projectID}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		projectID := r.PathValue("projectID")
		path := r.PathValue("path")
		triggers, err := ws.ListTriggers(r.Context(), projectID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		var matched *workspace.Trigger
		for i, t := range triggers {
			if t.Type == "webhook" && t.Enabled {
				var cfg workspace.WebhookConfig
				if json.Unmarshal(t.Config, &cfg) == nil && cfg.Path == path {
					matched = &triggers[i]
					break
				}
			}
		}
		if matched == nil {
			writeError(w, 404, errors.New("no webhook trigger found for this path"))
			return
		}
		var cfg workspace.WebhookConfig
		_ = json.Unmarshal(matched.Config, &cfg)
		// Build prompt from template + request body
		var body map[string]any
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
		prompt := cfg.PromptTemplate
		for k, v := range body {
			prompt = strings.ReplaceAll(prompt, "{{body."+k+"}}", fmt.Sprintf("%v", v))
		}
		// Create task and start run
		taskID := "trigger-" + matched.ID + "-" + time.Now().UTC().Format("20060102-150405")
		task := &workspace.Task{ID: taskID, ProjectID: projectID, AgentID: matched.AgentID, Title: "Webhook: " + matched.Name, Prompt: prompt}
		if err := ws.CreateTask(r.Context(), task); err != nil {
			writeError(w, 500, err)
			return
		}
		input := agent.RunInput{RunID: taskID + "-" + time.Now().UTC().Format("150405"), Project: projectID, TaskID: taskID, Prompt: prompt}
		if matched.AgentID != "" {
			if a, aErr := ws.GetAgent(r.Context(), matched.AgentID); aErr == nil {
				if a.Model != "" {
					input.Model = a.Model
				}
				if a.SystemPrompt != "" {
					input.SystemPrompt = a.SystemPrompt
				}
				if a.NetworkAccess != nil && *a.NetworkAccess {
					input.NetworkAccess = true
				}
			}
		}
		if err := activities.PrepareRun(&input); err != nil {
			writeError(w, 422, err)
			return
		}
		input.ActorID = "webhook-" + matched.ID
		workflowID := workflowIDFor(activities.SourceID, projectID, input.RunID)
		options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		run, wfErr := temporalClient.ExecuteWorkflow(r.Context(), options, "AgentRun", input)
		if wfErr != nil {
			writeError(w, 409, wfErr)
			return
		}
		wsRun := &workspace.Run{ID: input.RunID, TaskID: taskID, ProjectID: projectID, AgentID: matched.AgentID, RunID: input.RunID, Status: "started", Model: input.Model}
		_ = ws.CreateRun(r.Context(), wsRun)
		writeJSON(w, 202, map[string]string{"trigger_id": matched.ID, "run_id": input.RunID, "workflow_id": run.GetID()})
	})

	// Schedule trigger loop: polls all enabled schedule triggers every minute.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		lastRun := map[string]time.Time{} // triggerID → last fire time
		for range ticker.C {
			triggers, err := ws.ListAllTriggers(context.Background())
			if err != nil {
				slog.Error("list triggers", "error", err)
				continue
			}
			now := time.Now().UTC()
			for _, t := range triggers {
				if t.Type != "schedule" || !t.Enabled {
					continue
				}
				var cfg workspace.ScheduleConfig
				if err := json.Unmarshal(t.Config, &cfg); err != nil || cfg.Cron == "" || cfg.Prompt == "" {
					continue
				}
				// Simple cron matching: check if current minute matches
				if !cronMatches(cfg.Cron, now) {
					continue
				}
				// Don't fire twice in the same minute
				if last, ok := lastRun[t.ID]; ok && now.Sub(last) < 55*time.Second {
					continue
				}
				lastRun[t.ID] = now
				// Create task and start run
				taskID := "trigger-" + t.ID + "-" + now.Format("20060102-150405")
				task := &workspace.Task{ID: taskID, ProjectID: t.ProjectID, AgentID: t.AgentID, Title: "Scheduled: " + t.Name, Prompt: cfg.Prompt}
				if err := ws.CreateTask(context.Background(), task); err != nil {
					slog.Error("create trigger task", "trigger", t.ID, "error", err)
					continue
				}
				input := agent.RunInput{RunID: taskID + "-" + now.Format("150405"), Project: t.ProjectID, TaskID: taskID, Prompt: cfg.Prompt}
				if t.AgentID != "" {
					if a, aErr := ws.GetAgent(context.Background(), t.AgentID); aErr == nil {
						if a.Model != "" {
							input.Model = a.Model
						}
						if a.SystemPrompt != "" {
							input.SystemPrompt = a.SystemPrompt
						}
					}
				}
				if err := activities.PrepareRun(&input); err != nil {
					slog.Error("prepare trigger run", "trigger", t.ID, "error", err)
					continue
				}
				input.ActorID = "schedule-" + t.ID
				workflowID := workflowIDFor(activities.SourceID, t.ProjectID, input.RunID)
				options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
				if _, wfErr := temporalClient.ExecuteWorkflow(context.Background(), options, "AgentRun", input); wfErr != nil {
					slog.Error("start trigger run", "trigger", t.ID, "error", wfErr)
					continue
				}
				wsRun := &workspace.Run{ID: input.RunID, TaskID: taskID, ProjectID: t.ProjectID, AgentID: t.AgentID, RunID: input.RunID, Status: "started", Model: input.Model}
				_ = ws.CreateRun(context.Background(), wsRun)
				slog.Info("scheduled trigger fired", "trigger", t.ID, "name", t.Name, "run", input.RunID)
			}
		}
	}()

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

// cronMatches does a simple cron field check. Supports:
//
//	"* * * * *" — every minute
//	"0 9 * * 1-5" — weekdays at 9:00
//	"*/5 * * * *" — every 5 minutes
//
// Format: minute hour dayOfMonth month dayOfWeek (standard 5-field cron).
func cronMatches(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	checks := []struct {
		value  int
		ranges [2]int // min, max
	}{
		{t.Minute(), [2]int{0, 59}},
		{t.Hour(), [2]int{0, 23}},
		{t.Day(), [2]int{1, 31}},
		{int(t.Month()), [2]int{1, 12}},
		{int(t.Weekday()), [2]int{0, 6}},
	}
	for i, field := range fields {
		if field == "*" {
			continue
		}
		if strings.HasPrefix(field, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(field, "*/"))
			if err != nil || step <= 0 {
				return false
			}
			if checks[i].value%step != 0 {
				return false
			}
			continue
		}
		if strings.Contains(field, "-") {
			parts := strings.SplitN(field, "-", 2)
			lo, err1 := strconv.Atoi(parts[0])
			hi, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil || checks[i].value < lo || checks[i].value > hi {
				return false
			}
			continue
		}
		if strings.Contains(field, ",") {
			match := false
			for _, part := range strings.Split(field, ",") {
				if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v == checks[i].value {
					match = true
					break
				}
			}
			if !match {
				return false
			}
			continue
		}
		v, err := strconv.Atoi(field)
		if err != nil || v != checks[i].value {
			return false
		}
	}
	return true
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
