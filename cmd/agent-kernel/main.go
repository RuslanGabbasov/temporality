package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/mcpclient"
	"github.com/temporality-project/temporality/kernel/outbox"
	"github.com/temporality-project/temporality/kernel/priming"
	"github.com/temporality-project/temporality/kernel/quota"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/skills"
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
	if err = ws.Migrate(ctx, "migrations/000028_skills.up.sql"); err != nil {
		log.Error("migrate workspace v28", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000029_mcp_servers.up.sql"); err != nil {
		log.Error("migrate workspace v29", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000030_agent_definition.up.sql"); err != nil {
		log.Error("migrate workspace v30", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000031_run_agent_version.up.sql"); err != nil {
		log.Error("migrate workspace v31", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000032_user_channels.up.sql"); err != nil {
		log.Error("migrate workspace v32", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000033_skills_mcp_global.up.sql"); err != nil {
		log.Error("migrate workspace v33", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000034_org_structure.up.sql"); err != nil {
		log.Error("migrate workspace v34", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000035_project_member.up.sql"); err != nil {
		log.Error("migrate workspace v35", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000036_org_unit_roles.up.sql"); err != nil {
		log.Error("migrate workspace v36", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000037_run_exec_context.up.sql"); err != nil {
		log.Error("migrate workspace v37", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000038_execution_identity.up.sql"); err != nil {
		log.Error("migrate workspace v38", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000039_trigger_org_identity.up.sql"); err != nil {
		log.Error("migrate workspace v39", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000040_human_request.up.sql"); err != nil {
		log.Error("migrate workspace v40", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000041_org_policy.up.sql"); err != nil {
		log.Error("migrate workspace v41", "error", err)
		os.Exit(1)
	}
	// Curated builtin agents are templates, not auto-created agents
	// (docs/evaluable-agent.md §16): the user creates them deliberately from
	// the template gallery. Cleanup removes agents left by the earlier
	// auto-seeding model, so no builtin is forced on an existing workspace.
	cleanupSeededBuiltinAgents(ctx, log, ws)
	activities, err := agent.NewActivities(events)
	if err != nil {
		log.Error("configure activities", "error", err)
		os.Exit(1)
	}
	loadMCPServers(ctx, log, ws, activities.MCP)
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
	temporalWorker.RegisterActivityWithOptions(activities.ResolveAgent, activity.RegisterOptions{Name: agent.ActivityResolveAgent})
	temporalWorker.RegisterActivityWithOptions(activities.GenerateTitle, activity.RegisterOptions{Name: agent.ActivityGenerateTitle})
	temporalWorker.RegisterActivityWithOptions(activities.ExtractKnowledge, activity.RegisterOptions{Name: agent.ActivityExtractKnowledge})
	temporalWorker.RegisterActivityWithOptions(activities.SummarizeChildRun, activity.RegisterOptions{Name: agent.ActivitySummarizeChildRun})
	temporalWorker.RegisterActivityWithOptions(activities.NotifyChannel, activity.RegisterOptions{Name: agent.ActivityNotifyChannel})
	temporalWorker.RegisterActivityWithOptions(activities.CloseHumanRequest, activity.RegisterOptions{Name: agent.ActivityCloseHumanRequest})
	registerExampleWorkflow(temporalWorker)
	workerDone := make(chan error, 1)
	go func() { workerDone <- temporalWorker.Run(worker.InterruptCh()) }()
	gate, err := controlplane.NewGate(os.Getenv("KERNEL_AUTH_TOKENS"))
	if err != nil {
		log.Error("parse KERNEL_AUTH_TOKENS", "error", err)
		os.Exit(1)
	}
	// Workspace-user tokens live in the database and are loaded into a
	// separate reloadable gate token set below (see refreshUserTokens), so
	// the historical "restart the kernel after creating a user" limitation
	// is gone.
	if !gate.Enabled() {
		log.Warn("KERNEL_AUTH_TOKENS is empty: authentication disabled; configure tokens before sharing this instance")
	} else {
		log.Info("kernel authentication enabled", "tokens", gate.TokenCount())
	}
	// Workspace self-access: agent tools (skills, triggers) call this kernel's
	// own workspace API over loopback. A startup-random internal token lets the
	// calls pass the gate without exposing a reusable credential; when the gate
	// is disabled no token is needed.
	address := env("KERNEL_HTTP_ADDR", ":8090")
	if gate.Enabled() {
		internalToken := generateToken()
		if internalGate, igErr := controlplane.NewGate(internalToken + ":kernel-internal:operator:*"); igErr == nil {
			gate = gate.Merge(internalGate)
			activities.WorkspaceToken = internalToken
		}
	}
	// refreshUserTokens reloads workspace-user tokens from the database into
	// the gate. Called at startup and after every user or org mutation, so
	// created, edited, deactivated, deleted and regenerated tokens apply
	// immediately — and org chains stay in sync with tree moves and role
	// grants.
	refreshUserTokens := func() {
		reloadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		users, err := ws.ListUsersWithTokens(reloadCtx)
		if err != nil {
			log.Error("load workspace user tokens", "error", err)
			return
		}
		// One query for the whole tree; user chains resolve from the paths.
		units, err := ws.ListOrgUnits(reloadCtx)
		if err != nil {
			log.Error("load org units for user tokens", "error", err)
			return
		}
		unitByID := make(map[string]workspace.OrgUnit, len(units))
		for _, u := range units {
			unitByID[u.ID] = u
		}
		// Org role grants (docs/org-structure.md §13): one query, grouped per
		// user; each grant acts on its unit and subtree.
		grants, err := ws.ListAllOrgUnitRoleGrants(reloadCtx)
		if err != nil {
			log.Error("load org unit role grants", "error", err)
			return
		}
		grantByUser := map[string][]controlplane.OrgRoleGrant{}
		for _, g := range grants {
			role, rErr := controlplane.ParseRole(g.Role)
			if rErr != nil {
				log.Error("skip org unit role grant: unknown role", "user", g.UserID, "role", g.Role)
				continue
			}
			path := ""
			if unit, ok := unitByID[g.OrgUnitID]; ok {
				path = unit.SelfPath()
			}
			grantByUser[g.UserID] = append(grantByUser[g.UserID], controlplane.OrgRoleGrant{
				UnitID: g.OrgUnitID, Path: path, Role: role,
			})
		}
		principals := make(map[string]controlplane.Principal, len(users))
		for _, u := range users {
			if u.Token == "" || !u.Active {
				continue
			}
			role, err := controlplane.ParseRole(u.Role)
			if err != nil {
				log.Error("skip workspace user token: unknown role", "user", u.Name, "role", u.Role)
				continue
			}
			var visible []string
			if u.OrgUnitID != "" {
				if unit, ok := unitByID[u.OrgUnitID]; ok {
					visible = unit.Ancestors()
				} else {
					log.Error("skip org visibility for user: unknown unit", "user", u.Name, "unit", u.OrgUnitID)
				}
			}
			// Project scope follows the org model (docs/org-structure.md §15):
			// admins and org-unassigned users keep the transition "*"; assigned
			// users get the concrete id list — org intersection plus explicit
			// membership. The legacy user.projects column is no longer consulted.
			projects := []string{"*"}
			if role < controlplane.RoleAdmin && u.OrgUnitID != "" && visible != nil {
				if ids, pErr := ws.VisibleProjectIDs(reloadCtx, u.ID, false, visible); pErr != nil {
					log.Error("resolve visible projects for user", "user", u.Name, "error", pErr)
				} else {
					projects = ids
				}
			}
			principals[u.Token] = controlplane.Principal{
				Subject: u.Name, Role: role, Projects: projects, UserID: u.ID,
				OrgUnitID: u.OrgUnitID, VisibleUnits: visible,
				OrgRoles: grantByUser[u.ID],
			}
		}
		gate.SetDBTokens(principals)
		log.Info("loaded workspace user tokens", "count", len(principals))
	}
	refreshUserTokens()

	if os.Getenv("KERNEL_WORKSPACE_URL") == "" {
		activities.WorkspaceURL = "http://localhost" + address
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
		writeJSON(w, 200, map[string]any{"subject": principal.Subject, "role": principal.Role.String(), "projects": principal.Projects, "auth_enabled": true, "user_id": principal.UserID, "org_unit_id": principal.OrgUnitID})
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
		// Reader passes the flat gate; operation-level authorization
		// (docs/org-structure.md §22) decides below whether this caller may
		// actually start runs in the project with these resources.
		if !gate.Allow(w, r, controlplane.RoleReader, input.Project) {
			return
		}
		resolvedAgentID, resolvedAgent, aErr := resolveRunAgent(r.Context(), ws, input.Project, input.AgentID)
		if aErr != nil {
			if errors.Is(aErr, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("agent not found"))
				return
			}
			writeError(w, 500, aErr)
			return
		}
		if _, err := authorizeRunUse(r.Context(), ws, r, &input, resolvedAgentID, resolvedAgent); err != nil {
			writeError(w, 403, err)
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
			// Route escalations back to the operator who started the run when the
			// agent named no recipient: prefer the DB user behind the token.
			actor := "human-requester"
			if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
				actor = principal.UserID
			} else if token, _ := controlplane.BearerToken(r); token != "" {
				if user, err := ws.GetUserByToken(r.Context(), token); err == nil {
					actor = user.ID
				}
			}
			input.ActorID = actor
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
		// Close the human_request row when this approval answers one
		// (docs/org-structure.md §28): answered for ask_human responses,
		// cancelled when the human declined. Tool approvals carry operation
		// ids that match no row — the update is a no-op then.
		if approval.Response != "" || !approval.Approved {
			closeStatus := "answered"
			if !approval.Approved && approval.Response == "" {
				closeStatus = "cancelled"
			}
			actor := approval.ActorID
			if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
				actor = principal.UserID
			}
			var closeErr error
			response := approval.Response
			if response == "" {
				response = approval.Reason
			}
			if closeStatus == "answered" {
				_, closeErr = ws.AnswerHumanRequest(r.Context(), approval.OperationID, response, actor)
			} else {
				closeErr = ws.CancelHumanRequest(r.Context(), approval.OperationID, actor)
			}
			switch {
			case closeErr == nil:
				auditAction := workspace.AuditHumanAnswered
				if closeStatus == "cancelled" {
					auditAction = workspace.AuditHumanCancelled
				}
				_ = ws.RecordAccessAudit(r.Context(), actor, auditAction, "human_request", approval.OperationID, map[string]any{"run_id": r.PathValue("runID")})
			case errors.Is(closeErr, workspace.ErrNotFound):
			// not a human request — plain tool approval
			default:
				log.Warn("close human request on approval", "operation", approval.OperationID, "error", closeErr)
			}
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
	// visibleUnits returns the org chain a principal sees resources in; nil
	// means unfiltered — admins, service tokens and users not yet assigned to
	// an org unit (docs/org-structure.md §4 gradual migration).
	visibleUnits := func(r *http.Request) []string {
		principal, ok := controlplane.FromContext(r.Context())
		if !ok || principal.Role >= controlplane.RoleAdmin {
			return nil
		}
		return principal.VisibleUnits
	}

	// Org structure CRUD (docs/org-structure.md §5). The tree itself is
	// admin-only to mutate: moving nodes reshapes visibility for everyone.
	mux.HandleFunc("GET /v1/org/units", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		units, err := ws.ListOrgUnits(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"units": units})
	})
	mux.HandleFunc("POST /v1/org/units", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var req workspace.CreateOrgUnitRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, 422, errors.New("name is required"))
			return
		}
		if req.Kind == "" {
			req.Kind = workspace.OrgKindDepartment
		}
		if req.ID == "" {
			req.ID = slugify(req.Name)
		}
		unit := &workspace.OrgUnit{ID: req.ID, ParentID: req.ParentID, Kind: req.Kind, Name: req.Name}
		if err := ws.CreateOrgUnit(r.Context(), unit); err != nil {
			writeError(w, 409, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditOrgUnitCreated, "org_unit", unit.ID, map[string]any{"name": unit.Name, "parent_id": unit.ParentID}); err != nil {
			log.Warn("audit org unit create", "error", err)
		}
		refreshUserTokens()
		writeJSON(w, 201, unit)
	})
	mux.HandleFunc("PUT /v1/org/units/{unitID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var req workspace.CreateOrgUnitRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, 422, errors.New("name is required"))
			return
		}
		if req.Kind == "" {
			req.Kind = workspace.OrgKindDepartment
		}
		unit := workspace.OrgUnit{ID: r.PathValue("unitID"), Kind: req.Kind, Name: req.Name}
		if err := ws.UpdateOrgUnit(r.Context(), unit); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditOrgUnitUpdated, "org_unit", unit.ID, map[string]any{"name": unit.Name, "kind": unit.Kind}); err != nil {
			log.Warn("audit org unit update", "error", err)
		}
		// Optional re-parenting: parent_id present and different triggers a move.
		if req.ParentID != "" {
			current, err := ws.GetOrgUnit(r.Context(), unit.ID)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			if current.ParentID != req.ParentID {
				if err := ws.MoveOrgUnit(r.Context(), unit.ID, req.ParentID); err != nil {
					if errors.Is(err, workspace.ErrCycle) {
						writeError(w, 409, err)
						return
					}
					writeError(w, 500, err)
					return
				}
				if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditOrgUnitMoved, "org_unit", unit.ID, map[string]any{"parent_id": req.ParentID}); err != nil {
					log.Warn("audit org unit move", "error", err)
				}
			}
		}
		updated, err := ws.GetOrgUnit(r.Context(), unit.ID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		refreshUserTokens()
		writeJSON(w, 200, updated)
	})
	mux.HandleFunc("DELETE /v1/org/units/{unitID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		if err := ws.DeleteOrgUnit(r.Context(), r.PathValue("unitID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			if errors.Is(err, workspace.ErrUnitNotEmpty) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditOrgUnitDeleted, "org_unit", r.PathValue("unitID"), nil); err != nil {
			log.Warn("audit org unit delete", "error", err)
		}
		refreshUserTokens()
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("GET /v1/org/units/{unitID}/resources", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		resources, err := ws.ListUnitResources(r.Context(), r.PathValue("unitID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, resources)
	})
	// Effective policy at a unit (docs/org-structure.md §24): installation-wide
	// rows plus everything inherited from the unit's ancestors, merged
	// restrictively. The response carries the contributing rows so the UI can
	// show where each restriction comes from.
	mux.HandleFunc("GET /v1/org/units/{unitID}/effective-policy", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		effective, sources, err := ws.EffectivePolicy(r.Context(), r.PathValue("unitID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"policy": effective, "sources": sources})
	})
	// Org unit role grants (docs/org-structure.md §13): a grant upgrades the
	// grantee's effective role on the unit and its whole subtree. Mutations are
	// admin-only and land in the access audit log.
	mux.HandleFunc("GET /v1/org/units/{unitID}/roles", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		roles, err := ws.ListOrgUnitRoles(r.Context(), r.PathValue("unitID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"roles": roles})
	})
	mux.HandleFunc("PUT /v1/org/units/{unitID}/roles/{userID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var req struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		role, err := controlplane.ParseRole(req.Role)
		if err != nil || role == controlplane.RoleNone {
			writeError(w, 422, errors.New("role must be one of reader, writer, operator, admin"))
			return
		}
		actor := requestUserID(r)
		unitID, userID := r.PathValue("unitID"), r.PathValue("userID")
		if err := ws.SetOrgUnitRole(r.Context(), unitID, userID, role.String(), actor); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), actor, workspace.AuditRoleGranted, "org_unit", unitID, map[string]any{"user_id": userID, "role": role.String()}); err != nil {
			log.Warn("audit role grant", "error", err)
		}
		refreshUserTokens()
		writeJSON(w, 200, map[string]string{"user_id": userID, "org_unit_id": unitID, "role": role.String()})
	})
	mux.HandleFunc("DELETE /v1/org/units/{unitID}/roles/{userID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		unitID, userID := r.PathValue("unitID"), r.PathValue("userID")
		if err := ws.RemoveOrgUnitRole(r.Context(), unitID, userID); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		actor := requestUserID(r)
		if err := ws.RecordAccessAudit(r.Context(), actor, workspace.AuditRoleRevoked, "org_unit", unitID, map[string]any{"user_id": userID}); err != nil {
			log.Warn("audit role revoke", "error", err)
		}
		refreshUserTokens()
		writeJSON(w, 200, map[string]bool{"removed": true})
	})
	mux.HandleFunc("PUT /v1/org/resources/{kind}/{resourceID}/binding", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var req struct {
			OrgUnitID string `json:"org_unit_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := ws.SetResourceOrgUnit(r.Context(), r.PathValue("kind"), r.PathValue("resourceID"), req.OrgUnitID); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 400, err)
			return
		}
		action := workspace.AuditResourceBound
		if req.OrgUnitID == "" {
			action = workspace.AuditResourceUnbound
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), action, "resource", r.PathValue("resourceID"), map[string]any{"kind": r.PathValue("kind"), "org_unit_id": req.OrgUnitID}); err != nil {
			log.Warn("audit resource binding", "error", err)
		}
		writeJSON(w, 200, map[string]string{"kind": r.PathValue("kind"), "resource_id": r.PathValue("resourceID"), "org_unit_id": req.OrgUnitID})
	})
	mux.HandleFunc("DELETE /v1/org/resources/{kind}/{resourceID}/binding", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		if err := ws.SetResourceOrgUnit(r.Context(), r.PathValue("kind"), r.PathValue("resourceID"), ""); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 400, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditResourceUnbound, "resource", r.PathValue("resourceID"), map[string]any{"kind": r.PathValue("kind")}); err != nil {
			log.Warn("audit resource unbinding", "error", err)
		}
		writeJSON(w, 200, map[string]bool{"unbound": true})
	})
	// Access audit (docs/org-structure.md §36): the recent access-change
	// journal, admins only.
	mux.HandleFunc("GET /v1/org/audit", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		events, err := ws.ListAccessAudit(r.Context(), limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"events": events})
	})

	// Workspace CRUD endpoints
	mux.HandleFunc("GET /v1/workspace/projects", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		// Get user from token for project filtering
		var userID string
		var isAdmin bool
		units := visibleUnits(r)
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
		projects, err := ws.ListProjectsForUser(r.Context(), userID, isAdmin, units)
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
		project := &workspace.Project{ID: req.ID, Name: req.Name, Description: req.Description, DefaultAgentID: req.DefaultAgentID, DefaultModel: req.DefaultModel, AllowedUsers: req.AllowedUsers, OrgUnitIDs: req.OrgUnits}
		if err := ws.CreateProject(r.Context(), project); err != nil {
			writeError(w, 409, err)
			return
		}
		if len(req.OrgUnits) > 0 {
			if err := ws.SetProjectOrgUnits(r.Context(), project.ID, req.OrgUnits); err != nil {
				writeError(w, 400, err)
				return
			}
			// New org links change who sees the project — refresh token scopes.
			refreshUserTokens()
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
		// The legacy allowed_users column is frozen: an absent field keeps the
		// stored value (visibility no longer reads it — docs/org-structure.md §16).
		allowedUsers := req.AllowedUsers
		if allowedUsers == nil {
			if current, err := ws.GetProject(r.Context(), r.PathValue("projectID")); err == nil {
				allowedUsers = current.AllowedUsers
			}
		}
		project := workspace.Project{ID: r.PathValue("projectID"), Name: req.Name, Description: req.Description, DefaultAgentID: req.DefaultAgentID, DefaultModel: req.DefaultModel, AllowedUsers: allowedUsers}
		if err := ws.UpdateProject(r.Context(), project); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// Org links follow the payload when present; an absent field keeps the
		// current bindings so plain name/description edits never reset scope.
		if req.OrgUnits != nil {
			if err := ws.SetProjectOrgUnits(r.Context(), project.ID, req.OrgUnits); err != nil {
				writeError(w, 400, err)
				return
			}
			project.OrgUnitIDs = req.OrgUnits
			// Re-linked units change who sees the project — refresh token scopes.
			refreshUserTokens()
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
		// The deleted project leaves token scopes — refresh.
		refreshUserTokens()
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	// Explicit project membership (docs/org-structure.md §16): a member sees
	// the project even outside its org units. Mutations land in the access
	// audit log.
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/members", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		members, err := ws.ListProjectMembers(r.Context(), r.PathValue("projectID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"members": members})
	})
	mux.HandleFunc("POST /v1/workspace/projects/{projectID}/members", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.UserID) == "" {
			writeError(w, 422, errors.New("user_id is required"))
			return
		}
		if req.Role == "" {
			req.Role = "writer"
		}
		if _, err := controlplane.ParseRole(req.Role); err != nil {
			writeError(w, 422, err)
			return
		}
		actor := requestUserID(r)
		if err := ws.AddProjectMember(r.Context(), r.PathValue("projectID"), req.UserID, req.Role, actor); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), actor, workspace.AuditProjectMemberAdded, "project", r.PathValue("projectID"), map[string]any{"user_id": req.UserID, "role": req.Role}); err != nil {
			log.Warn("audit project member add", "error", err)
		}
		// Membership feeds token project scopes — refresh.
		refreshUserTokens()
		writeJSON(w, 201, map[string]bool{"added": true})
	})
	mux.HandleFunc("DELETE /v1/workspace/projects/{projectID}/members/{userID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		if err := ws.RemoveProjectMember(r.Context(), r.PathValue("projectID"), r.PathValue("userID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		actor := requestUserID(r)
		if err := ws.RecordAccessAudit(r.Context(), actor, workspace.AuditProjectMemberRemoved, "project", r.PathValue("projectID"), map[string]any{"user_id": r.PathValue("userID")}); err != nil {
			log.Warn("audit project member remove", "error", err)
		}
		// Membership feeds token project scopes — refresh.
		refreshUserTokens()
		writeJSON(w, 200, map[string]bool{"removed": true})
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
		agents, err := ws.ListAgentsVisible(r.Context(), visibleUnits(r))
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
			Skills: req.Skills, MCPServers: req.MCPServers, Tools: req.Tools,
			SandboxProfile: req.SandboxProfile, NetworkAccess: req.NetworkAccess, ReadOnly: req.ReadOnly,
			MaxTurns: req.MaxTurns, ApprovalMode: req.ApprovalMode, Labels: req.Labels,
			Definition: req.Definition, OrgUnitID: req.OrgUnitID,
		}
		if a.Definition != nil {
			a.DefinitionVersion = 1
		}
		if err := ws.CreateAgent(r.Context(), a); err != nil {
			writeError(w, 409, err)
			return
		}
		if a.Definition != nil {
			writeAgentVersion(r.Context(), ws, *a, requestSubject(r), req.PromptSource, req.GeneratorModel)
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
		if !workspace.OrgVisible(a.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
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
			Skills: req.Skills, MCPServers: req.MCPServers, Tools: req.Tools,
			SandboxProfile: req.SandboxProfile, NetworkAccess: req.NetworkAccess, ReadOnly: req.ReadOnly,
			MaxTurns: req.MaxTurns, ApprovalMode: req.ApprovalMode, Labels: req.Labels,
			Definition: req.Definition, OrgUnitID: req.OrgUnitID,
		}
		prev, prevErr := ws.GetAgent(r.Context(), a.ID)
		if prevErr != nil {
			if errors.Is(prevErr, workspace.ErrNotFound) {
				writeError(w, 404, prevErr)
				return
			}
			writeError(w, 500, prevErr)
			return
		}
		a.DefinitionVersion = prev.DefinitionVersion
		// Editing configuration must not re-scope an agent: bindings change
		// only through the admin binding endpoint, so the payload value is
		// ignored entirely and the previous binding is preserved.
		a.OrgUnitID = prev.OrgUnitID
		// Referenced resources must be org-visible to the caller: an editor
		// must not smuggle another department's skills or MCP servers into an
		// agent they can write (docs/org-structure.md §8). Dangling references
		// pass silently — run-time resolution degrades the same way.
		if units := visibleUnits(r); units != nil {
			for _, skillID := range a.Skills {
				skill, sErr := ws.GetSkill(r.Context(), skillID)
				if errors.Is(sErr, workspace.ErrNotFound) {
					continue
				}
				if sErr != nil {
					writeError(w, 500, sErr)
					return
				}
				if !workspace.OrgVisible(skill.OrgUnitID, units) {
					writeError(w, 403, fmt.Errorf("skill %q is not available in your org scope", skillID))
					return
				}
			}
			for _, serverID := range a.MCPServers {
				server, sErr := ws.GetMCPServer(r.Context(), serverID)
				if errors.Is(sErr, workspace.ErrNotFound) {
					continue
				}
				if sErr != nil {
					writeError(w, 500, sErr)
					return
				}
				if !workspace.OrgVisible(server.OrgUnitID, units) {
					writeError(w, 403, fmt.Errorf("MCP server %q is not available in your org scope", serverID))
					return
				}
			}
		}
		if agentSemanticsChanged(prev, a) {
			a.DefinitionVersion++
		}
		if err := ws.UpdateAgent(r.Context(), a); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if a.Definition != nil && a.DefinitionVersion != prev.DefinitionVersion {
			writeAgentVersion(r.Context(), ws, a, requestSubject(r), req.PromptSource, req.GeneratorModel)
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
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/versions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		versions, err := ws.ListAgentVersions(r.Context(), r.PathValue("agentID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"versions": versions})
	})
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/runs", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		runs, err := ws.ListRunsByAgent(r.Context(), r.PathValue("agentID"), 200)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"runs": runs})
	})
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/prompt", func(w http.ResponseWriter, r *http.Request) {
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
		source, prompt := agentPromptPreview(a)
		writeJSON(w, 200, map[string]any{"agent_id": a.ID, "version": a.DefinitionVersion, "source": source, "prompt": prompt})
	})
	// Delegation target resolution (docs/agent-delegation.md §4): returns the
	// enforced run configuration for launching this agent inside another run.
	// Writer role: the response is everything needed to start a run.
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/run-config", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleWriter, project) {
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
		// Agents are global or project-scoped; delegation must respect scope.
		if a.ProjectID != "" && a.ProjectID != project {
			writeError(w, 404, errors.New("agent not found"))
			return
		}
		input := agent.RunInput{}
		applyAgentConfig(r.Context(), ws, &a, &input)
		applyProjectModel(r.Context(), ws, project, &input)
		// The delegated run inherits the org position of the delegating chain:
		// the parent's actor unit (actor_id is passed by ResolveAgent) plus the
		// project's units. The trigger unit of ancestor runs does not propagate
		// through delegation — recorded limitation of this wave (§24).
		actorUnit := ""
		if actorID := r.URL.Query().Get("actor_id"); actorID != "" {
			if user, uErr := ws.GetUser(r.Context(), actorID); uErr == nil {
				actorUnit = user.OrgUnitID
			}
		}
		if pErr := applyRunPolicy(r.Context(), ws, costPrices, project, actorUnit, "", &input); pErr != nil {
			writeError(w, 403, pErr)
			return
		}
		writeJSON(w, 200, input)
	})
	// Curated builtin templates (docs/evaluable-agent.md §16): source for the
	// create-agent gallery and the “restore builtin” action. Templates are
	// never auto-created as agents.
	mux.HandleFunc("GET /v1/workspace/agents/builtins", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		writeJSON(w, 200, map[string]any{"builtins": workspace.BuiltinAgents()})
	})
	// Agent creation wizard: natural language in, structured definition out.
	// The user never writes a system prompt (docs/evaluable-agent.md §4).
	mux.HandleFunc("POST /v1/workspace/agents/draft", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Description string `json:"description"`
			Final       bool   `json:"final"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Description) == "" {
			writeError(w, 422, errors.New("description is required"))
			return
		}
		draft, err := agent.BuildAgentDraft(r.Context(), activities.Model, req.Description, activities.ToolsSummary(), req.Final)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		writeJSON(w, 200, draft)
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
		task, err := ws.GetTask(r.Context(), r.PathValue("taskID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// Reader passes the flat gate (scoped to the task's project);
		// operation-level authorization (docs/org-structure.md §22) decides
		// below whether this caller may actually start runs with these resources.
		if !gate.Allow(w, r, controlplane.RoleReader, task.ProjectID) {
			return
		}
		var req workspace.StartRunRequest
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
		// Resolve agent: explicit override > task default > project default.
		agentID := req.AgentID
		if agentID == "" {
			agentID = task.AgentID
		}
		agentID, agentCfg, aErr := resolveRunAgent(r.Context(), ws, task.ProjectID, agentID)
		if aErr != nil {
			if errors.Is(aErr, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("agent not found"))
				return
			}
			writeError(w, 500, aErr)
			return
		}
		runID := task.ID + "-" + time.Now().UTC().Format("20060102-150405")
		input := agent.RunInput{
			RunID:   runID,
			Project: task.ProjectID,
			TaskID:  task.ID,
			Prompt:  task.Prompt,
		}
		applyAgentConfig(r.Context(), ws, agentCfg, &input)
		applyProjectModel(r.Context(), ws, task.ProjectID, &input)
		if req.Model != "" {
			input.Model = req.Model
		}
		// Policy (docs/org-structure.md §24): resolved after the model override so
		// the allowlist judges the model the run will actually use. Position = the
		// acting user's unit plus the project's units.
		actorUnit := ""
		if principal, ok := controlplane.FromContext(r.Context()); ok {
			actorUnit = principal.OrgUnitID
		} else if token, _ := controlplane.BearerToken(r); token != "" {
			if user, uErr := ws.GetUserByToken(r.Context(), token); uErr == nil {
				actorUnit = user.OrgUnitID
			}
		}
		if pErr := applyRunPolicy(r.Context(), ws, costPrices, task.ProjectID, actorUnit, "", &input); pErr != nil {
			writeError(w, 403, pErr)
			return
		}
		// Operation-level authorization (docs/org-structure.md §22): project
		// visibility and effective writer role, org visibility of the agent and
		// its resources. Runs after applyAgentConfig so the check sees the fully
		// resolved skill and MCP lists; the returned effective role lands in the
		// execution context snapshot.
		effectiveRole, err := authorizeRunUse(r.Context(), ws, r, &input, agentID, agentCfg)
		if err != nil {
			writeError(w, 403, err)
			return
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
		// Route escalations back to the operator who started the run: prefer
		// the DB user behind the token.
		input.ActorID = "human-requester"
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
			input.ActorID = principal.UserID
		} else if token, _ := controlplane.BearerToken(r); token != "" {
			if user, uErr := ws.GetUserByToken(r.Context(), token); uErr == nil {
				input.ActorID = user.ID
			}
		}
		workflowID := workflowIDFor(activities.SourceID, task.ProjectID, runID)
		options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		run, wfErr := temporalClient.ExecuteWorkflow(r.Context(), options, "AgentRun", input)
		if wfErr != nil {
			writeError(w, 409, wfErr)
			return
		}
		wsRun := &workspace.Run{
			ID: runID, TaskID: task.ID, ProjectID: task.ProjectID,
			AgentID: agentID, AgentVersion: input.AgentVersion, RunID: runID, Status: "started", Model: input.Model,
			ExecContext: runExecContext(r.Context(), &input, agentID, effectiveRole),
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
		providers, err := ws.ListProvidersVisible(r.Context(), visibleUnits(r))
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
		provider := &workspace.Provider{ID: req.ID, Name: req.Name, BaseURL: req.BaseURL, APIKeyRef: req.APIKeyRef, Models: req.Models, Labels: req.Labels, OrgUnitID: req.OrgUnitID}
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
		if !workspace.OrgVisible(provider.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
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
		// Editing config must not silently re-scope a provider: bindings change
		// only through the admin binding endpoint.
		if prev, err := ws.GetProvider(r.Context(), provider.ID); err == nil {
			provider.OrgUnitID = prev.OrgUnitID
		}
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

	// Triggers (docs/org-structure.md §18, §21): org-scoped, optionally bound
	// to an execution identity whose allowed-lists are re-checked at every
	// automated run start.
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/triggers", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		triggers, err := ws.ListTriggers(r.Context(), r.PathValue("projectID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if units := visibleUnits(r); units != nil {
			visible := make([]workspace.Trigger, 0, len(triggers))
			for _, t := range triggers {
				if workspace.OrgVisible(t.OrgUnitID, units) {
					visible = append(visible, t)
				}
			}
			triggers = visible
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
		if req.OrgUnitID != "" {
			if _, err := ws.GetOrgUnit(r.Context(), req.OrgUnitID); err != nil {
				status := 500
				if errors.Is(err, workspace.ErrNotFound) {
					status = 404
				}
				writeError(w, status, err)
				return
			}
		}
		if req.ExecutionIdentityID != "" {
			if _, err := ws.GetExecutionIdentity(r.Context(), req.ExecutionIdentityID); err != nil {
				status := 500
				if errors.Is(err, workspace.ErrNotFound) {
					status = 404
				}
				writeError(w, status, err)
				return
			}
		}
		// Save-time authorization (§21): unit, project, agent, identity.
		if err := authorizeTriggerUse(r.Context(), ws, r, &req); err != nil {
			writeError(w, 403, err)
			return
		}
		if err := ws.CreateTrigger(r.Context(), &req); err != nil {
			writeError(w, 409, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTriggerCreated, "trigger", req.ID, map[string]any{"name": req.Name, "project_id": req.ProjectID, "org_unit_id": req.OrgUnitID, "execution_identity_id": req.ExecutionIdentityID}); err != nil {
			log.Warn("audit trigger create", "error", err)
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
		if !workspace.OrgVisible(trigger.OrgUnitID, visibleUnits(r)) {
			// Hidden triggers read as absent — existence is not leaked.
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		writeJSON(w, 200, trigger)
	})
	mux.HandleFunc("PUT /v1/workspace/triggers/{triggerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		existing, err := ws.GetTrigger(r.Context(), r.PathValue("triggerID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(existing.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		// Partial update: only the keys present in the payload change the
		// trigger — a toggle that sends {enabled} must not wipe name, agent or
		// identity. org_unit_id is ignored here: re-scoping goes through the
		// admin binding endpoint like every other resource.
		var body map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeError(w, 400, err)
			return
		}
		hasString := func(key string) (string, bool) {
			raw, ok := body[key]
			if !ok {
				return "", false
			}
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return "", false
			}
			return s, true
		}
		if v, ok := hasString("name"); ok && v != "" {
			existing.Name = v
		}
		if v, ok := hasString("agent_id"); ok {
			existing.AgentID = v
		}
		if v, ok := hasString("type"); ok && v != "" {
			existing.Type = v
		}
		if v, ok := hasString("execution_identity_id"); ok {
			if v != "" {
				if _, err := ws.GetExecutionIdentity(r.Context(), v); err != nil {
					status := 500
					if errors.Is(err, workspace.ErrNotFound) {
						status = 404
					}
					writeError(w, status, err)
					return
				}
			}
			existing.ExecutionIdentityID = v
		}
		if raw, ok := body["enabled"]; ok {
			_ = json.Unmarshal(raw, &existing.Enabled)
		}
		if raw, ok := body["config"]; ok && string(raw) != "null" {
			var cfg json.RawMessage
			if json.Unmarshal(raw, &cfg) == nil && len(cfg) > 0 {
				existing.Config = cfg
			}
		}
		// Re-authorize the merged trigger (§21): agent/identity may have changed.
		if err := authorizeTriggerUse(r.Context(), ws, r, &existing); err != nil {
			writeError(w, 403, err)
			return
		}
		if err := ws.UpdateTrigger(r.Context(), existing); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTriggerUpdated, "trigger", existing.ID, map[string]any{"name": existing.Name, "agent_id": existing.AgentID, "execution_identity_id": existing.ExecutionIdentityID, "enabled": existing.Enabled}); err != nil {
			log.Warn("audit trigger update", "error", err)
		}
		writeJSON(w, 200, existing)
	})
	mux.HandleFunc("DELETE /v1/workspace/triggers/{triggerID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
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
		if !workspace.OrgVisible(trigger.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
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
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTriggerDeleted, "trigger", trigger.ID, map[string]any{"name": trigger.Name, "project_id": trigger.ProjectID}); err != nil {
			log.Warn("audit trigger delete", "error", err)
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})

	// Skills (Living Skills registry — docs/living-skills.md). Skills are
	// workspace-global: one registry shared by every project.
	mux.HandleFunc("GET /v1/workspace/skills", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		list, err := ws.ListSkillsVisible(r.Context(), visibleUnits(r))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"skills": list, "count": len(list)})
	})
	mux.HandleFunc("POST /v1/workspace/skills", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req skillRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		skill, err := req.toSkill("")
		if err != nil {
			writeError(w, 422, err)
			return
		}
		if skill.ID == "" {
			writeError(w, 422, errors.New("id is required"))
			return
		}
		if err := ws.CreateSkill(r.Context(), &skill); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 201, skill)
	})
	mux.HandleFunc("GET /v1/workspace/skills/{skillID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		skill, err := ws.GetSkill(r.Context(), r.PathValue("skillID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(skill.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		writeJSON(w, 200, skill)
	})
	mux.HandleFunc("PUT /v1/workspace/skills/{skillID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req skillRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		skill, err := req.toSkill(r.PathValue("skillID"))
		if err != nil {
			writeError(w, 422, err)
			return
		}
		// Editing content must not re-scope a skill: bindings change only
		// through the admin binding endpoint, so the payload value is ignored
		// entirely and the previous binding is preserved.
		if prev, err := ws.GetSkill(r.Context(), skill.ID); err == nil {
			skill.OrgUnitID = prev.OrgUnitID
		}
		if err := ws.UpdateSkill(r.Context(), &skill); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, skill)
	})
	mux.HandleFunc("DELETE /v1/workspace/skills/{skillID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if err := ws.DeleteSkill(r.Context(), r.PathValue("skillID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	// Skill creation wizard: natural language in, structured draft out.
	// skill.yaml stays an internal artifact — the user never edits it directly.
	mux.HandleFunc("POST /v1/workspace/skills/draft", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Description string `json:"description"`
			Final       bool   `json:"final"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Description) == "" {
			writeError(w, 422, errors.New("description is required"))
			return
		}
		draft, err := agent.BuildSkillDraft(r.Context(), activities.Model, req.Description, activities.ToolsSummary(), req.Final)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		writeJSON(w, 200, draft)
	})
	mux.HandleFunc("GET /v1/workspace/skills/{skillID}/versions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		versions, err := ws.ListSkillVersions(r.Context(), r.PathValue("skillID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"versions": versions})
	})
	mux.HandleFunc("POST /v1/workspace/skills/{skillID}/validate", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		var req skillRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		_, issues := req.parse()
		writeJSON(w, 200, map[string]any{"valid": len(issues) == 0, "issues": issues})
	})
	mux.HandleFunc("GET /v1/workspace/skills/{skillID}/executions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		limit := 0
		if parsed, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
			limit = parsed
		}
		executions, err := ws.ListSkillExecutions(r.Context(), r.PathValue("skillID"), limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"executions": executions})
	})
	mux.HandleFunc("GET /v1/workspace/skills/{skillID}/memory", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		skill, err := ws.GetSkill(r.Context(), r.PathValue("skillID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		memory, err := skillMemory(r.Context(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), skill.ID)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		writeJSON(w, 200, map[string]any{"memory": memory})
	})

	// MCP servers (workspace-global registry; stdio / sse / http)
	mux.HandleFunc("GET /v1/workspace/mcp-servers", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		list, err := ws.ListMCPServersVisible(r.Context(), visibleUnits(r))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"servers": list, "count": len(list)})
	})
	mux.HandleFunc("POST /v1/workspace/mcp-servers", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req mcpServerRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
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
		server := req.toServer("")
		cfg := mcpConfig(server)
		if err := cfg.Validate(); err != nil {
			writeError(w, 422, err)
			return
		}
		if cfg.Enabled {
			if err := activities.MCP.Apply(r.Context(), cfg); err != nil {
				writeError(w, 422, fmt.Errorf("connect MCP server: %w", err))
				return
			}
		}
		if err := ws.CreateMCPServer(r.Context(), &server); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 201, server)
	})
	mux.HandleFunc("GET /v1/workspace/mcp-servers/{serverID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		server, err := ws.GetMCPServer(r.Context(), r.PathValue("serverID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(server.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		writeJSON(w, 200, server)
	})
	mux.HandleFunc("PUT /v1/workspace/mcp-servers/{serverID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req mcpServerRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		current, err := ws.GetMCPServer(r.Context(), r.PathValue("serverID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		server := req.toServer(current.ID)
		// Editing config must not re-scope a server: bindings change only
		// through the admin binding endpoint, so the payload value is ignored
		// entirely and the previous binding is preserved.
		server.OrgUnitID = current.OrgUnitID
		cfg := mcpConfig(server)
		if err := cfg.Validate(); err != nil {
			writeError(w, 422, err)
			return
		}
		if cfg.Enabled {
			if err := activities.MCP.Apply(r.Context(), cfg); err != nil {
				writeError(w, 422, fmt.Errorf("connect MCP server: %w", err))
				return
			}
		} else {
			activities.MCP.Remove(cfg.ID)
		}
		if err := ws.UpdateMCPServer(r.Context(), &server); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, server)
	})
	mux.HandleFunc("DELETE /v1/workspace/mcp-servers/{serverID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		id := r.PathValue("serverID")
		if err := ws.DeleteMCPServer(r.Context(), id); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		activities.MCP.Remove(id)
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	// Discover: connect with the posted config, list tools, do not persist.
	mux.HandleFunc("POST /v1/workspace/mcp-servers/discover", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req mcpServerRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		cfg := mcpConfig(req.toServer(""))
		if err := cfg.Validate(); err != nil {
			writeError(w, 422, err)
			return
		}
		tools, err := activities.MCP.Discover(r.Context(), cfg)
		if err != nil {
			writeError(w, 422, fmt.Errorf("connect MCP server: %w", err))
			return
		}
		writeJSON(w, 200, map[string]any{"tools": tools})
	})
	// mcp-tools aggregates tools across all registered servers for the agent tools panel.
	mux.HandleFunc("GET /v1/workspace/mcp-tools", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		writeJSON(w, 200, map[string]any{
			"servers":  activities.MCP.ServerTools(),
			"builtins": builtinToolInfo(activities),
		})
	})

	// Webhook triggers: POST /v1/workspace/webhook/{projectID}/{path}
	// Matches webhook triggers by projectID and config.path. The incoming
	// request is normalized into a trigger event (docs/triggers-and-escalations.md §1):
	// id/type/source/actor/payload. Delivery is idempotent by event id — the
	// same source event never creates a second run (§4).
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
		// Normalize the incoming request into the trigger event envelope.
		raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		eventID := firstNonEmpty(r.Header.Get("X-Event-ID"), r.Header.Get("X-Delivery-ID"), bodyString(body, "event_id"), bodyString(body, "id"))
		if eventID == "" {
			// No provider event id: derive one from the payload so retries of the
			// same delivery still deduplicate; timestamped ids stay unique per call.
			digest := sha256.Sum256(raw)
			eventID = "sha256:" + hex.EncodeToString(digest[:])[:16]
		}
		event := map[string]any{
			"id":        eventID,
			"type":      "webhook",
			"source":    "webhook:" + matched.Name,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"actor":     firstNonEmpty(r.Header.Get("X-Actor-ID"), bodyString(body, "actor")),
			"payload":   body,
		}
		// Build prompt from template + normalized event context (§3): the
		// agent never depends on the raw webhook format.
		prompt := cfg.PromptTemplate
		for k, v := range body {
			prompt = strings.ReplaceAll(prompt, "{{body."+k+"}}", fmt.Sprintf("%v", v))
		}
		if encoded, err := json.MarshalIndent(event, "", "  "); err == nil {
			prompt = prompt + "\n\n--- Trigger event ---\n" + string(encoded)
		}
		// Idempotent run identity: deterministic for a (trigger, event) pair.
		key := sha256.Sum256([]byte(matched.ID + "\x00" + eventID))
		taskID := "trigger-" + matched.ID + "-" + hex.EncodeToString(key[:])[:12]
		// Duplicate delivery (§4): the deterministic task id already exists —
		// report the existing run instead of touching the workflow again.
		if _, err := ws.GetTask(r.Context(), taskID); err == nil {
			writeJSON(w, 200, map[string]any{"trigger_id": matched.ID, "run_id": taskID, "duplicate": true})
			return
		}
		input := agent.RunInput{RunID: taskID, Project: projectID, TaskID: taskID, Prompt: prompt}
		effectiveAgentID, triggerAgent, aErr := resolveRunAgent(r.Context(), ws, projectID, matched.AgentID)
		if aErr != nil {
			writeError(w, 500, aErr)
			return
		}
		applyAgentConfig(r.Context(), ws, triggerAgent, &input)
		applyProjectModel(r.Context(), ws, projectID, &input)
		// Policy (§24) applies before fire-time authorization: a run the policy
		// already forbids (model allowlist) is a rejection, not a silent drop.
		policyErr := applyRunPolicy(r.Context(), ws, costPrices, projectID, "", matched.OrgUnitID, &input)
		if err := activities.PrepareRun(&input); err != nil {
			writeError(w, 422, err)
			return
		}
		input.ActorID = "webhook-" + matched.ID
		// Trigger observability (§16): received → accepted/rejected in the run's
		// event scope so the trajectory shows the origin of the run. Event ids
		// live in a dedicated trigger/ namespace: the workflow owns /event/NNNNNN.
		scope := agent.EventScope(activities.SourceID, projectID, input.RunID)
		triggerEventData := map[string]any{"trigger_id": matched.ID, "trigger_name": matched.Name, "event_id": eventID, "source": "webhook"}
		emitTriggerEvent := func(kind, eventIDSuffix string, data map[string]any) {
			item := observation.Event{
				Schema: observation.Schema, EventID: "trigger/" + scope + "/" + eventIDSuffix, OccurredAt: time.Now().UTC(),
				Source:  observation.Source{ID: activities.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
				Context: observation.Context{Project: projectID, Run: input.RunID, Task: taskID, Actor: observation.Actor{ID: input.ActorID, Type: "trigger"}},
				Type:    kind, Data: data,
			}
			if err := events.Enqueue(r.Context(), item); err != nil {
				slog.Error("emit trigger event", "trigger", matched.ID, "error", err)
			}
		}
		emitTriggerEvent("trigger.received", "000000", triggerEventData)
		// Fire-time re-authorization (§21): the execution identity still permits
		// this exact run. A failed check is a rejection, not a silent drop.
		identityID, authErr := authorizeTriggerRun(r.Context(), ws, matched, effectiveAgentID, triggerAgent, &input)
		if authErr == nil {
			authErr = policyErr
		}
		if authErr != nil {
			rejection := map[string]any{"trigger_id": matched.ID, "trigger_name": matched.Name, "event_id": eventID, "source": "webhook", "reason": authErr.Error()}
			emitTriggerEvent("trigger.rejected", "000001", rejection)
			writeError(w, 403, authErr)
			return
		}
		// Task creation comes after authorization: a rejected delivery leaves no
		// orphan task, so a corrected retry is not misreported as a duplicate.
		task := &workspace.Task{ID: taskID, ProjectID: projectID, AgentID: matched.AgentID, Title: "Webhook: " + matched.Name, Prompt: prompt}
		if err := ws.CreateTask(r.Context(), task); err != nil {
			writeError(w, 500, err)
			return
		}
		input.ExecutionIdentityID = identityID
		triggerEventData["execution_identity_id"] = identityID
		emitTriggerEvent("trigger.accepted", "000001", triggerEventData)
		workflowID := workflowIDFor(activities.SourceID, projectID, input.RunID)
		options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		run, wfErr := temporalClient.ExecuteWorkflow(r.Context(), options, "AgentRun", input)
		if wfErr != nil {
			// Duplicate delivery of the same source event: the workflow already
			// exists — report the existing run instead of erroring (§4).
			if strings.Contains(wfErr.Error(), "already running") || strings.Contains(wfErr.Error(), "WorkflowExecutionAlreadyStarted") || isWorkflowExistsError(wfErr) {
				writeJSON(w, 200, map[string]any{"trigger_id": matched.ID, "run_id": input.RunID, "workflow_id": workflowID, "duplicate": true})
				return
			}
			writeError(w, 409, wfErr)
			return
		}
		wsRun := &workspace.Run{ID: input.RunID, TaskID: taskID, ProjectID: projectID, AgentID: effectiveAgentID, AgentVersion: input.AgentVersion, RunID: input.RunID, Status: "started", Model: input.Model, ExecContext: runExecContext(r.Context(), &input, effectiveAgentID, controlplane.RoleNone)}
		wsRun.ExecContext["execution_identity_id"] = identityID
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
				// Task id is timestamped, so every fire is unique; the task itself is
				// created only after fire-time authorization succeeds.
				taskID := "trigger-" + t.ID + "-" + now.Format("20060102-150405")
				input := agent.RunInput{RunID: taskID + "-" + now.Format("150405"), Project: t.ProjectID, TaskID: taskID, Prompt: cfg.Prompt}
				effectiveAgentID, triggerAgent, aErr := resolveRunAgent(context.Background(), ws, t.ProjectID, t.AgentID)
				if aErr != nil {
					slog.Error("resolve trigger agent", "trigger", t.ID, "error", aErr)
					continue
				}
				applyAgentConfig(context.Background(), ws, triggerAgent, &input)
				applyProjectModel(context.Background(), ws, t.ProjectID, &input)
				// Policy (§24): a forbidden run never starts; the fire is logged.
				policyErr := applyRunPolicy(context.Background(), ws, costPrices, t.ProjectID, "", t.OrgUnitID, &input)
				if err := activities.PrepareRun(&input); err != nil {
					slog.Error("prepare trigger run", "trigger", t.ID, "error", err)
					continue
				}
				input.ActorID = "schedule-" + t.ID
				// Fire-time re-authorization (§21): the identity's allowed-lists are
				// re-checked on every scheduled fire; a revoked permission stops
				// future runs instead of silently failing mid-workflow.
				identityID, authErr := authorizeTriggerRun(context.Background(), ws, &t, effectiveAgentID, triggerAgent, &input)
				if authErr == nil {
					authErr = policyErr
				}
				if authErr != nil {
					slog.Warn("scheduled trigger rejected", "trigger", t.ID, "name", t.Name, "error", authErr)
					continue
				}
				task := &workspace.Task{ID: taskID, ProjectID: t.ProjectID, AgentID: t.AgentID, Title: "Scheduled: " + t.Name, Prompt: cfg.Prompt}
				if err := ws.CreateTask(context.Background(), task); err != nil {
					slog.Error("create trigger task", "trigger", t.ID, "error", err)
					continue
				}
				input.ExecutionIdentityID = identityID
				workflowID := workflowIDFor(activities.SourceID, t.ProjectID, input.RunID)
				options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
				if _, wfErr := temporalClient.ExecuteWorkflow(context.Background(), options, "AgentRun", input); wfErr != nil {
					slog.Error("start trigger run", "trigger", t.ID, "error", wfErr)
					continue
				}
				wsRun := &workspace.Run{ID: input.RunID, TaskID: taskID, ProjectID: t.ProjectID, AgentID: effectiveAgentID, AgentVersion: input.AgentVersion, RunID: input.RunID, Status: "started", Model: input.Model, ExecContext: runExecContext(context.Background(), &input, effectiveAgentID, controlplane.RoleNone)}
				wsRun.ExecContext["execution_identity_id"] = identityID
				_ = ws.CreateRun(context.Background(), wsRun)
				slog.Info("scheduled trigger fired", "trigger", t.ID, "name", t.Name, "run", input.RunID)
			}
		}
	}()

	// Execution identities (docs/org-structure.md §20): the security context
	// for automated runs. Storage and admin CRUD only for now — trigger wiring
	// arrives with the triggers wave.
	mux.HandleFunc("GET /v1/workspace/execution-identities", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		identities, err := ws.ListExecutionIdentities(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"identities": identities})
	})
	mux.HandleFunc("POST /v1/workspace/execution-identities", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var e workspace.ExecutionIdentity
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&e); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(e.Name) == "" {
			writeError(w, 422, errors.New("name is required"))
			return
		}
		if e.ID == "" {
			e.ID = "exec-" + shortID()
		}
		if err := ws.CreateExecutionIdentity(r.Context(), &e); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 409, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditExecIdentityCreated, "execution_identity", e.ID, map[string]any{"name": e.Name, "org_unit_id": e.OrgUnitID}); err != nil {
			log.Warn("audit execution identity create", "error", err)
		}
		writeJSON(w, 201, e)
	})
	mux.HandleFunc("PUT /v1/workspace/execution-identities/{identityID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var e workspace.ExecutionIdentity
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&e); err != nil {
			writeError(w, 400, err)
			return
		}
		e.ID = r.PathValue("identityID")
		if err := ws.UpdateExecutionIdentity(r.Context(), e); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditExecIdentityUpdated, "execution_identity", e.ID, map[string]any{"name": e.Name, "org_unit_id": e.OrgUnitID}); err != nil {
			log.Warn("audit execution identity update", "error", err)
		}
		// Answer with the stored row: the store defaults absent allowed-lists.
		stored, gErr := ws.GetExecutionIdentity(r.Context(), e.ID)
		if gErr != nil {
			writeJSON(w, 200, e)
			return
		}
		writeJSON(w, 200, stored)
	})
	mux.HandleFunc("DELETE /v1/workspace/execution-identities/{identityID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		if err := ws.DeleteExecutionIdentity(r.Context(), r.PathValue("identityID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditExecIdentityDeleted, "execution_identity", r.PathValue("identityID"), nil); err != nil {
			log.Warn("audit execution identity delete", "error", err)
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})

	// Policies (docs/org-structure.md §24): org-bound constraints that inherit
	// top-down and merge restrictively. Admin-managed; readers need the list for
	// the Org UI and effective-policy inspection.
	mux.HandleFunc("GET /v1/workspace/policies", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		policies, err := ws.ListPolicies(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"policies": policies})
	})
	mux.HandleFunc("POST /v1/workspace/policies", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var p workspace.Policy
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			writeError(w, 400, err)
			return
		}
		p.ID = ""
		if err := workspace.ValidatePolicy(p); err != nil {
			writeError(w, 422, err)
			return
		}
		p.ID = "policy-" + shortID()
		if err := ws.CreatePolicy(r.Context(), &p); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 409, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditPolicyCreated, "policy", p.ID, map[string]any{"name": p.Name, "org_unit_id": p.OrgUnitID}); err != nil {
			log.Warn("audit policy create", "error", err)
		}
		writeJSON(w, 201, p)
	})
	mux.HandleFunc("PUT /v1/workspace/policies/{policyID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var p workspace.Policy
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			writeError(w, 400, err)
			return
		}
		p.ID = r.PathValue("policyID")
		if err := ws.UpdatePolicy(r.Context(), p); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditPolicyUpdated, "policy", p.ID, map[string]any{"name": p.Name, "org_unit_id": p.OrgUnitID}); err != nil {
			log.Warn("audit policy update", "error", err)
		}
		stored, gErr := ws.GetPolicy(r.Context(), p.ID)
		if gErr != nil {
			writeJSON(w, 200, p)
			return
		}
		writeJSON(w, 200, stored)
	})
	mux.HandleFunc("DELETE /v1/workspace/policies/{policyID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		if err := ws.DeletePolicy(r.Context(), r.PathValue("policyID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditPolicyDeleted, "policy", r.PathValue("policyID"), nil); err != nil {
			log.Warn("audit policy delete", "error", err)
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})

	// Human requests (docs/org-structure.md §28, §37). Created and closed by
	// kernel activities through the internal token; the list is the UI inbox
	// backed by a real entity, not by event-stream reconstruction.
	mux.HandleFunc("GET /v1/workspace/human-requests", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		// Lazy expiry sweep (§33): rows whose wait ended without the close
		// activity still landing (kernel restart, activity failure).
		if expired, err := ws.ExpireHumanRequests(r.Context()); err != nil {
			log.Warn("sweep human requests", "error", err)
		} else if len(expired) > 0 {
			for _, id := range expired {
				_ = ws.RecordAccessAudit(r.Context(), "system", workspace.AuditHumanExpired, "human_request", id, nil)
			}
		}
		requests, err := ws.ListHumanRequests(r.Context(), r.URL.Query().Get("project"), r.URL.Query().Get("status"), r.URL.Query().Get("user"), r.URL.Query().Get("open") == "1")
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"requests": requests})
	})
	mux.HandleFunc("POST /v1/workspace/human-requests", func(w http.ResponseWriter, r *http.Request) {
		// Upsert from the notification activity (internal token): creates the
		// pending row and records resolution + delivery outcome in one call.
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req workspace.HumanRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if req.ID == "" || req.RunID == "" || req.ProjectID == "" || req.Question == "" {
			writeError(w, 400, errors.New("id, run_id, project_id and question are required"))
			return
		}
		if req.Status == "" {
			req.Status = workspace.HumanStatusPending
		}
		if err := ws.CreateHumanRequest(r.Context(), &req); err != nil {
			writeError(w, 500, err)
			return
		}
		if req.Status == workspace.HumanStatusDelivered {
			if err := ws.DeliverHumanRequest(r.Context(), req.ID, req.ResolvedUser, req.Status, req.Channel, ""); err != nil && !errors.Is(err, workspace.ErrNotFound) {
				log.Warn("mark human request delivered", "id", req.ID, "error", err)
			}
		}
		action := workspace.AuditHumanCreated
		if req.Status == workspace.HumanStatusDelivered {
			action = workspace.AuditHumanDelivered
		}
		if req.Status == workspace.HumanStatusRejected {
			action = workspace.AuditHumanRejected
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), action, "human_request", req.ID, map[string]any{"run_id": req.RunID, "project_id": req.ProjectID, "recipient": req.Recipient, "resolved_user": req.ResolvedUser, "channel": req.Channel}); err != nil {
			log.Warn("audit human request create", "error", err)
		}
		writeJSON(w, 201, &req)
	})
	mux.HandleFunc("POST /v1/workspace/human-requests/{requestID}/close", func(w http.ResponseWriter, r *http.Request) {
		// Close from the workflow activity after the wait resolved. The HTTP
		// approval path (below) closes answered/cancelled rows directly.
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var body struct {
			Status   string `json:"status"`
			Response string `json:"response"`
			ActorID  string `json:"actor_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeError(w, 400, err)
			return
		}
		id := r.PathValue("requestID")
		var err error
		switch body.Status {
		case "answered":
			_, err = ws.AnswerHumanRequest(r.Context(), id, body.Response, body.ActorID)
			if err == nil {
				err = ws.RecordAccessAudit(r.Context(), body.ActorID, workspace.AuditHumanAnswered, "human_request", id, map[string]any{"response": body.Response})
			}
		case "cancelled":
			err = ws.CancelHumanRequest(r.Context(), id, body.ActorID)
			if err == nil {
				err = ws.RecordAccessAudit(r.Context(), body.ActorID, workspace.AuditHumanCancelled, "human_request", id, nil)
			}
		case "expired":
			_, err = ws.ExpireHumanRequestByID(r.Context(), id)
			if err == nil {
				err = ws.RecordAccessAudit(r.Context(), "system", workspace.AuditHumanExpired, "human_request", id, nil)
			}
		default:
			writeError(w, 422, errors.New("status must be answered, cancelled or expired"))
			return
		}
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				// Unknown id: a tool approval reusing the endpoint, not a human
				// request — nothing to close.
				writeJSON(w, 200, map[string]bool{"closed": false})
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"closed": true})
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
	mux.HandleFunc("GET /v1/workspace/users/{userID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		user, err := ws.GetUser(r.Context(), r.PathValue("userID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		user.HasToken = user.Token != ""
		user.Token = "" // never expose bearer tokens on reads
		writeJSON(w, 200, user)
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
		if err := workspace.ValidateChannels(req.Channels); err != nil {
			writeError(w, 422, err)
			return
		}
		orgUnitID := ""
		if req.OrgUnitID != nil {
			orgUnitID = *req.OrgUnitID
		}
		user := &workspace.User{ID: req.ID, Name: req.Name, Email: req.Email, Role: req.Role, Token: token, Projects: req.Projects, OrgUnitID: orgUnitID, Active: active, Channels: req.Channels, PreferredChannel: req.PreferredChannel}
		if err := ws.CreateUser(r.Context(), user); err != nil {
			writeError(w, 500, err)
			return
		}
		refreshUserTokens()
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
		// Channels are profile-level self-service state (see the dedicated
		// /channels endpoint): an admin editing roles must not wipe them.
		existing, err := ws.GetUser(r.Context(), r.PathValue("userID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// nil org_unit_id keeps the assignment: plain role/name edits must not
		// silently relax a user's scope back to "sees everything".
		orgUnitID := existing.OrgUnitID
		if req.OrgUnitID != nil {
			orgUnitID = *req.OrgUnitID
		}
		user := workspace.User{ID: r.PathValue("userID"), Name: req.Name, Email: req.Email, Role: req.Role, Projects: req.Projects, OrgUnitID: orgUnitID, Active: active, Channels: existing.Channels, PreferredChannel: existing.PreferredChannel}
		if err := ws.UpdateUser(r.Context(), user); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// A scope change reshapes what the user sees — audit it (§36).
		if orgUnitID != existing.OrgUnitID {
			if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditUserOrgUnitChanged, "user", user.ID, map[string]any{"from": existing.OrgUnitID, "to": orgUnitID}); err != nil {
				log.Warn("audit user org unit change", "error", err)
			}
		}
		refreshUserTokens()
		writeJSON(w, 200, user)
	})
	// Self-service communication channels: a user manages their own delivery
	// transports (docs/triggers-and-escalations.md §6). Admins may edit any
	// user's channels; everyone else only their own record.
	mux.HandleFunc("PUT /v1/workspace/users/{userID}/channels", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		principal, authenticated := controlplane.FromContext(r.Context())
		userID := r.PathValue("userID")
		if authenticated && principal.Role < controlplane.RoleAdmin && principal.UserID != userID && principal.Subject != userID {
			writeError(w, 403, errors.New("channels can only be edited by their owner or an admin"))
			return
		}
		var req struct {
			Channels         []workspace.UserChannel `json:"channels"`
			PreferredChannel string                  `json:"preferred_channel"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := workspace.ValidateChannels(req.Channels); err != nil {
			writeError(w, 422, err)
			return
		}
		preferred := strings.TrimSpace(req.PreferredChannel)
		if preferred != "" && preferred != "web" {
			found := false
			for _, channel := range req.Channels {
				if channel.Type == preferred && channel.Enabled {
					found = true
					break
				}
			}
			if !found {
				writeError(w, 422, errors.New("preferred channel must be web or an enabled configured channel"))
				return
			}
		}
		if err := ws.UpdateUserChannels(r.Context(), userID, req.Channels, preferred); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		user, err := ws.GetUser(r.Context(), userID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		user.HasToken = user.Token != ""
		user.Token = "" // never expose bearer tokens on reads
		writeJSON(w, 200, user)
	})
	// Regenerate a user's bearer token server-side. The new token takes
	// effect immediately and the old one is revoked at once.
	mux.HandleFunc("POST /v1/workspace/users/{userID}/token", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		token := generateToken()
		if err := ws.UpdateUserToken(r.Context(), r.PathValue("userID"), token); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		refreshUserTokens()
		writeJSON(w, 200, map[string]any{"id": r.PathValue("userID"), "token": token})
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
		refreshUserTokens()
		writeJSON(w, 200, map[string]any{"deleted": true})
	})

	registerExampleRoutes(mux, temporalClient, taskQueue, activities, gate)
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
	return agent.WorkflowID(sourceID, project, runID)
}

// firstNonEmpty returns the first non-blank value (webhook event ids / actors
// can arrive in headers or in the payload).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// bodyString reads a top-level string field from a decoded webhook payload.
func bodyString(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return strings.TrimSpace(value)
}

// isWorkflowExistsError reports whether ExecuteWorkflow failed because the
// workflow id is already running or already completed (duplicate delivery).
func isWorkflowExistsError(err error) bool {
	var exists interface{ Error() string }
	if errors.As(err, &exists) {
		msg := exists.Error()
		return strings.Contains(msg, "workflow execution already started") ||
			strings.Contains(msg, "WorkflowExecutionAlreadyStartedFailure") ||
			strings.Contains(msg, "already exists")
	}
	return false
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

// resolveAgentSkills loads the agent's skills, builds compact digests for the
// system prompt and records the execution → skill version linkage (immutable:
// later skill edits never re-bind this run, docs/living-skills.md §39).
func resolveAgentSkills(ctx context.Context, ws *workspace.Store, a *workspace.Agent, input *agent.RunInput) {
	if a == nil || len(a.Skills) == 0 {
		return
	}
	// Skills are workspace-global (docs/living-skills.md): resolve them from
	// the shared registry; the run's project only lands in the execution
	// provenance record, never in skill resolution.
	all, _ := ws.ListAllSkills(ctx)
	byID := make(map[string]workspace.Skill, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	for _, id := range a.Skills {
		s, ok := byID[id]
		if !ok {
			continue
		}
		manifest, err := skills.ParseManifest(string(s.Manifest))
		if err != nil {
			// Stored manifests are parsed-JSON; YAML accepts JSON, so this only
			// fails on corrupt rows — degrade to digest without contract fields.
			manifest = skills.Manifest{Name: s.Name, Version: s.Version}
		}
		if manifest.Name == "" {
			manifest.Name = s.Name
		}
		if manifest.Version == "" {
			manifest.Version = s.Version
		}
		input.Skills = append(input.Skills, agent.SkillRef{
			ID: s.ID, Version: s.Version, Name: s.Name,
			Digest: skills.Digest(s.Name, s.Version, s.Markdown, manifest, 1500),
		})
		_ = ws.RecordSkillExecution(ctx, &workspace.SkillExecution{
			SkillID: s.ID, SkillVersion: s.Version, ProjectID: input.Project,
			RunID: input.RunID, AgentID: a.ID, StartedAt: time.Now().UTC(),
		})
	}
}

// resolveAgentMCP binds the agent's MCP servers and tool allowlist to the run
// input. Empty MCPServers keeps the legacy env-configured server; empty Tools
// allows every advertised tool.
func resolveAgentMCP(a *workspace.Agent, input *agent.RunInput) {
	if a == nil {
		return
	}
	input.MCPServers = a.MCPServers
	input.ToolAllowlist = a.Tools
}

// resolveRunAgent resolves the agent for a run: an explicit agent id wins,
// otherwise the project's default agent is used. It returns the effective
// agent id and its config; an empty id with nil config means a legacy
// bare-model run. A missing project default agent degrades to a bare run
// instead of failing, while a missing explicit agent stays an error so
// callers learn about bad references immediately.
func resolveRunAgent(ctx context.Context, ws *workspace.Store, projectID, agentID string) (string, *workspace.Agent, error) {
	explicit := agentID != ""
	if !explicit {
		proj, err := ws.GetProject(ctx, projectID)
		if err != nil {
			return "", nil, nil
		}
		agentID = proj.DefaultAgentID
	}
	if agentID == "" {
		return "", nil, nil
	}
	a, err := ws.GetAgent(ctx, agentID)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) && !explicit {
			return "", nil, nil
		}
		return "", nil, err
	}
	return agentID, &a, nil
}

// authorizeRunUse enforces operation-level authorization (docs/org-structure.md
// §22): the project must be visible to the caller (org intersection or
// explicit membership) with an effective role of at least writer, and the
// resolved agent plus every skill and MCP server the run touches must be
// org-visible. Admins and service tokens (VisibleUnits == nil) skip the check
// — the runtime must not depend on one person's rights — and the flat token
// gate has already run before this. The 403 answers name the exact reason.
// It returns the effective role the run was authorized under so the execution
// context snapshot can record it (§35).
func authorizeRunUse(ctx context.Context, ws *workspace.Store, r *http.Request, input *agent.RunInput, agentID string, agentCfg *workspace.Agent) (controlplane.Role, error) {
	principal, ok := controlplane.FromContext(r.Context())
	if !ok || principal.Role >= controlplane.RoleAdmin || principal.VisibleUnits == nil {
		return controlplane.RoleAdmin, nil
	}
	// Project: org visibility or explicit membership (§15-16).
	effective, err := projectAccessRole(ctx, ws, &principal, input.Project)
	if err != nil {
		return controlplane.RoleNone, err
	}
	if effective < controlplane.RoleWriter {
		return controlplane.RoleNone, fmt.Errorf("effective role %s is not enough to start runs in project %q (writer required)", effective, input.Project)
	}
	// Agent: org visibility plus an effective writer role at its binding.
	if agentCfg != nil && agentCfg.OrgUnitID != "" {
		if !workspace.OrgVisible(agentCfg.OrgUnitID, principal.VisibleUnits) {
			return controlplane.RoleNone, fmt.Errorf("agent %q is not available in your org scope", agentCfg.ID)
		}
		if unit, unitErr := ws.GetOrgUnit(ctx, agentCfg.OrgUnitID); unitErr == nil {
			if at := principal.MaxRoleAt(unit.ID, unit.Path); at < controlplane.RoleWriter {
				return controlplane.RoleNone, fmt.Errorf("effective role %s is not enough to run agent %q (writer required)", at, agentCfg.ID)
			}
		}
	}
	// Skills and MCP servers referenced by the run must be org-visible.
	// Unknown references degrade silently here exactly as resolveAgentSkills
	// and the MCP registry treat them at run time.
	for _, skillID := range input.Skills {
		skill, sErr := ws.GetSkill(ctx, skillID.ID)
		if sErr != nil {
			if errors.Is(sErr, workspace.ErrNotFound) {
				continue
			}
			return controlplane.RoleNone, sErr
		}
		if !workspace.OrgVisible(skill.OrgUnitID, principal.VisibleUnits) {
			return controlplane.RoleNone, fmt.Errorf("skill %q is not available in your org scope", skillID.ID)
		}
	}
	for _, serverID := range input.MCPServers {
		server, sErr := ws.GetMCPServer(ctx, serverID)
		if sErr != nil {
			if errors.Is(sErr, workspace.ErrNotFound) {
				continue
			}
			return controlplane.RoleNone, sErr
		}
		if !workspace.OrgVisible(server.OrgUnitID, principal.VisibleUnits) {
			return controlplane.RoleNone, fmt.Errorf("MCP server %q is not available in your org scope", serverID)
		}
	}
	return effective, nil
}

// projectAccessRole resolves the caller's effective role at a project
// (§13, §15-16): installation role, explicit membership role and org grants on
// the project's units or their ancestors — the maximum wins. It fails when
// the project is not visible at all.
func projectAccessRole(ctx context.Context, ws *workspace.Store, principal *controlplane.Principal, projectID string) (controlplane.Role, error) {
	links, err := ws.ProjectOrgUnits(ctx, projectID)
	if err != nil {
		return controlplane.RoleNone, err
	}
	memberRole := controlplane.RoleNone
	members, err := ws.ListProjectMembers(ctx, projectID)
	if err != nil {
		return controlplane.RoleNone, err
	}
	for _, m := range members {
		if m.UserID != principal.UserID {
			continue
		}
		if role, pErr := controlplane.ParseRole(m.Role); pErr == nil && role > memberRole {
			memberRole = role
		}
	}
	if !workspace.ProjectVisible(links, principal.VisibleUnits, memberRole > controlplane.RoleNone) {
		return controlplane.RoleNone, fmt.Errorf("token %q has no access to project %q", principal.Subject, projectID)
	}
	effective := principal.Role
	if memberRole > effective {
		effective = memberRole
	}
	for _, unitID := range links {
		unit, unitErr := ws.GetOrgUnit(ctx, unitID)
		if unitErr != nil {
			continue // a broken link must not crash the gate
		}
		if at := principal.MaxRoleAt(unit.ID, unit.Path); at > effective {
			effective = at
		}
	}
	return effective, nil
}

// authorizeTriggerUse enforces trigger-save authorization (docs/org-structure.md
// §21): the caller needs writer at the trigger's org unit, access to the
// project, the right to use the agent and the right to choose the execution
// identity — which itself must permit the agent and the project. Admins and
// service tokens skip the checks, exactly as authorizeRunUse does.
func authorizeTriggerUse(ctx context.Context, ws *workspace.Store, r *http.Request, t *workspace.Trigger) error {
	principal, ok := controlplane.FromContext(r.Context())
	if !ok || principal.Role >= controlplane.RoleAdmin || principal.VisibleUnits == nil {
		return nil
	}
	if t.OrgUnitID != "" {
		unit, err := ws.GetOrgUnit(ctx, t.OrgUnitID)
		if err != nil {
			return fmt.Errorf("org unit %q not found", t.OrgUnitID)
		}
		if at := principal.MaxRoleAt(unit.ID, unit.Path); at < controlplane.RoleWriter {
			return fmt.Errorf("effective role %s is not enough to manage triggers in unit %q (writer required)", at, unit.Name)
		}
	}
	effective, err := projectAccessRole(ctx, ws, &principal, t.ProjectID)
	if err != nil {
		return err
	}
	if effective < controlplane.RoleWriter {
		return fmt.Errorf("effective role %s is not enough to manage triggers in project %q (writer required)", effective, t.ProjectID)
	}
	if t.AgentID != "" {
		agent, err := ws.GetAgent(ctx, t.AgentID)
		if err != nil {
			return fmt.Errorf("agent %q not found", t.AgentID)
		}
		if !workspace.OrgVisible(agent.OrgUnitID, principal.VisibleUnits) {
			return fmt.Errorf("agent %q is not available in your org scope", agent.ID)
		}
		if agent.OrgUnitID != "" {
			if unit, unitErr := ws.GetOrgUnit(ctx, agent.OrgUnitID); unitErr == nil {
				if at := principal.MaxRoleAt(unit.ID, unit.Path); at < controlplane.RoleWriter {
					return fmt.Errorf("effective role %s is not enough to use agent %q (writer required)", at, agent.ID)
				}
			}
		}
	}
	if t.ExecutionIdentityID != "" {
		identity, err := ws.GetExecutionIdentity(ctx, t.ExecutionIdentityID)
		if err != nil {
			return fmt.Errorf("execution identity %q not found", t.ExecutionIdentityID)
		}
		if !workspace.OrgVisible(identity.OrgUnitID, principal.VisibleUnits) {
			return fmt.Errorf("execution identity %q is not available in your org scope", identity.ID)
		}
		if err := workspace.IdentityAllows(identity, t.AgentID, t.ProjectID, nil, ""); err != nil {
			return err
		}
	}
	return nil
}

// authorizeTriggerRun re-checks an automated run at every fire (§21): the
// execution identity still exists, still permits the agent, project, MCP
// servers and provider of this specific run, and the agent and MCP servers
// are still visible in the identity's org scope. The returned identity id is
// recorded in the run's execution context snapshot (§35).
func authorizeTriggerRun(ctx context.Context, ws *workspace.Store, t *workspace.Trigger, agentID string, triggerAgent *workspace.Agent, input *agent.RunInput) (string, error) {
	if t.ExecutionIdentityID == "" {
		// Legacy trigger without an identity (pre-wave-D): keeps running as
		// before — identities are opt-in per trigger.
		return "", nil
	}
	identity, err := ws.GetExecutionIdentity(ctx, t.ExecutionIdentityID)
	if err != nil {
		return "", fmt.Errorf("execution identity %q no longer exists", t.ExecutionIdentityID)
	}
	provider := ""
	if triggerAgent != nil {
		provider = triggerAgent.Provider
	}
	if err := workspace.IdentityAllows(identity, agentID, t.ProjectID, input.MCPServers, provider); err != nil {
		return "", err
	}
	// Org visibility re-check under the identity's scope: what the run touches
	// must still be visible from the identity's unit.
	chain, err := ws.UnitChain(ctx, identity.OrgUnitID)
	if err != nil {
		return "", err
	}
	if agentID != "" {
		agent, aErr := ws.GetAgent(ctx, agentID)
		if aErr == nil && !workspace.OrgVisible(agent.OrgUnitID, chain) {
			return "", fmt.Errorf("agent %q is not visible in the org scope of execution identity %q", agentID, identity.ID)
		}
	}
	for _, serverID := range input.MCPServers {
		server, sErr := ws.GetMCPServer(ctx, serverID)
		if sErr == nil && !workspace.OrgVisible(server.OrgUnitID, chain) {
			return "", fmt.Errorf("MCP server %q is not visible in the org scope of execution identity %q", serverID, identity.ID)
		}
	}
	return identity.ID, nil
}

// runExecContext snapshots the authorization state at run start
// (docs/org-structure.md §34-35): which agent and resources the run touches
// and who authorized it under which effective role. Ids only — the snapshot
// must survive later org-structure changes without resurrecting whole
// resource copies. authorizedAs is the effective role authorizeRunUse
// returned; RoleNone falls back to the flat principal role.
func runExecContext(ctx context.Context, input *agent.RunInput, agentID string, authorizedAs controlplane.Role) map[string]any {
	skillIDs := make([]string, 0, len(input.Skills))
	for _, s := range input.Skills {
		skillIDs = append(skillIDs, s.ID)
	}
	mcpIDs := input.MCPServers
	if mcpIDs == nil {
		mcpIDs = []string{}
	}
	authorizedBy := map[string]any{"user_id": input.ActorID, "role": "", "org_unit_id": ""}
	if principal, ok := controlplane.FromContext(ctx); ok {
		who := principal.UserID
		if who == "" {
			who = principal.Subject
		}
		role := authorizedAs
		if role == controlplane.RoleNone {
			role = principal.Role
		}
		authorizedBy = map[string]any{"user_id": who, "role": role.String(), "org_unit_id": principal.OrgUnitID}
	}
	return map[string]any{
		"agent_id": agentID, "agent_version": input.AgentVersion,
		"skill_ids": skillIDs, "mcp_ids": mcpIDs,
		"model": input.Model, "project_id": input.Project,
		"actor": input.ActorID, "execution_identity_id": "",
		"authorized_by": authorizedBy,
	}
}

// cleanupSeededBuiltinAgents removes agents created by the earlier
// auto-seeding model (per-project copies first, then global seeds) — both
// carry the builtin provenance label. Builtins ship as templates now; users
// create them explicitly, so anything still labelled builtin is a leftover.
// Project defaults pointing at a removed agent are cleared first. Best-effort.
func cleanupSeededBuiltinAgents(ctx context.Context, log *slog.Logger, ws *workspace.Store) {
	agents, err := ws.ListAllAgents(ctx)
	if err != nil {
		log.Error("cleanup seeded builtin agents", "error", err)
		return
	}
	var seeded []workspace.Agent
	for _, a := range agents {
		if a.Labels["builtin"] == "true" {
			seeded = append(seeded, a)
		}
	}
	if len(seeded) == 0 {
		return
	}
	removed := map[string]bool{}
	for _, a := range seeded {
		removed[a.ID] = true
	}
	projects, err := ws.ListProjects(ctx)
	if err != nil {
		log.Error("cleanup seeded builtin agents", "error", err)
		return
	}
	for _, p := range projects {
		if p.DefaultAgentID == "" || !removed[p.DefaultAgentID] {
			continue
		}
		p.DefaultAgentID = ""
		if err := ws.UpdateProject(ctx, p); err != nil {
			log.Error("clear project default agent", "project", p.ID, "error", err)
		}
	}
	for _, a := range seeded {
		if err := ws.DeleteAgent(ctx, a.ID); err != nil {
			log.Error("delete seeded builtin agent", "agent", a.ID, "error", err)
		}
	}
	log.Info("removed auto-seeded builtin agents", "count", len(seeded))
}

// requestSubject returns the authenticated token subject for version history.
func requestSubject(r *http.Request) string {
	if principal, ok := controlplane.FromContext(r.Context()); ok {
		return principal.Subject
	}
	return ""
}

// requestUserID resolves the audit actor: the workspace user id when the
// token belongs to one, otherwise the principal subject.
func requestUserID(r *http.Request) string {
	if principal, ok := controlplane.FromContext(r.Context()); ok {
		if principal.UserID != "" {
			return principal.UserID
		}
		return principal.Subject
	}
	return ""
}

// agentSemanticsChanged reports whether the compiled prompt inputs changed:
// the definition itself or the purpose text that feeds the identity section
// (docs/plan-evaluable-agent.md §2 этап 3).
func agentSemanticsChanged(prev, next workspace.Agent) bool {
	if prev.Description != next.Description {
		return true
	}
	prevDef, _ := json.Marshal(prev.Definition)
	nextDef, _ := json.Marshal(next.Definition)
	return !bytes.Equal(prevDef, nextDef)
}

// writeAgentVersion stores the immutable definition snapshot with the
// effective prompt compiled by the same deterministic function runs use
// (docs/plan-evaluable-agent.md, decision 2).
func writeAgentVersion(ctx context.Context, ws *workspace.Store, a workspace.Agent, author, promptSource, generatorModel string) {
	if promptSource == "" {
		promptSource = "manual"
	}
	_ = ws.InsertAgentVersion(ctx, workspace.AgentVersion{
		AgentID: a.ID, Version: a.DefinitionVersion, Definition: *a.Definition,
		Description: a.Description,
		CompiledPrompt: agent.EffectiveSystemPrompt(agent.AgentPromptInput{
			Name: a.Name, Description: a.Description, Definition: *a.Definition, Sandbox: a.SandboxProfile,
		}, "", a.SystemPrompt),
		PromptSource: promptSource, GeneratorModel: generatorModel,
		Author: author, CreatedAt: time.Now().UTC(),
	})
}

// agentPromptPreview returns the effective system prompt shown in the UI:
// override > compiled definition > stored manual prompt (legacy).
func agentPromptPreview(a workspace.Agent) (source, prompt string) {
	if a.Definition != nil {
		if a.Definition.PromptOverride != "" {
			return "override", a.Definition.PromptOverride
		}
		return "definition", agent.CompileAgentPrompt(agent.AgentPromptInput{
			Name: a.Name, Description: a.Description, Definition: *a.Definition, Sandbox: a.SandboxProfile,
		})
	}
	if a.SystemPrompt != "" {
		return "legacy", a.SystemPrompt
	}
	return "role", ""
}

// applyAgentConfig copies the resolved agent's settings onto the run input.
// Structured definitions compile the system prompt from semantic components
// and enforce capabilities by removing the corresponding tools and access
// (docs/evaluable-agent.md §6; plan §4.1 — enforcement is hard, never
// prompt-only).
func applyAgentConfig(ctx context.Context, ws *workspace.Store, a *workspace.Agent, input *agent.RunInput) {
	if a == nil {
		return
	}
	if a.Model != "" {
		input.Model = a.Model
	}
	if a.SystemPrompt != "" {
		input.SystemPrompt = a.SystemPrompt
	}
	if a.NetworkAccess != nil && *a.NetworkAccess {
		input.NetworkAccess = true
	}
	input.AgentID = a.ID
	input.AgentVersion = a.DefinitionVersion
	useSkills := true
	if a.Definition != nil {
		input.SystemPrompt = agent.EffectiveSystemPrompt(agent.AgentPromptInput{
			Name: a.Name, Description: a.Description, Definition: *a.Definition, Sandbox: a.SandboxProfile,
		}, input.Role, a.SystemPrompt)
		caps := a.Definition.Capabilities
		if !caps.Cap(caps.ModifyFiles) {
			input.ReadOnly = true
		}
		if !caps.Cap(caps.RunCommands) {
			input.DenyTools = append(input.DenyTools, "run_command")
		}
		if !caps.Cap(caps.Network) {
			input.NetworkAccess = false
		}
		useSkills = caps.Cap(caps.Skills)
		if !caps.Cap(caps.Knowledge) {
			input.SkipKnowledge = true
		}
	}
	// Delegation is an explicitly granted capability (docs/agent-delegation.md):
	// nil or false denies the delegate tool, and agents without a structured
	// definition never get it by default.
	if a.Definition == nil || a.Definition.Capabilities.Delegation == nil || !*a.Definition.Capabilities.Delegation {
		input.DenyTools = append(input.DenyTools, "delegate")
	}
	if a.ReadOnly != nil && *a.ReadOnly {
		input.ReadOnly = true
	}
	if useSkills {
		resolveAgentSkills(ctx, ws, a, input)
	}
	resolveAgentMCP(a, input)
}

// applyProjectModel fills input.Model from the project default model when the
// agent did not carry one.
func applyProjectModel(ctx context.Context, ws *workspace.Store, projectID string, input *agent.RunInput) {
	if input.Model != "" {
		return
	}
	proj, err := ws.GetProject(ctx, projectID)
	if err != nil {
		return
	}
	if proj.DefaultModel != "" {
		input.Model = proj.DefaultModel
	}
}

// applyRunPolicy resolves the effective policy for a run's org position
// (docs/org-structure.md §24) and enforces it on the run input: the project's
// units, the acting user's unit (manual runs) and the trigger's unit
// (automated runs) each expand to their ancestor chain and merge
// restrictively. A model outside the allowlist rejects the run; disallowed
// MCP servers drop out; deny/read_only/tools modes clamp the input; caps and
// the model's prices land in the workflow-enforced budget fields.
func applyRunPolicy(ctx context.Context, ws *workspace.Store, prices cost.Config, projectID, actorUnitID, triggerUnitID string, input *agent.RunInput) error {
	units := []string{actorUnitID, triggerUnitID}
	if projectID != "" {
		links, err := ws.ProjectOrgUnits(ctx, projectID)
		if err != nil {
			return err
		}
		units = append(units, links...)
	}
	effective, _, err := ws.EffectivePolicy(ctx, units...)
	if err != nil {
		return err
	}
	// An empty model means the kernel default (TEMPORALITY_MODEL_ID): the policy
	// must judge the model the run will actually use.
	model := input.Model
	if model == "" {
		model = strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID"))
	}
	if effective.AllowedModels != nil && model != "" && !workspace.PolicyAllowsList(effective.AllowedModels, model) {
		return fmt.Errorf("model %q is not allowed by policy in this org scope", model)
	}
	if effective.AllowedMCP != nil {
		kept := make([]string, 0, len(input.MCPServers))
		for _, serverID := range input.MCPServers {
			if workspace.PolicyAllowsList(effective.AllowedMCP, serverID) {
				kept = append(kept, serverID)
			}
		}
		input.MCPServers = kept
	}
	if effective.NetworkMode == "deny" {
		input.NetworkAccess = false
	}
	if effective.SandboxMode == "read_only" {
		input.ReadOnly = true
	}
	if effective.ApprovalMode == "tools" {
		input.RequireToolApproval = true
	}
	if effective.MaxTokens != nil {
		input.TokenBudget = *effective.MaxTokens
	}
	if effective.MaxBudgetUSD != nil {
		input.MaxBudgetUSD = *effective.MaxBudgetUSD
		if price, ok := prices.Price(model); ok {
			input.ModelPromptPricePer1k = price.PromptPer1k
			input.ModelCompPricePer1k = price.CompPer1k
		}
	}
	if effective.TimeoutSeconds != nil {
		input.TimeoutSeconds = *effective.TimeoutSeconds
	}
	return nil
}

// mcpConfig converts a stored workspace.MCPServer into a registry config.
func mcpConfig(m workspace.MCPServer) mcpclient.ServerConfig {
	return mcpclient.ServerConfig{
		ID: m.ID, Name: m.Name, Type: m.Type,
		Command: m.Command, Args: m.Args, Env: m.Env,
		URL: m.URL, Headers: m.Headers,
		Allow: m.AllowedTools, Approval: m.ApprovalTools,
		Enabled: m.Enabled,
	}
}

// mcpServerRequest is the API payload for creating/updating MCP servers.
type mcpServerRequest struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	URL           string            `json:"url"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           []string          `json:"env"`
	Headers       map[string]string `json:"headers"`
	AllowedTools  []string          `json:"allowed_tools"`
	ApprovalTools []string          `json:"approval_tools"`
	Enabled       *bool             `json:"enabled"`
	OrgUnitID     string            `json:"org_unit_id"` // only set on create; updates keep the stored binding
}

func (req mcpServerRequest) toServer(id string) workspace.MCPServer {
	serverID := id
	if serverID == "" {
		serverID = req.ID
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return workspace.MCPServer{
		ID: serverID, Name: req.Name, Type: strings.ToLower(strings.TrimSpace(req.Type)),
		URL: strings.TrimSpace(req.URL), Command: strings.TrimSpace(req.Command),
		Args: req.Args, Env: req.Env, Headers: req.Headers,
		AllowedTools: req.AllowedTools, ApprovalTools: req.ApprovalTools,
		Enabled: enabled, OrgUnitID: req.OrgUnitID,
	}
}

// builtinToolInfo lists kernel tools (plus run_command when the sandbox is
// configured) in the registry ToolInfo shape, so the agent tools panel shows
// the exact tool surface the kernel would advertise.
func builtinToolInfo(a *agent.Activities) []mcpclient.ToolInfo {
	defs := a.BuiltinToolDefs()
	result := make([]mcpclient.ToolInfo, 0, len(defs))
	for _, def := range defs {
		result = append(result, mcpclient.ToolInfo{Name: def.Name, Description: def.Description, ModelName: def.Name})
	}
	return result
}

// loadMCPServers applies every stored MCP server to the registry and starts a
// periodic refresher so restarted servers and changed tool sets stay current.
func loadMCPServers(ctx context.Context, log *slog.Logger, ws *workspace.Store, registry *mcpclient.Registry) {
	apply := func() {
		servers, err := ws.ListAllMCPServers(context.Background())
		if err != nil {
			log.Error("list mcp servers", "error", err)
			return
		}
		for _, server := range servers {
			if err := registry.Apply(context.Background(), mcpConfig(server)); err != nil {
				log.Error("apply mcp server", "server", server.ID, "error", err)
			}
		}
	}
	apply()
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				apply()
			}
		}
	}()
}

