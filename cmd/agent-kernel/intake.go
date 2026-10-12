package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/quota"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// Intake pipeline (docs/inbound-messages.md): initiative channel messages
// become runs. The system project "intake" holds the intake runs; the intake
// agent (create-if-absent bootstrap, admin-tunable afterwards) carries the
// classification/clarification prompt and the intake tool surface.

// IntakeProjectID is the system project every intake run executes in. It has
// no org-unit links, so org-assigned users never see it in project lists —
// only admins and the service itself (docs/inbound-messages.md §4.2).
const IntakeProjectID = "intake"

// IntakeAgentID is the create-if-absent system agent for intake runs.
const IntakeAgentID = "intake"

// inboundExpiryTTL bounds how long a message may sit in received/clarifying
// before the lazy sweep expires it (docs/inbound-messages.md §6.1): longer
// than any reasonable clarification dialog, shorter than forever.
const inboundExpiryTTL = 48 * time.Hour

// intakeSystemPrompt instructs the classification/clarification dialog
// (docs/inbound-messages.md §4.3, §6). Tunable: the row is created once and
// never overwritten, so an admin may refine it in the agent editor.
const intakeSystemPrompt = `You are the intake agent of Temporality. A user sent an initiative message through a messaging channel; your job is to turn it into a correctly routed task — or to close it politely without creating work.

Always follow this order:

1. Classify the message first (call intake_decide exactly once, before anything else):
   - task: the user asks for something to be done;
   - amendment: it adds to or changes an already running task from this dialog;
   - status: it asks about progress of earlier work;
   - cancel: it asks to stop earlier work;
   - chatter: thanks, greetings, anything that must not create work.

2. For a task, resolve the target project:
   - search_projects finds projects visible to the sender (pass the name you see in the message);
   - exactly one plausible match: use it; confirm with the user via ask_human only when the message named no project at all;
   - several matches or none: ask_human with the candidate list as options; never guess silently;
   - questions go to the sender (the default recipient) with short options; timeout_policy cancel.

3. Then call start_task with a self-contained title and prompt: the executing agent sees only what you write. Translate natural instructions into explicit steps and checkpoints (e.g. "after approval from <person> pass the result to <team>"). Preserve the original wording of the request inside the prompt.

4. For status: call run_status and summarize the sender's tasks (status and latest answer per run) — never start new work for a progress question. For cancel: list the sender's runs via run_status when unclear which one is meant, then cancel_run with the exact run_id; confirm with the user via ask_human only when several runs are plausible. For chatter: a short friendly reply, no run.

Rules:
- The message body in the prompt is the only source of user intent; never invent project names, people or deadlines.
- Start at most one task per message.
- Answer in the language of the user's message.
- After start_task succeeds, tell the user which project the task landed in and its title, briefly.`

// intakeTools is the closed tool surface of the intake agent: classification,
// search, clarification, task creation and the sender's own run lifecycle.
// No sandbox, no delegation, no generic tools — the intake dialog never needs
// them.
var intakeTools = []string{
	"ask_human", "human_contacts", "intake_decide",
	"search_projects", "search_org_units", "start_task",
	"run_status", "cancel_run",
}

