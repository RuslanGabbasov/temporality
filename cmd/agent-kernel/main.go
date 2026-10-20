package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/kernel/mcpclient"
	"github.com/temporality-project/temporality/kernel/outbox"
	"github.com/temporality-project/temporality/kernel/quota"
	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/skills"
	"github.com/temporality-project/temporality/workspace"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// BuildVersion is injected at link time by the release pipeline
// (-ldflags "-X main.buildVersion=v2026.1.0"); "dev" when built by hand.
var buildVersion = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	log.Info("temporality-agent-kernel starting", "version", buildVersion)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := controlplane.LoadFileSecrets("DATABASE_URL", "TEMPORALITY_MODEL_API_KEY", "TEMPORALITY_API_TOKEN", "KERNEL_AUTH_TOKENS", "KERNEL_SECRET_MASTER_KEY"); err != nil {
		log.Error("load file secrets", "error", err)
		os.Exit(1)
	}
	databaseURL := requiredEnv("DATABASE_URL")
	observationURL := env("TEMPORALITY_URL", "http://localhost:8080")
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
	if len(costPrices.Prices()) > 0 {
		log.Info("model cost accounting enabled", "models", len(costPrices.Prices()))
	}
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
	if err = ws.Migrate(ctx, "migrations/000042_skill_version_provenance.up.sql"); err != nil {
		log.Error("migrate workspace v42", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000043_drop_legacy_access_columns.up.sql"); err != nil {
		log.Error("migrate workspace v43", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000044_skill_evolution_phase2.up.sql"); err != nil {
		log.Error("migrate workspace v44", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000045_channel_transport.up.sql"); err != nil {
		log.Error("migrate workspace v45", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000046_user_password.up.sql"); err != nil {
		log.Error("migrate workspace v46", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000047_teams.up.sql"); err != nil {
		log.Error("migrate workspace v47", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000048_agent_evaluations.up.sql"); err != nil {
		log.Error("migrate workspace v48", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000049_agent_proposals.up.sql"); err != nil {
		log.Error("migrate workspace v49", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000050_secrets.up.sql"); err != nil {
		log.Error("migrate workspace v50 (secrets)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000051_channel_transport_payload.up.sql"); err != nil {
		log.Error("migrate workspace v51 (channel transport payload)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000052_channel_cursor.up.sql"); err != nil {
		log.Error("migrate workspace v52 (channel cursor)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000053_inbound_message.up.sql"); err != nil {
		log.Error("migrate workspace v53 (inbound message)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000054_org_unit_head.up.sql"); err != nil {
		log.Error("migrate workspace v54 (org unit head)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000055_human_request_delivery.up.sql"); err != nil {
		log.Error("migrate workspace v55 (human request delivery)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000056_intake_policy.up.sql"); err != nil {
		log.Error("migrate workspace v56 (intake policy)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000057_workspace_setting.up.sql"); err != nil {
		log.Error("migrate workspace v57 (workspace setting)", "error", err)
		os.Exit(1)
	}
	// Quality-loop migrations (specs/quality-loop.md), renumbered onto the
	// free tail of main's chain: 000058 pins the agent version a human
	// request was created under (HITL verdicts, §3), 000059 stores golden
	// eval-case candidates (§4), 000060 links a gate-started evaluation run
	// to its proposal (M3 idempotent 202, §5.3), 000061 adds the unified run
	// verdict, judge metadata and the calibration history (M4/M5, §5-§7).
	if err = ws.Migrate(ctx, "migrations/000058_human_request_agent_version.up.sql"); err != nil {
		log.Error("migrate workspace v58 (human request agent version)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000059_eval_case_candidates.up.sql"); err != nil {
		log.Error("migrate workspace v59 (eval case candidates)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000060_eval_gate_proposal_link.up.sql"); err != nil {
		log.Error("migrate workspace v60 (eval gate proposal link)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000061_judge_quality_loop.up.sql"); err != nil {
		log.Error("migrate workspace v61 (judge quality loop)", "error", err)
		os.Exit(1)
	}
	// 000062 unwraps trigger configs that the UI used to store double-encoded
	// (a JSON string node holding the document) — see the migration header.
	if err = ws.Migrate(ctx, "migrations/000062_trigger_config_unwrap.up.sql"); err != nil {
		log.Error("migrate workspace v62 (trigger config unwrap)", "error", err)
		os.Exit(1)
	}
	// 000063 creates the reference artifact registry (docs/artifact-registry.md):
	// documents skills point at from their manifests and agents load on demand.
	if err = ws.Migrate(ctx, "migrations/000063_artifacts.up.sql"); err != nil {
		log.Error("migrate workspace v63 (artifact registry)", "error", err)
		os.Exit(1)
	}
	// 000064 adds the artifact lifecycle (docs/artifact-lifecycle.md): status/
	// kind/version/origin/saga columns and the review table. Existing rows
	// become approved v1 reference documents — their authors signed them in.
	if err = ws.Migrate(ctx, "migrations/000064_artifact_lifecycle.up.sql"); err != nil {
		log.Error("migrate workspace v64 (artifact lifecycle)", "error", err)
		os.Exit(1)
	}
	// 000065 adds human_request.thread_ref (docs/channels.md §9): the
	// conversation thread of the delivered question — slack-style threads keep
	// follow-ups and reply matching inside one conversation.
	if err = ws.Migrate(ctx, "migrations/000065_human_request_thread.up.sql"); err != nil {
		log.Error("migrate workspace v65 (human request thread)", "error", err)
		os.Exit(1)
	}
	// 000066 creates the research-session store (docs/human-knowledge-
	// explorer.md §11): saved knowledge-explorer investigations — references
	// only, the projection is re-resolved on every open.
	if err = ws.Migrate(ctx, "migrations/000066_research_sessions.up.sql"); err != nil {
		log.Error("migrate workspace v66 (research sessions)", "error", err)
		os.Exit(1)
	}
	// 000067-000069 create the auth-federation surface (docs/auth-federation.md
	// §7): the single IdP config row, server-side browser sessions, one-time
	// OIDC request state, claim mappings (schema lands with phase 1, application
	// in phase 2) and the federation identity on workspace_user.
	if err = ws.Migrate(ctx, "migrations/000067_auth_oidc.up.sql"); err != nil {
		log.Error("migrate workspace v67 (auth oidc)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000068_auth_claim_mapping.up.sql"); err != nil {
		log.Error("migrate workspace v68 (auth claim mapping)", "error", err)
		os.Exit(1)
	}
	if err = ws.Migrate(ctx, "migrations/000069_workspace_user_federation.up.sql"); err != nil {
		log.Error("migrate workspace v69 (workspace user federation)", "error", err)
		os.Exit(1)
	}
	// 000070 creates the sandbox profile registry (docs/sandbox-profiles.md):
	// builtin catalog rows plus custom profiles; the dead
	// restricted/standard/privileged values on agent cards migrate to the empty
	// default (the installation image).
	if err = ws.Migrate(ctx, "migrations/000070_sandbox_profiles.up.sql"); err != nil {
		log.Error("migrate workspace v70 (sandbox profiles)", "error", err)
		os.Exit(1)
	}
	// 000071 adds custom profile build recipes: a custom row either references
	// an external image or composes on a known base (base + extra packages).
	if err = ws.Migrate(ctx, "migrations/000071_sandbox_profile_recipes.up.sql"); err != nil {
		log.Error("migrate workspace v71 (sandbox profile recipes)", "error", err)
		os.Exit(1)
	}
	// 000072 adds server-side chat persistence (docs/resilience-and-scale.md
	// §4): UI conversations sync here so a browser wipe is no longer data
	// loss, and chats follow the user across devices.
	if err = ws.Migrate(ctx, "migrations/000072_workspace_chats.up.sql"); err != nil {
		log.Error("migrate workspace v72 (workspace chats)", "error", err)
		os.Exit(1)
	}
	// 000073 lets runs exist without a task: intake runs (docs/inbound-messages.md)
	// hit the NOT NULL on task_id, the insert failed silently and their answers
	// were undeliverable (the channel watcher reads this row).
	if err = ws.Migrate(ctx, "migrations/000073_run_task_id_nullable.up.sql"); err != nil {
		log.Error("migrate workspace v73 (run task_id nullable)", "error", err)
		os.Exit(1)
	}
	// 000074 adds explicit per-user facts («отвечай кратко», ownership notes):
	// the accumulated half of the sender picture the dialogs open with.
	if err = ws.Migrate(ctx, "migrations/000074_user_fact.up.sql"); err != nil {
		log.Error("migrate workspace v74 (user facts)", "error", err)
		os.Exit(1)
	}
	// Curated builtin agents are templates, not auto-created agents
	// (docs/evaluable-agent.md §16): the user creates them deliberately from
	// the template gallery. Cleanup removes agents left by the earlier
	// auto-seeding model, so no builtin is forced on an existing workspace.
	cleanupSeededBuiltinAgents(ctx, log, ws)
	// Builtin sandbox profile rows (docs/sandbox-profiles.md §5): seed the
	// catalog; a missing seed degrades profile assignment to the explicit
	// not-built error, so failure is logged, not fatal.
	if err := ws.SeedSandboxProfiles(ctx); err != nil {
		log.Error("seed builtin sandbox profiles", "error", err)
	}
	// Kernel→journal auth must be resolved before every consumer reads
	// TEMPORALITY_API_TOKEN: the outbox publisher, the cost API and the
	// activities' direct journal calls. On a fresh install the env is empty and
	// the journal's gate switches on with the first workspace user — without
	// this, every kernel journal call 401s: runs publish nothing (invisible in
	// the UI) and /v1/agent/operations 500s on every poll. The probe keeps open
	// dev stands open (see resolveJournalToken).
	journalToken, journalTokenSource, err := resolveJournalToken(ctx, &http.Client{Timeout: 5 * time.Second}, observationURL, strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")), func() (string, error) {
		return ws.EnsureKernelServiceToken(ctx)
	})
	if err != nil {
		log.Error("kernel journal auth: provisioning the service principal failed", "error", err)
		os.Exit(1)
	}
	switch journalTokenSource {
	case journalTokenService:
		os.Setenv("TEMPORALITY_API_TOKEN", journalToken)
		log.Info("kernel journal auth: TEMPORALITY_API_TOKEN is empty or rejected; using the auto-provisioned service principal", "user", workspace.KernelServiceUserName)
	case journalTokenEnvStale:
		if journalToken == "" {
			log.Info("kernel journal auth: journal unreachable at boot; the re-resolver self-heals within a minute once it is up")
		}
	}
	// Journal auth re-resolver (docs/resilience-and-scale.md §4): a boot-order
	// race (the journal comes up after the kernel) used to leave the kernel
	// without a working token until a manual restart — runs published nothing
	// and every journal consumer 401'd. All journal consumers read the env
	// lazily now, so a periodic re-resolution self-heals them: runs become
	// visible and the supervisor/embedding passes recover without a restart.
	go func() {
		lastSource := journalTokenSource
		lastToken := os.Getenv("TEMPORALITY_API_TOKEN")
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
			token, source, err := resolveJournalToken(ctx, &http.Client{Timeout: 5 * time.Second}, observationURL, strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")), func() (string, error) {
				return ws.EnsureKernelServiceToken(ctx)
			})
			if err != nil {
				continue
			}
			if token == lastToken && source == lastSource {
				continue // nothing changed
			}
			os.Setenv("TEMPORALITY_API_TOKEN", token)
			log.Info("kernel journal auth re-resolved", "source", source, "was", lastSource)
			lastToken, lastSource = token, source
		}
	}()
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
	costAPI := cost.NewCostAPI(costPrices, observationURL, os.Getenv("TEMPORALITY_API_TOKEN"))
	// Human-attention coefficients (docs/supervisor-model.md §5) feed the
	// persistent HAB/HWT rollups; read once at boot — settings changes apply
	// after a restart. The same coefficients price the per-user HAB slice in
	// the dashboard's Users tab.
	habFactors := cost.DefaultHumanFactors
	if factors, err := ws.HumanFactors(ctx); err == nil {
		habFactors = cost.HumanFactors{
			ApprovalSeconds:   factors.ApprovalSeconds,
			AnswerBaseSeconds: factors.AnswerBaseSeconds,
			ReadingWPM:        factors.ReadingWPM,
		}
		costAPI.SetHumanFactors(habFactors)
	}
	activities, err := agent.NewActivities(events)
	if err != nil {
		log.Error("configure activities", "error", err)
		os.Exit(1)
	}
	// A missing default model is legal on a clean install (providers get
	// configured through onboarding); say so loudly instead of failing later
	// with a cryptic per-run error.
	if strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_BASE_URL")) == "" || strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID")) == "" {
		log.Warn("no default model configured (TEMPORALITY_MODEL_BASE_URL/TEMPORALITY_MODEL_ID); runs must select a model served by a workspace provider")
	}
	// The sandbox root decides where every project workspace lives; say it out
	// loud at boot so «where did my workspaces go» never needs code archaeology.
	if swappable := activities.SandboxSwappable(); swappable != nil {
		root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
		if root == "" {
			root = ".sandbox"
		}
		if resolved := swappable.SandboxRoot(); resolved != "" {
			root = resolved
		}
		if image := swappable.Image(); image != "" {
			log.Info("sandbox runner ready", "root", root, "image", image)
		} else {
			log.Warn("sandbox runner not configured: run_command stays inert until onboarding builds an image (KERNEL_SANDBOX_IMAGE)", "root", root)
		}
	}
	// Ephemeral live-token streaming: the activity worker and the HTTP server
	// share one process, so an in-memory bus connects them without a broker.
	activities.Tokens = agent.NewTokenBus()
	// Admin-editable channel settings (channel_transport row) overlay the
	// KERNEL_CHANNEL__* env vars: delivery reads the database per send, so
	// saving the settings screen takes effect without a restart.
	activities.ChannelSettings = func(ctx context.Context) (agent.ChannelSettings, error) {
		row, err := ws.ChannelTransport(ctx)
		if err != nil {
			return agent.ChannelSettings{}, err
		}
		return agent.ChannelSettings{Channels: row.Payload, UIURL: row.UIURL}, nil
	}
	// Secrets (docs/secrets-management.md Phase 1): the broker resolves
	// agent-granted secrets over the loopback workspace API with a TTL cache;
	// the master key stays in this process and never reaches activities.
	secretMasterKey := loadSecretMasterKey(ctx, log, ws)
	// Background quality work (skill builder) falls back to the supervisor
	// judge model when no project/run default model is configured.
	activities.JudgeModel = func(ctx context.Context) (*llm.Client, error) {
		return judgeModelClient(ctx, ws, secretMasterKey)
	}
	activities.Secrets = &agent.SecretBroker{
		HTTP: &http.Client{Timeout: 10 * time.Second},
		TTL:  secretCacheTTL(),
	}
	loadMCPServers(ctx, log, ws, activities.MCP, secretMasterKey)
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
	temporalWorker.RegisterWorkflowWithOptions(agent.KnowledgeExtractionWorkflow, workflow.RegisterOptions{Name: agent.KnowledgeExtractionWorkflowName})
	temporalWorker.RegisterActivityWithOptions(activities.RecordEvent, activity.RegisterOptions{Name: agent.ActivityRecordEvent})
	temporalWorker.RegisterActivityWithOptions(activities.CallModel, activity.RegisterOptions{Name: agent.ActivityCallModel})
	temporalWorker.RegisterActivityWithOptions(activities.RunTool, activity.RegisterOptions{Name: agent.ActivityRunTool})
	temporalWorker.RegisterActivityWithOptions(activities.KnowledgeHints, activity.RegisterOptions{Name: agent.ActivityKnowledgeHints})
	temporalWorker.RegisterActivityWithOptions(activities.KnowledgeLookup, activity.RegisterOptions{Name: agent.ActivityKnowledgeLookup})
	temporalWorker.RegisterActivityWithOptions(activities.ResolveAgent, activity.RegisterOptions{Name: agent.ActivityResolveAgent})
	temporalWorker.RegisterActivityWithOptions(activities.GenerateTitle, activity.RegisterOptions{Name: agent.ActivityGenerateTitle})
	temporalWorker.RegisterActivityWithOptions(activities.ExtractKnowledge, activity.RegisterOptions{Name: agent.ActivityExtractKnowledge})
	temporalWorker.RegisterActivityWithOptions(activities.SummarizeChildRun, activity.RegisterOptions{Name: agent.ActivitySummarizeChildRun})
	temporalWorker.RegisterActivityWithOptions(activities.WriteTeamContract, activity.RegisterOptions{Name: agent.ActivityWriteTeamContract})
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
	// A tokenless instance still needs a live gate object: workspace-user
	// tokens loaded below must be able to switch authentication on without a
	// restart — the first user created through onboarding locks the anonymous
	// admin out immediately (Enabled counts DB tokens too). NewGate returns
	// nil for an empty configuration.
	if gate == nil {
		gate = &controlplane.Gate{}
	}
	// Workspace-user tokens live in the database and are loaded into a
	// separate reloadable gate token set below (see refreshUserTokens), so
	// the historical "restart the kernel after creating a user" limitation
	// is gone.
	if !gate.Enabled() {
		log.Warn("KERNEL_AUTH_TOKENS is empty: authentication disabled; create the first workspace user to enable it")
	} else {
		log.Info("kernel authentication enabled", "tokens", gate.TokenCount())
	}
	// Workspace self-access: agent tools (skills, triggers) call this kernel's
	// own workspace API over loopback. A startup-random internal token lets the
	// calls pass the gate once authentication is on (including after the first
	// workspace user activates it) without exposing a reusable credential.
	address := env("KERNEL_HTTP_ADDR", ":8090")
	internalToken := generateToken()
	gate.SetInternalToken(internalToken)
	activities.WorkspaceToken = internalToken
	// First-run onboarding bootstrap: when KERNEL_SANDBOX_IMAGE is not set but
	// the workspace holds a previously built image reference
	// (workspace_setting.sandbox_image), install it without a restart. The env
	// var always wins — an operator pinning a specific digest is never
	// overridden by the database.
	if strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_IMAGE")) == "" {
		if image, gErr := ws.GetSetting(ctx, "sandbox_image"); gErr == nil && image != "" {
			root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
			if root == "" {
				root = ".sandbox"
			}
			if runner, nErr := sandbox.New(image, root); nErr != nil {
				log.Error("install sandbox runner from workspace_setting.sandbox_image", "image", image, "error", nErr)
			} else if swappable := activities.SandboxSwappable(); swappable != nil {
				swappable.Swap(runner)
				log.Info("sandbox runner installed from workspace_setting", "image", image, "root", runner.Root)
			}
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
		principals, err := ws.LoadTokenPrincipals(reloadCtx, log)
		if err != nil {
			log.Error("load workspace user tokens", "error", err)
			return
		}
		gate.SetDBTokens(principals)
		log.Info("loaded workspace user tokens", "count", len(principals))
	}
	refreshUserTokens()

	if os.Getenv("KERNEL_WORKSPACE_URL") == "" {
		activities.WorkspaceURL = "http://localhost" + address
	}
	// The secret broker resolves at the same execution boundary as the other
	// agent tools: same loopback workspace API, same startup-random internal
	// credential. Wiring it here (after the gate and address are settled) keeps
	// KERNEL_WORKSPACE_URL overrides working for secrets too — without it every
	// run_command of an agent with granted secrets fails closed.
	activities.Secrets.WorkspaceURL = activities.WorkspaceURL
	activities.Secrets.WorkspaceToken = activities.WorkspaceToken
	// Web research tools (docs/web-research.md): SearXNG backs web_search —
	// empty URL leaves the tool unregistered while web_fetch still works.
	activities.SearXNGURL = strings.TrimRight(strings.TrimSpace(os.Getenv("KERNEL_SEARXNG_URL")), "/")
	activities.WebFetchAllowPrivate = strings.EqualFold(strings.TrimSpace(os.Getenv("KERNEL_WEB_FETCH_ALLOW_PRIVATE")), "true")
	// Global web_search budget: engines ban the server IP, and each query
	// fans out into dozens of engine requests, so the cap is per kernel.
	webSearchPerMin := 30
	if raw := strings.TrimSpace(os.Getenv("KERNEL_WEB_SEARCH_PER_MIN")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			webSearchPerMin = n
		}
	}
	activities.WebSearchPerMin = webSearchPerMin
	// Headless browser backend (docs/web-research.md §4): empty URL leaves
	// the browser tool unregistered. The CDP endpoint is operator-configured
	// like SearXNG and lives on the isolated browser network.
	activities.BrowserCDPURL = strings.TrimRight(strings.TrimSpace(os.Getenv("KERNEL_BROWSER_CDP_URL")), "/")
	if raw := strings.TrimSpace(os.Getenv("KERNEL_BROWSER_PER_MIN")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n != 0 {
			activities.BrowserPerMin = n
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": buildVersion})
	})
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
	// Password login exchanges name-or-email + password for the user's bearer
	// token. The route is mounted outside the auth gate (see server setup
	// below): unauthenticated callers may reach exactly this one endpoint.
	// Every failure is the same generic 401 — unknown login, wrong password,
	// inactive user and "no password configured" are indistinguishable, so
	// the endpoint cannot be used to enumerate users. Registration sits below
	// (registerPasswordLogin) — it shares authDeps with the federation wiring.
	//
	// Auth federation phase 1 (docs/auth-federation.md §5-§8): OIDC login,
	// server-side browser sessions, JIT provisioning and the admin IdP-config
	// surface. The provider cache memoizes discovery; the session resolver
	// teaches the gate to accept the cookie the callback issues.
	authSessionTTL := 12 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("KERNEL_SESSION_TTL")); raw != "" {
		if parsed, pErr := time.ParseDuration(raw); pErr == nil && parsed > 0 {
			authSessionTTL = parsed
		} else {
			log.Warn("KERNEL_SESSION_TTL is not a valid duration, using 12h", "value", raw)
		}
	}
	authAbsoluteTTL := 7 * 24 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("KERNEL_SESSION_ABSOLUTE_TTL")); raw != "" {
		if parsed, pErr := time.ParseDuration(raw); pErr == nil && parsed > 0 {
			authAbsoluteTTL = parsed
		} else {
			log.Warn("KERNEL_SESSION_ABSOLUTE_TTL is not a valid duration, using 168h", "value", raw)
		}
	}
	if authAbsoluteTTL < authSessionTTL {
		authAbsoluteTTL = authSessionTTL
	}
	oidcProviders := newOIDCProviderCache()
	authDeps := authDependencies{
		store: ws,
		resolveKey: func(ctx context.Context, ref string) (string, error) {
			return resolveProviderAPIKey(ctx, ws, ref, secretMasterKey)
		},
		log:          log,
		sessionTTL:   authSessionTTL,
		absoluteTTL:  authAbsoluteTTL,
		cookieSecure: strings.EqualFold(strings.TrimSpace(os.Getenv("KERNEL_COOKIE_SECURE")), "true"),
	}
	registerAuthRoutes(mux, authDeps, gate, oidcProviders)
	registerPasswordLogin(mux, authDeps, ws, refreshUserTokens)
	gate.SetSessionResolver(sessionPrincipalResolver(ws, func(ctx context.Context, userID string) (controlplane.Principal, bool) {
		return ws.PrincipalForUser(ctx, log, userID)
	}, log, authSessionTTL, authAbsoluteTTL))
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
		// Agent-card defaults for direct API runs: the caller's explicit fields
		// (model, prompt, turn budget, skills, MCP) win, everything else —
		// including capability enforcement — comes from the agent card, then the
		// project default model and the org policy. Same resolution order as the
		// task/trigger paths, so every entrypoint produces equivalent runs.
		applyAgentDefaults(r.Context(), ws, resolvedAgent, &input)
		applyProjectModel(r.Context(), ws, input.Project, &input)
		actorUnit := ""
		if principal, ok := controlplane.FromContext(r.Context()); ok {
			actorUnit = principal.OrgUnitID
		} else if token, _ := controlplane.BearerToken(r); token != "" {
			if user, uErr := ws.GetUserByToken(r.Context(), token); uErr == nil {
				actorUnit = user.OrgUnitID
			}
		}
		if pErr := applyRunPolicy(r.Context(), ws, costPrices, input.Project, actorUnit, "", &input); pErr != nil {
			writeError(w, 403, pErr)
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
	// Team program runs (docs/agent-teams.md §9): snapshot the definition,
	// resolve every slot up front and execute the compiled protocol as real
	// child AgentRuns. Writer role + operation-level authorization below.
	mux.HandleFunc("POST /v1/agent/teams/{teamID}/runs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project  string            `json:"project"`
			Goal     string            `json:"goal"`
			Version  string            `json:"version,omitempty"`
			Bindings map[string]string `json:"bindings,omitempty"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Project) == "" || strings.TrimSpace(req.Goal) == "" {
			writeError(w, 422, errors.New("project and goal are required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleWriter, req.Project) {
			return
		}
		team, err := ws.GetTeam(r.Context(), r.PathValue("teamID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("team not found"))
				return
			}
			writeError(w, 500, err)
			return
		}
		manifestRaw := team.Manifest
		if strings.TrimSpace(req.Version) != "" && req.Version != team.Version {
			version, vErr := ws.GetTeamVersion(r.Context(), team.ID, req.Version)
			if vErr != nil {
				if errors.Is(vErr, workspace.ErrNotFound) {
					writeError(w, 404, fmt.Errorf("team version %s not found", req.Version))
					return
				}
				writeError(w, 500, vErr)
				return
			}
			manifestRaw = version.Manifest
		}
		manifest, mErr := workspace.ParseTeamManifest(manifestRaw)
		if mErr != nil {
			writeError(w, 422, mErr)
			return
		}
		// Team visibility follows the org tree like every other resource; admins
		// and service tokens (nil VisibleUnits) skip the check.
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.VisibleUnits != nil && principal.Role < controlplane.RoleAdmin {
			if !workspace.OrgVisible(team.OrgUnitID, principal.VisibleUnits) {
				writeError(w, 403, fmt.Errorf("team %q is not available in your org scope", team.ID))
				return
			}
		}
		// Project visibility + effective writer (docs/org-structure.md §22).
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.VisibleUnits != nil && principal.Role < controlplane.RoleAdmin {
			if effective, pErr := projectAccessRole(r.Context(), ws, &principal, req.Project); pErr != nil {
				writeError(w, 403, pErr)
				return
			} else if effective < controlplane.RoleWriter {
				writeError(w, 403, fmt.Errorf("effective role %s is not enough to start runs in project %q (writer required)", effective, req.Project))
				return
			}
		}
		// Launch-time slot resolution (docs/agent-teams.md §7, wave C): the roster
		// is this caller's visible agents filtered to the run's project scope —
		// the same population the delegation roster serves. Role slots match on
		// capabilities/skills with preferred as a tie-breaker; ambiguity is a 409
		// listing candidates, an empty match is a 422 listing requirements.
		rosterAgents, rErr := ws.ListAgentsVisible(r.Context(), candidateVisibleUnits(r))
		if rErr != nil {
			writeError(w, 500, rErr)
			return
		}
		roster := make([]workspace.Agent, 0, len(rosterAgents))
		for _, candidate := range rosterAgents {
			if candidate.ProjectID == "" || candidate.ProjectID == req.Project {
				roster = append(roster, candidate)
			}
		}
		bindings, _, rejection := agent.ResolveTeamBindings(manifest, roster, req.Bindings)
		if rejection != nil {
			writeJSON(w, rejection.Status, map[string]any{
				"error": rejection.Reason, "slot_id": rejection.SlotID,
				"candidates": rejection.Candidates, "requirements": rejection.Requirements,
			})
			return
		}
		// Every bound agent must exist and be org-visible with an effective
		// writer role at its binding — the same rule authorizeRunUse applies to
		// manual runs, checked per slot before anything starts (effect=none).
		boundAgents := make(map[string]bool, len(bindings))
		for _, slot := range manifest.Slots {
			binding := bindings[slot.ID]
			if boundAgents[binding.AgentID] {
				continue
			}
			boundAgents[binding.AgentID] = true
			a, aErr := ws.GetAgent(r.Context(), binding.AgentID)
			if aErr != nil {
				if errors.Is(aErr, workspace.ErrNotFound) {
					writeError(w, 422, fmt.Errorf("slot %q: agent %q not found", slot.ID, binding.AgentID))
					return
				}
				writeError(w, 500, aErr)
				return
			}
			if principal, ok := controlplane.FromContext(r.Context()); ok && principal.VisibleUnits != nil && principal.Role < controlplane.RoleAdmin {
				if a.OrgUnitID != "" && !workspace.OrgVisible(a.OrgUnitID, principal.VisibleUnits) {
					writeError(w, 403, fmt.Errorf("slot %q: agent %q is not available in your org scope", slot.ID, a.ID))
					return
				}
				if a.OrgUnitID != "" {
					if unit, uErr := ws.GetOrgUnit(r.Context(), a.OrgUnitID); uErr == nil {
						if at := principal.MaxRoleAt(unit.ID, unit.Path); at < controlplane.RoleWriter {
							writeError(w, 403, fmt.Errorf("slot %q: effective role %s is not enough to run agent %q (writer required)", slot.ID, at, a.ID))
							return
						}
					}
				}
			}
		}
		// Compilability is validated before start: an invalid program is
		// rejected with 422 and nothing launches (docs/agent-teams.md §6).
		if !agent.TeamIsLeadWorkers(manifest.Protocol.Kind) {
			if _, cReason := agent.CompileTeamProgram(manifest, bindings, req.Goal); cReason != "" {
				writeError(w, 422, errors.New("team program is invalid: "+cReason))
				return
			}
		}
		runID := team.ID + "-" + time.Now().UTC().Format("20060102-150405")
		input := agent.RunInput{
			RunID:   runID,
			Project: req.Project,
			TaskID:  runID,
			Prompt:  req.Goal,
			// The goal is the team run's display title — no title-model call
			// needed for program runs.
			Title: req.Goal,
			Team: &agent.TeamRunInput{
				TeamID:   team.ID,
				TeamName: team.Name,
				Version:  team.Version,
				Goal:     req.Goal,
				Bindings: bindings,
				Manifest: manifest,
			},
		}
		if req.Version != "" {
			input.Team.Version = req.Version
		}
		if manifest.Defaults.ParallelLimit > 0 {
			input.DelegationWidth = manifest.Defaults.ParallelLimit
		}
		if manifest.Defaults.BudgetUSD > 0 {
			// Team-level budget ceiling: recorded on the root run. The root itself
			// makes no model calls; per-child enforcement stays with each child's
			// own policy, and the rollup in team.completed reports the honest
			// total (full enforcement lands with the telemetry wave).
			input.MaxBudgetUSD = manifest.Defaults.BudgetUSD
		}
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
			input.ActorID = principal.UserID
		} else {
			input.ActorID = requestUserID(r)
		}
		workflowID := workflowIDFor(activities.SourceID, req.Project, runID)
		options := client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		run, wfErr := temporalClient.ExecuteWorkflow(r.Context(), options, "AgentRun", input)
		if wfErr != nil {
			writeError(w, 409, wfErr)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamRunStarted, "team", team.ID, map[string]any{"run_id": runID, "version": input.Team.Version, "project": req.Project})
		writeJSON(w, http.StatusAccepted, map[string]string{"run_id": runID, "project": req.Project, "team_id": team.ID, "workflow_id": run.GetID(), "temporal_run_id": run.GetRunID(), "status": "started"})
	})
	// Binding preview for the launch dialog (docs/agent-teams.md §7, wave C):
	// resolves every slot against the caller's roster without starting anything.
	// Ambiguous and unresolvable slots are reported per slot — a preview never
	// fails as a whole.
	mux.HandleFunc("GET /v1/agent/teams/{teamID}/resolve", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		team, err := ws.GetTeam(r.Context(), r.PathValue("teamID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("team not found"))
				return
			}
			writeError(w, 500, err)
			return
		}
		manifestRaw := team.Manifest
		version := team.Version
		if requested := strings.TrimSpace(r.URL.Query().Get("version")); requested != "" && requested != team.Version {
			v, vErr := ws.GetTeamVersion(r.Context(), team.ID, requested)
			if vErr != nil {
				if errors.Is(vErr, workspace.ErrNotFound) {
					writeError(w, 404, fmt.Errorf("team version %s not found", requested))
					return
				}
				writeError(w, 500, vErr)
				return
			}
			manifestRaw, version = v.Manifest, requested
		}
		manifest, mErr := workspace.ParseTeamManifest(manifestRaw)
		if mErr != nil {
			writeError(w, 422, mErr)
			return
		}
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.VisibleUnits != nil && principal.Role < controlplane.RoleAdmin {
			if !workspace.OrgVisible(team.OrgUnitID, principal.VisibleUnits) {
				writeError(w, 403, fmt.Errorf("team %q is not available in your org scope", team.ID))
				return
			}
		}
		rosterAgents, rErr := ws.ListAgentsVisible(r.Context(), candidateVisibleUnits(r))
		if rErr != nil {
			writeError(w, 500, rErr)
			return
		}
		roster := make([]workspace.Agent, 0, len(rosterAgents))
		for _, candidate := range rosterAgents {
			if candidate.ProjectID == "" || candidate.ProjectID == project {
				roster = append(roster, candidate)
			}
		}
		_, choices, rejection := agent.ResolveTeamBindings(manifest, roster, nil)
		preview := map[string]any{"team_id": team.ID, "version": version, "project": project, "slots": choices, "resolvable": rejection == nil}
		if rejection != nil {
			preview["reason"] = rejection.Reason
			if rejection.SlotID != "" {
				preview["slot_id"] = rejection.SlotID
			}
			if len(rejection.Candidates) > 0 {
				preview["candidates"] = rejection.Candidates
			}
			if len(rejection.Requirements) > 0 {
				preview["requirements"] = rejection.Requirements
			}
		}
		writeJSON(w, 200, preview)
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
			status = workflowStatusName(response.WorkflowExecutionInfo.Status)
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
			// The ask was issued from a delegated child run: every AgentRun
			// workflow (top-level or child) is identified by the full run path.
			workflowID = workflowIDFor(sourceID, project, childRunID)
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
				row, cerr := ws.AnswerHumanRequest(r.Context(), approval.OperationID, response, actor)
				closeErr = cerr
				if cerr == nil {
					// Terminal transition with a verdict → hitl.verdict.recorded
					// (specs/quality-loop.md §9) through the durable outbox.
					emitHitlVerdict(r.Context(), events, row)
				}
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
	// Extraction kickoff for external agents (docs/external-agents.md): any
	// framework that reported its run through the journal's event ingest can
	// request the experience loop — knowledge extraction, projection and hint
	// availability — without running inside the kernel harness. The kickoff
	// verifies the run is terminal and journal-backed, then starts the same
	// detached KnowledgeExtraction workflow native runs use; REJECT_DUPLICATE
	// makes repeat calls idempotent (409 already extracted).
	mux.HandleFunc("POST /v1/agent/runs/{runID}/extract", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleWriter, project) {
			return
		}
		runID := r.PathValue("runID")
		var body struct {
			SourceID string `json:"source_id"`
			Model    string `json:"model"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body)
		sourceID := strings.TrimSpace(body.SourceID)
		if sourceID == "" {
			sourceID = querySourceID(r, activities.SourceID)
		}
		// The run must exist in the journal and be terminal: extraction replays
		// finished work, so a live or unknown run is a caller error. The listing
		// is scoped to the project, so a foreign project's run id answers 404.
		terminal := map[string]bool{"run.completed": true, "run.failed": true, "run.cancelled": true}
		lastEventID, terminalSeen := "", false
		runEvents, err := activities.RunEvents(r.Context(), project, runID)
		if err != nil {
			writeError(w, 500, fmt.Errorf("journal listing failed: %w", err))
			return
		}
		for _, event := range runEvents {
			lastEventID = event.EventID
			if terminal[event.Type] {
				terminalSeen = true
			}
		}
		if lastEventID == "" {
			writeError(w, 404, errors.New("run not found in the journal — report its events through POST /v1/observations/events first"))
			return
		}
		if !terminalSeen {
			writeError(w, 409, errors.New("run is not terminal yet — extraction waits for run.completed, run.failed or run.cancelled"))
			return
		}
		actor := "external"
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
			actor = principal.UserID
		}
		input := agent.KnowledgeExtractionInput{
			Run: agent.RunInput{
				RunID: runID, Project: project, SourceID: sourceID, ActorID: actor,
				Model: strings.TrimSpace(body.Model),
				// External runs did not run inside the harness: extraction reads
				// the journal events, everything run-specific stays empty.
			},
			// A fresh deterministic chain: extraction events form their own
			// sequence (the foreign events carry the reporter's own event ids),
			// context.run ties them to the run's timeline.
			Scope: agent.EventScope(sourceID, project, runID) + "-extract",
		}
		options := client.StartWorkflowOptions{ID: agent.ExtractionWorkflowIDPublic(sourceID, project, runID), TaskQueue: taskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		if _, err := temporalClient.ExecuteWorkflow(r.Context(), options, "KnowledgeExtraction", input); err != nil {
			if strings.Contains(err.Error(), "already started") || strings.Contains(err.Error(), "already exists") {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "extraction already started for this run", "run_id": runID})
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "run_id": runID, "project": project, "detail": "extraction started; knowledge will appear in the journal projection and in hints"})
	})
	// Force stop (POST /terminate): TerminateWorkflow kills the workflow even
	// when a cancel cannot land — the prime case being a workflow task stuck
	// failing its determinism check, which processes no further signals. The
	// workflow's own deferred run.cancelled never fires on terminate, so the
	// endpoint journals the terminal event itself; the UI flips the run to
	// «остановлен» immediately instead of waiting on a zombie.
	mux.HandleFunc("POST /v1/agent/runs/{runID}/terminate", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			writeError(w, 400, errors.New("project query parameter is required"))
			return
		}
		if !gate.Allow(w, r, controlplane.RoleWriter, project) {
			return
		}
		runID := r.PathValue("runID")
		sourceID := querySourceID(r, activities.SourceID)
		workflowID := workflowIDFor(sourceID, project, runID)
		if err := temporalClient.TerminateWorkflow(r.Context(), workflowID, "", "terminated by operator"); err != nil {
			// A workflow Temporal does not know is already gone — the common case
			// being a stand whose Temporal volume was rebuilt while the journal
			// kept older runs. Those zombies stay 'running' in lists forever and
			// every stop press 409s. Heal instead: journal the terminal event and
			// settle the workspace status, answer success.
			if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "NotFound") {
				writeError(w, 409, err)
				return
			}
			actorGone := requestUserID(r)
			goneEvent := observation.Event{
				Schema:     observation.Schema,
				EventID:    fmt.Sprintf("run-terminate/%s/%d", runID, time.Now().UnixNano()),
				OccurredAt: time.Now().UTC(),
				Source:     observation.Source{ID: sourceID, Integration: "temporality-agent-kernel", Version: "1"},
				Context:    observation.Context{Project: project, Run: runID, Actor: observation.Actor{ID: actorGone, Type: "human"}},
				Type:       "run.cancelled",
				Data:       map[string]any{"reason": "terminated", "detail": "workflow not found in the orchestrator (already gone)", "actor_id": actorGone},
			}
			if err := events.Enqueue(r.Context(), goneEvent); err != nil {
				writeError(w, 500, err)
				return
			}
			_ = ws.UpdateRunStatus(r.Context(), runID, "cancelled", "", 0, "")
			writeJSON(w, 200, map[string]string{"run_id": runID, "status": "already_gone"})
			return
		}
		actor := requestUserID(r)
		event := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("run-terminate/%s/%d", runID, time.Now().UnixNano()),
			OccurredAt: time.Now().UTC(),
			Source:     observation.Source{ID: sourceID, Integration: "temporality-agent-kernel", Version: "1"},
			Context:    observation.Context{Project: project, Run: runID, Actor: observation.Actor{ID: actor, Type: "human"}},
			Type:       "run.cancelled",
			Data:       map[string]any{"reason": "terminated", "actor_id": actor},
		}
		if err := events.Enqueue(r.Context(), event); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]string{"run_id": runID, "status": "terminated"})
	})
	mux.HandleFunc("POST /v1/agent/runs/{runID}/nudge", func(w http.ResponseWriter, r *http.Request) {
		var nudge agent.Nudge
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&nudge); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, 400, err)
			return
		}
		if nudge.ActorID == "" {
			if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
				nudge.ActorID = principal.UserID
			}
		}
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
		// Turn 0 means "any in-flight model call" — the workflow selector
		// matches it against every live turn.
		if err := temporalClient.SignalWorkflow(r.Context(), workflowID, "", agent.NudgeSignal, nudge); err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /v1/agent/runs/{runID}/note", func(w http.ResponseWriter, r *http.Request) {
		var note agent.UserNote
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&note); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(note.Text) == "" {
			writeError(w, 400, errors.New("text is required"))
			return
		}
		if note.ActorID == "" {
			if principal, ok := controlplane.FromContext(r.Context()); ok && principal.UserID != "" {
				note.ActorID = principal.UserID
			}
		}
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
		// The workflow drains the note at the nearest turn boundary and appends
		// it to the conversation; signaling a terminal run fails here with 409,
		// which the client turns into a fresh-run fallback.
		if err := temporalClient.SignalWorkflow(r.Context(), workflowID, "", agent.UserNoteSignal, note); err != nil {
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
	mux.HandleFunc("GET /v1/agent/cost/task", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		costAPI.TaskCostHandler(w, r)
	})
	mux.HandleFunc("GET /v1/agent/cost/project-budget", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		costAPI.ProjectBudgetHandler(w, r)
	})
	// Projects overview (docs/system-dashboard.md §3.2): server-side window
	// aggregation per project. The gate only checks the reader role — project
	// visibility is resolved the same way as GET /v1/workspace/projects and
	// applied to the aggregated rows, so a scoped reader sees their own slice.
	mux.HandleFunc("GET /v1/agent/cost/projects-overview", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		visible, err := overviewVisibleProjects(r, ws)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		costAPI.ProjectsOverviewHandlerWithVisible(w, r, visible)
	})
	// Team run rollup (docs/agent-teams.md §10): the whole child-run tree of
	// one team program run — tokens, cost, durations, approvals, rework.
	mux.HandleFunc("GET /v1/agent/cost/team-run", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		costAPI.TeamRunCostHandler(w, r)
	})
	liveness := &temporalLiveness{client: temporalClient}
	mux.HandleFunc("GET /v1/agent/operations", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		// Empty project = global listing constrained to the principal's visible
		// projects (docs/activity-scoping.md §5) — no more 403 "set a project
		// filter" for scoped tokens.
		principal, allowed := gate.AllowQuery(w, r, controlplane.RoleReader, project)
		if !allowed {
			return
		}
		operations, err := activities.UncertainOperations(r.Context(), project, liveness)
		if err != nil {
			// The listing pages through the journal; its failures used to vanish
			// into a bare 500 with no server-side trace. Log the cause so the next
			// occurrence is diagnosable from docker logs without guessing.
			log.Warn("list uncertain operations", "project", project, "error", err)
			writeError(w, 500, err)
			return
		}
		if project == "" {
			if visible, all := principal.AllowedProjects(); !all {
				filtered := make([]agent.UncertainOperation, 0, len(operations))
				for _, operation := range operations {
					if slices.Contains(visible, operation.Project) {
						filtered = append(filtered, operation)
					}
				}
				operations = filtered
			}
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

	// wizardModelClient serves every description→draft wizard (agent, skill,
	// team): the env gateway when configured, otherwise the first workspace
	// provider. Fresh installs configured purely through onboarding have no env
	// gateway — activities.Model is nil there — and the wizards must keep
	// working the same way runs do, through the provider registry. On failure it
	// writes the 502 response itself and returns nil.
	wizardModelClient := func(w http.ResponseWriter, r *http.Request) *llm.Client {
		if activities.Model != nil {
			return activities.Model
		}
		client, err := workspaceDraftModel(r.Context(), ws, func(ctx context.Context, ref string) (string, error) {
			return resolveProviderAPIKey(ctx, ws, ref, secretMasterKey)
		})
		if err != nil {
			writeError(w, 502, err)
			return nil
		}
		return client
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
		headID := derefString(req.HeadUserID)
		if headID != "" {
			if head, err := ws.GetUser(r.Context(), headID); err != nil || !head.Active {
				writeError(w, 422, fmt.Errorf("head_user_id %q is not an active user", headID))
				return
			}
		}
		unit := &workspace.OrgUnit{ID: req.ID, ParentID: req.ParentID, Kind: req.Kind, Name: req.Name, HeadUserID: headID}
		if err := ws.CreateOrgUnit(r.Context(), unit); err != nil {
			writeError(w, 409, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditOrgUnitCreated, "org_unit", unit.ID, map[string]any{"name": unit.Name, "parent_id": unit.ParentID, "head_user_id": unit.HeadUserID}); err != nil {
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
		current, err := ws.GetOrgUnit(r.Context(), r.PathValue("unitID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// Absent head_user_id keeps the current head (move-only or rename-only
		// requests); an explicit value — including "" — sets or clears it.
		headID := current.HeadUserID
		if req.HeadUserID != nil {
			headID = derefString(req.HeadUserID)
		}
		if headID != "" {
			if head, gErr := ws.GetUser(r.Context(), headID); gErr != nil || !head.Active {
				writeError(w, 422, fmt.Errorf("head_user_id %q is not an active user", headID))
				return
			}
		}
		unit := workspace.OrgUnit{ID: current.ID, Kind: req.Kind, Name: req.Name, HeadUserID: headID}
		if err := ws.UpdateOrgUnit(r.Context(), unit); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditOrgUnitUpdated, "org_unit", unit.ID, map[string]any{"name": unit.Name, "kind": unit.Kind, "head_user_id": unit.HeadUserID}); err != nil {
			log.Warn("audit org unit update", "error", err)
		}
		// Optional re-parenting: parent_id present and different triggers a move.
		if req.ParentID != "" {
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

	// First-run onboarding (docs/ROADMAP.md — bootstrap): status tells the UI
	// what is still missing before the installation is usable, the build
	// endpoint lets the wizard produce the sandbox image without a shell.
	mux.HandleFunc("GET /v1/setup/status", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		projects, err := ws.ListProjects(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		providers, err := ws.ListProviders(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		sandboxStatus := map[string]any{"configured": false, "image": "", "available": false}
		if swappable := activities.SandboxSwappable(); swappable != nil {
			if image := swappable.Image(); image != "" {
				sandboxStatus["configured"] = true
				sandboxStatus["image"] = image
				sandboxStatus["available"] = sandbox.ImageAvailable(r.Context(), env("KERNEL_DOCKER_BINARY", "docker"), image)
			}
		}
		// A digest pinned via KERNEL_SANDBOX_IMAGE is an operator decision the
		// build endpoint refuses to override — the wizard needs to know that
		// up front, so the sandbox step renders "skip" instead of a button
		// that can only ever answer 409.
		sandboxStatus["pinned_by_env"] = strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_IMAGE")) != ""
		writeJSON(w, 200, map[string]any{
			"needs_setup":           !hasUserProjects(projects),
			"auth_enabled":          gate.Enabled(),
			"master_key_configured": secretMasterKey != nil,
			"sandbox":               sandboxStatus,
			"model_configured":      len(providers) > 0 || strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_API_KEY")) != "",
		})
	})

	// Tool readiness (docs/onboarding-readiness): every background feature
	// that silently degrades when a piece of configuration is missing — which
	// tools stop working, and what to toggle. The dashboard renders this as a
	// checklist; tool errors name the same keys.
	mux.HandleFunc("GET /v1/setup/readiness", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		judgeCfg, jErr := ws.JudgeConfig(r.Context())
		judgeOK := jErr == nil && judgeCfg.Configured
		defaultModelEnv := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_BASE_URL")) != "" && strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID")) != ""
		// Channels are a user choice, not a requirement: an install may have
		// none, or add one later. The check stays informational — it only
		// flags anything when NO channel is configured, and marks itself
		// optional so the dashboard does not paint it as a misconfiguration.
		// default_model: only runs started WITHOUT an explicit model depend on
		// it. Background features (skill builder, extraction) ride the judge
		// when the default is absent, so a configured judge downgrades this
		// entry to informational.
		defaultModelEntry := map[string]any{
			"key":        "default_model",
			"configured": defaultModelEnv || judgeOK,
			"used_by":    []string{"runs started without an explicit model"},
		}
		if defaultModelEnv {
			defaultModelEntry["detail"] = "the env default serves runs that do not name a model"
		} else if judgeOK {
			defaultModelEntry["detail"] = "unset — the judge model covers background features; runs pick their model from workspace providers"
		} else {
			defaultModelEntry["fix"] = "Set TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID in .env, or pick a judge model in Agents → Quality settings"
		}
		matrixOK := false
		anyChannelConfigured := false
		for _, channelType := range []string{"matrix", "telegram", "slack"} {
			if driver := agent.ChannelDriverByTypePublic(channelType); driver != nil {
				if channelSettings, cErr := activities.ChannelSettings(r.Context()); cErr == nil {
					if driver.IsConfigured(channelSettings.Channels[channelType]) {
						if channelType == "matrix" {
							matrixOK = true
						}
						anyChannelConfigured = true
					}
				}
			}
		}
		embedStatus := activities.EmbeddingsStatus()
		// Sandbox readiness mirrors the onboarding wizard's setup/status probe.
		sandboxOK := false
		if swappable := activities.SandboxSwappable(); swappable != nil && swappable.Image() != "" {
			sandboxOK = sandbox.ImageAvailable(r.Context(), env("KERNEL_DOCKER_BINARY", "docker"), swappable.Image())
		}
		// Channels: optional by nature. Always 'configured'; the detail line
		// appears only when nothing is set, phrased as availability, not error.
		channelsConfigured := matrixOK || anyChannelConfigured
		channelsEntry := map[string]any{
			"key":        "channels",
			"configured": true,
			"optional":   true,
			"used_by":    []string{"intake from messengers", "ask_human and send_file delivery to messengers"},
		}
		if !channelsConfigured {
			channelsEntry["detail"] = "no chat transport configured — agents reach you only in the web inbox; add one under Channels when needed"
		}
		checks := []map[string]any{
			defaultModelEntry,
			{"key": "judge_model", "configured": judgeOK, "used_by": []string{"skill_propose (fallback)", "knowledge contradiction triage (B1)", "knowledge hygiene (B4)", "area normalization"},
				"fix": "Agents → Quality settings → pick a judge model"},
			{"key": "embeddings", "configured": embedStatus.Ready, "used_by": []string{"semantic hints", "knowledge conflict prediction (C5)", "semantic knowledge search"},
				"fix": "Wait for the model to load (first boot downloads it); check KERNEL_EMBEDDINGS_MODEL", "detail": embedStatus.Error},
			{"key": "sandbox_image", "configured": sandboxOK, "used_by": []string{"run_command", "write_file", "read_file", "web_fetch (page rendering)"},
				"fix": "Run the sandbox build in onboarding, or set KERNEL_SANDBOX_IMAGE"},
			{"key": "journal_auth", "configured": strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")) != "" || !gate.Enabled(), "used_by": []string{"operations panel", "knowledge extraction", "saga tools"},
				"fix": "Fresh installs auto-provision the service principal; set TEMPORALITY_API_TOKEN only for a custom journal", "detail": "the kernel re-resolves the token every minute"},
			channelsEntry,
		}
		writeJSON(w, 200, map[string]any{"checks": checks})
	})
	mux.HandleFunc("POST /v1/setup/sandbox/build", func(w http.ResponseWriter, r *http.Request) {
		// Building a sandbox image changes what model-authored commands may
		// execute — an operator-level decision even on a fresh install.
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_IMAGE")) != "" {
			writeError(w, 409, errors.New("KERNEL_SANDBOX_IMAGE is pinned in the environment; rebuild through sandbox/build.sh to change it"))
			return
		}
		swappable := activities.SandboxSwappable()
		if swappable == nil {
			writeError(w, 500, errors.New("sandbox runner is not swappable"))
			return
		}
		ref, err := sandbox.BuildEmbedded(r.Context(), env("KERNEL_DOCKER_BINARY", "docker"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
		if root == "" {
			root = ".sandbox"
		}
		runner, err := sandbox.New(ref, root)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if err := ws.SetSetting(r.Context(), "sandbox_image", ref); err != nil {
			writeError(w, 500, err)
			return
		}
		swappable.Swap(runner)
		log.Info("sandbox image built and pinned through onboarding", "image", ref, "root", runner.Root)
		writeJSON(w, 200, map[string]any{"image": ref})
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
		project := &workspace.Project{ID: req.ID, Name: req.Name, Description: req.Description, DefaultAgentID: req.DefaultAgentID, DefaultModel: req.DefaultModel, OrgUnitIDs: req.OrgUnits, IntakeEnabled: req.IntakeEnabled != nil && *req.IntakeEnabled}
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
		project := workspace.Project{ID: r.PathValue("projectID"), Name: req.Name, Description: req.Description, DefaultAgentID: req.DefaultAgentID, DefaultModel: req.DefaultModel}
		// Intake opt-in changes only when the payload carries it; otherwise the
		// stored flag survives (the store's UPDATE always writes the struct
		// field, so pre-read the current value first).
		if req.IntakeEnabled != nil {
			project.IntakeEnabled = *req.IntakeEnabled
		} else if current, gErr := ws.GetProject(r.Context(), project.ID); gErr == nil {
			project.IntakeEnabled = current.IntakeEnabled
		}
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
	mux.HandleFunc("POST /v1/workspace/projects/{projectID}/files", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		// Uploaded files land in the project's persistent sandbox workspace, so
		// agents read them at /workspace/uploads/... like any other project file.
		root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
		if root == "" {
			writeError(w, 503, errors.New("file uploads require KERNEL_SANDBOX_ROOT to be configured"))
			return
		}
		projectID := r.PathValue("projectID")
		if _, err := ws.GetProject(r.Context(), projectID); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 200<<20)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, 413, err)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, 422, errors.New("multipart field 'file' is required"))
			return
		}
		defer file.Close()
		name := filepath.Base(filepath.Clean(header.Filename))
		if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
			writeError(w, 422, errors.New("invalid file name"))
			return
		}
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		final := slugify(translit(base)) + ext
		if final == "" || final == ext {
			final = "upload" + ext
		}
		dir := filepath.Join(root, "projects", projectID, "uploads")
		if err := os.MkdirAll(dir, 0o777); err != nil {
			writeError(w, 500, err)
			return
		}
		target := filepath.Join(dir, final)
		if _, err := os.Stat(target); err == nil {
			final = time.Now().UTC().Format("20060102-150405") + "-" + final
			target = filepath.Join(dir, final)
		}
		dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		defer dst.Close()
		written, err := io.Copy(dst, file)
		if err != nil {
			os.Remove(target)
			writeError(w, 500, err)
			return
		}
		if err := dst.Chmod(0o666); err != nil {
			log.Warn("chmod upload", "error", err) // container user must be able to read it
		}
		writeJSON(w, 201, map[string]any{
			"name": final,
			"path": "/workspace/uploads/" + final,
			"size": written,
		})
	})
	// GET mirrors POST /files: agents produce files in the sandbox (reports,
	// exports, archives) and send_file shares them by workspace path; the chat
	// turns those paths into download links through this endpoint. Reads are
	// sandboxed to the project's own workspace directory.
	mux.HandleFunc("GET /v1/workspace/projects/{projectID}/files", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
		if root == "" {
			writeError(w, 503, errors.New("file downloads require KERNEL_SANDBOX_ROOT to be configured"))
			return
		}
		projectID := r.PathValue("projectID")
		workspacePath := filepath.Join(root, "projects", projectID)
		pathArg := strings.TrimSpace(r.URL.Query().Get("path"))
		pathArg = strings.TrimPrefix(pathArg, "/workspace")
		pathArg = strings.TrimPrefix(pathArg, "/")
		if pathArg == "" {
			writeError(w, 400, errors.New("query parameter 'path' is required"))
			return
		}
		clean := filepath.Clean(pathArg)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			writeError(w, 422, errors.New("path escapes the workspace"))
			return
		}
		abs := filepath.Join(workspacePath, clean)
		if abs != workspacePath && !strings.HasPrefix(abs, workspacePath+string(filepath.Separator)) {
			writeError(w, 422, errors.New("path escapes the workspace"))
			return
		}
		info, err := os.Stat(abs)
		if err != nil {
			writeError(w, 404, err)
			return
		}
		if info.IsDir() {
			writeError(w, 422, errors.New("path is a directory"))
			return
		}
		name := filepath.Base(clean)
		contentType := mime.TypeByExtension(filepath.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		disposition := "attachment"
		if r.URL.Query().Get("inline") == "1" {
			disposition = "inline"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(name, `"`, "_")+`"`)
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		file, err := os.Open(abs)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		defer file.Close()
		if _, err := io.Copy(w, file); err != nil {
			log.Warn("serve workspace file", "project", projectID, "path", clean, "error", err)
			return
		}
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
		// Pending evolution proposals per agent (list badge). Scoped to the
		// agents this viewer can actually see so the counts leak nothing.
		counts, err := ws.PendingProposalCounts(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		visible := make(map[string]int, len(counts))
		for _, a := range agents {
			if counts[a.ID] > 0 {
				visible[a.ID] = counts[a.ID]
			}
		}
		writeJSON(w, 200, map[string]any{"agents": agents, "pending_proposals": visible})
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
		if err := validateAgentSandboxProfile(r.Context(), ws, req.SandboxProfile); err != nil {
			writeError(w, 422, err)
			return
		}
		if req.ID == "" {
			req.ID = slugify(req.Name)
		}
		a := &workspace.Agent{
			ID: req.ID, ProjectID: req.ProjectID, Name: req.Name, Description: req.Description,
			Model: req.Model, Provider: req.Provider, SystemPrompt: req.SystemPrompt,
			Temperature: req.Temperature, MaxTokens: req.MaxTokens,
			Skills: req.Skills, MCPServers: req.MCPServers, Secrets: req.Secrets, Tools: req.Tools,
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
		if err := validateAgentSandboxProfile(r.Context(), ws, req.SandboxProfile); err != nil {
			writeError(w, 422, err)
			return
		}
		a := workspace.Agent{
			ID: r.PathValue("agentID"), ProjectID: req.ProjectID, Name: req.Name, Description: req.Description,
			Model: req.Model, Provider: req.Provider, SystemPrompt: req.SystemPrompt,
			Temperature: req.Temperature, MaxTokens: req.MaxTokens,
			Skills: req.Skills, MCPServers: req.MCPServers, Secrets: req.Secrets, Tools: req.Tools,
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
			for _, secretName := range a.Secrets {
				secret, sErr := ws.GetSecretByName(r.Context(), secretName)
				if errors.Is(sErr, workspace.ErrNotFound) {
					continue
				}
				if sErr != nil {
					writeError(w, 500, sErr)
					return
				}
				if !workspace.OrgVisible(secret.OrgUnitID, units) {
					writeError(w, 403, fmt.Errorf("secret %q is not available in your org scope", secretName))
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
	// Agent evaluation suites and runs (docs/plan-evaluable-agent.md §2.2 stage 8).
	// A run is pinned to a definition version: the runner tests the compiled
	// prompt snapshot of that exact version (0 = current effective prompt).
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/evaluation-suite", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		suite, err := ws.GetAgentEvaluationSuite(r.Context(), r.PathValue("agentID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, suite)
	})
	mux.HandleFunc("PUT /v1/workspace/agents/{agentID}/evaluation-suite", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var suite workspace.AgentEvaluationSuite
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&suite); err != nil {
			writeError(w, 400, err)
			return
		}
		if len(suite.Cases) > 16 {
			writeError(w, 422, errors.New("at most 16 evaluation cases are supported"))
			return
		}
		// Judge-case validation (specs/quality-loop.md §6.1-§6.2, M4): the
		// check class must be known, judge checks need a 0-100 threshold and
		// a configured installation judge model — otherwise saving would
		// produce a suite whose runs can only end in error.
		judgeCases := false
		for i, c := range suite.Cases {
			if strings.TrimSpace(c.Input) == "" {
				writeError(w, 422, fmt.Errorf("case %d: input is required", i+1))
				return
			}
			switch workspace.NormalizeEvalCheck(c.Check) {
			case workspace.EvalCheckPatterns:
			case workspace.EvalCheckJudge, workspace.EvalCheckPatternsJudge:
				judgeCases = true
				if c.JudgeThreshold == nil || *c.JudgeThreshold < 0 || *c.JudgeThreshold > 100 {
					writeError(w, 422, fmt.Errorf("case %d: judge_threshold (0-100) is required for judge checks", i+1))
					return
				}
			default:
				writeError(w, 422, fmt.Errorf("case %d: unknown check %q (use patterns, judge or patterns+judge)", i+1, c.Check))
				return
			}
		}
		if judgeCases {
			judgeConfig, cfgErr := ws.JudgeConfig(r.Context())
			if cfgErr != nil {
				writeError(w, 500, cfgErr)
				return
			}
			if !judgeConfig.Configured {
				writeError(w, 422, errors.New("judge_not_configured: set EVAL_JUDGE_MODEL to a configured provider model before saving judge cases"))
				return
			}
		}
		agentID := r.PathValue("agentID")
		if _, err := ws.GetAgent(r.Context(), agentID); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.SaveAgentEvaluationSuite(r.Context(), agentID, suite.Cases); err != nil {
			writeError(w, 500, err)
			return
		}
		saved, _ := ws.GetAgentEvaluationSuite(r.Context(), agentID)
		writeJSON(w, 200, saved)
	})
	// POST runs the suite synchronously (one grounded model call per case) and
	// records the run; GET lists run history. The internal record sink lives at
	// .../evaluation-runs so the evaluator's loopback write cannot recurse into
	// a fresh evaluation.
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/evaluation-runs", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var run workspace.AgentEvaluationRun
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&run); err != nil {
			writeError(w, 400, err)
			return
		}
		run.AgentID = r.PathValue("agentID")
		if run.AgentVersion <= 0 {
			writeError(w, 422, errors.New("agent_version is required"))
			return
		}
		if err := ws.RecordAgentEvaluationRun(r.Context(), &run); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, run)
	})
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/evaluations", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		var req struct {
			Version int `json:"version"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
				writeError(w, 400, err)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if _, err := activities.RunAgentEvaluation(ctx, agentID, req.Version); err != nil {
			writeError(w, 422, err)
			return
		}
		runs, err := ws.ListAgentEvaluationRuns(r.Context(), agentID, 1)
		if err != nil || len(runs) == 0 {
			writeError(w, 500, errors.New("evaluation ran but the result could not be read back"))
			return
		}
		writeJSON(w, 200, runs[0])
	})
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/evaluations", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		runs, err := ws.ListAgentEvaluationRuns(r.Context(), r.PathValue("agentID"), 0)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"runs": runs})
	})
	// HITL verdicts of one run (specs/quality-loop.md §3, §8.1): every human
	// decision on the run — ask_human answers plus human approvals and manual
	// reconciliations — projected with a type, an author and the agent version
	// pinned at ask time. Reader role, like the evaluation endpoints.
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/runs/{runID}/verdicts", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		agentID, runID := r.PathValue("agentID"), r.PathValue("runID")
		a, err := ws.GetAgent(r.Context(), agentID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("agent not found"))
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(a.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		run, err := ws.GetRun(r.Context(), runID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("run not found"))
				return
			}
			writeError(w, 500, err)
			return
		}
		if run.AgentID != "" && run.AgentID != agentID {
			writeError(w, 404, errors.New("run not found"))
			return
		}
		verdicts, err := ws.RunVerdicts(r.Context(), run.ProjectID, run.RunID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{
			"run_id":        runID,
			"agent_version": run.AgentVersion,
			"outcome":       workspace.ClassifyRunOutcome(run.Status, verdicts),
			"verdicts":      verdicts,
		})
	})
	// Evolution proposals (docs/plan-evaluable-agent.md §2.2 stage 10): agents
	// and humans propose definition changes with full provenance; only a human
	// applies, through the regular update path so the version grows normally.
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/proposals", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		agentID := r.PathValue("agentID")
		a, err := ws.GetAgent(r.Context(), agentID)
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
		proposals, err := ws.ListAgentProposals(r.Context(), agentID, r.URL.Query().Get("status"), 0)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"proposals": proposals})
	})
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/proposals", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		var req struct {
			Problem        string                     `json:"problem"`
			Change         string                     `json:"change"`
			Effect         string                     `json:"effect"`
			Description    string                     `json:"description"`
			Definition     *workspace.AgentDefinition `json:"definition"`
			Evidence       []string                   `json:"evidence"`
			Author         string                     `json:"author"`
			GeneratorModel string                     `json:"generator_model"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Problem) == "" || strings.TrimSpace(req.Change) == "" {
			writeError(w, 422, errors.New("problem and change are required"))
			return
		}
		a, err := ws.GetAgent(r.Context(), agentID)
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
		// The proposal carries the full target definition; omitted parts default
		// to the agent's current state so a proposal can change only what it
		// mentions. Capabilities are copied from the current definition — an
		// agent-authored proposal must never grant itself capabilities.
		definition := workspace.AgentDefinition{}
		if a.Definition != nil {
			definition = *a.Definition
		}
		if req.Definition != nil {
			// Absent field = keep the current value; an explicit empty array
			// clears it. Partial proposals never wipe what they do not mention.
			if req.Definition.Constraints != nil {
				definition.Constraints = req.Definition.Constraints
			}
			if req.Definition.Completion != nil {
				definition.Completion = req.Definition.Completion
			}
		}
		proposal := workspace.AgentProposal{
			AgentID: a.ID, BaseVersion: a.DefinitionVersion,
			Definition:  definition,
			Description: req.Description, Problem: req.Problem, Change: req.Change, Effect: req.Effect,
			Evidence: req.Evidence, Author: req.Author, GeneratorModel: req.GeneratorModel,
		}
		if proposal.Description == "" {
			proposal.Description = a.Description
		}
		// Manual proposals carry the deciding user as author; agent proposals
		// name their run explicitly.
		if proposal.Author == "" {
			proposal.Author = requestSubject(r)
		}
		if err := ws.CreateAgentProposal(r.Context(), &proposal); err != nil {
			writeError(w, 500, err)
			return
		}
		emitAgentDefinitionEvent(r.Context(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), "agent.definition.proposed", a, proposal, actorFromRequest(r), nil)
		writeJSON(w, 201, proposal)
	})
	// Applying is gated (specs/quality-loop.md §5 — cmd/agent-kernel/
	// evalgate.go): a fresh green eval run on the proposal's base version
	// is required, red blocks unless a writer overrides with justification,
	// no run starts one and answers 202. The cleared apply continues on the
	// transactional Store.ApplyAgentProposal (§10.1a): claim + version++ +
	// immutable snapshot land atomically (docs/living-skills.md §26: agents
	// propose, people apply). The bool result tells the gate handler whether
	// the apply landed, so the gate:false path journals its skip only after
	// a real apply (F-1). A gate:false apply passes its audit event along
	// (F-2): the skip record joins the apply's own transaction
	// (Store.ApplyAgentProposalAudited), so a failed audit INSERT rolls the
	// apply back and answers 503 audit_write_failed — a gate-less apply can
	// never land unjournaled (fail-closed, CWE-778).
	RegisterEvalGateRoutes(mux, gate, ws, kernelGateRunner{activities: activities}, events,
		func(w http.ResponseWriter, r *http.Request, a workspace.Agent, proposal workspace.AgentProposal, audit *workspace.AccessAuditEvent) bool {
			next := a
			next.Description = proposal.Description
			definition := proposal.Definition
			next.Definition = &definition
			// Bindings never change through a proposal (mirror of the PUT freeze).
			next.OrgUnitID = a.OrgUnitID
			if agentSemanticsChanged(a, next) {
				next.DefinitionVersion++
			}
			var snapshot *workspace.AgentVersion
			if next.Definition != nil && next.DefinitionVersion != a.DefinitionVersion {
				version := agentVersionSnapshot(next, requestSubject(r), "proposal", proposal.GeneratorModel)
				snapshot = &version
			}
			var decided workspace.AgentProposal
			var err error
			if audit != nil {
				decided, err = ws.ApplyAgentProposalAudited(r.Context(), next, proposal.ID, requestSubject(r), snapshot, audit)
			} else {
				decided, err = ws.ApplyAgentProposal(r.Context(), next, proposal.ID, requestSubject(r), snapshot)
			}
			if err != nil {
				switch {
				case errors.Is(err, workspace.ErrAuditWriteFailed):
					// F-2 fail-closed: the audit INSERT rode the apply
					// transaction, so the apply rolled back with it —
					// surface 503, never a success without a bypass trail.
					writeJSON(w, 503, map[string]any{"error": "audit_write_failed"})
				case errors.Is(err, workspace.ErrNotFound):
					writeError(w, 404, err)
				case errors.Is(err, workspace.ErrBaseVersionStale):
					writeJSON(w, 409, map[string]any{
						"error":           "base_version_stale",
						"base_version":    proposal.BaseVersion,
						"current_version": next.DefinitionVersion,
					})
				case errors.Is(err, workspace.ErrConflict):
					writeError(w, 409, err)
				default:
					writeError(w, 500, err)
				}
				return false
			}
			emitAgentDefinitionEvent(r.Context(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), "agent.definition.applied", next, decided, actorFromRequest(r),
				map[string]any{"from_version": a.DefinitionVersion, "to_version": next.DefinitionVersion})
			writeJSON(w, 200, map[string]any{"agent": next, "proposal": decided})
			return true
		},
		visibleUnits, time.Now, log)
	// Artifact lifecycle routes (docs/artifact-lifecycle.md §6): submit a
	// draft to review, list review rounds, resolve them by human decision.
	// Reviewer notices ride the channel registry (§6 Phase 1), best-effort.
	RegisterArtifactLifecycleRoutes(mux, gate, ws, events, &channelReviewNotifier{
		ws: ws, settings: activities.ChannelSettings, client: activities.HTTP, log: log,
	})
	// Manual saga management (docs/sagas.md §8): rename, merge and task
	// re-binding from the UI. Event-sourced like the agent tools — the routes
	// only validate against the journal and emit through the same outbox.
	RegisterSagaRoutes(mux, gate, events, observationURL)
	// Server-side chat persistence (docs/resilience-and-scale.md §4).
	RegisterChatRoutes(mux, gate, ws)
	// Research sessions (docs/human-knowledge-explorer.md §11): saved
	// knowledge-explorer investigations — list/read for everyone with read
	// access, create/update/delete for writers.
	RegisterResearchSessionRoutes(mux, gate, ws)
	// Sandbox profile registry (docs/sandbox-profiles.md §6): the catalog of
	// execution environments agents may be assigned to, plus builtin builds.
	RegisterSandboxProfileRoutes(mux, gate, ws, events, dockerProfileBuilder{binary: sandboxDockerBinary()})
	// Storage inventory (docs/sandbox-storage.md §5, phase 1 read-only): the
	// sandbox filesystem footprint, per-project trees and host health.
	storageRoot := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
	storageSnapshots := &storageSnapshotCache{}
	RegisterStorageRoutes(mux, gate, func(r *http.Request) (map[string][]string, error) {
		return overviewVisibleProjects(r, ws)
	}, storageRoot, sandboxDockerBinary(), storageSnapshots, ws)
	// Sandbox profile wizard (docs/sandbox-profiles.md): plain-language task
	// description → profile draft via the supervisor-scope wizard client.
	RegisterSandboxBuilderRoutes(mux, gate, wizardModelClient)
	// agentskills.io import: folder or archive → parsed SKILL.md draft for the
	// skill editor. Pure parse, no persistence — saving follows the normal flow.
	RegisterSkillImportRoutes(mux, gate)
	// Rejecting keeps the proposal in history; a rejected proposal can never
	// be applied — deciding twice is a conflict.
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/proposals/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		proposalID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, 400, errors.New("invalid proposal id"))
			return
		}
		a, _, ok := loadAgentProposal(w, r, ws, agentID, proposalID)
		if !ok {
			return
		}
		if !workspace.OrgVisible(a.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		decided, err := ws.DecideAgentProposal(r.Context(), proposalID, "rejected", requestSubject(r))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		emitAgentDefinitionEvent(r.Context(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), "agent.definition.rejected", a, decided, actorFromRequest(r), nil)
		writeJSON(w, 200, decided)
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
		a, err := resolveAgentRef(r.Context(), ws, r.PathValue("agentID"), project)
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
		// The wizard runs on fresh installs configured purely through
		// onboarding: a workspace provider exists, but the env gateway
		// (activities.Model) is nil. wizardModelClient falls back to a workspace
		// provider client — the same registry run routing uses.
		modelClient := wizardModelClient(w, r)
		if modelClient == nil {
			return
		}
		draft, err := agent.BuildAgentDraft(r.Context(), modelClient, req.Description, activities.ToolsSummary(), req.Final)
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
		// The chat stores the whole conversation transcript in the task prompt,
		// so the body grows with every turn — same 1MB ceiling as POST /v1/agent/runs.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" || strings.TrimSpace(req.ProjectID) == "" {
			writeError(w, 422, errors.New("title, prompt and project_id are required"))
			return
		}
		if req.ID == "" {
			// Transliterate first: slugify drops non-Latin runes, so a Cyrillic
			// title would collapse to "untitled". Cap the slug so the id stays
			// readable next to its timestamp suffix.
			slug := slugify(translit(req.Title))
			if runes := []rune(slug); len(runes) > 48 {
				slug = string(runes[:48])
			}
			req.ID = slug + "-" + time.Now().UTC().Format("20060102-150405")
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
		// Follow-up chat turns PATCH the full accumulated transcript into the
		// prompt; 64KB broke long conversations (docs: chat transcript in prompt).
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
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
			Title:   task.Title,
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
	// resolveRunState merges the stored run row with its live Temporal state:
	// the AgentRun workflow does not write its result back, so the row alone
	// stays "started" forever — the terminal status and the answer exist only
	// in the workflow result until this enrichment lazily persists them. The
	// API route and the channel answer watcher share it; project "" takes the
	// row's own project.
	resolveRunState := func(ctx context.Context, project, runID string) (workspace.Run, error) {
		run, err := ws.GetRun(ctx, runID)
		if err != nil {
			return workspace.Run{}, err
		}
		if project == "" {
			project = run.ProjectID
		}
		workflowID := workflowIDFor(activities.SourceID, project, run.RunID)
		response, tErr := temporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
		if tErr != nil || response.WorkflowExecutionInfo == nil {
			return run, nil
		}
		run.Status = workflowStatusName(response.WorkflowExecutionInfo.Status)
		if response.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
			return run, nil
		}
		var result agent.RunResult
		if gErr := temporalClient.GetWorkflow(ctx, workflowID, "").Get(ctx, &result); gErr != nil {
			return run, nil
		}
		run.Answer = result.Answer
		run.Turns = result.Turns
		run.Status = result.Status
		if run.Status == "" {
			run.Status = "completed"
		}
		_ = ws.UpdateRunStatus(ctx, run.ID, run.Status, run.Answer, run.Turns, "")
		return run, nil
	}
	// runJournal fetches one page of a run's journal events, oldest first —
	// the channel answer watcher tails them for chat progress updates.
	runJournal := func(ctx context.Context, runID string) ([]observation.Event, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			observationURL+"/v1/observations/events?run="+url.QueryEscape(runID)+"&limit=500", nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+os.Getenv("TEMPORALITY_API_TOKEN"))
		response, err := activities.HTTP.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("journal events returned HTTP %d", response.StatusCode)
		}
		var page struct {
			Events []observation.Event `json:"events"`
		}
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
			return nil, err
		}
		return page.Events, nil
	}
	mux.HandleFunc("GET /v1/workspace/runs/{runID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		run, err := resolveRunState(r.Context(), "", r.PathValue("runID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
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
		// Child runs carry no workspace_run row; the journal resolves their
		// project (see /trajectory).
		run, err := ws.GetRun(r.Context(), runID)
		if err != nil && !errors.Is(err, workspace.ErrNotFound) {
			writeError(w, 500, err)
			return
		}
		project := ""
		if err == nil {
			project = run.ProjectID
		} else if resolved, perr := ws.RunProjectID(r.Context(), runID); perr == nil {
			project = resolved
		} else {
			writeError(w, 404, workspace.ErrNotFound)
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
		if r.URL.Query().Get("tail") == "1" {
			// Reconnect mode: replay only the recent past — the latest answer
			// fragment, the last activity lines and, when the run finished while
			// the client was away, its terminal event — then continue live from
			// the journal tail. A full replay of a long run floods the chat with
			// hundreds of stale updates before reaching the present.
			events, next, tErr := ws.StreamRunEventsTail(r.Context(), project, runID, 50)
			if tErr == nil {
				for _, ev := range events {
					data, _ := json.Marshal(ev)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
					flusher.Flush()
					if ev.Type == "run.completed" || ev.Type == "run.failed" || ev.Type == "run.cancelled" {
						done = true
					}
				}
				cursor = next
			} else {
				slog.Warn("stream tail fallback to full replay", "error", tErr.Error())
			}
		}
		for !done {
			select {
			case <-ctx.Done():
				done = true
			default:
			}
			if done {
				break
			}
			events, nextCursor, eErr := ws.StreamRunEvents(r.Context(), project, runID, cursor, 50)
			if eErr == nil {
				for _, ev := range events {
					data, _ := json.Marshal(ev)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
					flusher.Flush()
					// run.cancelled is terminal too: without it a cancelled run's
					// stream never sends `done` and the chat spinner never stops.
					if ev.Type == "run.completed" || ev.Type == "run.failed" || ev.Type == "run.cancelled" {
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

	// Ephemeral token stream: live model output (answer text + reasoning) as
	// it is generated, fed by the CallModel activity through the in-memory
	// TokenBus. Tokens never enter the journal; the run's terminal state is
	// observed through Temporal so the stream ends with a done event even when
	// nobody else asks for the run.
	mux.HandleFunc("GET /v1/workspace/runs/{runID}/tokens", func(w http.ResponseWriter, r *http.Request) {
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
		events, snapshot, cancel := activities.Tokens.Subscribe(runID)
		defer cancel()
		writeEvent := func(event agent.TokenEvent) bool {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
			return event.Type == "done"
		}
		if snapshot.Type == "done" {
			writeEvent(TokenDoneEvent)
			return
		}
		if snapshot.Text != "" || snapshot.Reasoning != "" || snapshot.Args != "" {
			writeEvent(snapshot)
		}
		ctx := r.Context()
		statusTicker := time.NewTicker(5 * time.Second)
		defer statusTicker.Stop()
		terminal := func() bool {
			description, dErr := temporalClient.DescribeWorkflowExecution(ctx, workflowIDFor(activities.SourceID, run.ProjectID, run.RunID), "")
			if dErr != nil {
				return false
			}
			if description.WorkflowExecutionInfo == nil {
				return false
			}
			switch description.WorkflowExecutionInfo.Status {
			case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED,
				enums.WORKFLOW_EXECUTION_STATUS_FAILED,
				enums.WORKFLOW_EXECUTION_STATUS_CANCELED,
				enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
				enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
				enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
				return true
			default:
				return false
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok || writeEvent(event) {
					// Bus closed (run finished): also drop the bus entry so late
					// subscribers get done immediately.
					activities.Tokens.Close(runID)
					return
				}
			case <-statusTicker.C:
				if terminal() {
					activities.Tokens.Close(runID)
					writeEvent(TokenDoneEvent)
					return
				}
			}
		}
	})
	mux.HandleFunc("GET /v1/workspace/runs/{runID}/trajectory", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		runID := r.PathValue("runID")
		// The journal is the source of truth: delegated child runs never get a
		// workspace_run row (only API/trigger/intake starts do), so a missing
		// row must not block the extraction. Project resolution: the run row
		// when present, then the ?project= param, then the run's own events.
		project := r.URL.Query().Get("project")
		if run, err := ws.GetRun(r.Context(), runID); err == nil {
			project = run.ProjectID
		} else if errors.Is(err, workspace.ErrNotFound) && project == "" {
			if resolved, perr := ws.RunProjectID(r.Context(), runID); perr == nil {
				project = resolved
			}
		} else if !errors.Is(err, workspace.ErrNotFound) && project == "" {
			writeError(w, 500, err)
			return
		}
		if project == "" {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		events, _, eErr := ws.StreamRunEvents(r.Context(), project, runID, "", 500)
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
				Context:    struct{ Run, Project string }{Run: runID, Project: project},
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
			// Same journal-first project resolution as the single-run endpoint:
			// compared runs are often delegated children without a run row.
			project := ""
			if run, err := ws.GetRun(r.Context(), runID); err == nil {
				project = run.ProjectID
			} else if resolved, perr := ws.RunProjectID(r.Context(), runID); perr == nil {
				project = resolved
			} else {
				return nil, workspace.ErrNotFound
			}
			events, _, eErr := ws.StreamRunEvents(r.Context(), project, runID, "", 500)
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
					Context: struct{ Run, Project string }{Run: runID, Project: project},
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
	// Catalog sync (providermodels.go): mirrors workspace_provider.models
	// with the provider's own GET /models listing — a background loop on a
	// schedule, a hook right after save, and manual refresh/probe endpoints.
	modelSync := NewProviderModelSync(ws, func(ctx context.Context, ref string) (string, error) {
		return resolveProviderAPIKey(ctx, ws, ref, secretMasterKey)
	}, log)
	modelSync.RegisterRoutes(mux, gate)
	go modelSync.Loop(ctx, providerModelsRefreshInterval(log))
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
		// An operator who typed a model list curated it: tag the row so the
		// background loop never overwrites it. An empty list means "discover
		// from the provider" — sync starts right after the row lands.
		if len(provider.Models) > 0 {
			provider.Labels = markProviderModelsSource(provider.Labels, providerModelsManual)
		}
		if err := ws.CreateProvider(r.Context(), provider); err != nil {
			writeError(w, 500, err)
			return
		}
		if len(provider.Models) == 0 {
			modelSync.RefreshAsync(ctx, provider.ID)
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
		// Same catalog-ownership contract as create: a non-empty model list is
		// a curated one (loop must not touch it), an empty list returns the
		// provider to auto-discovery.
		if len(provider.Models) > 0 {
			provider.Labels = markProviderModelsSource(provider.Labels, providerModelsManual)
		} else {
			delete(provider.Labels, providerModelsSourceLabel)
		}
		if err := ws.UpdateProvider(r.Context(), provider); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if len(provider.Models) == 0 {
			modelSync.RefreshAsync(ctx, provider.ID)
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

	// Model routing: which provider serves a model for a project. The kernel's
	// call_model activity hits this on the hot path (60s TTL cache on the
	// caller side) to route non-default models to the right provider instead
	// of falling back to the env provider with a swapped model name.
	mux.HandleFunc("GET /v1/workspace/model-config", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		model := strings.TrimSpace(r.URL.Query().Get("model"))
		if model == "" {
			writeError(w, 422, errors.New("model is required"))
			return
		}
		project := strings.TrimSpace(r.URL.Query().Get("project"))
		// Scope routing to the project's org units; an empty (non-nil) slice
		// keeps ListProvidersVisible filtering to global providers only — a
		// project without org links must not borrow providers from random
		// departments.
		units := []string{}
		if project != "" {
			linked, err := ws.ProjectOrgUnits(r.Context(), project)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			units = linked
		}
		providers, err := ws.ListProvidersVisible(r.Context(), units)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		for _, p := range providers {
			for _, m := range p.Models {
				if m == model {
					apiKey, kErr := resolveProviderAPIKey(r.Context(), ws, p.APIKeyRef, secretMasterKey)
					if kErr != nil {
						// Routing found a provider but its credential did not resolve —
						// a misconfigured secret must surface as an error, not as a
						// silent fallback to the env provider.
						writeError(w, 502, fmt.Errorf("provider %q credential: %w", p.ID, kErr))
						return
					}
					payload := map[string]any{"provider_id": p.ID, "base_url": p.BaseURL, "model": m}
					// Plaintext credentials leave the process only for the kernel's
					// service principal (the call_model hot path). Human callers get
					// the routing answer with the reference, not the key — the same
					// boundary the secret resolve endpoint enforces.
					if principal, ok := controlplane.FromContext(r.Context()); ok && principal.Subject == "kernel-internal" {
						payload["api_key"] = apiKey
					} else {
						payload["api_key_ref"] = p.APIKeyRef
					}
					writeJSON(w, 200, payload)
					return
				}
			}
		}
		writeError(w, 404, fmt.Errorf("no provider serves model %q", model))
	})

	// Triggers (docs/org-structure.md §18, §21): org-scoped, optionally bound
	// to an execution identity whose allowed-lists are re-checked at every
	// automated run start.
	mux.HandleFunc("GET /v1/workspace/triggers", func(w http.ResponseWriter, r *http.Request) {
		// Cross-project listing (docs/activity-scoping.md §5): the project
		// filter is optional — an absent filter shows every project the
		// principal may see (visible projects ∩ org-unit bindings), a concrete
		// one behaves like the per-project route.
		principal, allowed := gate.AllowQuery(w, r, controlplane.RoleReader, r.URL.Query().Get("project"))
		if !allowed {
			return
		}
		var projects []string // nil = no project constraint
		if project := r.URL.Query().Get("project"); project != "" {
			projects = []string{project}
		} else if visible, all := principal.AllowedProjects(); !all {
			projects = visible // possibly empty: the store's ANY-list then matches nothing
		}
		triggers, err := ws.ListTriggersScoped(r.Context(), projects)
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
		// Some clients (historically the UI) send the config double-encoded —
		// a JSON string holding the document. Unwrap before storing: every
		// consumer unmarshals the config into a struct.
		req.Config = unwrapJSONString(req.Config)
		// Event triggers carry a constructor the listener evaluates (eventtriggers.go):
		// a malformed one must fail at save, not silently never-match.
		if req.Type == "event" {
			if err := validateEventTriggerConfig(req.Config); err != nil {
				writeError(w, 422, err)
				return
			}
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
		// Moving between projects is explicit: a trigger is born in whatever
		// project the creator had selected, and the name/config text may claim
		// another — the row's project_id is the truth. authorizeTriggerUse below
		// re-checks the new target. The webhook route embeds the project
		// (POST /v1/workspace/webhook/{projectID}/{path}), so the caller must
		// repoint the external system to the new URL; the per-trigger token
		// hash lives in the config and survives the move.
		if v, ok := hasString("project_id"); ok && v != "" && v != existing.ProjectID {
			existing.ProjectID = v
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
		// The per-trigger delivery token hash is managed exclusively through
		// the /webhook-token endpoints. A config round-trip from a form that
		// never saw the real hash must neither clobber nor forge it: capture
		// the stored hash before the config replacement and restore it after.
		storedWebhookTokenHash := webhookStoredTokenHash(existing.Config)
		if raw, ok := body["config"]; ok && string(raw) != "null" {
			var cfg json.RawMessage
			if json.Unmarshal(raw, &cfg) == nil && len(cfg) > 0 {
				// Tolerate double-encoded configs (JSON string holding the
				// document) the same way create does.
				existing.Config = unwrapJSONString(cfg)
			}
		}
		existing.Config = preserveWebhookTokenHash(existing.Config, storedWebhookTokenHash, existing.Type)
		// Same constructor validation as create: a type flip to event (or a bad
		// config edit) fails here, not silently in the listener.
		if existing.Type == "event" {
			if err := validateEventTriggerConfig(existing.Config); err != nil {
				writeError(w, 422, err)
				return
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
	// Per-trigger webhook delivery token (operator only): generate a fresh
	// "whk_…" credential whose sha256 hash lands in the trigger config. The
	// raw token is returned exactly once — losing it means regenerating. The
	// token authorizes deliveries to THIS trigger's endpoint only and is safe
	// to hand to external systems (TFS/Azure DevOps, CI, chat bots) without
	// exposing a workspace principal. DELETE revokes it.
	mux.HandleFunc("POST /v1/workspace/triggers/{triggerID}/webhook-token", func(w http.ResponseWriter, r *http.Request) {
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
		if trigger.Type != "webhook" {
			writeError(w, 422, errors.New("delivery tokens exist only for webhook triggers"))
			return
		}
		var cfg workspace.WebhookConfig
		if err := json.Unmarshal(trigger.Config, &cfg); err != nil {
			writeError(w, 422, errors.New("trigger config is not valid webhook config"))
			return
		}
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			writeError(w, 500, err)
			return
		}
		token := "whk_" + base64.RawURLEncoding.EncodeToString(random)
		digest := sha256.Sum256([]byte(token))
		cfg.AuthTokenHash = hex.EncodeToString(digest[:])
		merged, mErr := json.Marshal(cfg)
		if mErr != nil {
			writeError(w, 500, mErr)
			return
		}
		trigger.Config = merged
		if err := ws.UpdateTrigger(r.Context(), trigger); err != nil {
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTriggerUpdated, "trigger", trigger.ID, map[string]any{"webhook_token": "generated"}); err != nil {
			log.Warn("audit webhook token generate", "error", err)
		}
		writeJSON(w, 200, map[string]any{"trigger_id": trigger.ID, "token": token})
	})
	mux.HandleFunc("DELETE /v1/workspace/triggers/{triggerID}/webhook-token", func(w http.ResponseWriter, r *http.Request) {
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
		var cfg workspace.WebhookConfig
		if json.Unmarshal(trigger.Config, &cfg) != nil || cfg.AuthTokenHash == "" {
			writeJSON(w, 200, map[string]any{"trigger_id": trigger.ID, "revoked": false})
			return
		}
		cfg.AuthTokenHash = ""
		merged, mErr := json.Marshal(cfg)
		if mErr != nil {
			writeError(w, 500, mErr)
			return
		}
		trigger.Config = merged
		if err := ws.UpdateTrigger(r.Context(), trigger); err != nil {
			writeError(w, 500, err)
			return
		}
		if err := ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTriggerUpdated, "trigger", trigger.ID, map[string]any{"webhook_token": "revoked"}); err != nil {
			log.Warn("audit webhook token revoke", "error", err)
		}
		writeJSON(w, 200, map[string]any{"trigger_id": trigger.ID, "revoked": true})
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
		// Skill-manifest invariant (docs/artifact-lifecycle.md §8): artifacts[]
		// may reference approved versions only — a draft or rejected work product
		// never enters an active skill.
		if err := ws.ArtifactRefsApproved(r.Context(), skill.Manifest); err != nil {
			writeError(w, 422, err)
			return
		}
		if skill.ID == "" {
			writeError(w, 422, errors.New("id is required"))
			return
		}
		if req.Draft {
			// Agent proposal for a brand-new capability: the skill appears in the
			// UI as a draft and is never served to agents until a human applies it.
			if err := ws.CreateSkillDraft(r.Context(), &skill, req.provenance(workspace.SkillOriginAgentProposal)); err != nil {
				writeError(w, 409, err)
				return
			}
			writeJSON(w, 201, skill)
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
		// skill_inspect enrichment (docs/artifact-registry.md §3.4): manifest
		// references resolve to registry rows — title, applicability description,
		// provider — never bodies, so the agent decides whether artifact_read is
		// worth its tokens before spending them.
		response := map[string]any{}
		if raw, err := json.Marshal(skill); err == nil {
			_ = json.Unmarshal(raw, &response)
		}
		var refs struct {
			Artifacts []skills.ArtifactRef `json:"artifacts"`
		}
		if len(skill.Manifest) > 0 {
			_ = json.Unmarshal(skill.Manifest, &refs)
		}
		if len(refs.Artifacts) > 0 {
			ids := make([]string, 0, len(refs.Artifacts))
			notes := make(map[string]string, len(refs.Artifacts))
			for _, ref := range refs.Artifacts {
				ids = append(ids, ref.ID)
				if ref.Note != "" {
					notes[ref.ID] = ref.Note
				}
			}
			if rows, err := ws.ArtifactsByIDs(r.Context(), ids); err == nil {
				views := make([]map[string]any, 0, len(rows))
				for _, row := range rows {
					if !workspace.OrgVisible(row.OrgUnitID, visibleUnits(r)) {
						continue
					}
					view := map[string]any{
						"id": row.ID, "title": row.Title, "description": row.Description,
						"provider": row.Provider, "size_bytes": row.SizeBytes,
						"status": row.Status, "version": row.Version,
					}
					// Invariant warning (docs/artifact-lifecycle.md §8): a referenced
					// artifact that lost its approved version (rollback edge) must not
					// silently pass for verified — the skill keeps working, the agent
					// learns to double-check before leaning on the document.
					if row.Status != workspace.ArtifactApproved {
						view["warning"] = fmt.Sprintf("artifact is %s (v%d): not approved — treat as unverified and do not rely on it as an established rule", row.Status, row.Version)
					}
					if row.ExternalRef != "" {
						view["external_ref"] = row.ExternalRef
					}
					if notes[row.ID] != "" {
						view["note"] = notes[row.ID]
					}
					views = append(views, view)
				}
				response["artifacts"] = views
			}
		}
		writeJSON(w, 200, response)
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
		if err := ws.ArtifactRefsApproved(r.Context(), skill.Manifest); err != nil {
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
		modelClient := wizardModelClient(w, r)
		if modelClient == nil {
			return
		}
		draft, err := agent.BuildSkillDraft(r.Context(), modelClient, req.Description, activities.ToolsSummary(), req.Final)
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
	// Propose a new draft version for an existing skill without touching the
	// current revision (docs/knowledge-evolution.md Wave A). Used by the
	// skill_propose agent tool; the draft goes live only via the apply endpoint.
	mux.HandleFunc("POST /v1/workspace/skills/{skillID}/versions", func(w http.ResponseWriter, r *http.Request) {
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
		if err := ws.ArtifactRefsApproved(r.Context(), skill.Manifest); err != nil {
			writeError(w, 422, err)
			return
		}
		// The manifest's own version stays advisory: an empty request version
		// picks the next free patch, an occupied one is a conflict.
		version, err := ws.ProposeSkillVersion(r.Context(), skill.ID, req.Version, skill.Markdown, skill.Manifest, req.provenance(workspace.SkillOriginAgentProposal))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, version)
	})
	// Apply a version: drafts become the current revision, applying an older
	// active version is the rollback path (docs/living-skills.md §24).
	mux.HandleFunc("POST /v1/workspace/skills/{skillID}/versions/{version}/apply", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		skillID, version := r.PathValue("skillID"), r.PathValue("version")
		skill, applied, err := ws.ApplySkillVersion(r.Context(), skillID, version)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		emitSkillLifecycleEvent(r.Context(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), "skill.applied", skillID, skill.Name, applied, actorFromRequest(r))
		writeJSON(w, 200, skill)
	})
	// Reject a draft proposal (docs/living-skills.md §23): the version stays in
	// history as rejected and can no longer be applied. Human-driven path —
	// agents propose, people decide.
	mux.HandleFunc("POST /v1/workspace/skills/{skillID}/versions/{version}/reject", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		skillID, version := r.PathValue("skillID"), r.PathValue("version")
		rejected, err := ws.RejectSkillVersion(r.Context(), skillID, version)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		skill, _ := ws.GetSkill(r.Context(), skillID)
		emitSkillLifecycleEvent(r.Context(), observationURL, os.Getenv("TEMPORALITY_API_TOKEN"), "skill.rejected", skillID, skill.Name, rejected, actorFromRequest(r))
		writeJSON(w, 200, rejected)
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

	// Skill evaluation suites and runs (docs/living-skills.md §29).
	mux.HandleFunc("GET /v1/workspace/skills/{skillID}/evaluation-suite", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		suite, err := ws.GetEvaluationSuite(r.Context(), r.PathValue("skillID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, suite)
	})
	mux.HandleFunc("PUT /v1/workspace/skills/{skillID}/evaluation-suite", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var suite workspace.EvaluationSuite
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&suite); err != nil {
			writeError(w, 400, err)
			return
		}
		if len(suite.Cases) > 16 {
			writeError(w, 422, errors.New("at most 16 evaluation cases are supported"))
			return
		}
		for i, c := range suite.Cases {
			if strings.TrimSpace(c.Input) == "" {
				writeError(w, 422, fmt.Errorf("case %d: input is required", i+1))
				return
			}
		}
		skillID := r.PathValue("skillID")
		if _, err := ws.GetSkill(r.Context(), skillID); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if err := ws.SaveEvaluationSuite(r.Context(), skillID, suite.Cases); err != nil {
			writeError(w, 500, err)
			return
		}
		saved, _ := ws.GetEvaluationSuite(r.Context(), skillID)
		writeJSON(w, 200, saved)
	})
	// POST runs the suite synchronously (one grounded model call per case) and
	// records the run; GET lists run history. The internal record sink lives at
	// .../evaluation-runs so the evaluator's loopback write cannot recurse into
	// a fresh evaluation.
	mux.HandleFunc("POST /v1/workspace/skills/{skillID}/evaluation-runs", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var run workspace.EvaluationRun
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&run); err != nil {
			writeError(w, 400, err)
			return
		}
		run.SkillID = r.PathValue("skillID")
		if run.SkillVersion == "" {
			writeError(w, 422, errors.New("skill_version is required"))
			return
		}
		if err := ws.RecordEvaluationRun(r.Context(), &run); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, run)
	})
	mux.HandleFunc("POST /v1/workspace/skills/{skillID}/evaluations", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		skillID := r.PathValue("skillID")
		var req struct {
			Version string `json:"version"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
				writeError(w, 400, err)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if _, err := activities.RunSkillEvaluation(ctx, skillID, req.Version); err != nil {
			writeError(w, 422, err)
			return
		}
		runs, err := ws.ListEvaluationRuns(r.Context(), skillID, 1)
		if err != nil || len(runs) == 0 {
			writeError(w, 500, errors.New("evaluation ran but the result could not be read back"))
			return
		}
		writeJSON(w, 200, runs[0])
	})
	mux.HandleFunc("GET /v1/workspace/skills/{skillID}/evaluations", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		runs, err := ws.ListEvaluationRuns(r.Context(), r.PathValue("skillID"), 0)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"runs": runs})
	})

	// Agent teams (docs/agent-teams.md Wave A): versioned definitions of
	// multi-agent compositions. The manifest (protocol + role slots) is data;
	// the runtime compiles it into plan/delegate (Wave B). Users edit the
	// structured form — team.yaml never surfaces as a user-facing format.
	// Team creation wizard (docs/agent-teams.md §6, wave E): natural language
	// in, a structured manifest draft out. Like the skill builder it creates
	// nothing — the draft only prefills the form, saving goes through the
	// regular team endpoints below.
	mux.HandleFunc("POST /v1/workspace/teams/builder", func(w http.ResponseWriter, r *http.Request) {
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
		// The roster grounds fixed bindings and skill requirements in agents
		// this viewer can actually see (same visibility as launch resolution).
		rosterAgents, rErr := ws.ListAgentsVisible(r.Context(), candidateVisibleUnits(r))
		if rErr != nil {
			writeError(w, 500, fmt.Errorf("list roster agents: %w", rErr))
			return
		}
		// Same onboarding fallback as the other wizards: without an env gateway
		// the draft model comes from the workspace provider registry.
		modelClient := wizardModelClient(w, r)
		if modelClient == nil {
			return
		}
		draft, err := agent.BuildTeamDraft(r.Context(), modelClient, req.Description, agent.FormatTeamRoster(rosterAgents), req.Final)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		writeJSON(w, 200, draft)
	})
	mux.HandleFunc("GET /v1/workspace/teams", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		list, err := ws.ListTeamsVisible(r.Context(), visibleUnits(r))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"teams": list, "count": len(list)})
	})
	mux.HandleFunc("POST /v1/workspace/teams", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req teamRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		team, prov, err := req.toTeam("")
		if err != nil {
			writeError(w, 422, err)
			return
		}
		if team.ID == "" {
			writeError(w, 422, errors.New("id is required"))
			return
		}
		if req.Draft {
			if err := ws.CreateTeamDraft(r.Context(), &team, prov); err != nil {
				writeError(w, 409, err)
				return
			}
			_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamCreated, "team", team.ID, map[string]any{"name": team.Name, "draft": true})
			writeJSON(w, 201, team)
			return
		}
		if err := ws.CreateTeam(r.Context(), &team); err != nil {
			writeError(w, 409, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamCreated, "team", team.ID, map[string]any{"name": team.Name, "version": team.Version})
		writeJSON(w, 201, team)
	})
	mux.HandleFunc("GET /v1/workspace/teams/{teamID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		team, err := ws.GetTeam(r.Context(), r.PathValue("teamID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(team.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		writeJSON(w, 200, team)
	})
	mux.HandleFunc("PUT /v1/workspace/teams/{teamID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req teamRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		team, _, err := req.toTeam(r.PathValue("teamID"))
		if err != nil {
			writeError(w, 422, err)
			return
		}
		// Editing content must not re-scope a team: bindings change only
		// through the admin binding endpoint (same rule as skills).
		if prev, err := ws.GetTeam(r.Context(), team.ID); err == nil {
			team.OrgUnitID = prev.OrgUnitID
		}
		if err := ws.UpdateTeam(r.Context(), &team); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamUpdated, "team", team.ID, map[string]any{"version": team.Version})
		writeJSON(w, 200, team)
	})
	mux.HandleFunc("DELETE /v1/workspace/teams/{teamID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		if err := ws.DeleteTeam(r.Context(), r.PathValue("teamID")); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamDeleted, "team", r.PathValue("teamID"), nil)
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("GET /v1/workspace/teams/{teamID}/versions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		versions, err := ws.ListTeamVersions(r.Context(), r.PathValue("teamID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"versions": versions})
	})
	mux.HandleFunc("GET /v1/workspace/teams/{teamID}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		version, err := ws.GetTeamVersion(r.Context(), r.PathValue("teamID"), r.PathValue("version"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, version)
	})
	mux.HandleFunc("POST /v1/workspace/teams/{teamID}/versions", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req teamVersionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := workspace.ValidateTeamManifest(req.Manifest); err != nil {
			writeError(w, 422, err)
			return
		}
		version, err := ws.ProposeTeamVersion(r.Context(), r.PathValue("teamID"), req.Version, req.Manifest, req.provenance())
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamVersionProposed, "team", r.PathValue("teamID"), map[string]any{"version": version.Version, "origin": version.Origin})
		writeJSON(w, 201, version)
	})
	mux.HandleFunc("POST /v1/workspace/teams/{teamID}/versions/{version}/apply", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		team, _, err := ws.ApplyTeamVersion(r.Context(), r.PathValue("teamID"), r.PathValue("version"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamVersionApplied, "team", team.ID, map[string]any{"version": team.Version})
		writeJSON(w, 200, team)
	})
	mux.HandleFunc("POST /v1/workspace/teams/{teamID}/versions/{version}/reject", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		version, err := ws.RejectTeamVersion(r.Context(), r.PathValue("teamID"), r.PathValue("version"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamVersionRejected, "team", r.PathValue("teamID"), map[string]any{"version": version.Version})
		writeJSON(w, 200, version)
	})

	// Version telemetry (docs/agent-teams.md §10–§11): per-version volume,
	// accept-first-try, rework, spend and slot disputes from the team's runs
	// in the requested project.
	mux.HandleFunc("GET /v1/workspace/teams/{teamID}/metrics", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return
		}
		costAPI.TeamMetricsHandler(w, r, r.PathValue("teamID"))
	})

	// Evolution detector (docs/agent-teams.md §11): deterministic scan for
	// slot pairs rejected again and again across runs of the current version.
	// A signal becomes a draft version (origin=agent_proposal) with the
	// evidence attached; applying stays a human decision on the Evolution tab.
	mux.HandleFunc("POST /v1/workspace/teams/{teamID}/evolution/detect", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		project := r.URL.Query().Get("project")
		teamID := r.PathValue("teamID")
		team, err := ws.GetTeam(r.Context(), teamID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		metrics, err := costAPI.TeamMetrics(r.Context(), project, teamID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		signals := cost.DetectTeamEvolution(metrics, team.Version)
		if len(signals) == 0 {
			writeJSON(w, 200, map[string]any{"detected": false, "team_id": teamID, "version": team.Version})
			return
		}
		// One proposal per detection: the strongest signal, all disputes listed
		// in the change summary so the human sees the full picture.
		signal := signals[0]
		var manifest workspace.TeamManifest
		if err := json.Unmarshal(team.Manifest, &manifest); err != nil {
			writeError(w, 422, errors.New("stored team manifest is not valid JSON: "+err.Error()))
			return
		}
		enriched := false
		var problemLines []string
		for _, dispute := range cost.DisputesOf(metrics, team.Version) {
			problemLines = append(problemLines, fmt.Sprintf("%s → %s ×%d", dispute.SlotID, dispute.RejectedBy, dispute.Rejections))
		}
		for i := range manifest.Slots {
			slot := &manifest.Slots[i]
			if slot.ID != signal.Dispute.SlotID {
				continue
			}
			requirement := "Recurring reviewer expectation (evolution proposal): " + strings.TrimSpace(signal.Dispute.LastFeedback)
			if requirement == "Recurring reviewer expectation (evolution proposal): " {
				requirement += "align the output with the " + signal.Dispute.RejectedBy + " review checklist"
			}
			if !strings.Contains(slot.Contract, requirement) {
				if slot.Contract != "" {
					slot.Contract += ". " + requirement
				} else {
					slot.Contract = requirement
				}
				enriched = true
			}
		}
		if !enriched {
			// Either the contract already carries the expectation or the disputed
			// slot no longer exists — nothing to change, and an honest "no
			// proposal" beats a no-op draft.
			reason := "contract already reflects the recurring expectation"
			if signal.Dispute.SlotID != "" {
				found := false
				for i := range manifest.Slots {
					if manifest.Slots[i].ID == signal.Dispute.SlotID {
						found = true
						break
					}
				}
				if !found {
					reason = "disputed slot " + signal.Dispute.SlotID + " is no longer part of the team"
				}
			}
			writeJSON(w, 200, map[string]any{"detected": false, "team_id": teamID, "version": team.Version, "reason": reason})
			return
		}
		updated, err := json.Marshal(manifest)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		version, err := ws.ProposeTeamVersion(r.Context(), teamID, "", updated, workspace.TeamProvenance{
			Origin:          workspace.TeamOriginAgentProposal,
			SourceRuns:      signal.Dispute.SourceRuns,
			ObservedProblem: signal.ObservedProblem,
			ProposedChange:  signal.ProposedChange,
			ExpectedEffect:  signal.ExpectedEffect,
		})
		if err != nil {
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditTeamVersionProposed, "team", teamID, map[string]any{"version": version.Version, "origin": version.Origin, "detector": "slot-disputes", "disputes": strings.Join(problemLines, "; ")})
		writeJSON(w, 201, map[string]any{"detected": true, "version": version})
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
		cfg, rErr := resolveMCPConfigSecrets(r.Context(), ws, mcpConfig(server), secretMasterKey)
		if rErr != nil {
			writeError(w, 422, rErr)
			return
		}
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
		cfg, rErr := resolveMCPConfigSecrets(r.Context(), ws, mcpConfig(server), secretMasterKey)
		if rErr != nil {
			writeError(w, 422, rErr)
			return
		}
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
		cfg, rErr := resolveMCPConfigSecrets(r.Context(), ws, mcpConfig(req.toServer("")), secretMasterKey)
		if rErr != nil {
			writeError(w, 422, rErr)
			return
		}
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

	// Secrets registry (docs/secrets-management.md Phase 1): metadata CRUD for
	// humans, values write-only, resolution service-only. The plaintext value
	// never appears in any response — HasValue is the only observable signal.
	mux.HandleFunc("GET /v1/workspace/secrets", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		list, err := ws.ListSecretsVisible(r.Context(), visibleUnits(r))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"secrets": list, "count": len(list)})
	})
	mux.HandleFunc("POST /v1/workspace/secrets", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req secretRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, 422, errors.New("name is required"))
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.ID == "" {
			req.ID = slugify(req.Name)
		}
		if req.Provider == "" {
			req.Provider = "builtin"
		}
		if req.Provider != "builtin" && req.Provider != "env" {
			writeError(w, 422, fmt.Errorf("provider must be builtin or env (got %q)", req.Provider))
			return
		}
		if existing, err := ws.GetSecretByName(r.Context(), req.Name); err == nil && existing.ID != req.ID {
			writeError(w, 409, fmt.Errorf("secret name %q is already taken by %q", req.Name, existing.ID))
			return
		}
		if req.Provider == "builtin" && req.Value == "" {
			writeError(w, 422, errors.New("value is required for builtin secrets"))
			return
		}
		if req.Provider == "builtin" && len(req.Value) > 64<<10 {
			writeError(w, 422, errors.New("value exceeds 64 KiB"))
			return
		}
		secret := workspace.Secret{ID: req.ID, Name: req.Name, Provider: req.Provider, ExternalRef: strings.TrimSpace(req.ExternalRef), Description: req.Description, OrgUnitID: req.OrgUnitID}
		if err := ws.CreateSecret(r.Context(), &secret); err != nil {
			writeError(w, 409, err)
			return
		}
		if req.Provider == "builtin" {
			if err := storeSecretValue(r.Context(), ws, secretMasterKey, secret.ID, req.Value); err != nil {
				_ = ws.DeleteSecret(r.Context(), secret.ID)
				writeError(w, 422, err)
				return
			}
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSecretCreated, "secret", secret.ID, map[string]any{"name": secret.Name, "provider": secret.Provider})
		secret.HasValue = req.Provider == "builtin"
		writeJSON(w, 201, secret)
	})
	mux.HandleFunc("GET /v1/workspace/secrets/{secretID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		secret, err := ws.GetSecret(r.Context(), r.PathValue("secretID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(secret.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		writeJSON(w, 200, secret)
	})
	mux.HandleFunc("PUT /v1/workspace/secrets/{secretID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req secretRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		current, err := ws.GetSecret(r.Context(), r.PathValue("secretID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// Editing must not re-scope a secret: bindings change only through the
		// admin binding endpoint (same contract as agents and MCP servers).
		secret := workspace.Secret{ID: current.ID, Name: strings.TrimSpace(req.Name), Provider: req.Provider, ExternalRef: strings.TrimSpace(req.ExternalRef), Description: req.Description, OrgUnitID: current.OrgUnitID}
		if secret.Name == "" {
			secret.Name = current.Name
		}
		if secret.Provider == "" {
			secret.Provider = current.Provider
		}
		if secret.Provider != "builtin" && secret.Provider != "env" {
			writeError(w, 422, fmt.Errorf("provider must be builtin or env (got %q)", secret.Provider))
			return
		}
		if other, gErr := ws.GetSecretByName(r.Context(), secret.Name); gErr == nil && other.ID != secret.ID {
			writeError(w, 409, fmt.Errorf("secret name %q is already taken by %q", secret.Name, other.ID))
			return
		}
		if req.Value != "" {
			if secret.Provider != "builtin" {
				writeError(w, 422, errors.New("values can only be stored for builtin secrets"))
				return
			}
			if len(req.Value) > 64<<10 {
				writeError(w, 422, errors.New("value exceeds 64 KiB"))
				return
			}
			if err := storeSecretValue(r.Context(), ws, secretMasterKey, secret.ID, req.Value); err != nil {
				writeError(w, 422, err)
				return
			}
			_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSecretValueSet, "secret", secret.ID, nil)
		}
		if err := ws.UpdateSecret(r.Context(), secret); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSecretUpdated, "secret", secret.ID, map[string]any{"name": secret.Name})
		updated, _ := ws.GetSecret(r.Context(), secret.ID)
		writeJSON(w, 200, updated)
	})
	mux.HandleFunc("DELETE /v1/workspace/secrets/{secretID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		id := r.PathValue("secretID")
		if err := ws.DeleteSecret(r.Context(), id); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSecretDeleted, "secret", id, nil)
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	// Value rotation without touching metadata: write-only by design.
	mux.HandleFunc("POST /v1/workspace/secrets/{secretID}/value", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if req.Value == "" {
			writeError(w, 422, errors.New("value is required"))
			return
		}
		if len(req.Value) > 64<<10 {
			writeError(w, 422, errors.New("value exceeds 64 KiB"))
			return
		}
		secret, err := ws.GetSecret(r.Context(), r.PathValue("secretID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if secret.Provider != "builtin" {
			writeError(w, 422, errors.New("values can only be stored for builtin secrets"))
			return
		}
		if err := storeSecretValue(r.Context(), ws, secretMasterKey, secret.ID, req.Value); err != nil {
			writeError(w, 422, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSecretValueSet, "secret", secret.ID, nil)
		writeJSON(w, 200, map[string]bool{"stored": true})
	})
	// resolve turns a secret name into plaintext for the execution boundary.
	// Service-principal-only: the kernel-internal loopback identity may call it,
	// human tokens — any role — may not (docs/secrets-management.md §4.1).
	mux.HandleFunc("GET /v1/workspace/secrets/{secretID}/resolve", func(w http.ResponseWriter, r *http.Request) {
		if principal, ok := controlplane.FromContext(r.Context()); !ok || principal.Subject != "kernel-internal" {
			writeError(w, http.StatusForbidden, errors.New("secret resolution is reserved for the agent kernel"))
			return
		}
		if secretMasterKey == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("KERNEL_SECRET_MASTER_KEY is not configured"))
			return
		}
		name := r.PathValue("secretID")
		secret, err := ws.GetSecretByName(r.Context(), name)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		var value string
		switch secret.Provider {
		case "env":
			value = strings.TrimSpace(os.Getenv("SECRET_" + strings.ToUpper(strings.ReplaceAll(secret.Name, "-", "_"))))
			if value == "" {
				writeError(w, 404, fmt.Errorf("env secret %q is not set", secret.Name))
				return
			}
		default:
			ciphertext, nonce, vErr := ws.GetSecretValue(r.Context(), secret.ID)
			if vErr != nil {
				writeError(w, 404, fmt.Errorf("secret %q has no stored value", secret.Name))
				return
			}
			plaintext, dErr := workspace.OpenSecret(secretMasterKey, ciphertext, nonce)
			if dErr != nil {
				writeError(w, 500, fmt.Errorf("decrypt secret %q: %w", secret.Name, dErr))
				return
			}
			value = string(plaintext)
		}
		writeJSON(w, 200, map[string]string{"name": secret.Name, "value": value})
	})

	// Reference artifact registry (docs/artifact-registry.md). Artifacts are
	// documents skills point at from their manifests; bodies never enter the
	// prompt implicitly — the kernel reads them chunked for artifact_read.
	mux.HandleFunc("GET /v1/workspace/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		// Lifecycle filters (docs/artifact-lifecycle.md §9): status/kind exact
		// match, saga_id traces one saga's work products.
		filter := workspace.ArtifactFilter{
			Status: r.URL.Query().Get("status"), Kind: r.URL.Query().Get("kind"), SagaID: r.URL.Query().Get("saga_id"),
		}
		list, err := ws.ListArtifactsFiltered(r.Context(), visibleUnits(r), filter)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"artifacts": list, "count": len(list)})
	})
	mux.HandleFunc("POST /v1/workspace/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req artifactRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		artifact, err := req.toArtifact("")
		if err != nil {
			writeError(w, 422, err)
			return
		}
		if artifact.ID == "" {
			writeError(w, 422, errors.New("id is required (derive it from the title)"))
			return
		}
		// Origin split (docs/artifact-lifecycle.md §4): a human create is itself
		// an approval act; the kernel service identity (agent tools) creates
		// drafts that must clear a human review before skills may reference them.
		if principal, ok := controlplane.FromContext(r.Context()); ok && principal.Subject == "kernel-internal" {
			artifact.Status = workspace.ArtifactDraft
			artifact.OriginType = "agent"
			if artifact.OriginRun == "" {
				writeError(w, 422, errors.New("origin_run is required for agent-created artifacts"))
				return
			}
		} else {
			artifact.Status = workspace.ArtifactApproved
			artifact.OriginType = "human"
			artifact.OriginUser = requestUserID(r)
		}
		if err := ws.CreateArtifact(r.Context(), &artifact); err != nil {
			writeError(w, 409, err)
			return
		}
		auditAction := workspace.AuditArtifactCreated
		if artifact.OriginType == "agent" {
			auditAction = workspace.AuditArtifactProposed
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), auditAction, "artifact", artifact.ID, map[string]any{"title": artifact.Title, "provider": artifact.Provider, "kind": artifact.Kind, "status": artifact.Status})
		if artifact.OriginType == "agent" {
			emitArtifactLifecycleEvent(r.Context(), events, "artifact.proposed", artifact.ID, map[string]any{
				"artifact_id": artifact.ID, "title": artifact.Title, "kind": artifact.Kind, "saga_id": artifact.SagaID,
				"origin_run": artifact.OriginRun, "version": artifact.Version,
			})
		}
		writeJSON(w, 201, artifact)
	})
	// Exact path beats the {artifactID} wildcard: the UI fetches the whole
	// map once to render "used by N skills" badges on the registry list.
	mux.HandleFunc("GET /v1/workspace/artifacts/references", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		references, err := ws.ArtifactReferences(r.Context(), visibleUnits(r))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if references == nil {
			references = map[string][]string{}
		}
		writeJSON(w, 200, map[string]any{"references": references})
	})
	mux.HandleFunc("GET /v1/workspace/artifacts/{artifactID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		offset, limit := artifactRange(r)
		artifact, err := ws.GetArtifactRange(r.Context(), r.PathValue("artifactID"), offset, limit)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(artifact.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		// Chunk framing lets the kernel page large documents without shipping
		// megabytes per call; SizeBytes stays the whole-body size.
		writeJSON(w, 200, map[string]any{
			"artifact": artifact, "offset": offset, "next_offset": offset + len(artifact.Content),
			"truncated": artifact.Provider == "builtin" && int64(offset+len(artifact.Content)) < artifact.SizeBytes,
		})
	})
	mux.HandleFunc("PUT /v1/workspace/artifacts/{artifactID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req artifactRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		current, err := ws.GetArtifactRange(r.Context(), r.PathValue("artifactID"), 0, 0)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		artifact, err := req.toArtifact(current.ID)
		if err != nil {
			writeError(w, 422, err)
			return
		}
		// An edit without an explicit kind keeps the stored classification;
		// origin (who/what created it) survives edits by design — it is history,
		// not a mutable field. Editing must not re-scope an artifact either:
		// bindings change only through the admin binding endpoint.
		if req.Kind == "" {
			artifact.Kind = current.Kind
		}
		// saga_id distinguishes "not mentioned" from "explicitly cleared": a
		// payload without the field keeps the stored link (agent tool omits it),
		// an explicit empty string detaches — a company/department regulation
		// carries no saga at all.
		if req.SagaID == nil {
			artifact.SagaID = current.SagaID
		}
		artifact.OrgUnitID = current.OrgUnitID
		if err := ws.SaveArtifactEdit(r.Context(), artifact); err != nil {
			if errors.Is(err, workspace.ErrArtifactLocked) {
				writeError(w, 409, err)
				return
			}
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditArtifactUpdated, "artifact", artifact.ID, map[string]any{"title": artifact.Title})
		updated, _ := ws.GetArtifactRange(r.Context(), artifact.ID, 0, 0)
		updated.Content = ""
		writeJSON(w, 200, updated)
	})
	mux.HandleFunc("DELETE /v1/workspace/artifacts/{artifactID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		id := r.PathValue("artifactID")
		// A referenced artifact is load-bearing for skill manifests: dropping
		// it would silently break the skill's document trail. Remove the
		// reference (new skill version) first — the error says exactly that.
		refs, err := ws.ArtifactReferencedBy(r.Context(), id)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if len(refs) > 0 {
			writeError(w, 409, fmt.Errorf("artifact is referenced by skills %s; remove the manifest references first", strings.Join(refs, ", ")))
			return
		}
		if err := ws.DeleteArtifact(r.Context(), id); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditArtifactDeleted, "artifact", id, nil)
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})

	// Webhook triggers: POST /v1/workspace/webhook/{projectID}/{path}
	// Matches webhook triggers by projectID and config.path. The incoming
	// request is normalized into a trigger event (docs/triggers-and-escalations.md §1):
	// id/type/source/actor/payload. Delivery is idempotent by event id — the
	// same source event never creates a second run (§4).
	mux.HandleFunc("POST /v1/workspace/webhook/{projectID}/{path...}", func(w http.ResponseWriter, r *http.Request) {
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
		// Authorization: a per-trigger delivery token (hash in the config)
		// admits the caller for THIS endpoint only — the route is mounted past
		// the gate (see the root handler), so external systems never need a
		// workspace credential. Otherwise fall through to the regular writer
		// gate: Allow writes the 401/403 itself for anonymous callers.
		authorized := false
		if cfg.AuthTokenHash != "" {
			if token, ok := controlplane.BearerToken(r); ok && webhookTokenMatches(token, cfg.AuthTokenHash) {
				authorized = true
			}
		}
		if !authorized && !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
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
		// Idempotent run identity: deterministic for a (trigger, event) pair.
		key := sha256.Sum256([]byte(matched.ID + "\x00" + eventID))
		taskID := "trigger-" + matched.ID + "-" + hex.EncodeToString(key[:])[:12]
		// Delivery observability: every attempt that got past authorization is
		// journalled, so the UI can show what a webhook call actually did —
		// accepted, duplicate or rejected — instead of firing into the void.
		deliveryScope := agent.EventScope(activities.SourceID, projectID, taskID)
		emitDeliveryEvent := func(kind string, data map[string]any) {
			item := observation.Event{
				Schema: observation.Schema, EventID: "trigger/" + deliveryScope + "/" + kind, OccurredAt: time.Now().UTC(),
				Source:  observation.Source{ID: activities.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
				Context: observation.Context{Project: projectID, Run: taskID, Task: taskID, Actor: observation.Actor{ID: "webhook-" + matched.ID, Type: "trigger"}},
				Type:    "trigger." + kind, Data: data,
			}
			if err := events.Enqueue(r.Context(), item); err != nil {
				slog.Error("emit trigger delivery event", "trigger", matched.ID, "kind", kind, "error", err)
			}
		}
		// Optional origin/integrity hardening: when the trigger carries a
		// secret, the delivery must be signed with it (HMAC-SHA256 over the raw
		// body). Native format X-Temporality-Signature: sha256-<hex>; the
		// GitHub-style X-Hub-Signature-256: sha256=<hex> is accepted too.
		// Unsigned senders simply leave the secret empty.
		if cfg.Secret != "" && !webhookSignatureValid(r, raw, cfg.Secret) {
			emitDeliveryEvent("rejected", map[string]any{"trigger_id": matched.ID, "trigger_name": matched.Name, "event_id": eventID, "source": "webhook", "reason": "invalid or missing webhook signature (X-Temporality-Signature)"})
			writeError(w, 401, errors.New("invalid or missing webhook signature (X-Temporality-Signature)"))
			return
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
		// Duplicate delivery (§4): the deterministic task id already exists —
		// report the existing run instead of touching the workflow again.
		if _, err := ws.GetTask(r.Context(), taskID); err == nil {
			emitDeliveryEvent("duplicate", map[string]any{"trigger_id": matched.ID, "trigger_name": matched.Name, "event_id": eventID, "source": "webhook", "run_id": taskID})
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
		input.Title = task.Title
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
				emitDeliveryEvent("duplicate", map[string]any{"trigger_id": matched.ID, "trigger_name": matched.Name, "event_id": eventID, "source": "webhook", "run_id": input.RunID})
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

	// Team scratch sweeper (docs/agent-teams.md §8): slot contracts and shared
	// intermediate files outlive a team run for inspection for a TTL, then go.
	// The paths are deterministic (team/<run>/ under the project workspace), so
	// a periodic sweep is enough — no per-run cleanup bookkeeping.
	go func() {
		ttl := 7 * 24 * time.Hour
		if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_TEAM_SCRATCH_TTL")); raw != "" {
			if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
				ttl = parsed
			} else {
				slog.Warn("TEMPORALITY_TEAM_SCRATCH_TTL is not a valid duration, using 168h", "value", raw)
			}
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			removed, err := activities.SweepTeamScratch(time.Now(), ttl)
			if err != nil {
				slog.Warn("sweep team scratch", "error", err)
			} else if removed > 0 {
				slog.Info("swept team scratch directories", "removed", removed)
			}
		}
	}()

	// Auth garbage sweep (docs/auth-federation.md §5.2): expired sessions and
	// stale one-time login states leave rows behind — the hourly pass keeps
	// both tables from growing with abandoned logins.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := ws.PurgeAuthGarbage(context.Background(), time.Now().UTC()); err != nil {
				slog.Warn("purge auth garbage", "error", err)
			}
		}
	}()

	// Storage snapshot loop (docs/sandbox-storage.md §6, phase 1: read-only):
	// hourly walk of the sandbox root refreshing the API cache and journaling
	// the footprint. KERNEL_STORAGE_SWEEPER=off disables it; no deletions yet.
	startStorageSnapshotLoop(storageRoot, storageSnapshots, events)

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
				input.Title = task.Title
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

	// Event triggers (docs/triggers-and-escalations.md §1 type=event): tail the
	// kernel's own journal and fire runs on matching events — hooks around the
	// agent cycle (unapproved artifacts, sandbox incidents, mass export,
	// security events). No event triggers configured → the journal is not
	// tailed at all.
	eventListener := newEventTriggerListener(ws, activities, costPrices, temporalClient, taskQueue, events, activities.SourceID, observationURL, log)
	go eventListener.run(ctx)

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
		// Empty project lists across projects, scoped server-side to the
		// principal's visible set (docs/activity-scoping.md §5).
		principal, allowed := gate.AllowQuery(w, r, controlplane.RoleReader, r.URL.Query().Get("project"))
		if !allowed {
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
		var requests []workspace.HumanRequest
		if project := r.URL.Query().Get("project"); project != "" {
			var err error
			requests, err = ws.ListHumanRequests(r.Context(), project, r.URL.Query().Get("status"), r.URL.Query().Get("user"), r.URL.Query().Get("open") == "1")
			if err != nil {
				writeError(w, 500, err)
				return
			}
		} else if visible, all := principal.AllowedProjects(); all {
			var err error
			requests, err = ws.ListHumanRequests(r.Context(), "", r.URL.Query().Get("status"), r.URL.Query().Get("user"), r.URL.Query().Get("open") == "1")
			if err != nil {
				writeError(w, 500, err)
				return
			}
		} else {
			var err error
			requests, err = ws.ListHumanRequestsInProjects(r.Context(), visible, r.URL.Query().Get("status"), r.URL.Query().Get("user"), r.URL.Query().Get("open") == "1")
			if err != nil {
				writeError(w, 500, err)
				return
			}
		}
		writeJSON(w, 200, map[string]any{"requests": requests})
	})
	// Inbound messages (docs/inbound-messages.md §11): the intake pipeline's
	// durable state machine — the UI inbox of channel-initiated messages. The
	// lazy expiry sweep ends clarification dialogs that outlived any reasonable
	// wait (kernel down, user gone) so the list never shows eternal "received".
	mux.HandleFunc("GET /v1/workspace/inbound-messages", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		if err := ws.ExpireStaleInboundMessages(r.Context(), inboundExpiryTTL); err != nil {
			log.Warn("sweep inbound messages", "error", err)
		}
		// Retention scrub (docs/inbound-messages.md §12 phase 4, PII): bodies
		// older than the window are blanked, the rows stay as the audit chain.
		// The same lazy trigger as expiry — an unvisited inbox retires slowly,
		// which is fine: nothing reads those rows either.
		if days := intakeRetentionDaysFromEnv(); days > 0 {
			retired, rErr := ws.RetireInboundMessages(r.Context(), time.Now().UTC().AddDate(0, 0, -days))
			if rErr != nil {
				log.Warn("retire inbound messages", "error", rErr)
			} else if len(retired) > 0 {
				// Staged attachments were kept for the audit trail; with the body
				// gone they are PII without a purpose. Inbox copies inside projects
				// are work artifacts and stay.
				if root, sErr := sandboxRoot(); sErr == nil {
					for _, id := range retired {
						_ = os.RemoveAll(filepath.Join(root, "inbound", inboundStagingKey(id)))
					}
				}
				log.Info("retired inbound messages", "count", len(retired))
			}
		}
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, pErr := strconv.Atoi(raw); pErr == nil {
				limit = parsed
			}
		}
		messages, err := ws.ListInboundMessages(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("channel"), limit)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		// Status counters are independent of the list limit: the inbox shows
		// them on the filter chips, so a bounded list does not hide the scale.
		response := map[string]any{"messages": messages}
		if counts, cErr := ws.InboundMessageStatusCounts(r.Context()); cErr != nil {
			log.Warn("inbound message status counts", "error", cErr)
		} else {
			response["counts"] = counts
		}
		writeJSON(w, 200, response)
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
		// Pin the agent definition version at ask time (specs/quality-loop.md
		// §3.1): the verdict attributes to the snapshot that produced the
		// question. A client-supplied value is never trusted; an unresolvable
		// agent keeps 0 = unknown and stays out of per-version metrics.
		req.AgentVersion = 0
		if req.AgentID != "" {
			if a, aErr := ws.GetAgent(r.Context(), req.AgentID); aErr == nil {
				req.AgentVersion = a.DefinitionVersion
			}
		}
		if err := ws.CreateHumanRequest(r.Context(), &req); err != nil {
			writeError(w, 500, err)
			return
		}
		// The thread a retry re-posts into (activity replay after a partial
		// failure) must still pin the conversation: create is a no-op on an
		// existing row, so fill-when-empty here.
		if req.ThreadRef != "" {
			if err := ws.SetHumanRequestThreadRef(r.Context(), req.ID, req.ThreadRef); err != nil && !errors.Is(err, workspace.ErrNotFound) {
				log.Warn("pin human request thread", "id", req.ID, "error", err)
			}
		}
		emitHitlVerdict(r.Context(), events, req)
		if req.Status == workspace.HumanStatusDelivered {
			if err := ws.DeliverHumanRequest(r.Context(), req.ID, req.ResolvedUser, req.Status, req.Channel, ""); err != nil && !errors.Is(err, workspace.ErrNotFound) {
				log.Warn("mark human request delivered", "id", req.ID, "error", err)
			}
		}
		// Team fan-out rows (docs/inbound-messages.md §8): upsert after the main
		// row so the delivery list always references an existing request.
		if len(req.Deliveries) > 0 {
			if err := ws.AddHumanRequestDeliveries(r.Context(), req.ID, req.Deliveries); err != nil {
				log.Warn("record human request deliveries", "id", req.ID, "error", err)
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
			row, aErr := ws.AnswerHumanRequest(r.Context(), id, body.Response, body.ActorID)
			if aErr == nil {
				// Terminal transition with a verdict → hitl.verdict.recorded
				// (specs/quality-loop.md §9) through the durable outbox.
				emitHitlVerdict(r.Context(), events, row)
				err = ws.RecordAccessAudit(r.Context(), body.ActorID, workspace.AuditHumanAnswered, "human_request", id, map[string]any{"response": body.Response})
			} else {
				err = aErr
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
	registerUserMetricsRoutes(mux, gate, ws, habFactors)
	// User facts (docs/inbound-messages.md §4.3): the explicit half of the
	// per-user picture — preferences and durable facts the dialogs work with.
	mux.HandleFunc("GET /v1/workspace/users/{userID}/facts", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		facts, err := ws.ListUserFacts(r.Context(), r.PathValue("userID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"facts": facts})
	})
	mux.HandleFunc("POST /v1/workspace/users/{userID}/facts", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Fact   string `json:"fact"`
			Origin string `json:"origin,omitempty"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Fact) == "" {
			writeError(w, 422, fmt.Errorf("fact is required"))
			return
		}
		fact, err := ws.AddUserFact(r.Context(), r.PathValue("userID"), req.Fact, req.Origin)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, fact)
	})
	mux.HandleFunc("DELETE /v1/workspace/users/{userID}/facts/{factID}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("factID"), 10, 64)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		if err := ws.DeleteUserFact(r.Context(), r.PathValue("userID"), id); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
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
		if err := validateChannelTypes(req.Channels); err != nil {
			writeError(w, 422, err)
			return
		}
		orgUnitID := ""
		if req.OrgUnitID != nil {
			orgUnitID = *req.OrgUnitID
		}
		// Optional password login: hashed before the insert, never stored raw.
		passwordHash := ""
		if req.Password != nil && *req.Password != "" {
			if len(*req.Password) < 8 {
				writeError(w, 422, errors.New("password must be at least 8 characters"))
				return
			}
			hash, err := workspace.HashPassword(*req.Password)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			passwordHash = hash
		}
		user := &workspace.User{ID: req.ID, Name: req.Name, Email: req.Email, Role: req.Role, Token: token, PasswordHash: passwordHash, OrgUnitID: orgUnitID, Active: active, Channels: req.Channels, PreferredChannel: req.PreferredChannel}
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
		user := workspace.User{ID: r.PathValue("userID"), Name: req.Name, Email: req.Email, Role: req.Role, OrgUnitID: orgUnitID, Active: active, Channels: existing.Channels, PreferredChannel: existing.PreferredChannel}
		if err := ws.UpdateUser(r.Context(), user); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		// A non-empty password replaces the stored hash; empty/absent keeps the
		// current one — an admin editing a role must not lock the user out.
		if req.Password != nil && *req.Password != "" {
			if len(*req.Password) < 8 {
				writeError(w, 422, errors.New("password must be at least 8 characters"))
				return
			}
			hash, err := workspace.HashPassword(*req.Password)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			if err := ws.SetUserPassword(r.Context(), user.ID, hash); err != nil {
				writeError(w, 500, err)
				return
			}
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
		if err := validateChannelTypes(req.Channels); err != nil {
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
	// Deliverable channel types (docs/triggers-and-escalations.md §7): the
	// kernel's transport registry, served to the UI so users pick from what
	// the kernel can actually deliver. Configured flags reflect the effective
	// credentials (database overlay over env), so an admin wiring bot
	// credentials in the settings screen is visible at once.
	mux.HandleFunc("GET /v1/workspace/channel-types", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		stored, err := ws.ChannelTransport(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		effective := agent.ChannelSettings{Channels: stored.Payload, UIURL: stored.UIURL}.OverlayEnv()
		writeJSON(w, 200, map[string]any{"types": agent.ChannelTypesWith(effective)})
	})
	// channelSettingsView is the masked admin view of the channel settings
	// overlay, rendered generically from the driver registry: plain values for
	// non-secrets, set+hint for secrets, and the winning source per field so
	// precedence is never a mystery. The schema section tells the UI which
	// channels and fields exist — adding a channel needs no UI work.
	channelSettingsView := func(stored workspace.ChannelTransport, envSettings agent.ChannelSettings, err error) (map[string]any, error) {
		if err != nil {
			return nil, err
		}
		plain := func(storedValue, envValue string) map[string]any {
			field := map[string]any{"value": storedValue, "source": "none"}
			switch {
			case storedValue != "":
				field["source"] = "database"
			case envValue != "":
				field["value"] = envValue
				field["source"] = "env"
			}
			return field
		}
		secret := func(storedValue, envValue string) map[string]any {
			field := map[string]any{"set": false, "source": "none"}
			switch {
			case storedValue != "":
				field["set"] = true
				field["hint"] = agent.SecretHint(storedValue)
				field["source"] = "database"
			case envValue != "":
				field["set"] = true
				field["hint"] = agent.SecretHint(envValue)
				field["source"] = "env"
			}
			return field
		}
		schema := []map[string]any{}
		channels := map[string]any{}
		for _, driver := range agent.ChannelDrivers() {
			spec := driver.Spec()
			if len(spec.Settings) == 0 {
				continue // self-contained channels have nothing to configure
			}
			fields := []map[string]any{}
			values := map[string]any{}
			for _, field := range spec.Settings {
				storedValue := stored.Payload[driver.Type()][field.Name]
				envValue := envSettings.Channels[driver.Type()][field.Name]
				if field.Secret {
					values[field.Name] = secret(storedValue, envValue)
				} else {
					values[field.Name] = plain(storedValue, envValue)
				}
				fields = append(fields, map[string]any{"name": field.Name, "label": field.Label, "secret": field.Secret})
			}
			channels[driver.Type()] = values
			schema = append(schema, map[string]any{"type": driver.Type(), "label": spec.Label, "fields": fields})
		}
		return map[string]any{
			"schema":     schema,
			"channels":   channels,
			"ui_url":     plain(stored.UIURL, envSettings.UIURL),
			"updated_at": stored.UpdatedAt,
		}, nil
	}
	// Transport settings (admin): read masked, write upsert. Secrets never
	// travel back — GET reports set+hint only; PUT sets a secret when a
	// non-empty value is posted and clears the named fields via "clear".
	mux.HandleFunc("GET /v1/workspace/channel-settings", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		stored, err := ws.ChannelTransport(r.Context())
		view, viewErr := channelSettingsView(stored, agent.ChannelSettingsFromEnv(), err)
		if viewErr != nil {
			writeError(w, 500, viewErr)
			return
		}
		writeJSON(w, 200, view)
	})
	// Transport settings (admin): read masked, write upsert. Secrets never
	// travel back — GET reports set+hint only; PUT sets a secret when a
	// non-empty value is posted and clears fields via "clear" ("type.field"
	// names, or "ui_url"). The writable surface is the driver registry:
	// unknown channels/fields are rejected, not silently stored.
	mux.HandleFunc("PUT /v1/workspace/channel-settings", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var req struct {
			UIURL    *string                       `json:"ui_url"`
			Channels map[string]map[string]*string `json:"channels"`
			Clear    []string                      `json:"clear"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		known := map[string]map[string]bool{} // channel type → field name → secret
		for _, driver := range agent.ChannelDrivers() {
			fields := map[string]bool{}
			for _, field := range driver.Spec().Settings {
				fields[field.Name] = field.Secret
			}
			known[driver.Type()] = fields
		}
		for channelType, fields := range req.Channels {
			if _, ok := known[channelType]; !ok {
				writeError(w, 422, fmt.Errorf("unknown channel type %q", channelType))
				return
			}
			for name := range fields {
				if _, ok := known[channelType][name]; !ok {
					writeError(w, 422, fmt.Errorf("unknown setting %q for channel %q", name, channelType))
					return
				}
			}
		}
		current, err := ws.ChannelTransport(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if current.Payload == nil {
			current.Payload = map[string]map[string]string{}
		}
		if req.UIURL != nil {
			current.UIURL = strings.TrimSpace(*req.UIURL)
		}
		for channelType, fields := range req.Channels {
			stored := current.Payload[channelType]
			if stored == nil {
				stored = map[string]string{}
				current.Payload[channelType] = stored
			}
			for name, value := range fields {
				if value == nil {
					continue
				}
				trimmed := strings.TrimSpace(*value)
				// Secrets: an empty post means "not changing" — the form never
				// echoes values back, so only a non-empty input replaces one.
				// Plain fields: presence sets, even to empty (clears the overlay).
				if known[channelType][name] && trimmed == "" {
					continue
				}
				stored[name] = trimmed
			}
		}
		for _, name := range req.Clear {
			if name == "ui_url" {
				current.UIURL = ""
				continue
			}
			channelType, field, ok := strings.Cut(name, ".")
			if !ok {
				writeError(w, 422, fmt.Errorf("clear entry %q must be a channel.field name", name))
				return
			}
			if _, isKnown := known[channelType][field]; !isKnown {
				writeError(w, 422, fmt.Errorf("unknown setting %q", name))
				return
			}
			delete(current.Payload[channelType], field)
		}
		if err := ws.SaveChannelTransport(r.Context(), current); err != nil {
			writeError(w, 500, err)
			return
		}
		stored, err := ws.ChannelTransport(r.Context())
		view, viewErr := channelSettingsView(stored, agent.ChannelSettingsFromEnv(), err)
		if viewErr != nil {
			writeError(w, 500, viewErr)
			return
		}
		writeJSON(w, 200, view)
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

	// Quality dashboard and version diffs (specs/quality-loop.md §7).
	RegisterQualityRoutes(mux, gate, ws, events, visibleUnits)
	// LLM-judge configuration and calibration (specs/quality-loop.md §6).
	RegisterJudgeRoutes(mux, gate, ws, activities, events, visibleUnits, log)
	// Judge-driven knowledge-area normalization (docs/human-knowledge-explorer.md §4.2).
	RegisterKnowledgeAreaRoutes(mux, gate, ws, secretMasterKey, observationURL, log)
	// Semantic knowledge search (docs/supervisor-model.md §6.6): lexical +
	// semantic tiers through the journal's hint matcher, exposed as search.
	RegisterKnowledgeSearchRoutes(mux, gate, activities)
	// Supervisor settings (docs/supervisor-model.md): judge/vision/embedding
	// models and the human-attention coefficients.
	RegisterSupervisorRoutes(mux, gate, ws, activities)
	// Semantic knowledge layer (docs/supervisor-model.md §6): preload the
	// in-process embedding model, then backfill knowledge missing a vector.
	activities.StartEmbeddingPreload()
	startEmbeddingBackfillLoop(ctx, log, activities, observationURL)
	// Supervisor triage (docs/supervisor-model.md §3 wave A1): diagnose
	// failed runs with the supervisor model, journal supervisor.verdict.
	startSupervisorTriageLoop(ctx, log, activities, ws, observationURL, func(ctx context.Context) (*llm.Client, error) {
		return judgeModelClient(ctx, ws, secretMasterKey)
	})
	// Scheduled quality runs (docs/agent-quality-loop.md §8.1): the cadence is
	// an operator setting the loop re-reads every minute, so UI changes apply
	// without a restart. Anchored on the newest scheduled run row.
	registerQualityScheduleRoutes(mux, gate, ws, events)
	go runQualityScheduleJob(ctx, ws, activities, log)
	registerExampleRoutes(mux, temporalClient, taskQueue, activities, gate)
	// Golden eval-case candidates (specs/quality-loop.md §4): review
	// endpoints, candidate events and the harvesting/reaping background job.
	RegisterEvalCandidateRoutes(ctx, mux, ws, gate, events, temporalClient, activities.SourceID, log)
	// Intake pipeline (docs/inbound-messages.md): the system project + agent
	// exist before the first message arrives; create-if-absent, never
	// overwritten. The kill switch records and rejects messages without
	// starting runs when intake is administratively disabled.
	bootstrapIntake(ctx, log, ws)
	intakeEnabled := intakeEnabledFromEnv()
	intakeRatePerHour := intakeRatePerHourFromEnv()
	type modelClientFn func(ctx context.Context) (*llm.Client, error)
	intake := &intakeRunner{
		log: log, ws: ws, temporal: temporalClient, taskQueue: taskQueue,
		sourceID: activities.SourceID, activities: activities,
		costPrices: costPrices,
		// Wave C3 (docs/supervisor-model.md §3): validate the chosen amendment
		// target with the supervisor model before the amendment run starts.
		supervisorModel: func(ctx context.Context) (*llm.Client, error) {
			return judgeModelClient(ctx, ws, secretMasterKey)
		},
	}
	// Target-run answers follow the message's channel: the loop's watcher
	// follows the run and delivers the final answer to the sender's chat.
	targetWatchers := map[string]func(row workspace.InboundMessage, runID string){}
	intake.targetRunWatcher = func(row workspace.InboundMessage, runID string) {
		if watcher, ok := targetWatchers[row.Channel]; ok && watcher != nil {
			watcher(row, runID)
		}
	}
	intakeEmitter := intakeEventEnqueuer(events.Enqueue, activities.SourceID)
	// Internal intake endpoints (docs/inbound-messages.md §7): the intake
	// agent's tools call them loopback with the kernel token; every identity
	// decision inside resolves back to the message sender.
	registerIntakeRoutes(mux, gate, intake, quotas, intakeEmitter)
	// Inbound channel loops (docs/channels.md §5): every two-way driver gets
	// its inbound feed — pull drivers a poll loop, push drivers a webhook
	// route on the kernel's own mux. Replies in a chat route back into paused
	// runs through the same approval signal the web inbox uses, and everything
	// else enters the intake pipeline. Poll loops idle (30s re-check) while
	// the channel is not configured; cursors and account identity survive
	// restarts in workspace_channel_cursor.
	for _, driver := range agent.ChannelDrivers() {
		if hook := driver.Webhook(); hook != nil {
			receiver := &channelWebhookReceiver{
				loop: &channelInboundLoop{
					driver:     driver,
					httpClient: activities.HTTP,
					settings:   activities.ChannelSettings,
					store:      ws,
					resolveRun: resolveRunState,
					journal:    runJournal,
					sourceID:   activities.SourceID,
					log:        log,
					signal: func(ctx context.Context, workflowID string, approval agent.Approval) error {
						return temporalClient.SignalWorkflow(ctx, workflowID, "", agent.ApprovalSignal, approval)
					},
					note: func(ctx context.Context, workflowID string, note agent.UserNote) error {
						return temporalClient.SignalWorkflow(ctx, workflowID, "", agent.UserNoteSignal, note)
					},
					intake: func(ctx context.Context, message agent.InboundMessage, senderUserID string) (string, error) {
						user, err := senderUser(ctx, ws, senderUserID)
						if err != nil {
							return "", err
						}
						return intake.start(ctx, message, user)
					},
					emit:          intakeEmitter,
					intakeEnabled: intakeEnabled,
					ratePerHour:   intakeRatePerHour,
					events:        events,
				},
				hook: hook,
				log:  log,
			}
			targetWatchers[driver.Type()] = receiver.loop.WatchTargetRun
			mux.HandleFunc(hook.Route(), receiver.handle)
			log.Info("channel webhook registered", "channel", driver.Type(), "route", hook.Route())
			continue
		}
		poller := driver.Poller()
		if poller == nil {
			continue // outbound-only channel
		}
		loop := &channelInboundLoop{
			driver:     driver,
			poller:     poller,
			httpClient: activities.HTTP,
			settings:   activities.ChannelSettings,
			store:      ws,
			resolveRun: resolveRunState,
			journal:    runJournal,
			sourceID:   activities.SourceID,
			log:        log,
			signal: func(ctx context.Context, workflowID string, approval agent.Approval) error {
				return temporalClient.SignalWorkflow(ctx, workflowID, "", agent.ApprovalSignal, approval)
			},
			note: func(ctx context.Context, workflowID string, note agent.UserNote) error {
				return temporalClient.SignalWorkflow(ctx, workflowID, "", agent.UserNoteSignal, note)
			},
			intake: func(ctx context.Context, message agent.InboundMessage, senderUserID string) (string, error) {
				user, err := senderUser(ctx, ws, senderUserID)
				if err != nil {
					return "", err
				}
				return intake.start(ctx, message, user)
			},
			emit:          intakeEmitter,
			intakeEnabled: intakeEnabled,
			ratePerHour:   intakeRatePerHour,
			events:        events,
		}
		targetWatchers[driver.Type()] = loop.WatchTargetRun
		go loop.run(ctx)
	}
	// The gate wraps everything except the password-login route: an
	// unauthenticated caller may reach exactly POST /v1/auth/login, every other
	// path still requires a bearer token (or the anonymous pass-through when
	// the gate is disabled).
	//
	// nginx (see debugger/nginx.conf) proxies /kernel-api/ WITHOUT a URI part
	// so that run ids with encoded slashes (%2F) reach us byte-for-byte
	// untouched — the only forwarding style that neither decodes nor
	// double-escapes them. The price is that the /kernel-api prefix arrives
	// intact, so it is stripped here, from both Path and RawPath, before any
	// routing decision. The strip is a no-op when the prefix is absent (vite
	// dev proxy and direct :8090 calls), and idempotent when present.
	authed := gate.Authenticate(mux)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripKernelAPIProxyPrefix(r)
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login" {
			mux.ServeHTTP(w, r)
			return
		}
		// Federation login surface (docs/auth-federation.md §5): methods feed
		// the login screen, start/callback drive the OIDC redirect flow, logout
		// must work with a stale cookie. None can exist behind the bearer gate.
		if r.Method == http.MethodGet && r.URL.Path == "/v1/auth/methods" {
			mux.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/auth/oidc/") {
			mux.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/logout" {
			mux.ServeHTTP(w, r)
			return
		}
		// Webhook trigger deliveries authorize against the matched trigger's
		// own token (or fall back to the writer gate inside the handler), so
		// they enter the mux directly — an unknown per-trigger token must
		// reach the handler, not die in the Authenticate middleware.
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/workspace/webhook/") {
			mux.ServeHTTP(w, r)
			return
		}
		authed.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: address, Handler: root, ReadHeaderTimeout: 5 * time.Second}
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

// stripKernelAPIProxyPrefix removes the leading "/kernel-api" proxy prefix
// in place. nginx (debugger/nginx.conf) forwards /kernel-api/ WITHOUT a URI
// part in proxy_pass so that run ids containing encoded slashes (%2F) arrive
// byte-for-byte untouched — the only forwarding style that neither decodes
// nor double-escapes them. The price is the intact prefix, stripped here from
// both Path and RawPath before any routing decision so that http.ServeMux
// wildcard matching (which runs on the escaped path) sees the same request a
// direct caller would. The strip is a no-op when the prefix is absent (vite
// dev proxy and direct :8090 calls).
func stripKernelAPIProxyPrefix(r *http.Request) {
	const proxyPrefix = "/kernel-api"
	if r.URL.Path != proxyPrefix && !strings.HasPrefix(r.URL.Path, proxyPrefix+"/") {
		return
	}
	// Record the prefix for derivations that must address the kernel the way
	// the browser does — the OIDC redirect_uri (docs/auth-federation.md §5.1)
	// is built as publicOrigin + prefix + path.
	if r.Header.Get("X-Forwarded-Prefix") == "" {
		r.Header.Set("X-Forwarded-Prefix", proxyPrefix)
	}
	r.URL.Path = strings.TrimPrefix(r.URL.Path, proxyPrefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	if r.URL.RawPath != "" {
		r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, proxyPrefix)
	}
}

// webhookSignatureValid verifies the HMAC-SHA256 signature of the raw body
// against the trigger secret. Accepted header forms (checked in order):
// X-Temporality-Signature: sha256-<hex> (native, matches the outbound channel
// webhook driver) and X-Hub-Signature-256: sha256=<hex> (GitHub style).
func webhookSignatureValid(r *http.Request, raw []byte, secret string) bool {
	candidate := func(header, prefix string) string {
		value := strings.TrimSpace(r.Header.Get(header))
		if value == "" {
			return ""
		}
		return strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	signatures := []string{
		candidate("X-Temporality-Signature", "sha256-"),
		candidate("X-Hub-Signature-256", "sha256="),
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, got := range signatures {
		if got != "" && hmac.Equal([]byte(got), []byte(expected)) {
			return true
		}
	}
	return false
}

// webhookTokenMatches compares a presented per-trigger delivery token with
// the stored sha256 hex hash in constant time.
func webhookTokenMatches(token, storedHash string) bool {
	if token == "" {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return hmac.Equal([]byte(hex.EncodeToString(digest[:])), []byte(storedHash))
}

// unwrapJSONString undoes accidental double encoding: a config that arrived
// as a JSON *string* holding a JSON document ("\"{\\\"path\\\":…}\"") is
// unwrapped to the embedded object. Anything else — plain objects, plain
// strings, malformed input — passes through unchanged.
func unwrapJSONString(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var inner string
	if json.Unmarshal(raw, &inner) != nil {
		return raw
	}
	trimmed := strings.TrimSpace(inner)
	if !strings.HasPrefix(trimmed, "{") {
		return raw
	}
	var probe json.RawMessage
	if json.Unmarshal([]byte(inner), &probe) != nil {
		return raw
	}
	return json.RawMessage(inner)
}

// webhookStoredTokenHash reads the delivery-token hash out of a stored
// webhook trigger config ("" when absent or unparsable).
func webhookStoredTokenHash(config json.RawMessage) string {
	var cfg workspace.WebhookConfig
	if json.Unmarshal(config, &cfg) != nil {
		return ""
	}
	return cfg.AuthTokenHash
}

// preserveWebhookTokenHash restores the server-stored delivery-token hash
// after a client-supplied config replacement. The hash is managed exclusively
// through the /webhook-token endpoints: a form that round-trips a config it
// never fully saw can neither clobber nor forge it, and a client cannot
// install a hash of its own when none is stored. Non-webhook configs pass
// through untouched.
func preserveWebhookTokenHash(config json.RawMessage, storedHash, triggerType string) json.RawMessage {
	if triggerType != "webhook" {
		return config
	}
	var cfg workspace.WebhookConfig
	if json.Unmarshal(config, &cfg) != nil || cfg.AuthTokenHash == storedHash {
		return config
	}
	cfg.AuthTokenHash = storedHash
	merged, err := json.Marshal(cfg)
	if err != nil {
		return config
	}
	return merged
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

// workflowStatusName maps a Temporal execution status onto the project's
// canonical run status vocabulary. The journal (run.cancelled events),
// workspace_run.status and every UI terminal list use British "cancelled",
// while Temporal reports American "Canceled" — leaking the raw enum name
// broke terminal-state detection in the chat (the spinner never stopped for
// cancelled runs).
func workflowStatusName(status enums.WorkflowExecutionStatus) string {
	switch status {
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return "completed"
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED:
		return "failed"
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return "cancelled"
	case enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return "failed"
	case enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return "running"
	default:
		return strings.ToLower(status.String())
	}
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
		// Drafts await human apply (docs/knowledge-evolution.md Wave A): agents
		// never bind unreviewed content, the current revision keeps serving.
		if s.VersionStatus == workspace.SkillVersionDraft {
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
	if !ok {
		return controlplane.RoleAdmin, nil
	}
	return authorizeRunUseFor(ctx, ws, &principal, input, agentID, agentCfg)
}

// authorizeRunUseFor is authorizeRunUse against an explicit principal: the
// intake pipeline authorizes the task it starts as the identified sender
// (docs/inbound-messages.md §9), not as the kernel's internal token.
func authorizeRunUseFor(ctx context.Context, ws *workspace.Store, principal *controlplane.Principal, input *agent.RunInput, agentID string, agentCfg *workspace.Agent) (controlplane.Role, error) {
	if principal == nil || principal.Role >= controlplane.RoleAdmin || principal.VisibleUnits == nil {
		return controlplane.RoleAdmin, nil
	}
	// Project: org visibility or explicit membership (§15-16).
	effective, err := projectAccessRole(ctx, ws, principal, input.Project)
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
	principal, _ := controlplane.FromContext(ctx)
	return runExecContextFor(principal, input, agentID, authorizedAs)
}

// runExecContextFor is runExecContext with an explicit authorizing principal:
// the intake start-task path attributes the run to the message sender, while
// the request context carries the kernel's internal token.
func runExecContextFor(principal controlplane.Principal, input *agent.RunInput, agentID string, authorizedAs controlplane.Role) map[string]any {
	skillIDs := make([]string, 0, len(input.Skills))
	for _, s := range input.Skills {
		skillIDs = append(skillIDs, s.ID)
	}
	mcpIDs := input.MCPServers
	if mcpIDs == nil {
		mcpIDs = []string{}
	}
	authorizedBy := map[string]any{"user_id": input.ActorID, "role": "", "org_unit_id": ""}
	if principal.UserID != "" || principal.Subject != "" {
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
		"authorized_by":   authorizedBy,
		"sandbox_profile": sandboxProfileName(input),
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

// validateChannelTypes checks channel rows against the kernel transport
// registry: storage accepts any slug, but a user must not be able to save a
// channel nothing can deliver to.
func validateChannelTypes(channels []workspace.UserChannel) error {
	if len(channels) == 0 {
		return nil
	}
	known := make(map[string]bool, len(channels))
	for _, spec := range agent.ChannelTypes() {
		known[spec.Type] = true
	}
	for _, channel := range channels {
		if !known[channel.Type] {
			return fmt.Errorf("unknown channel type %q — the kernel cannot deliver to it", channel.Type)
		}
	}
	return nil
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

// loadAgentProposal resolves the agent and one of its proposals for the
// decide endpoints, writing the error response when resolution fails. Org
// visibility stays with the caller: it needs the visibleUnits closure.
func loadAgentProposal(w http.ResponseWriter, r *http.Request, ws *workspace.Store, agentID string, proposalID int64) (workspace.Agent, workspace.AgentProposal, bool) {
	failure := func(status int, err error) (workspace.Agent, workspace.AgentProposal, bool) {
		writeError(w, status, err)
		return workspace.Agent{}, workspace.AgentProposal{}, false
	}
	a, err := ws.GetAgent(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return failure(404, err)
		}
		return failure(500, err)
	}
	proposal, err := ws.GetAgentProposal(r.Context(), agentID, proposalID)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return failure(404, err)
		}
		return failure(500, err)
	}
	return a, proposal, true
}

// agentVersionSnapshot builds the immutable definition snapshot with the
// effective prompt compiled by the same deterministic function runs use
// (docs/plan-evaluable-agent.md, decision 2).
func agentVersionSnapshot(a workspace.Agent, author, promptSource, generatorModel string) workspace.AgentVersion {
	if promptSource == "" {
		promptSource = "manual"
	}
	return workspace.AgentVersion{
		AgentID: a.ID, Version: a.DefinitionVersion, Definition: *a.Definition,
		Description: a.Description,
		CompiledPrompt: agent.EffectiveSystemPrompt(agent.AgentPromptInput{
			Name: a.Name, Description: a.Description, Definition: *a.Definition,
		}, "", a.SystemPrompt),
		PromptSource: promptSource, GeneratorModel: generatorModel,
		Author: author, CreatedAt: time.Now().UTC(),
	}
}

// writeAgentVersion stores the immutable definition snapshot (used by the
// manual-edit paths; the gated proposal apply rides the transactional
// Store.ApplyAgentProposal and passes its snapshot directly).
func writeAgentVersion(ctx context.Context, ws *workspace.Store, a workspace.Agent, author, promptSource, generatorModel string) {
	_ = ws.InsertAgentVersion(ctx, agentVersionSnapshot(a, author, promptSource, generatorModel))
}

// agentPromptPreview returns the effective system prompt shown in the UI:
// override > compiled definition > stored manual prompt (legacy).
func agentPromptPreview(a workspace.Agent) (source, prompt string) {
	if a.Definition != nil {
		if a.Definition.PromptOverride != "" {
			return "override", a.Definition.PromptOverride
		}
		return "definition", agent.CompileAgentPrompt(agent.AgentPromptInput{
			Name: a.Name, Description: a.Description, Definition: *a.Definition,
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
// agentRefLookup is the storage surface resolveAgentRef needs; the fake in
// tests satisfies the same contract.
type agentRefLookup interface {
	GetAgent(ctx context.Context, id string) (workspace.Agent, error)
	FindAgentsByName(ctx context.Context, name, projectID string) ([]workspace.Agent, error)
}

// resolveAgentRef resolves a delegation target reference: the exact id first,
// then a case-insensitive display-name match scoped to the project (globals +
// the project's own agents, mirroring ListAgentsByProject scope). Models
// derive ids from display names ("Lead" → "lead") and miss on non-slug ids;
// an unambiguous name match saves the failed round-trip, an ambiguous one
// reports the candidate ids so the delegating agent can disambiguate with the
// agents tool. A miss returns ErrNotFound — indistinguishable from an unknown
// id on purpose (existence of agents in other projects is not leaked).
func resolveAgentRef(ctx context.Context, ws agentRefLookup, ref, project string) (workspace.Agent, error) {
	a, err := ws.GetAgent(ctx, ref)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, workspace.ErrNotFound) {
		return workspace.Agent{}, err
	}
	matches, mErr := ws.FindAgentsByName(ctx, ref, project)
	if mErr != nil {
		return workspace.Agent{}, mErr
	}
	switch len(matches) {
	case 0:
		return workspace.Agent{}, workspace.ErrNotFound
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return workspace.Agent{}, fmt.Errorf("agent name %q matches multiple agents (%s); use an exact agent_id — list valid ids with the agents tool", ref, strings.Join(ids, ", "))
	}
}

// validateAgentSandboxProfile guards agent save endpoints: an empty value is
// the installation default; anything else must be a registry profile id. The
// dead restricted/standard/privileged triple is rejected with a pointer to
// what replaced it (docs/sandbox-profiles.md §9).
func validateAgentSandboxProfile(ctx context.Context, ws *workspace.Store, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	switch value {
	case "restricted", "standard", "privileged":
		return fmt.Errorf("sandbox_profile %q is retired: assign a sandbox profile from the registry (Settings → Sandbox profiles)", value)
	}
	if _, err := ws.GetSandboxProfile(ctx, value); err != nil {
		return fmt.Errorf("unknown sandbox profile %q", value)
	}
	return nil
}

func applyAgentConfig(ctx context.Context, ws *workspace.Store, a *workspace.Agent, input *agent.RunInput) {
	if a == nil {
		return
	}
	if a.Model != "" {
		input.Model = a.Model
	}
	// The agent card's turn budget is part of the enforced run configuration:
	// honoring it here carries the configured budget through every path that
	// resolves an agent (task runs, triggers, delegation via run-config)
	// instead of silently falling back to the workflow default.
	if a.MaxTurns != nil && *a.MaxTurns > 0 {
		input.MaxTurns = *a.MaxTurns
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
			Name: a.Name, Description: a.Description, Definition: *a.Definition,
		}, input.Role, a.SystemPrompt)
		caps := a.Definition.Capabilities
		if !caps.Cap(caps.ModifyFiles) {
			input.ReadOnly = true
			// The native file tools bypass the sandbox container mount, so
			// read-only runs must deny them here too (defense-in-depth beside
			// the activity-side read-only check).
			input.DenyTools = append(input.DenyTools, "write_file", "edit_file")
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
	// definition never get it by default. The agents roster rides along: an
	// agent that cannot delegate has no use for the target list.
	if a.Definition == nil || a.Definition.Capabilities.Delegation == nil || !*a.Definition.Capabilities.Delegation {
		input.DenyTools = append(input.DenyTools, "delegate", "plan", "agents")
	}
	if a.ReadOnly != nil && *a.ReadOnly {
		input.ReadOnly = true
	}
	resolveAgentSandbox(ctx, ws, a, input)
	if useSkills {
		resolveAgentSkills(ctx, ws, a, input)
	}
	resolveAgentMCP(a, input)
	// Granted secret names ride the run config (docs/secrets-management.md §8):
	// values are resolved per tool call at the execution boundary, so a rotated
	// value applies mid-run and nothing secret persists in the workflow.
	input.Secrets = a.Secrets
}

// applyAgentDefaults merges an agent card into a run input coming from a
// direct API caller (POST /v1/agent/runs). Unlike applyAgentConfig — used by
// paths that build the input server-side and therefore own every field —
// it treats the caller's explicit fields as overrides: model, turn budget,
// system prompt, skills and MCP servers only fill in what the caller left
// unset. Capability enforcement (read-only, denied tools, network off
// without the capability, knowledge skip, delegation deny, granted secrets)
// always applies — those are policy, not defaults. One deliberate
// limitation, matching every other run path: the caller cannot force
// network off for an agent whose card grants it.
func applyAgentDefaults(ctx context.Context, ws *workspace.Store, a *workspace.Agent, input *agent.RunInput) {
	if a == nil {
		return
	}
	if input.Model == "" {
		input.Model = a.Model
	}
	if input.MaxTurns == 0 && a.MaxTurns != nil && *a.MaxTurns > 0 {
		input.MaxTurns = *a.MaxTurns
	}
	// Card-granted network rides in as a default: the caller's bare false
	// (zero value) means "unset", and — matching every other run path —
	// there is no way to force network off for a network agent via this API.
	// The caps check below can still clamp it off.
	if a.NetworkAccess != nil && *a.NetworkAccess {
		input.NetworkAccess = true
	}
	callerPrompt := input.SystemPrompt
	input.AgentID = a.ID
	input.AgentVersion = a.DefinitionVersion
	useSkills := true
	if a.Definition != nil {
		// A caller-provided prompt wins verbatim; otherwise the compiled
		// effective prompt (definition + card prompt) applies as-is.
		if callerPrompt == "" {
			input.SystemPrompt = agent.EffectiveSystemPrompt(agent.AgentPromptInput{
				Name: a.Name, Description: a.Description, Definition: *a.Definition,
			}, input.Role, a.SystemPrompt)
		}
		caps := a.Definition.Capabilities
		if !caps.Cap(caps.ModifyFiles) {
			input.ReadOnly = true
			// The native file tools bypass the sandbox container mount, so
			// read-only runs must deny them here too (defense-in-depth beside
			// the activity-side read-only check).
			input.DenyTools = append(input.DenyTools, "write_file", "edit_file")
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
	} else if callerPrompt == "" && a.SystemPrompt != "" {
		input.SystemPrompt = a.SystemPrompt
	}
	// Delegation is an explicitly granted capability (docs/agent-delegation.md):
	// nil or false denies the delegate tool, and agents without a structured
	// definition never get it by default. The agents roster rides along: an
	// agent that cannot delegate has no use for the target list.
	if a.Definition == nil || a.Definition.Capabilities.Delegation == nil || !*a.Definition.Capabilities.Delegation {
		input.DenyTools = append(input.DenyTools, "delegate", "plan", "agents")
	}
	if a.ReadOnly != nil && *a.ReadOnly {
		input.ReadOnly = true
	}
	resolveAgentSandbox(ctx, ws, a, input)
	if useSkills {
		resolveAgentSkills(ctx, ws, a, input)
		input.Skills = dedupeSkillRefs(input.Skills)
	}
	if len(input.MCPServers) == 0 {
		input.MCPServers = a.MCPServers
	}
	if len(input.ToolAllowlist) == 0 {
		input.ToolAllowlist = a.Tools
	}
	input.DenyTools = dedupeStrings(input.DenyTools)
	input.Secrets = a.Secrets
}

// resolveAgentSandbox carries the agent's sandbox profile into the run input
// (docs/sandbox-profiles.md §3.2). An empty card value keeps nil — the runner's
// installation image and the legacy Go cache env, exactly the pre-profile
// behavior. A missing profile row or an unbuilt image yields a spec without
// an image: run_command then fails with an explicit profile-not-built error
// instead of silently substituting another toolchain (§4.3).
func resolveAgentSandbox(ctx context.Context, ws *workspace.Store, a *workspace.Agent, input *agent.RunInput) {
	if a == nil || strings.TrimSpace(a.SandboxProfile) == "" {
		return
	}
	spec := &agent.SandboxSpec{Profile: a.SandboxProfile}
	if profile, err := ws.GetSandboxProfile(ctx, a.SandboxProfile); err == nil && profile.Image != "" {
		spec.Image = profile.Image
		spec.Memory = profile.Memory
		spec.CPUs = profile.CPUs
		spec.ScratchSize = profile.ScratchSize
		spec.TmpSize = profile.TmpSize
		spec.CacheEnv = profile.CacheEnv
	}
	input.Sandbox = spec
}

// sandboxProfileName returns the run's sandbox profile id for the exec
// context snapshot; empty means the installation default.
func sandboxProfileName(input *agent.RunInput) string {
	if input.Sandbox == nil {
		return ""
	}
	return input.Sandbox.Profile
}

func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, v := range values {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func dedupeSkillRefs(refs []agent.SkillRef) []agent.SkillRef {
	if len(refs) < 2 {
		return refs
	}
	seen := make(map[string]struct{}, len(refs))
	out := refs[:0]
	for _, ref := range refs {
		if _, dup := seen[ref.ID]; dup {
			continue
		}
		seen[ref.ID] = struct{}{}
		out = append(out, ref)
	}
	return out
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

// secretRequest is the API payload for secret create/update. Value is
// write-only: it is accepted on create/update/rotation and never returned.
type secretRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"` // builtin (default) | env
	ExternalRef string `json:"external_ref"`
	Description string `json:"description"`
	OrgUnitID   string `json:"org_unit_id"` // only set on create; updates keep the stored binding
	Value       string `json:"value"`
}

// maxArtifactContentBytes caps a builtin artifact body (docs/artifact-registry.md
// §5): the registry holds reference documents, not file storage.
const maxArtifactContentBytes = 2 << 20 // 2 MiB

type artifactRequest struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Provider    string  `json:"provider"` // builtin (default) | url
	ExternalRef string  `json:"external_ref"`
	Content     string  `json:"content"`
	MimeType    string  `json:"mime_type"`
	Kind        string  `json:"kind"`    // reference (default) | work
	SagaID      *string `json:"saga_id"` // saga link; nil = keep on edit, "" = detach
	OriginRun   string  `json:"origin_run"`
	OrgUnitID   string  `json:"org_unit_id"` // only set on create; updates keep the stored binding
}

// toArtifact validates the request and converts it into a workspace.Artifact.
// ID is optional here (create derives it from the title); the caller sets or
// checks it. Errors land in 422 responses, so every message names the field.
func (r artifactRequest) toArtifact(id string) (workspace.Artifact, error) {
	if r.Provider == "" {
		r.Provider = "builtin"
	}
	if r.Provider != "builtin" && r.Provider != "url" {
		return workspace.Artifact{}, fmt.Errorf("provider must be builtin or url (got %q)", r.Provider)
	}
	if strings.TrimSpace(r.Title) == "" {
		return workspace.Artifact{}, errors.New("title is required")
	}
	if id == "" {
		id = r.ID
	}
	if r.Provider == "url" {
		ref, err := url.Parse(r.ExternalRef)
		if err != nil || ref.Scheme != "http" && ref.Scheme != "https" || ref.Host == "" {
			return workspace.Artifact{}, errors.New("external_ref must be an absolute http(s) URL for provider=url")
		}
		if r.Content != "" {
			return workspace.Artifact{}, errors.New("content must be empty for provider=url: the body lives outside the registry")
		}
	}
	if r.Provider == "builtin" && len(r.Content) == 0 {
		return workspace.Artifact{}, errors.New("content is required for provider=builtin")
	}
	if r.Provider == "builtin" && len(r.Content) > maxArtifactContentBytes {
		return workspace.Artifact{}, fmt.Errorf("content exceeds %d bytes", maxArtifactContentBytes)
	}
	if r.Kind == "" {
		r.Kind = workspace.ArtifactKindReference
	}
	if r.Kind != workspace.ArtifactKindReference && r.Kind != workspace.ArtifactKindWork {
		return workspace.Artifact{}, fmt.Errorf("kind must be reference or work (got %q)", r.Kind)
	}
	return workspace.Artifact{
		ID:          id,
		Title:       strings.TrimSpace(r.Title),
		Description: strings.TrimSpace(r.Description),
		Provider:    r.Provider,
		ExternalRef: r.ExternalRef,
		Content:     r.Content,
		MimeType:    strings.TrimSpace(r.MimeType),
		Kind:        r.Kind,
		SagaID:      derefString(r.SagaID),
		OriginRun:   strings.TrimSpace(r.OriginRun),
		OrgUnitID:   strings.TrimSpace(r.OrgUnitID),
	}, nil
}

// artifactRange parses the offset/limit query pair for the artifact read
// API. Negative values collapse to zero; limit=0 means "the whole body".
func artifactRange(r *http.Request) (offset, limit int) {
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	return offset, limit
}

// storeSecretValue encrypts and persists one secret value.
func storeSecretValue(ctx context.Context, ws *workspace.Store, masterKey []byte, secretID, value string) error {
	if masterKey == nil {
		return errors.New("KERNEL_SECRET_MASTER_KEY is not configured: values cannot be stored")
	}
	ciphertext, nonce, err := workspace.SealSecret(masterKey, []byte(value))
	if err != nil {
		return err
	}
	return ws.SetSecretValue(ctx, secretID, ciphertext, nonce)
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
// secret:// references in env and headers are resolved here, at apply time:
// the registry only ever sees plaintext connection credentials, while the
// stored config keeps the references.
func loadMCPServers(ctx context.Context, log *slog.Logger, ws *workspace.Store, registry *mcpclient.Registry, masterKey []byte) {
	apply := func() {
		servers, err := ws.ListAllMCPServers(context.Background())
		if err != nil {
			log.Error("list mcp servers", "error", err)
			return
		}
		for _, server := range servers {
			cfg, rErr := resolveMCPConfigSecrets(context.Background(), ws, mcpConfig(server), masterKey)
			if rErr != nil {
				log.Error("resolve mcp server secrets", "server", server.ID, "error", rErr)
				continue
			}
			if err := registry.Apply(context.Background(), cfg); err != nil {
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

// loadSecretMasterKey reads the envelope-encryption key (32 bytes, hex or
// base64). Resolution order: KERNEL_SECRET_MASTER_KEY env var (always wins,
// so operators can pin the key explicitly), then the workspace_setting row
// "secret_master_key" (bootstrapped on first start, survives restarts).
// When neither exists the key is generated, persisted to the database and a
// loud warning is logged: a fresh installation should work out of the box
// while production installs are nudged to set the env var.
func loadSecretMasterKey(ctx context.Context, log *slog.Logger, ws *workspace.Store) []byte {
	if err := controlplane.LoadFileSecrets("KERNEL_SECRET_MASTER_KEY"); err != nil {
		log.Error("load KERNEL_SECRET_MASTER_KEY_FILE", "error", err)
		return nil
	}
	raw := strings.TrimSpace(os.Getenv("KERNEL_SECRET_MASTER_KEY"))
	if raw != "" {
		key, err := decodeSecretKey(raw)
		if err != nil {
			log.Error("KERNEL_SECRET_MASTER_KEY must be 32 bytes of hex or base64", "error", err)
			return nil
		}
		return key
	}
	stored, err := ws.GetSetting(ctx, "secret_master_key")
	if err != nil && !errors.Is(err, workspace.ErrNotFound) {
		log.Error("read secret_master_key setting", "error", err)
		return nil
	}
	if stored != "" {
		key, dErr := decodeSecretKey(stored)
		if dErr != nil {
			log.Error("stored secret_master_key is not 32 bytes of hex or base64; set KERNEL_SECRET_MASTER_KEY to override", "error", dErr)
			return nil
		}
		return key
	}
	generated := make([]byte, 32)
	if _, gErr := rand.Read(generated); gErr != nil {
		log.Error("generate secret master key", "error", gErr)
		return nil
	}
	encoded := hex.EncodeToString(generated)
	if sErr := ws.SetSetting(ctx, "secret_master_key", encoded); sErr != nil {
		log.Error("persist generated secret master key", "error", sErr)
		return nil
	}
	log.Warn("KERNEL_SECRET_MASTER_KEY is not set: generated a new master key and stored it in workspace_setting.secret_master_key — set the env var in production to pin the key explicitly (the env var always wins)")
	return generated
}

func decodeSecretKey(raw string) ([]byte, error) {
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("key must decode to exactly 32 bytes")
	}
	return decoded, nil
}

func secretCacheTTL() time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KERNEL_SECRET_CACHE_TTL_SECONDS"))); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Minute
}

// resolveMCPConfigSecrets substitutes secret://<name>[#key] references in env
// entries and header values with plaintext, decrypting builtin values with the
// master key. The returned config is for live connections only — it must never
// be written back to storage or API responses.
func resolveMCPConfigSecrets(ctx context.Context, ws *workspace.Store, cfg mcpclient.ServerConfig, masterKey []byte) (mcpclient.ServerConfig, error) {
	if !configHasSecretRefs(cfg) {
		return cfg, nil
	}
	if masterKey == nil {
		return cfg, errors.New("KERNEL_SECRET_MASTER_KEY is not configured")
	}
	env := make([]string, len(cfg.Env))
	for i, entry := range cfg.Env {
		resolved, err := resolveSecretEnvEntry(ctx, ws, entry, masterKey)
		if err != nil {
			return cfg, err
		}
		env[i] = resolved
	}
	cfg.Env = env
	if len(cfg.Headers) > 0 {
		headers := make(map[string]string, len(cfg.Headers))
		for name, value := range cfg.Headers {
			if ref, ok := workspace.ParseSecretReference(value); ok {
				resolved, err := resolveSecretValue(ctx, ws, ref, masterKey)
				if err != nil {
					return cfg, fmt.Errorf("header %q: %w", name, err)
				}
				value = resolved
			}
			headers[name] = value
		}
		cfg.Headers = headers
	}
	return cfg, nil
}

func configHasSecretRefs(cfg mcpclient.ServerConfig) bool {
	for _, entry := range cfg.Env {
		if _, ok := workspace.ParseSecretReference(envEntryValue(entry)); ok {
			return true
		}
	}
	for _, value := range cfg.Headers {
		if _, ok := workspace.ParseSecretReference(value); ok {
			return true
		}
	}
	return false
}

// envEntryValue returns the VALUE part of KEY=VALUE; secret references are
// recognized there (KEY=secret://name).
func envEntryValue(entry string) string {
	if idx := strings.IndexByte(entry, '='); idx >= 0 {
		return entry[idx+1:]
	}
	return entry
}

func resolveSecretEnvEntry(ctx context.Context, ws *workspace.Store, entry string, masterKey []byte) (string, error) {
	idx := strings.IndexByte(entry, '=')
	if idx < 0 {
		return entry, nil
	}
	key, value := entry[:idx], entry[idx+1:]
	ref, ok := workspace.ParseSecretReference(value)
	if !ok {
		return entry, nil
	}
	resolved, err := resolveSecretValue(ctx, ws, ref, masterKey)
	if err != nil {
		return "", fmt.Errorf("env %q: %w", key, err)
	}
	return key + "=" + resolved, nil
}

// providerEnvNameRe matches UPPER_SNAKE identifiers — the env-var name shape
// api_key_ref may carry (e.g. XIAOMI_API_KEY). A literal provider key (sk-…,
// ghp_…) never matches, so the env lookup cannot shadow a legacy literal.
var providerEnvNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// resolveProviderAPIKey turns a provider's api_key_ref into the bearer key for
// model calls. Three shapes, in order:
//   - secret://name[#key] — a workspace secret (builtin envelope or env-backed);
//   - an UPPER_SNAKE env var name that is set in this process;
//   - a legacy literal key stored in the column as-is.
//
// Resolution happens here, at the routing boundary, so plaintext keys live
// only in memory and never in the database (docs/secrets-management.md §4).
// workspaceDraftModel picks a model client for workspace-level LLM features
// (the agent wizard) on installs that have no env gateway: the first
// provider (by ID, for determinism) that lists models, first model in its
// catalog. Mirrors the run-path routing contract — provider credentials
// resolve at the boundary — without inventing a "default model" setting.
func workspaceDraftModel(ctx context.Context, lister providerLister, resolveKey func(ctx context.Context, ref string) (string, error)) (*llm.Client, error) {
	providers, err := lister.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers for the agent wizard: %w", err)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	for _, p := range providers {
		if len(p.Models) == 0 {
			continue
		}
		model := p.Models[0]
		apiKey, kErr := resolveKey(ctx, p.APIKeyRef)
		if kErr != nil {
			return nil, fmt.Errorf("provider %q credential: %w", p.ID, kErr)
		}
		// OptionalConfigFromEnv keeps the shared defaults (timeouts,
		// MaxOutputTokens=16384) without requiring an env endpoint — the same
		// contract run routing applies. A zero config here would send
		// max_tokens=0 and 400 at the provider.
		config, cErr := llm.OptionalConfigFromEnv()
		if cErr != nil {
			return nil, fmt.Errorf("provider %q model config: %w", p.ID, cErr)
		}
		config.BaseURL = p.BaseURL
		config.Model = model
		config.APIKey = apiKey
		return llm.New(config), nil
	}
	return nil, errors.New("no model configured for the agent wizard: add a provider with models (Providers page) or set TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID")
}

type providerLister interface {
	ListProviders(ctx context.Context) ([]workspace.Provider, error)
}

func resolveProviderAPIKey(ctx context.Context, ws *workspace.Store, ref string, masterKey []byte) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if secretRef, ok := workspace.ParseSecretReference(ref); ok {
		return resolveSecretValue(ctx, ws, secretRef, masterKey)
	}
	if providerEnvNameRe.MatchString(ref) {
		if value := strings.TrimSpace(os.Getenv(ref)); value != "" {
			return value, nil
		}
	}
	return ref, nil
}

// resolveSecretValue decrypts a builtin secret by name. Sub-keys (#key) select
// a field of a JSON-valued secret.
func resolveSecretValue(ctx context.Context, ws *workspace.Store, ref workspace.SecretReference, masterKey []byte) (string, error) {
	secret, err := ws.GetSecretByName(ctx, ref.Name)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return "", fmt.Errorf("secret %q is not configured", ref.Name)
		}
		return "", err
	}
	var value string
	switch secret.Provider {
	case "env":
		value = strings.TrimSpace(os.Getenv("SECRET_" + strings.ToUpper(strings.ReplaceAll(ref.Name, "-", "_"))))
		if value == "" {
			return "", fmt.Errorf("env secret %q is not set", "SECRET_"+strings.ToUpper(strings.ReplaceAll(ref.Name, "-", "_")))
		}
	default:
		ciphertext, nonce, vErr := ws.GetSecretValue(ctx, secret.ID)
		if vErr != nil {
			return "", fmt.Errorf("secret %q has no stored value", ref.Name)
		}
		plaintext, dErr := workspace.OpenSecret(masterKey, ciphertext, nonce)
		if dErr != nil {
			return "", fmt.Errorf("decrypt secret %q: %w", ref.Name, dErr)
		}
		value = string(plaintext)
	}
	if ref.Key == "" {
		return value, nil
	}
	var structured map[string]any
	if jErr := json.Unmarshal([]byte(value), &structured); jErr != nil {
		return "", fmt.Errorf("secret %q is not a JSON object, cannot extract key %q", ref.Name, ref.Key)
	}
	parts := strings.Split(ref.Key, ":")
	current := any(structured)
	for _, part := range parts {
		mapping, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("secret %q: key path %q not found", ref.Name, ref.Key)
		}
		current, ok = mapping[part]
		if !ok {
			return "", fmt.Errorf("secret %q: key path %q not found", ref.Name, ref.Key)
		}
	}
	switch typed := current.(type) {
	case string:
		return typed, nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded), nil
	}
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
	// Draft=true stores the first version as a draft awaiting human apply
	// (docs/knowledge-evolution.md Wave A). Provenance fields record who
	// authored the change and which runs produced it.
	Draft         bool     `json:"draft"`
	Origin        string   `json:"origin"`
	SourceRuns    []string `json:"source_runs"`
	EvidenceRefs  []string `json:"evidence_refs"`
	KnowledgeIDs  []string `json:"knowledge_ids"`
	ChangeSummary string   `json:"change_summary"`
	// Evolution proposal anatomy (docs/living-skills.md §21).
	ObservedProblem string `json:"observed_problem"`
	ProposedChange  string `json:"proposed_change"`
	ExpectedEffect  string `json:"expected_effect"`
}

func (req skillRequest) provenance(defaultOrigin string) workspace.SkillProvenance {
	origin := req.Origin
	if origin != workspace.SkillOriginAgentProposal && origin != workspace.SkillOriginHumanEdit && origin != workspace.SkillOriginInitial {
		origin = defaultOrigin
	}
	return workspace.SkillProvenance{
		Origin: origin, SourceRuns: req.SourceRuns, EvidenceRefs: req.EvidenceRefs,
		KnowledgeIDs: req.KnowledgeIDs, ChangeSummary: req.ChangeSummary,
		ObservedProblem: req.ObservedProblem, ProposedChange: req.ProposedChange, ExpectedEffect: req.ExpectedEffect,
	}
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

// teamRequest is the create/update payload for a team. The manifest arrives as
// structured JSON (protocol + slots + defaults) and is validated against the
// spec vocabulary before storage; team.yaml never appears in the API surface.
type teamRequest struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Manifest    json.RawMessage `json:"manifest"`
	OrgUnitID   string          `json:"org_unit_id"` // only set on create; updates keep the stored binding
	Draft       bool            `json:"draft"`
}

func (req teamRequest) toTeam(teamID string) (workspace.Team, workspace.TeamProvenance, error) {
	if strings.TrimSpace(req.Name) == "" {
		return workspace.Team{}, workspace.TeamProvenance{}, errors.New("name is required")
	}
	if err := workspace.ValidateTeamManifest(req.Manifest); err != nil {
		return workspace.Team{}, workspace.TeamProvenance{}, err
	}
	id := req.ID
	if teamID != "" {
		id = teamID
	}
	if id == "" {
		id = slugify(req.Name)
	}
	version := req.Version
	if version == "" {
		version = "1.0.0"
	}
	return workspace.Team{
		ID: id, Name: req.Name, Description: req.Description,
		Version: version, Manifest: req.Manifest, OrgUnitID: req.OrgUnitID,
	}, workspace.TeamProvenance{Origin: workspace.TeamOriginInitial}, nil
}

// teamVersionRequest proposes a draft version (docs/agent-teams.md §12):
// evolution proposals carry the observed problem, the proposed change and the
// expected effect so the version chain stays explainable.
type teamVersionRequest struct {
	Version  string          `json:"version"`
	Manifest json.RawMessage `json:"manifest"`
	Origin   string          `json:"origin"`
	// Evolution proposal anatomy.
	SourceRuns      []string `json:"source_runs"`
	ChangeSummary   string   `json:"change_summary"`
	ObservedProblem string   `json:"observed_problem"`
	ProposedChange  string   `json:"proposed_change"`
	ExpectedEffect  string   `json:"expected_effect"`
}

func (req teamVersionRequest) provenance() workspace.TeamProvenance {
	origin := req.Origin
	if origin != workspace.TeamOriginAgentProposal && origin != workspace.TeamOriginHumanEdit && origin != workspace.TeamOriginInitial {
		origin = workspace.TeamOriginAgentProposal
	}
	return workspace.TeamProvenance{
		Origin: origin, SourceRuns: req.SourceRuns, ChangeSummary: req.ChangeSummary,
		ObservedProblem: req.ObservedProblem, ProposedChange: req.ProposedChange, ExpectedEffect: req.ExpectedEffect,
	}
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

// actorFromRequest resolves the applying user for skill lifecycle events;
// anonymous when the auth gate is disabled.
func actorFromRequest(r *http.Request) observation.Actor {
	if principal, ok := controlplane.FromContext(r.Context()); ok {
		return observation.Actor{ID: principal.Subject, Type: "human"}
	}
	return observation.Actor{ID: "anonymous", Type: "human"}
}

// overviewVisibleProjects resolves which projects the caller may see in the
// cost projects overview, with their org units — the same resolution as
// GET /v1/workspace/projects: admins, service tokens and org-unassigned users
// (nil org chain) get every project, an org-scoped user gets their visible
// list. A resolution failure must surface as a 500 upstream: an empty map is
// never allowed to degrade into "show everything" for a user who sees
// nothing.
func overviewVisibleProjects(r *http.Request, ws *workspace.Store) (map[string][]string, error) {
	var userID string
	var isAdmin bool
	units := candidateVisibleUnits(r)
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
		return nil, err
	}
	visible := make(map[string][]string, len(projects))
	for _, p := range projects {
		visible[p.ID] = p.OrgUnitIDs
	}
	return visible, nil
}

// TokenDoneEvent is the terminal frame of the ephemeral token stream.
var TokenDoneEvent = agent.TokenEvent{Type: "done"}

// emitSkillLifecycleEvent records skill.proposed / skill.applied in the
// journal so the evolution chain is observable (docs/knowledge-evolution.md
// §12). Best-effort: a journal outage must not fail the workspace write —
// the version rows remain the source of truth.
func emitSkillLifecycleEvent(ctx context.Context, observationURL, token, eventType, skillID, skillName string, version workspace.SkillVersion, actor observation.Actor) {
	if observationURL == "" {
		return
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", eventType, skillID, version.Version, time.Now().UnixNano())))
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("skill/%s/%s", eventType, hex.EncodeToString(digest[:10])),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Run:   firstNonEmpty(version.SourceRuns...),
			Actor: actor,
		},
		Type: eventType,
		Data: map[string]any{
			"skill_id":       skillID,
			"skill_name":     skillName,
			"version":        version.Version,
			"origin":         version.Origin,
			"change_summary": version.ChangeSummary,
			"source_runs":    version.SourceRuns,
		},
	}
	if err := event.Validate(); err != nil {
		return
	}
	payload, err := json.Marshal(map[string]any{"events": []observation.Event{event}})
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, observationURL+"/v1/observations/events", bytes.NewReader(payload))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
}

// emitAgentDefinitionEvent records agent.definition.proposed/applied/rejected
// in the journal so proposal decisions stay observable (docs/plan-evaluable-agent.md
// §2.2 stage 10). Best-effort: the proposal row remains the source of truth.
func emitAgentDefinitionEvent(ctx context.Context, observationURL, token, eventType string, a workspace.Agent, proposal workspace.AgentProposal, actor observation.Actor, extra map[string]any) {
	if observationURL == "" {
		return
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", eventType, a.ID, proposal.ID, time.Now().UnixNano())))
	data := map[string]any{
		"agent_id":     a.ID,
		"agent_name":   a.Name,
		"proposal_id":  proposal.ID,
		"base_version": proposal.BaseVersion,
		"problem":      proposal.Problem,
		"change":       proposal.Change,
		"effect":       proposal.Effect,
		"evidence":     proposal.Evidence,
		"author":       proposal.Author,
	}
	for key, value := range extra {
		data[key] = value
	}
	// The first run-scoped evidence ref becomes the event's run context.
	runRef := ""
	for _, ref := range proposal.Evidence {
		if id, ok := strings.CutPrefix(ref, "run:"); ok {
			runRef = id
			break
		}
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("agent-definition/%s/%s", eventType, hex.EncodeToString(digest[:10])),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Run:   runRef,
			Actor: actor,
		},
		Type: eventType,
		Data: data,
	}
	if err := event.Validate(); err != nil {
		return
	}
	payload, err := json.Marshal(map[string]any{"events": []observation.Event{event}})
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, observationURL+"/v1/observations/events", bytes.NewReader(payload))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
}

// hitlOutbox is the durable-outbox surface the verdict emission needs;
// *outbox.Outbox satisfies it and tests provide fakes.
type hitlOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// emitHitlVerdict records hitl.verdict.recorded (specs/quality-loop.md §9)
// on every terminal human_request transition that carries a verdict.
// Best-effort: the human_request row stays the source of truth. Event ids
// include UnixNano (§8 idempotency note), so journal duplicates are impossible.
func emitHitlVerdict(ctx context.Context, events hitlOutbox, req workspace.HumanRequest) {
	verdictType := workspace.Verdict(req)
	if verdictType == "" || events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("hitl-verdict/%s/%d", req.ID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: req.ProjectID, Run: req.RunID,
			Actor: observation.Actor{ID: req.AnsweredBy, Type: "human"},
		},
		Type: "hitl.verdict.recorded",
		Data: map[string]any{
			"run_id":        req.RunID,
			"agent_id":      req.AgentID,
			"agent_version": req.AgentVersion,
			"type":          verdictType,
			"source":        workspace.VerdictSourceHumanRequest,
			"answered_by":   req.AnsweredBy,
		},
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit hitl verdict", "operation", req.ID, "error", err)
	}
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

// translit maps Cyrillic runes to Latin so uploaded file names survive
// slugification (slugify itself only keeps [a-z0-9]).
func translit(s string) string {
	var b strings.Builder
	// translitMap only covers lowercase Cyrillic, so fold the input to
	// lowercase first; callers feed the result into slugify which is
	// case-insensitive anyway.
	for _, r := range strings.ToLower(s) {
		if lat, ok := translitMap[r]; ok {
			b.WriteString(lat)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var translitMap = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// derefString unwraps an optional request field (nil → "").
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
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
	// Trim both ends: a title opening with an emoji or quote used to leave a
	// leading dash ("-12-cpu-2026…" run ids).
	result := strings.Trim(b.String(), "-")
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
