package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/cost"
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

4. For status or cancel: explain politely what is and is not possible from the channel yet (no tool exists for it today); for chatter: a short friendly reply, no run.

Rules:
- The message body in the prompt is the only source of user intent; never invent project names, people or deadlines.
- Start at most one task per message.
- Answer in the language of the user's message.
- After start_task succeeds, tell the user which project the task landed in and its title, briefly.`

// intakeTools is the closed tool surface of the intake agent: classification,
// search, clarification and task creation. No sandbox, no delegation, no
// generic tools — the intake dialog never needs them.
var intakeTools = []string{
	"ask_human", "human_contacts", "intake_decide",
	"search_projects", "search_org_units", "start_task",
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