// bootstrapIntake creates the system project and agent once per installation.
// Never overwrites: admin edits to the intake agent survive restarts.
func bootstrapIntake(ctx context.Context, log *slog.Logger, ws *workspace.Store) {
	if _, err := ws.GetProject(ctx, IntakeProjectID); err != nil {
		if !isNotFound(err) {
			log.Warn("intake project lookup", "error", err)
			return
		}
		project := &workspace.Project{ID: IntakeProjectID, Name: "Intake (system)", Description: "System project for channel-initiated message processing (docs/inbound-messages.md). Not visible to regular users."}
		if err := ws.CreateProject(ctx, project); err != nil {
			log.Warn("intake project create", "error", err)
			return
		}
		log.Info("intake project created", "project", IntakeProjectID)
	}
	if _, err := ws.GetAgent(ctx, IntakeAgentID); err == nil {
		return
	} else if !isNotFound(err) {
		log.Warn("intake agent lookup", "error", err)
		return
	}
	maxTurns := 12
	readOnly := true
	network := false
	intakeAgent := &workspace.Agent{
		ID: IntakeAgentID, ProjectID: IntakeProjectID,
		Name:          "Intake",
		Description:   "Turns initiative channel messages into routed tasks: classification, clarification, project resolution (docs/inbound-messages.md).",
		SystemPrompt:  intakeSystemPrompt,
		Tools:         intakeTools,
		MaxTurns:      &maxTurns,
		ReadOnly:      &readOnly,
		NetworkAccess: &network,
		Labels:        map[string]string{"system": "intake"},
	}
	if err := ws.CreateAgent(ctx, intakeAgent); err != nil {
		log.Warn("intake agent create", "error", err)
		return
	}
	log.Info("intake agent created", "agent", IntakeAgentID)
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

// intakeRunner starts intake runs in the system project (docs §4.2, §10).
type intakeRunner struct {
	log        *slog.Logger
	ws         *workspace.Store
	temporal   client.Client
	taskQueue  string
	sourceID   string
	activities *agent.Activities
	costPrices cost.Config
}

// start resolves the intake agent, applies its config and starts the AgentRun
// workflow. ActorID = the identified sender, so ask_human inside the dialog
// addresses them by default and the audit chain attributes the spawned task
// to the human (docs §10).
func (r *intakeRunner) start(ctx context.Context, message agent.InboundMessage, senderUser workspace.User) (string, error) {
	agentID, agentCfg, err := resolveRunAgent(ctx, r.ws, IntakeProjectID, IntakeAgentID)
	if err != nil {
		return "", fmt.Errorf("resolve intake agent: %w", err)
	}
	runID := intakeRunID(message.ID)
	input := agent.RunInput{
		RunID:   runID,
		Project: IntakeProjectID,
		Prompt:  intakePrompt(message, senderUser),
		ActorID: senderUser.ID,
	}
	if agentCfg != nil {
		applyAgentConfig(ctx, r.ws, agentCfg, &input)
	}
	applyProjectModel(ctx, r.ws, IntakeProjectID, &input)
	// The intake project carries no org links and no policy in MVP; applying
	// with the sender's unit keeps a future unit policy honest from day one.
	if pErr := applyRunPolicy(ctx, r.ws, r.costPrices, IntakeProjectID, senderUser.OrgUnitID, "", &input); pErr != nil {
		return "", pErr
	}
	if err := r.activities.PrepareRun(&input); err != nil {
		return "", err
	}
	options := client.StartWorkflowOptions{
		ID:                    workflowIDFor(r.sourceID, IntakeProjectID, runID),
		TaskQueue:             r.taskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}
	if _, err := r.temporal.ExecuteWorkflow(ctx, options, "AgentRun", input); err != nil {
		return "", err
	}
	_ = r.ws.CreateRun(ctx, &workspace.Run{
		ID: runID, ProjectID: IntakeProjectID, AgentID: agentID,
		AgentVersion: input.AgentVersion, RunID: runID, Status: "started", Model: input.Model,
		ExecContext: map[string]any{
			"intake":  true,
			"message": workspace.InboundMessageID(message.Channel, message.ID),
			"sender":  senderUser.ID,
			"channel": message.Channel,
		},
	})
	return runID, nil
}

// intakeRunID derives the deterministic intake run id from the message: the
// same channel message replayed after a crash maps to the same workflow id
// and REJECT_DUPLICATE stops the second start (docs/inbound-messages.md §10).
func intakeRunID(messageID string) string {
	slug := slugify(translit(messageID))
	if slug == "" || slug == "untitled" {
		slug = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return "intake-" + slug
}

// intakePrompt renders the run prompt from the message and the sender
// position. The intake agent sees exactly what the user wrote plus who they
// are — nothing else is inferred here.
func intakePrompt(message agent.InboundMessage, sender workspace.User) string {
	var builder strings.Builder
	builder.WriteString("An initiative message arrived through the " + message.Channel + " channel and must be turned into a task (or closed politely).\n\n")
	builder.WriteString("Sender: " + sender.Name + " (user id " + sender.ID + ")\n")
	if sender.OrgUnitID != "" {
		builder.WriteString("Sender org unit: " + sender.OrgUnitID + "\n")
	}
	builder.WriteString("\nMessage (verbatim):\n---\n" + strings.TrimSpace(message.Text) + "\n---\n")
	if len(message.Files) > 0 {
		names := make([]string, 0, len(message.Files))
		for _, file := range message.Files {
			names = append(names, file.Name)
		}
		builder.WriteString("\nThe message carried attachments: " + strings.Join(names, ", ") + ".\n")
	}
	builder.WriteString("\nClassify it (intake_decide), resolve the project if it is a task (search_projects, ask_human to clarify), then start_task — or close it per your instructions.")
	return builder.String()
}

// intakeEventEnqueuer journals intake.* events (docs/inbound-messages.md §11)
// into the system intake project scope. Events are observability, not state:
// the workspace row is the state machine.
func intakeEventEnqueuer(enqueue func(context.Context, observation.Event) error, sourceID string) func(context.Context, string, agent.InboundMessage, string, map[string]any) {
	return func(ctx context.Context, eventType string, message agent.InboundMessage, senderUserID string, data map[string]any) {
		if data == nil {
			data = map[string]any{}
		}
		data["message_id"] = message.ID
		data["channel"] = message.Channel
		data["sender"] = message.Sender
		data["room"] = message.ChatID
		if senderUserID != "" {
			data["sender_user_id"] = senderUserID
		}
		event := observation.Event{
			Schema: observation.Schema, EventID: eventType + "/" + message.Channel + "/" + message.ID,
			OccurredAt: time.Now().UTC(),
			Source:     observation.Source{ID: sourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
			Context:    observation.Context{Project: IntakeProjectID, Actor: observation.Actor{ID: senderUserID, Type: "human"}},
			Type:       eventType, Data: data,
		}
		if err := enqueue(ctx, event); err != nil {
			// Losing an intake event degrades observability, not correctness.
			_ = err
		}
	}
}

// senderUser loads the workspace user behind the closed-world sender match.
func senderUser(ctx context.Context, ws *workspace.Store, userID string) (workspace.User, error) {
	user, err := ws.GetUser(ctx, userID)
	if err != nil {
		return user, err
	}
	if !user.Active {
		return user, fmt.Errorf("user %q is not active", userID)
	}
	return user, nil
}

// intakeEnabledFromEnv reads the pipeline switch: the loop records and
// rejects messages even when disabled, but never starts runs.
func intakeEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KERNEL_INTAKE_ENABLED"))) {
	case "false", "0", "off", "no":
		return false
	}
	return true
}

// registerIntakeRoutes exposes the internal intake endpoints the intake
// agent's tools call loopback with the kernel token (docs/inbound-messages.md
// §7, §9). The flat gate keeps them kernel-internal (operator); the identity
// for every authorization decision comes from the message row — the sender —
// never from the calling token, so a task lands with the sender's rights.
func registerIntakeRoutes(mux *http.ServeMux, gate *controlplane.Gate, r *intakeRunner, quotas *quota.Limiter, emit intakeEmitFunc) {
	// Status of the sender's earlier work (docs/inbound-messages.md §4.2):
	// every routed message with its target run. run_status behind the intake
	// dialog — "what happened to my estimate?" must not spawn a new run.
	mux.HandleFunc("GET /v1/intake/runs", func(w http.ResponseWriter, req *http.Request) {
		if !gate.Allow(w, req, controlplane.RoleOperator) {
			return
		}
		sender := strings.TrimSpace(req.URL.Query().Get("sender"))
		if sender == "" {
			writeError(w, 422, errors.New("sender is required"))
			return
		}
		limit := 0
		if raw := req.URL.Query().Get("limit"); raw != "" {
			if parsed, pErr := strconv.Atoi(raw); pErr == nil {
				limit = parsed
			}
		}
		messages, err := r.ws.ListRoutedInboundMessages(req.Context(), sender, limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		entries := make([]map[string]any, 0, len(messages))
		for _, row := range messages {
			entry := map[string]any{
				"message_id": row.ID,
				"run_id":     row.TargetRunID,
				"project_id": row.ProjectID,
				"task_id":    intakeTaskID(row),
				"asked_at":   row.CreatedAt,
			}
			if run, gErr := r.ws.GetRun(req.Context(), row.TargetRunID); gErr == nil {
				entry["run_status"] = run.Status
				if run.Answer != "" {
					entry["answer"] = truncateForLog(run.Answer, 500)
				}
				if run.Error != "" {
					entry["error"] = truncateForLog(run.Error, 300)
				}
			} else {
				entry["run_status"] = "unknown"
			}
			entries = append(entries, entry)
		}
		writeJSON(w, 200, map[string]any{"runs": entries})
	})
	// Cancel a run the sender's message produced (docs/inbound-messages.md §10):
	// the target run must belong to one of the sender's routed messages — the
	// channel may cancel own work, not somebody else's.
	mux.HandleFunc("POST /v1/intake/runs/{runID}/cancel", func(w http.ResponseWriter, req *http.Request) {
		if !gate.Allow(w, req, controlplane.RoleOperator) {
			return
		}
		var body struct {
			RunID string `json:"run_id"`
		}
		if err := decodeJSON(w, req, &body); err != nil {
			writeError(w, 400, err)
			return
		}
		targetRun := strings.TrimSpace(body.RunID)
		if targetRun == "" {
			writeError(w, 422, errors.New("run_id is required"))
			return
		}
		row, err := r.ws.GetInboundMessageByIntakeRun(req.Context(), req.PathValue("runID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("no inbound message for this run"))
				return
			}
			writeError(w, 500, err)
			return
		}
		if row.SenderUserID == "" {
			writeError(w, 409, errors.New("message has no identified sender"))
			return
		}
		// Ownership: the run must be what one of the sender's messages produced.
		owned, err := r.ws.ListRoutedInboundMessages(req.Context(), row.SenderUserID, 100)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		projectID := ""
		for _, candidate := range owned {
			if candidate.TargetRunID == targetRun {
				projectID = candidate.ProjectID
				break
			}
		}
		if projectID == "" {
			writeError(w, 403, fmt.Errorf("run %q was not started from this sender's messages", targetRun))
			return
		}
		if err := r.temporal.CancelWorkflow(req.Context(), workflowIDFor(r.sourceID, projectID, targetRun), ""); err != nil {
			writeError(w, 409, err)
			return
		}
		_ = r.ws.UpdateRunStatus(req.Context(), targetRun, "cancelled", "", 0, "cancelled by sender from channel")
		if emit != nil {
			emit(req.Context(), "intake.cancelled", inboundMessageToAgent(row), row.SenderUserID, map[string]any{
				"target_run_id": targetRun, "project_id": projectID,
			})
		}
		_ = r.ws.RecordAccessAudit(req.Context(), row.SenderUserID, workspace.AuditIntakeCancelled, "run", targetRun, map[string]any{
			"project": projectID, "message": row.ID,
		})
		writeJSON(w, 200, map[string]string{"run_id": targetRun, "status": "cancel_requested"})
	})
	// Projects visible to the message sender: search_projects behind the
	// intake dialog. Query is a case-insensitive substring over id and name.
	mux.HandleFunc("GET /v1/intake/projects", func(w http.ResponseWriter, req *http.Request) {
		if !gate.Allow(w, req, controlplane.RoleOperator) {
			return
		}
		sender := strings.TrimSpace(req.URL.Query().Get("sender"))
		if sender == "" {
			writeError(w, 422, errors.New("sender is required"))
			return
		}
		principal, ok := r.ws.PrincipalForUser(req.Context(), r.log, sender)
		if !ok {
			writeError(w, 404, fmt.Errorf("sender %q is not an active user", sender))
			return
		}
		projects, err := r.ws.ListProjectsForUser(req.Context(), sender, principal.Role >= controlplane.RoleAdmin, principal.VisibleUnits)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		query := strings.ToLower(strings.TrimSpace(req.URL.Query().Get("query")))
		matched := []map[string]any{}
		for _, project := range projects {
			if project.Archived || project.ID == IntakeProjectID {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(project.ID+" "+project.Name), query) {
				continue
			}
			entry := map[string]any{"id": project.ID, "name": project.Name}
			if project.DefaultAgentID != "" {
				entry["default_agent_id"] = project.DefaultAgentID
			}
			matched = append(matched, entry)
			if len(matched) >= 10 {
				break
			}
		}
		writeJSON(w, 200, map[string]any{"projects": matched})
	})
	// Classification decision: intake_decide journals the chosen class on the
	// message row. Status only advances received → classified; a row already
	// clarifying or routed keeps its lifecycle position.
	mux.HandleFunc("POST /v1/intake/runs/{runID}/decision", func(w http.ResponseWriter, req *http.Request) {
		if !gate.Allow(w, req, controlplane.RoleOperator) {
			return
		}
		var body struct {
			Classification string `json:"classification"`
			Note           string `json:"note"`
		}
		if err := decodeJSON(w, req, &body); err != nil {
			writeError(w, 400, err)
			return
		}
		if !workspace.ValidInboundClassification(body.Classification) {
			writeError(w, 422, fmt.Errorf("classification must be one of task, amendment, status, cancel, chatter, reply"))
			return
		}
		row, err := r.ws.GetInboundMessageByIntakeRun(req.Context(), req.PathValue("runID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("no inbound message for this run"))
				return
			}
			writeError(w, 500, err)
			return
		}
		update := workspace.InboundMessageUpdate{Classification: body.Classification}
		if row.Status == workspace.InboundStatusReceived {
			update.Status = workspace.InboundStatusClassified
		}
		if err := r.ws.UpdateInboundMessage(req.Context(), row.ID, update); err != nil {
			writeError(w, 500, err)
			return
		}
		if emit != nil {
			data := map[string]any{"classification": body.Classification}
			if body.Note != "" {
				data["note"] = body.Note
			}
			emit(req.Context(), "intake.classified", inboundMessageToAgent(row), row.SenderUserID, data)
		}
		writeJSON(w, 200, map[string]any{"classified": body.Classification})
	})
	// Task creation: start_task resolves the message row from the intake run,
	// rebuilds the sender's principal and runs the full task-start cycle —
	// agent resolution, card defaults, model policy, operation-level
	// authorization (§22), quota — exactly as POST /v1/workspace/tasks/{id}/runs
	// does, but attributed to the sender (ActorID, audit, exec context).
	mux.HandleFunc("POST /v1/intake/runs/{runID}/start-task", func(w http.ResponseWriter, req *http.Request) {
		if !gate.Allow(w, req, controlplane.RoleOperator) {
			return
		}
		var body struct {
			ProjectID string `json:"project_id"`
			AgentID   string `json:"agent_id"`
			Title     string `json:"title"`
			Prompt    string `json:"prompt"`
		}
		if err := decodeJSON(w, req, &body); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(body.ProjectID) == "" || strings.TrimSpace(body.Title) == "" || strings.TrimSpace(body.Prompt) == "" {
			writeError(w, 422, errors.New("project_id, title and prompt are required"))
			return
		}
		row, err := r.ws.GetInboundMessageByIntakeRun(req.Context(), req.PathValue("runID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("no inbound message for this run"))
				return
			}
			writeError(w, 500, err)
			return
		}
		if row.SenderUserID == "" {
			writeError(w, 409, errors.New("message has no identified sender"))
			return
		}
		if row.Status == workspace.InboundStatusRouted {
			writeError(w, 409, fmt.Errorf("message already routed to task %s", row.ProjectID))
			return
		}
		if row.Status == workspace.InboundStatusRejected || row.Status == workspace.InboundStatusExpired {
			writeError(w, 409, fmt.Errorf("message is %s and cannot start tasks", row.Status))
			return
		}
		sender, err := senderUser(req.Context(), r.ws, row.SenderUserID)
		if err != nil {
			writeError(w, 404, err)
			return
		}
		principal, ok := r.ws.PrincipalForUser(req.Context(), r.log, sender.ID)
		if !ok {
			writeError(w, 404, fmt.Errorf("sender %q has no resolvable principal", sender.ID))
			return
		}
		// Deterministic task id from the message: a replayed start_task for the
		// same message fails cleanly on the task primary key instead of
		// duplicating work.
		taskID := intakeTaskID(row)
		if err := r.ws.CreateTask(req.Context(), &workspace.Task{ID: taskID, ProjectID: body.ProjectID, Title: body.Title, Prompt: body.Prompt}); err != nil {
			writeError(w, 409, fmt.Errorf("task %s already exists (message already routed)", taskID))
			return
		}
		agentID, agentCfg, aErr := resolveRunAgent(req.Context(), r.ws, body.ProjectID, body.AgentID)
		if aErr != nil {
			if errors.Is(aErr, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("agent not found"))
				return
			}
			writeError(w, 500, aErr)
			return
		}
		runID := taskID + "-" + time.Now().UTC().Format("20060102-150405")
		input := agent.RunInput{
			RunID: runID, Project: body.ProjectID, TaskID: taskID,
			Prompt: body.Prompt, ActorID: sender.ID,
		}
		applyAgentConfig(req.Context(), r.ws, agentCfg, &input)
		applyProjectModel(req.Context(), r.ws, body.ProjectID, &input)
		if pErr := applyRunPolicy(req.Context(), r.ws, r.costPrices, body.ProjectID, sender.OrgUnitID, "", &input); pErr != nil {
			writeError(w, 403, pErr)
			return
		}
		effectiveRole, err := authorizeRunUseFor(req.Context(), r.ws, &principal, &input, agentID, agentCfg)
		if err != nil {
			writeError(w, 403, err)
			return
		}
		if err := r.activities.PrepareRun(&input); err != nil {
			writeError(w, 422, err)
			return
		}
		if quotas.Enabled() {
			verdict, qErr := quotas.Allow(req.Context(), body.ProjectID)
			if qErr != nil {
				writeError(w, 500, qErr)
				return
			}
			if !verdict.Allowed {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "daily run quota exceeded for project " + body.ProjectID, "quota": verdict})
				return
			}
		}
		options := client.StartWorkflowOptions{ID: workflowIDFor(r.sourceID, body.ProjectID, runID), TaskQueue: r.taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		run, wfErr := r.temporal.ExecuteWorkflow(req.Context(), options, "AgentRun", input)
		if wfErr != nil {
			writeError(w, 409, wfErr)
			return
		}
		_ = r.ws.CreateRun(req.Context(), &workspace.Run{
			ID: runID, TaskID: taskID, ProjectID: body.ProjectID,
			AgentID: agentID, AgentVersion: input.AgentVersion, RunID: runID, Status: "started", Model: input.Model,
			ExecContext: runExecContextFor(principal, &input, agentID, effectiveRole),
		})
		_ = r.ws.UpdateInboundMessage(req.Context(), row.ID, workspace.InboundMessageUpdate{
			ProjectID: body.ProjectID, TargetRunID: runID, Status: workspace.InboundStatusRouted,
		})
		if emit != nil {
			// The event lands in the intake scope; the target project sees the
			// routing through the run row and its own run events — dual visibility
			// without a second journal entry.
			emit(req.Context(), "intake.routed", inboundMessageToAgent(row), sender.ID, map[string]any{
				"project_id": body.ProjectID, "task_id": taskID, "run_id": runID,
			})
		}
		_ = r.ws.RecordAccessAudit(req.Context(), sender.ID, workspace.AuditIntakeRouted, "task", taskID, map[string]any{
			"project": body.ProjectID, "run_id": runID, "message": row.ID,
			"channel": row.Channel, "workflow_id": run.GetID(),
		})
		writeJSON(w, http.StatusAccepted, map[string]string{
			"run_id": runID, "task_id": taskID, "project": body.ProjectID, "status": "started",
		})
	})
}

// decodeJSON is the intake routes' small body reader (mirrors the inline
// handlers' MaxBytesReader pattern).
func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dest)
}

// intakeTaskID derives the deterministic target task id from the message row:
// one message → one task, replays collide on the primary key.
func intakeTaskID(row workspace.InboundMessage) string {
	return "inbound-" + slugify(translit(row.Channel+"-"+row.MessageID))
}

// inboundMessageToAgent rebuilds the channel-neutral message projection the
// event enqueuer logs from a stored row.
func inboundMessageToAgent(row workspace.InboundMessage) agent.InboundMessage {
	return agent.InboundMessage{ID: row.MessageID, Channel: row.Channel, ChatID: row.Room, Sender: row.SenderRaw, Text: row.Body}
}

// intakeEmitFunc is the loop's event enqueuer type, reused by the routes.
type intakeEmitFunc = func(context.Context, string, agent.InboundMessage, string, map[string]any)