// skillRequest is the API shape for skill create/update: manifest arrives as
// YAML text (skill.yaml) and is stored parsed.
type skillRequest struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Version      string `json:"version"`
	Markdown     string `json:"markdown"`
	ManifestYAML string `json:"manifest_yaml"`
	OrgUnitID    string `json:"org_unit_id"` // only set on create; updates keep the stored binding
}

// parse decodes the manifest, falling back to legacy inference from SKILL.md
// when no skill.yaml is provided (docs/living-skills.md §40).
func (req skillRequest) parse() (skills.Manifest, []skills.Issue) {
	manifest, err := skills.ParseManifest(req.ManifestYAML)
	if err != nil {
		return manifest, []skills.Issue{{Field: "manifest_yaml", Message: err.Error()}}
	}
	if manifest.ID == "" && manifest.Name == "" {
		manifest = skills.InferFromMarkdown(req.Markdown)
	}
	if manifest.ID == "" {
		manifest.ID = slugify(req.Name)
	}
	if manifest.Name == "" {
		manifest.Name = req.Name
	}
	if manifest.Description == "" {
		manifest.Description = req.Description
	}
	if manifest.Version == "" {
		manifest.Version = "1.0.0"
	}
	return manifest, manifest.Validate()
}

func (req skillRequest) toSkill(skillID string) (workspace.Skill, error) {
	manifest, issues := req.parse()
	if len(issues) > 0 {
		messages := make([]string, 0, len(issues))
		for _, issue := range issues {
			messages = append(messages, issue.Field+": "+issue.Message)
		}
		return workspace.Skill{}, errors.New("invalid skill manifest: " + strings.Join(messages, "; "))
	}
	id := req.ID
	if skillID != "" {
		id = skillID
	}
	name := req.Name
	if name == "" {
		name = manifest.Name
	}
	version := req.Version
	if version == "" {
		version = manifest.Version
	}
	return workspace.Skill{
		ID: id, Name: name, Description: req.Description,
		Version: version, Markdown: req.Markdown, Manifest: skills.MarshalJSONForStorage(manifest),
		OrgUnitID: req.OrgUnitID,
	}, nil
}

// skillMemory returns knowledge linked to a skill: knowledge.proposed events
// carrying data.skill_id, enriched with current state from the knowledge
// projection. Skills are workspace-global, so events are scanned without a
// project filter and states are fetched for every project the skill ran in.
func skillMemory(ctx context.Context, observationURL, apiToken, skillID string) ([]map[string]any, error) {
	query := func(path string) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, observationURL+path, nil)
		if err != nil {
			return nil, err
		}
		if apiToken != "" {
			request.Header.Set("Authorization", "Bearer "+apiToken)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if err != nil {
			return nil, err
		}
		if response.StatusCode != 200 {
			return nil, fmt.Errorf("journal %s: %s", path, strings.TrimSpace(string(body)))
		}
		return body, nil
	}
	// Current states from the knowledge projection, per project the matched
	// events belong to (the endpoint requires a project).
	states := map[string]string{}
	fetchStates := func(project string) {
		if project == "" {
			return
		}
		body, err := query("/v1/observations/knowledge?project=" + url.QueryEscape(project))
		if err != nil {
			return
		}
		var projection struct {
			Knowledge []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"knowledge"`
		}
		if json.Unmarshal(body, &projection) == nil {
			for _, item := range projection.Knowledge {
				states[item.ID] = item.State
			}
		}
	}
	stateProjects := map[string]bool{}
	var result []map[string]any
	cursor := ""
	for {
		path := "/v1/observations/events?type=knowledge.proposed&limit=500"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		body, err := query(path)
		if err != nil {
			return nil, err
		}
		var page struct {
			Events []struct {
				EventID    string `json:"event_id"`
				OccurredAt string `json:"occurred_at"`
				Context    struct {
					Run     string `json:"run"`
					Project string `json:"project"`
				} `json:"context"`
				Data map[string]any `json:"data"`
			} `json:"events"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		for _, event := range page.Events {
			if id, _ := event.Data["skill_id"].(string); id != skillID {
				continue
			}
			if !stateProjects[event.Context.Project] {
				stateProjects[event.Context.Project] = true
				fetchStates(event.Context.Project)
			}
			knowledgeID, _ := event.Data["knowledge_id"].(string)
			item := map[string]any{
				"knowledge_id": knowledgeID,
				"proposition":  event.Data["proposition"],
				"capability":   event.Data["capability"],
				"run_id":       event.Context.Run,
				"project":      event.Context.Project,
				"event_id":     event.EventID,
				"occurred_at":  event.OccurredAt,
			}
			if state, ok := states[knowledgeID]; ok {
				item["state"] = state
			} else {
				item["state"] = "proposed"
			}
			result = append(result, item)
		}
		if page.NextCursor == "" || len(result) >= 500 {
			break
		}
		cursor = page.NextCursor
	}
	return result, nil
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
