package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/kernel/mcpclient"
	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

type EventOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// SandboxRunner is the sandbox contract the kernel depends on: resolving a
// workspace against the configured root and executing commands inside it.
// It exists so failure-injection drills can fake the runner in tests.
type SandboxRunner interface {
	SandboxRoot() string
	ResolveWorkspace(path string) (string, error)
	Execute(context.Context, sandbox.Request) (sandbox.Result, error)
}

// sandboxConfigured reports whether a sandbox runner actually backs command
// execution: activities.Sandbox may be a swappable wrapper installed before
// any image exists (first-run onboarding adds one later), so a plain nil
// check is not enough — an empty root means the same as no runner.
func sandboxConfigured(r SandboxRunner) bool {
	return r != nil && r.SandboxRoot() != ""
}

type Activities struct {
	Events EventOutbox
	Model  *llm.Client
	// JudgeModel resolves the installation's supervisor/judge model — the
	// fallback for background quality work (skill builder, area normalization)
	// when the run/project carries no default model.
	JudgeModel func(ctx context.Context) (*llm.Client, error)
	HTTP       *http.Client
	// TemporalityURL is the journal base URL (observations, hints, events).
	TemporalityURL string
	// WorkspaceURL is the base URL of the kernel's own workspace API
	// (skills, triggers, projects). Agent tools that read or write workspace
	// entities must call this, not the journal.
	WorkspaceURL string
	// WorkspaceToken authenticates loopback calls against the kernel's own
	// workspace API when the kernel gate is enabled. It is a startup-random
	// internal credential injected in main, never exposed via config.
	WorkspaceToken string
	// APIToken authenticates direct journal calls (hints, knowledge lookup)
	// when the journal requires bearer auth; it mirrors TEMPORALITY_API_TOKEN
	// used by the event outbox publisher.
	APIToken string
	MCP      *mcpclient.Registry
	SourceID string
	Sandbox  SandboxRunner
	// Secrets resolves agent-granted secret names into values at the execution
	// boundary; nil disables secret injection (tests, env-only setups).
	Secrets *SecretBroker
	// NetworkAccess mirrors the run's network capability: web tools register
	// only when the agent is allowed to reach the internet (PrepareRun).
	NetworkAccess bool
	// SearXNGURL is the base URL of the self-hosted SearXNG instance backing
	// the web_search tool; empty disables the tool.
	SearXNGURL string
	// WebFetchAllowPrivate permits web_fetch to reach private/loopback targets
	// (e.g. an internal docs server); off by default — SSRF guard stays strict.
	WebFetchAllowPrivate bool
	// WebSearchPerMin caps web_search calls per minute for the whole kernel
	// process (0 disables the cap). One SearXNG query fans out into dozens of
	// engine requests and search engines ban the server IP, so the budget is
	// global, not per run or per project.
	WebSearchPerMin int
	// searchLimiter materializes WebSearchPerMin lazily so directly-constructed
	// Activities (tests) need no init step.
	searchLimiterOnce sync.Once
	searchLimiter     *webSearchLimiter
	// BrowserCDPURL is the base URL of the CDP-compatible headless browser
	// backing the browser tool (Lightpanda by default); empty disables the
	// tool. The endpoint is operator-configured like SearXNG, so the SSRF
	// guard does not apply — the browser container sits on an isolated
	// network without access to internal services.
	BrowserCDPURL string
	// BrowserPerMin caps browser actions per minute for the whole kernel
	// process (0 means the default of 60; a negative value disables the cap).
	BrowserPerMin int
	// browserOnce materializes the engine and the action budget lazily.
	browserOnce    sync.Once
	browser        *browserEngine
	browserLimiter *webSearchLimiter
	// browserTabs tracks one tab per run; idle tabs are swept on every call.
	browserTabsMu sync.Mutex
	browserTabs   map[string]*browserTab
	// Tokens is the ephemeral live-token bus feeding the /runs/{id}/tokens SSE
	// endpoint; nil disables streaming (tests run the blocking path).
	Tokens *TokenBus
	// Console is the ephemeral run_command output registry feeding
	// /v1/agent/console/{operationID} (v2-consolidated.md §6); nil disables
	// the live stream and the registry — a test constructing Activities for
	// other tools must not pay for console bookkeeping.
	Console *Console
	// providerCacheMu + providerCache hold recent model→provider resolutions
	// (see modelConfigFor). call_model fires every turn; providers change
	// rarely — a short TTL keeps the hot path free of workspace round trips.
	providerCacheMu sync.Mutex
	providerCache   map[string]providerCacheEntry
	// supervisorMu + supervisor cache the panel settings (vision model) for
	// the vision routing decision; TTL keeps operator edits near-real-time.
	supervisorMu    sync.Mutex
	supervisorCache supervisorSettingsCache
	// artifactCacheMu + artifactCache memoize url-provider bodies fetched by
	// artifact_read (docs/artifact-registry.md §10, phase 2): egress lives at
	// the kernel — the sandbox never dials out — and the TTL keeps repeated
	// reads of one external document from hammering the corporate system.
	artifactCacheMu sync.Mutex
	artifactCache   map[string]cachedArtifact
	// ChannelSettings loads the admin-editable channel settings overlay
	// (workspace channel_transport row: per-channel payload + UI URL). Nil —
	// or a load error — degrades to the KERNEL_CHANNEL__* environment
	// variables, preserving env-only deployments.
	ChannelSettings func(context.Context) (ChannelSettings, error)
	// OperationsWindow bounds how far back the uncertain-operations listing
	// scans the journal (TEMPORALITY_OPERATIONS_WINDOW, default 7d). Zero falls
	// back to the default at call time so directly-constructed Activities
	// (tests) need no init step.
	OperationsWindow time.Duration
	// secretIssuedMu + secretIssued dedupe secret.issued per run: every
	// run_command with a granted secret re-resolves it at the execution
	// boundary (the agent never holds values), so without the gate each
	// command journals another "issued" event and six git calls read as six
	// grants. Denials stay per-call — a retry after a mid-run admin grant is
	// a real, actionable change.
	secretIssuedMu sync.Mutex
	secretIssued   map[string]time.Time
	// skillEmbedCache memoizes per-skill passage vectors (keyed by skill id,
	// invalidated on version change) so skill_search's semantic tier does not
	// re-embed the registry on every query.
	skillEmbedMu    sync.Mutex
	skillEmbedCache map[string]skillEmbedEntry
}

// skillEmbedEntry pairs a cached passage vector with the skill version it
// was computed from.
type skillEmbedEntry struct {
	version string
	vec     []float32
}

// providerCacheEntry is one cached model-config resolution; ok=false is the
// cached negative "no provider serves this model" answer.
type providerCacheEntry struct {
	config    llm.Config
	ok        bool
	fetchedAt time.Time
}

// providerCacheTTL bounds how stale a resolution may be: long enough to keep
// the per-turn hot path cheap, short enough that a provider edit applies to
// running agents within a minute.
const providerCacheTTL = 60 * time.Second

// supervisorCacheTTL bounds how stale the supervisor panel values (vision
// model, HAB coefficients) may be in the kernel's hot path: an operator edit
// applies within a minute.
const supervisorCacheTTL = 60 * time.Second

type supervisorSettingsCache struct {
	visionModel    string
	embeddingModel string
	fetchedAt      time.Time
}

func NewActivities(events EventOutbox) (*Activities, error) {
	// Boot tolerates a missing default model (clean install configuring
	// providers through onboarding): runs that need the default fail at call
	// time with an actionable error instead of taking the kernel down.
	config, err := llm.OptionalConfigFromEnv()
	if err != nil {
		return nil, err
	}
	var model *llm.Client
	if config.BaseURL != "" && config.Model != "" {
		model = llm.New(config)
	}
	registry := mcpclient.NewRegistry()
	legacy, err := mcpclient.FromEnv(context.Background())
	if err != nil {
		return nil, err
	}
	if legacy != nil {
		registry.AdoptLegacy(legacy)
	}
	sandboxRunner, err := sandbox.NewFromEnv()
	if err != nil {
		return nil, err
	}
	// The swappable wrapper lets first-run onboarding install a runner (built
	// and pinned through POST /v1/setup/sandbox/build) after startup; with no
	// image configured it stays transparently inert — sandboxConfigured checks
	// the root, not the wrapper.
	activities := &Activities{Events: events, Model: model, HTTP: &http.Client{Timeout: config.Timeout}, TemporalityURL: strings.TrimRight(env("TEMPORALITY_URL", "http://localhost:8080"), "/"), WorkspaceURL: strings.TrimRight(env("KERNEL_WORKSPACE_URL", "http://localhost:8090"), "/"), APIToken: strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")), MCP: registry, SourceID: env("KERNEL_SOURCE_ID", "temporality-agent-kernel"), Sandbox: sandbox.NewSwappable(sandboxRunner)}
	// The operations listing window keeps journal scans bounded (see
	// reconcile.go defaultOperationsWindow); a bad value must fail loudly at
	// boot instead of silently unbounding the scan.
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_OPERATIONS_WINDOW")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("TEMPORALITY_OPERATIONS_WINDOW must be a positive duration (e.g. 168h), got %q", raw)
		}
		activities.OperationsWindow = parsed
	}
	return activities, nil
}

// Sandbox returns the swappable sandbox runner so the control plane can
// replace it at runtime (onboarding build endpoint).
func (a *Activities) SandboxSwappable() *sandbox.Swappable {
	if s, ok := a.Sandbox.(*sandbox.Swappable); ok {
		return s
	}
	return nil
}

func (a *Activities) ToolDefs() []llm.ToolDef {
	return a.ToolDefsFor(nil)
}

// runCommandTool is the sandbox execution surface. /workspace is the
// project's persistent storage shared across agents, runs and sessions
// (PrepareRun), so the description is the contract that tells models what
// survives there and what does not.
var runCommandTool = llm.ToolDef{
	Name:        "run_command",
	Description: "Run a command in an isolated sandbox container. The working directory is /workspace — persistent storage shared by all agents and runs of this project: files saved there survive across sessions and it may already contain files from earlier work, so treat them as shared and do not remove what you did not create. /tmp and /scratch are ephemeral and disappear between calls. Toolchain caches (GOCACHE, GOMODCACHE) point at the persistent per-project /cache volume, so builds keep their cache across runs. Network access depends on agent configuration. Provide argv as an array.",
	Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "timeout_sec": map[string]any{"type": "integer", "description": "Wall-clock cap for this command, up to 900 (15 min, the hard ceiling). Default 300. Test suites, builds and installs: pass 600+ explicitly instead of relying on the default"}}, "required": []string{"command"}},
}

// BuiltinToolDefs returns kernel tools plus the sandbox surface (run_command
// and the native file tools, when the sandbox is configured) — the tool set
// independent of any MCP server.
func (a *Activities) BuiltinToolDefs() []llm.ToolDef {
	defs := KernelTools()
	if sandboxConfigured(a.Sandbox) {
		defs = append(defs, runCommandTool)
		defs = append(defs, fileToolDefs...)
	}
	return defs
}

// ToolDefsFor returns configured MCP tools for the given servers plus the
// sandbox surface (run_command, file tools). An empty server list keeps the
// legacy env server only.
func (a *Activities) ToolDefsFor(servers []string) []llm.ToolDef {
	defs := a.MCP.ToolDefsFor(servers)
	if sandboxConfigured(a.Sandbox) {
		defs = append(defs, runCommandTool)
		defs = append(defs, fileToolDefs...)
	}
	return defs
}

func (a *Activities) ApprovalTools() []string {
	return a.ApprovalToolsFor(nil)
}

func (a *Activities) ApprovalToolsFor(servers []string) []string {
	return a.MCP.ApprovalToolsFor(servers)
}

func (a *Activities) PrepareRun(input *RunInput) error {
	input.SourceID = a.SourceID
	input.Tools = a.ToolDefsFor(input.MCPServers)
	input.ApprovalTools = a.ApprovalToolsFor(input.MCPServers)
	input.AutoApproveTools = nil
	input.MCPServer = MCPServerName()
	a.NetworkAccess = input.NetworkAccess
	if sandboxConfigured(a.Sandbox) {
		// Consequential workspace tools covered by the kernel's sandbox policy
		// (sandbox.workspace.v1): the project workspace is an isolated sandbox
		// root, so commands and file writes there need no human round-trip.
		autoApproveTools := []string{"run_command", "write_file", "edit_file"}
		input.AutoApproveTools = autoApproveTools
		if input.WorkspacePath == "" {
			// Persistent workspace per project: shared across agents and runs,
			// so build caches (node_modules, go mod, pip) and artifacts persist.
			projectDir := filepath.Join(a.Sandbox.SandboxRoot(), "projects", input.Project)
			if err := os.MkdirAll(projectDir, 0o777); err != nil {
				return fmt.Errorf("create project workspace: %w", err)
			}
			input.WorkspacePath = projectDir
		}
		workspace, err := a.Sandbox.ResolveWorkspace(input.WorkspacePath)
		if err != nil {
			return err
		}
		input.WorkspacePath = workspace
	}
	// Web research tools (docs/web-research.md): registered only for agents
	// with the network capability. With network on, the sandbox can already
	// curl the same endpoints — the tools are the safe, capped, observable
	// version of that, so they ride the sandbox auto-approve policy.
	if input.NetworkAccess {
		input.AutoApproveTools = append(input.AutoApproveTools, "web_fetch")
		input.Tools = append(input.Tools, webFetchTool)
		if a.SearXNGURL != "" {
			input.AutoApproveTools = append(input.AutoApproveTools, "web_search")
			input.Tools = append(input.Tools, webSearchTool)
		}
		input.AutoApproveTools = append(input.AutoApproveTools, "fetch_image")
		input.Tools = append(input.Tools, fetchImageTool)
		// Headless browser (docs/web-research.md §4): registered when a CDP
		// backend is configured; rides the sandbox auto-approve like the other
		// web tools — the isolated browser container reaches exactly the
		// internet the sandbox could curl anyway, with no credentials inside.
		if a.BrowserCDPURL != "" {
			input.AutoApproveTools = append(input.AutoApproveTools, "browser")
			input.Tools = append(input.Tools, browserTool)
		}
	}
	return nil
}

// ResolveAgentRequest asks the workspace for the enforced run configuration of
// a delegation target (docs/agent-delegation.md §4).
type ResolveAgentRequest struct {
	Project         string `json:"project"`
	AgentID         string `json:"agent_id"`
	RunID           string `json:"run_id"` // child run id assigned by the parent workflow
	TaskID          string `json:"task_id,omitempty"`
	Prompt          string `json:"prompt"`
	ActorID         string `json:"actor_id,omitempty"`
	MaxTurns        int    `json:"max_turns,omitempty"`
	DelegationDepth int    `json:"delegation_depth,omitempty"`
}

// ResolveAgent builds the child RunInput for a delegated run: it fetches the
// target agent's enforced configuration from the workspace API (the same code
// path a manual run start uses), applies sandbox/tool preparation and denies
// further delegation when the depth limit is reached.
func (a *Activities) ResolveAgent(ctx context.Context, request ResolveAgentRequest) (RunInput, error) {
	endpoint := fmt.Sprintf("%s/v1/workspace/agents/%s/run-config?project=%s", a.WorkspaceURL, url.PathEscape(request.AgentID), url.QueryEscape(request.Project))
	if request.ActorID != "" {
		// The delegated run inherits the org position of the delegating chain:
		// the run-config endpoint resolves the actor's policies from it (§24).
		endpoint += "&actor_id=" + url.QueryEscape(request.ActorID)
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return RunInput{}, err
	}
	if status == http.StatusNotFound {
		// The body carries the actionable detail the workspace computed (unknown
		// id, ambiguous name with candidate ids, scope miss) — surfacing it lets
		// the model self-correct instead of guessing again.
		return RunInput{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("agent %q is not available in project %q: %s", request.AgentID, request.Project, workspaceErrorDetail(body)), "AgentNotAvailable", nil)
	}
	if status != http.StatusOK {
		return RunInput{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("workspace returned %d resolving agent %q: %s", status, request.AgentID, truncate(body, 400)), "AgentResolutionFailed", nil)
	}
	var child RunInput
	if err := json.Unmarshal([]byte(body), &child); err != nil {
		return RunInput{}, temporal.NewNonRetryableApplicationError("agent run configuration is malformed: "+err.Error(), "AgentResolutionFailed", nil)
	}
	child.RunID = request.RunID
	child.Project = request.Project
	child.TaskID = request.TaskID
	child.Prompt = request.Prompt
	child.ActorID = request.ActorID
	child.DelegationDepth = request.DelegationDepth
	if request.MaxTurns > 0 {
		// The delegating model guesses budgets low (observed 9–15 for real
		// subtasks): children died mid-work, were marked incomplete, and the
		// lead burned its own budget re-planning. The card stays the ceiling of
		// what the child may use; the floor keeps a model guess from crippling
		// the work below any chance to finish.
		child.MaxTurns = max(request.MaxTurns, DelegatedTurnsFloor)
	}
	if err := a.PrepareRun(&child); err != nil {
		return RunInput{}, temporal.NewNonRetryableApplicationError("prepare delegated run: "+err.Error(), "AgentResolutionFailed", nil)
	}
	if child.DelegationDepth >= MaxDelegationDepth {
		// agents is the delegation roster: at the depth limit the child cannot
		// delegate, so the list would only burn a turn.
		child.DenyTools = append(child.DenyTools, "delegate", "plan", "agents")
	}
	return child, nil
}

// MCPServerName identifies the MCP server behind mcp__* tools in the event
// stream. An explicit KERNEL_MCP_SERVER_NAME wins; otherwise the basename of
// KERNEL_MCP_COMMAND serves as a stable default. Empty when no MCP server is
// configured.
func MCPServerName() string {
	if name := strings.TrimSpace(os.Getenv("KERNEL_MCP_SERVER_NAME")); name != "" {
		return name
	}
	command := strings.TrimSpace(os.Getenv("KERNEL_MCP_COMMAND"))
	if command == "" {
		return ""
	}
	return filepath.Base(command)
}

func (a *Activities) RecordEvent(ctx context.Context, event observation.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if a.Events == nil {
		return errors.New("event outbox is not configured")
	}
	return a.Events.Enqueue(ctx, event)
}

// apiToken resolves the journal bearer at call time: TEMPORALITY_API_TOKEN
// is preferred (the background re-resolver updates it after a boot-order
// race — journal down at kernel start), falling back to the captured field.
func (a *Activities) apiToken() string {
	if t := strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")); t != "" {
		return t
	}
	return a.APIToken
}

// hasImages reports whether any message carries image blocks: the trigger
// condition for the supervisor's vision-model routing.
func (a *Activities) hasImages(request ModelRequest) bool {
	for _, message := range request.Messages {
		if len(message.Images) > 0 {
			return true
		}
	}
	return false
}

// visionModelFor returns the supervisor panel's vision model, cached for
// supervisorCacheTTL. Failures degrade to "no override".
func (a *Activities) visionModelFor(ctx context.Context) string {
	a.supervisorMu.Lock()
	defer a.supervisorMu.Unlock()
	if time.Since(a.supervisorCache.fetchedAt) < supervisorCacheTTL {
		return a.supervisorCache.visionModel
	}
	vision := ""
	if a.WorkspaceURL != "" {
		endpoint := fmt.Sprintf("%s/v1/workspace/supervisor", a.WorkspaceURL)
		if body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil); err == nil && status == 200 {
			var view struct {
				VisionModel string `json:"vision_model"`
			}
			if json.Unmarshal([]byte(body), &view) == nil {
				vision = strings.TrimSpace(view.VisionModel)
			}
		}
	}
	a.supervisorCache = supervisorSettingsCache{visionModel: vision, fetchedAt: time.Now()}
	return vision
}

func (a *Activities) CallModel(ctx context.Context, request ModelRequest) (llm.Completion, error) {
	// A retried attempt is the frozen-host recovery path (see the workflow
	// comment on call_model options): after a sleeping machine burns the
	// StartToClose ceiling, this is a fresh start. The journal cannot rely on
	// model.started (emitted once per turn by the deterministic workflow), so
	// retried attempts and stream fallbacks are journaled directly through the
	// outbox under non-colliding ids — without them a dead provider looks like
	// a dead run for the whole silent retry ladder. GetLogger panics outside a
	// real activity context, hence the IsActivity guard.
	attempt := 1
	activityID := ""
	if activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		attempt = int(info.Attempt)
		activityID = info.ActivityID
		// Heartbeat so kernel.nudge cancellation reaches the in-flight HTTP
		// call: Temporal delivers activity cancellation only through heartbeat
		// responses, so without this a cancelled call keeps streaming as a
		// zombie until its own watchdog dies — burning provider tokens and
		// emitting late model.attempt markers next to the retry the nudge
		// already started. The goroutine exits when the activity returns
		// (closed channel) or when the delivered cancellation fires ctx.Done.
		heartbeatDone := make(chan struct{})
		defer close(heartbeatDone)
		go func() {
			ticker := time.NewTicker(modelHeartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					activity.RecordHeartbeat(ctx, nil)
				}
			}
		}()
		if info.Attempt > 1 {
			if logger := activity.GetLogger(ctx); logger != nil {
				logger.Info("model call retrying after attempt failure",
					"run_id", request.RunID, "turn", request.Turn, "attempt", info.Attempt)
			}
			a.emitModelAttempt(ctx, request, attempt, activityID, "activity_retry", 0, "")
		}
	}
	client := a.Model
	// Vision fallback (docs/supervisor-model.md §4): when a call carries images
	// and the supervisor panel pins a vision model, route the call there — the
	// main model may be text-only. Routing reuse means the vision model just
	// needs to be declared by a provider.
	if a.hasImages(request) {
		if vision := a.visionModelFor(ctx); vision != "" && vision != request.Model {
			request.Model = vision
		}
	}
	if request.Model != "" && (a.Model == nil || request.Model != a.Model.Model()) {
		// The existing client config is immutable; constructing a provider client
		// per run keeps model selection out of deterministic Workflow code.
		//
		// A non-default model is routed to the workspace provider that serves
		// it (workspace_provider.models, scoped by the project's org units);
		// only when no provider claims the model do we fall back to the env
		// config with the model name swapped in — the pre-routing behavior.
		routed := false
		if a.WorkspaceURL != "" {
			config, ok, err := a.modelConfigFor(ctx, request.Project, request.Model)
			if err != nil {
				// Transient lookup failure (workspace unreachable, 5xx): do not
				// fail the turn — fall back to env and keep going.
				if activity.IsActivity(ctx) {
					if logger := activity.GetLogger(ctx); logger != nil {
						logger.Warn("model-config lookup failed, falling back to env provider",
							"run_id", request.RunID, "model", request.Model, "error", err.Error())
					}
				}
			} else if ok {
				client = llm.New(config)
				routed = true
			}
		}
		if !routed {
			config, err := llm.ConfigFromEnv()
			if err != nil {
				return llm.Completion{}, fmt.Errorf("model %q is not served by any workspace provider and no default model is configured (set TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID, or add the model to a provider)", request.Model)
			}
			config.Model = request.Model
			client = llm.New(config)
		}
	}
	if client == nil {
		return llm.Completion{}, errors.New("no model configured: set the run's model or a project default, or configure TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID")
	}
	if a.Tokens != nil && request.RunID != "" {
		// Live path: stream tokens to the ephemeral bus for subscribed UIs. A
		// streaming failure falls through to the blocking call below, which has
		// its own retries — the live preview is best-effort, the Completion is
		// the source of truth the workflow records. StreamFailed marks the
		// salvage so model.completed shows the double generation (stream +
		// blocking regenerate) instead of hiding it inside an inflated latency;
		// StreamMs/StreamError record how long the dead stream lived and why.
		streamStarted := time.Now()
		completion, streamErr := client.StreamComplete(ctx, request.Messages, request.Tools, func(delta llm.StreamDelta) bool {
			if delta.Text != "" || delta.Reasoning != "" {
				a.Tokens.Publish(request.RunID, request.Turn, delta.Text, delta.Reasoning)
			}
			// Tool-call deltas are the invisible majority of agent streaming: a
			// turn that goes straight for a tool produces no text at all, so
			// without publishing these the UI shows "waiting for the first
			// token" while arguments stream for the whole call.
			for _, call := range delta.ToolCalls {
				if call.Name != "" || call.ArgsDelta != "" {
					a.Tokens.PublishTool(request.RunID, request.Turn, call.Name, call.ArgsDelta)
				}
			}
			return false
		}, llm.WithPromptCacheKey(request.PromptCacheKey))
		if streamErr == nil {
			return completion, nil
		}
		// A cancelled context means the nudge (or workflow termination) killed
		// the call on purpose: the workflow has already journaled model.nudged
		// and started the retry, so a stream_fallback marker here would show a
		// bogus "retry" for an intentional cancel, and the blocking salvage
		// cannot run on a dead context anyway. Return fast so the cancellation
		// is recorded instead of grinding through the fallback ladder.
		if ctx.Err() != nil {
			return llm.Completion{}, streamErr
		}
		streamMs := time.Since(streamStarted).Milliseconds()
		// A stream that dies of a watchdog timeout means the provider accepted
		// the request and then went silent — it is queueing us server-side. A
		// blocking retry of the same request joins the same queue and only adds
		// minutes of silent waiting (3 attempts × request timeout); failing the
		// attempt here lets the activity retry open a fresh connection, which is
		// exactly the recovery a manual nudge provides — but automatic and
		// sooner. Protocol/status failures keep the blocking fallback: those can
		// be a gateway that simply cannot stream this request.
		if llm.StreamTimedOut(streamErr) {
			if activity.IsActivity(ctx) {
				if logger := activity.GetLogger(ctx); logger != nil {
					logger.Warn("model stream timed out, retrying on a fresh connection",
						"run_id", request.RunID, "turn", request.Turn,
						"stream_ms", streamMs, "error", streamErr.Error())
				}
			}
			a.emitModelAttempt(ctx, request, attempt, activityID, "stream_timeout", streamMs, boundedAttemptError(streamErr))
			return llm.Completion{}, streamErr
		}
		// A stream that stalls behind gateway keepalives can burn most of the
		// activity budget before dying; the fallback then looks suspiciously
		// fast. Log the failure where operators triage live runs and journal it
		// as a model.attempt marker so the trace shows the internal transition
		// instead of a silent multi-minute gap. GetLogger panics outside a real
		// activity context, hence the IsActivity guard.
		if activity.IsActivity(ctx) {
			if logger := activity.GetLogger(ctx); logger != nil {
				logger.Error("model stream failed, falling back to blocking",
					"run_id", request.RunID, "turn", request.Turn,
					"stream_ms", streamMs, "error", streamErr.Error())
			}
		}
		a.emitModelAttempt(ctx, request, attempt, activityID, "stream_fallback", streamMs, boundedAttemptError(streamErr))
		completion, err := client.Complete(ctx, request.Messages, request.Tools, llm.WithPromptCacheKey(request.PromptCacheKey))
		if err == nil {
			completion.StreamFailed = true
			completion.StreamMs = streamMs
			completion.StreamError = streamErr.Error()
		}
		return completion, err
	}
	return client.Complete(ctx, request.Messages, request.Tools, llm.WithPromptCacheKey(request.PromptCacheKey))
}

// boundedAttemptError clips a transport error for the journal marker; the
// full diagnostics live in the kernel log, the event only needs the cause.
func boundedAttemptError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 200 {
		return message[:200]
	}
	return message
}

// emitModelAttempt journals one model.attempt marker straight through the
// event outbox. The deterministic workflow numbers its own events
// sequentially (scope/event/NNNNNN); these activity-side markers live under a
// separate namespace keyed by the Temporal scheduled event id + attempt, so
// they never collide with the workflow sequence and stay idempotent across
// outbox redeliveries (the journal dedups by event_id). Best-effort by
// design: an outbox failure must not fail the model call itself.
func (a *Activities) emitModelAttempt(ctx context.Context, request ModelRequest, attempt int, activityID string, reason string, streamMs int64, streamErr string) {
	if a.Events == nil || request.RunID == "" {
		return
	}
	eventID := fmt.Sprintf("%s/model-attempt/%s/%d", eventScope(a.SourceID, request.Project, request.RunID), activityID, attempt)
	if reason == "stream_fallback" {
		eventID += "/fallback"
	}
	data := map[string]any{"turn": request.Turn, "model": request.Model, "attempt": attempt, "reason": reason}
	if streamMs > 0 {
		data["stream_ms"] = streamMs
	}
	if streamErr != "" {
		data["error"] = streamErr
	}
	event := observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: time.Now().UTC(),
		Source:  observation.Source{ID: a.SourceID, Integration: "temporality-agent-kernel", Version: "0.1"},
		Context: observation.Context{Project: request.Project, Run: request.RunID, Actor: observation.Actor{ID: "system", Type: "agent"}},
		Type:    "model.attempt", Data: data,
	}
	if err := event.Validate(); err != nil {
		return
	}
	// Enqueue is a local buffer write, not a network call; a failure only
	// costs the marker, never the attempt.
	_ = a.Events.Enqueue(ctx, event)
}

// sandboxRequest assembles the run_command sandbox request: the read-only
// and network posture of the run plus the resolved profile overrides
// (docs/sandbox-profiles.md §3.3). Network posture comes from the worker-wide
// field PrepareRun keeps in sync with the current run (pre-existing behavior).
func (a *Activities) sandboxRequest(request ToolRequest, command []string, timeout int, secretEnv []string) (sandbox.Request, error) {
	net := ""
	if a.NetworkAccess {
		net = "bridge"
	}
	req := sandbox.Request{
		Workspace:      request.WorkspacePath,
		Command:        command,
		TimeoutSeconds: timeout,
		ReadOnly:       request.ReadOnly || request.Role == "reviewer" || request.Role == "qa",
		Network:        net,
		Env:            secretEnv,
		// Observability labels (docs/sandbox-storage.md): the health endpoint
		// lists running sandboxes with their run/agent/actor so the dashboard
		// can show who consumes the host.
		Labels: sandboxLabels(request),
	}
	if spec := request.Sandbox; spec != nil {
		if spec.Image == "" {
			// Never silently substitute the installation image for an assigned
			// profile (docs/sandbox-profiles.md §4.3): a QA run needs Node, and a
			// quiet fallback to the Go image would fail its commands in confusing
			// ways instead of naming the fix.
			return sandbox.Request{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("sandbox profile %q is not built or missing — build it in Settings → Sandbox profiles", spec.Profile), "SandboxProfileNotBuilt", nil)
		}
		req.Image = spec.Image
		req.Memory = spec.Memory
		req.CPUs = spec.CPUs
		req.ScratchSize = spec.ScratchSize
		req.TmpSize = spec.TmpSize
		req.CacheEnv = spec.CacheEnv
		req.DockerSocket = spec.DockerSocket
	}
	return req, nil
}

// sandboxLabels tags a sandbox container with the run identity: only non-empty
// values are emitted so ad-hoc runs without an agent definition still work.
func sandboxLabels(request ToolRequest) map[string]string {
	labels := map[string]string{}
	for key, value := range map[string]string{
		"temporality.project": request.Project,
		"temporality.run":     request.RunID,
		"temporality.agent":   request.AgentID,
		"temporality.actor":   request.ActorID,
	} {
		if value != "" {
			labels[key] = value
		}
	}
	return labels
}

func (a *Activities) RunTool(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if len(request.AllowedTools) > 0 && !allowedForAgent(request.AllowedTools, request.Name) {
		// Defense in depth: the workflow already filters the tools advertised to
		// the model; this rejects calls to tools outside the agent's allowlist.
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("tool %q is not allowed for this agent", request.Name), "ToolNotAllowed", nil)
	}
	if allowedForAgent(request.DeniedTools, request.Name) {
		// Capability enforcement: tools disabled by the agent definition are
		// neither advertised nor executable, regardless of the allowlist.
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("tool %q is disabled for this agent", request.Name), "ToolNotAllowed", nil)
	}
	if intakeDialogTools[request.Name] && request.Project != IntakeProjectID {
		// Second layer of the intake-tool scoping: the catalog no longer
		// advertises these outside the intake project, and the execution
		// boundary refuses them even if a crafted prompt sneaks a call through.
		// A tool-result refusal (not an activity error): the run continues.
		return ToolResult{Content: fmt.Sprintf("error: %s only works in the intake project's dialog runs (no inbound message is bound to this run)", request.Name)}, nil
	}
	switch request.Name {
	case "echo":
		value, _ := request.Arguments["text"].(string)
		if value == "" {
			return ToolResult{}, errors.New("echo requires text")
		}
		return ToolResult{Content: value}, nil
	case "run_command":
		if !sandboxConfigured(a.Sandbox) {
			return ToolResult{}, errors.New("sandbox is not configured")
		}
		command, err := stringArgs(request.Arguments["command"])
		if err != nil {
			// Argument validation happens before anything executes: the failure is
			// deterministic, has no side effect, and is fixable by the caller, so it
			// must be neither retried nor reported as an uncertain effect.
			return ToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidToolArguments", nil)
		}
		timeout := 0
		if value, ok := request.Arguments["timeout_sec"].(float64); ok {
			timeout = int(value)
		}
		// Secrets resolve here — at the execution boundary: values go straight
		// into the container environment and never enter the model context.
		secretEnv, deniedSecrets, sErr := a.sandboxSecretEnv(ctx, request)
		if sErr != nil {
			return ToolResult{}, sErr
		}
		if len(deniedSecrets) > 0 {
			return ToolResult{Content: fmt.Sprintf("error: secrets not available for this agent: %s", strings.Join(deniedSecrets, ", "))}, nil
		}
		if faultAfterEffect(request.RunID, "run_command", request.OperationID) {
			// Fault injection for live reconciliation drills: run the command for
			// real (the effect lands), then report a worker-style crash so the
			// workflow records tool.failed with effect=uncertain.
			sbReq, err := a.sandboxRequest(request, command, timeout, secretEnv)
			if err != nil {
				return ToolResult{}, err
			}
			if _, err := a.Sandbox.Execute(ctx, sbReq); err != nil {
				return ToolResult{}, err
			}
			return ToolResult{}, fmt.Errorf("injected worker crash after effect (operation %s)", request.OperationID)
		}
		sbReq, err := a.sandboxRequest(request, command, timeout, secretEnv)
		if err != nil {
			return ToolResult{}, err
		}
		// Live console (v2-consolidated.md §6): when a registry and an
		// operation id exist, stdout/stderr chunks stream into the in-memory
		// tail buffer as the command runs — no journal write, no file, no
		// database (§11.1 defers persistent storage). A nil registry keeps
		// the classic wait-for-completion behavior.
		sbReq.OnOutput = a.Console.Hook(request.Project, request.OperationID)
		result, err := a.Sandbox.Execute(ctx, sbReq)
		if err != nil {
			return ToolResult{}, err
		}
		if a.Console != nil {
			a.Console.Done(request.Project, request.OperationID, result.ExitCode)
		}
		exitCode := result.ExitCode
		return ToolResult{Content: fmt.Sprintf("exit_code=%d\n%s", result.ExitCode, result.Output), ExitCode: &exitCode}, nil
	case "read_file":
		return a.handleReadFile(request)
	case "write_file":
		return a.handleWriteFile(request)
	case "edit_file":
		return a.handleEditFile(request)
	case "web_fetch":
		return a.handleWebFetch(ctx, request)
	case "web_search":
		return a.handleWebSearch(ctx, request)
	case "view_image":
		return a.handleViewImage(request)
	case "fetch_image":
		return a.handleFetchImage(ctx, request)
	case "browser":
		return a.handleBrowser(ctx, request)
	case "list_triggers":
		return a.handleListTriggers(ctx, request)
	case "agents":
		return a.handleAgents(ctx, request)
	case "human_contacts":
		return a.handleHumanContacts(ctx, request)
	case "send_file":
		return a.handleSendFile(ctx, request)
	case "create_trigger":
		return a.handleCreateTrigger(ctx, request)
	case "update_trigger":
		return a.handleUpdateTrigger(ctx, request)
	case "delete_trigger":
		return a.handleDeleteTrigger(ctx, request)
	case "skill_search":
		return a.handleSkillSearch(ctx, request)
	case "user_remember":
		return a.handleUserRemember(ctx, request)
	case "artifact_search":
		return a.handleArtifactSearch(ctx, request)
	case "artifact_read":
		return a.handleArtifactRead(ctx, request)
	case "artifact_create":
		return a.handleArtifactCreate(ctx, request)
	case "artifact_update":
		return a.handleArtifactUpdate(ctx, request)
	case "artifact_submit":
		return a.handleArtifactSubmit(ctx, request)
	case "secret_list":
		return a.handleSecretList(ctx, request)
	case "knowledge_lookup":
		return a.handleKnowledgeLookupTool(ctx, request)
	case "knowledge_explore":
		return a.handleKnowledgeExploreTool(ctx, request)
	case "saga_register":
		return a.handleSagaRegister(ctx, request)
	case "saga_attach":
		return a.handleSagaAttach(ctx, request)
	case "saga_list":
		return a.handleSagaList(ctx, request)
	case "skill_inspect", "skill_validate", "skill_history", "skill_executions", "skill_memory":
		return a.handleSkillAction(ctx, request)
	case "skill_propose":
		return a.handleSkillPropose(ctx, request)
	case "agent_propose":
		return a.handleAgentPropose(ctx, request)
	case "skill_evaluate":
		return a.handleSkillEvaluate(ctx, request)
	case "skill_diff":
		return a.handleSkillDiff(ctx, request)
	case "search_projects":
		return a.handleIntakeSearchProjects(ctx, request)
	case "search_org_units":
		return a.handleIntakeSearchOrgUnits(ctx, request)
	case "start_task":
		return a.handleIntakeStartTask(ctx, request)
	case "intake_decide":
		return a.handleIntakeDecide(ctx, request)
	case "run_status":
		return a.handleIntakeRunStatus(ctx, request)
	case "cancel_run":
		return a.handleIntakeCancelRun(ctx, request)
	default:
		if a.MCP != nil && a.MCP.HasTool(request.Name) {
			if faultAfterEffect(request.RunID, request.Name, request.OperationID) {
				// The effect must really commit before the "crash": the drill
				// exercises the window where the side effect landed but no
				// terminal event will ever arrive. The idempotency key keeps a
				// later operator-driven re-issue from duplicating it.
				if _, err := a.MCP.Call(ctx, request.Name, request.Arguments, request.OperationID); err != nil {
					return ToolResult{}, err
				}
				return ToolResult{}, fmt.Errorf("injected worker crash after effect (operation %s)", request.OperationID)
			}
			content, err := a.MCP.Call(ctx, request.Name, request.Arguments, request.OperationID)
			return ToolResult{Content: content}, err
		}
		return ToolResult{}, fmt.Errorf("tool %q is not registered", request.Name)
	}
}

func allowedForAgent(allowed []string, name string) bool {
	for _, candidate := range allowed {
		if candidate == name {
			return true
		}
	}
	return false
}

// Trigger tool handlers — call the kernel's own HTTP API.

func (a *Activities) handleListTriggers(ctx context.Context, request ToolRequest) (ToolResult, error) {
	project := request.Project
	if project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	url := fmt.Sprintf("%s/v1/workspace/projects/%s/triggers", a.WorkspaceURL, project)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: list triggers failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleCreateTrigger(ctx context.Context, request ToolRequest) (ToolResult, error) {
	name, _ := request.Arguments["name"].(string)
	typ, _ := request.Arguments["type"].(string)
	prompt, _ := request.Arguments["prompt"].(string)
	if name == "" || typ == "" || prompt == "" {
		return ToolResult{Content: "error: name, type, and prompt are required"}, nil
	}
	config := map[string]any{"prompt": prompt}
	if cron, ok := request.Arguments["cron"].(string); ok && cron != "" {
		config["cron"] = cron
	}
	if path, ok := request.Arguments["path"].(string); ok && path != "" {
		config["path"] = path
		config["prompt_template"] = prompt
		delete(config, "prompt")
	}
	if eventType, ok := request.Arguments["event_type"].(string); ok && eventType != "" {
		config["event_type"] = eventType
		// Event filters (workspace.EventConfig): actor_id / actor_type /
		// project / data.<field> — the server validates the vocabulary.
		if filters, ok := request.Arguments["filter"].(map[string]any); ok && len(filters) > 0 {
			clean := make(map[string]string, len(filters))
			for key, value := range filters {
				if s, ok := value.(string); ok && s != "" {
					clean[key] = s
				}
			}
			if len(clean) > 0 {
				config["filter"] = clean
			}
		}
	}
	trigger := map[string]any{
		"project_id": request.Project,
		"name":       name,
		"type":       typ,
		"enabled":    true,
		"config":     config,
	}
	if agentID, ok := request.Arguments["agent_id"].(string); ok && agentID != "" {
		trigger["agent_id"] = agentID
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost, a.WorkspaceURL+"/v1/workspace/triggers", trigger)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: create trigger failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleUpdateTrigger(ctx context.Context, request ToolRequest) (ToolResult, error) {
	triggerID, _ := request.Arguments["trigger_id"].(string)
	if triggerID == "" {
		return ToolResult{Content: "error: trigger_id is required"}, nil
	}
	update := map[string]any{}
	if enabled, ok := request.Arguments["enabled"].(bool); ok {
		update["enabled"] = enabled
	}
	if name, ok := request.Arguments["name"].(string); ok && name != "" {
		update["name"] = name
	}
	if cron, ok := request.Arguments["cron"].(string); ok && cron != "" {
		update["config"] = map[string]any{"cron": cron}
	}
	if prompt, ok := request.Arguments["prompt"].(string); ok && prompt != "" {
		cfg, _ := update["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}
		cfg["prompt"] = prompt
		update["config"] = cfg
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost, a.WorkspaceURL+"/v1/workspace/triggers/"+triggerID, update)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: update trigger failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleDeleteTrigger(ctx context.Context, request ToolRequest) (ToolResult, error) {
	triggerID, _ := request.Arguments["trigger_id"].(string)
	if triggerID == "" {
		return ToolResult{Content: "error: trigger_id is required"}, nil
	}
	url := fmt.Sprintf("%s/v1/workspace/triggers/%s", a.WorkspaceURL, triggerID)
	body, status, err := a.workspaceDo(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: delete trigger failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: fmt.Sprintf("deleted trigger %s", triggerID)}, nil
}

// modelConfigFor resolves which provider serves a model for a project: it
// asks the workspace which of the project's visible providers lists the model
// and returns a ready llm.Config pointed at that provider. ok=false with a
// nil error is the clean negative "no provider serves this model" answer
// (cached); a non-nil error is a transport/server failure the caller may
// retry or fall back from.
func (a *Activities) modelConfigFor(ctx context.Context, project, model string) (config llm.Config, ok bool, err error) {
	key := project + "|" + model
	a.providerCacheMu.Lock()
	if a.providerCache == nil {
		a.providerCache = map[string]providerCacheEntry{}
	}
	if hit, seen := a.providerCache[key]; seen && time.Since(hit.fetchedAt) < providerCacheTTL {
		a.providerCacheMu.Unlock()
		return hit.config, hit.ok, nil
	}
	a.providerCacheMu.Unlock()

	endpoint := fmt.Sprintf("%s/v1/workspace/model-config?model=%s&project=%s", a.WorkspaceURL, url.QueryEscape(model), url.QueryEscape(project))
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return llm.Config{}, false, err
	}
	if status == http.StatusNotFound {
		// Definitive negative: no visible provider lists this model. Cache it —
		// the fallback path below still works, and hammering the endpoint every
		// turn would only burn a round trip.
		a.providerCachePut(key, llm.Config{}, false)
		return llm.Config{}, false, nil
	}
	if status >= 400 {
		return llm.Config{}, false, fmt.Errorf("model-config lookup failed (HTTP %d): %s", status, truncate(body, 200))
	}
	var answer struct {
		ProviderID string `json:"provider_id"`
		BaseURL    string `json:"base_url"`
		APIKey     string `json:"api_key"`
		Model      string `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		return llm.Config{}, false, fmt.Errorf("model-config decode: %w", err)
	}
	if answer.BaseURL == "" || answer.Model == "" {
		return llm.Config{}, false, fmt.Errorf("model-config response incomplete for model %q", model)
	}
	// An empty api_key is a legitimate keyless provider (ollama, lmstudio);
	// llm.Config sends no Authorization header for those.
	// The endpoint carries routing only (base URL, key, model); timeouts and
	// caps come from the env config when it parses, else the same hard defaults
	// ConfigFromEnv would apply.
	config = llm.Config{BaseURL: answer.BaseURL, APIKey: answer.APIKey, Model: answer.Model}
	if envConfig, err := llm.ConfigFromEnv(); err == nil {
		config.Temperature = envConfig.Temperature
		config.Timeout = envConfig.Timeout
		config.StreamIdleTimeout = envConfig.StreamIdleTimeout
		config.StreamNoProgressTimeout = envConfig.StreamNoProgressTimeout
		config.StreamFirstTokenTimeout = envConfig.StreamFirstTokenTimeout
		config.MaxOutputTokens = envConfig.MaxOutputTokens
		config.Reasoning = envConfig.Reasoning
	} else {
		config.Timeout = 180 * time.Second
		config.StreamIdleTimeout = 90 * time.Second
		config.StreamNoProgressTimeout = 4 * time.Minute
		config.StreamFirstTokenTimeout = 2 * time.Minute
		config.MaxOutputTokens = 16384
	}
	a.providerCachePut(key, config, true)
	return config, true, nil
}

// providerCachePut stores one resolution under the mutex.
func (a *Activities) providerCachePut(key string, config llm.Config, ok bool) {
	a.providerCacheMu.Lock()
	defer a.providerCacheMu.Unlock()
	if a.providerCache == nil {
		a.providerCache = map[string]providerCacheEntry{}
	}
	a.providerCache[key] = providerCacheEntry{config: config, ok: ok, fetchedAt: time.Now()}
}

// workspaceErrorDetail extracts the message the workspace API reports in its
// error envelope ({"error": ...}); anything else is truncated raw so callers
// still see actionable detail instead of an opaque status code.
func workspaceErrorDetail(body string) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(body), &payload) == nil && payload.Error != "" {
		return truncate(payload.Error, 300)
	}
	return truncate(strings.TrimSpace(body), 300)
}

// workspaceDo performs an authenticated request against the kernel's own
// workspace API (skills, triggers). It returns the response body and status;
// transport failures come back as errors so tool results never present a
// 404 payload as usable content.
func (a *Activities) workspaceDo(ctx context.Context, method, url string, body any) (string, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return "", 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token := a.WorkspaceToken
	if token == "" {
		token = a.apiToken()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), resp.StatusCode, nil
}

// Intake tool handlers (docs/inbound-messages.md §7). The closed tool surface
// of the intake agent: search, classification and task creation. Identity
// never comes from model arguments — the activity supplies the sender
// (request.ActorID) and the message (resolved by intake run id on the API
// side), so the dialog cannot misattribute a task.
func (a *Activities) handleIntakeSearchProjects(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.ActorID == "" {
		return ToolResult{Content: "error: sender is not set for this run"}, nil
	}
	query, _ := request.Arguments["query"].(string)
	endpoint := fmt.Sprintf("%s/v1/intake/projects?sender=%s&query=%s", a.WorkspaceURL, url.QueryEscape(request.ActorID), url.QueryEscape(query))
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: project search failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: body}, nil
}

func (a *Activities) handleIntakeSearchOrgUnits(ctx context.Context, request ToolRequest) (ToolResult, error) {
	body, status, err := a.workspaceDo(ctx, http.MethodGet, a.WorkspaceURL+"/v1/org/units", nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: org unit search failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: compactJSON(body, 4000)}, nil
}

func (a *Activities) handleIntakeStartTask(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.RunID == "" {
		return ToolResult{Content: "error: run id is not set"}, nil
	}
	projectID, _ := request.Arguments["project_id"].(string)
	title, _ := request.Arguments["title"].(string)
	prompt, _ := request.Arguments["prompt"].(string)
	if projectID == "" || title == "" || prompt == "" {
		return ToolResult{Content: "error: project_id, title and prompt are required"}, nil
	}
	payload := map[string]any{"project_id": projectID, "title": title, "prompt": prompt}
	if agentID, ok := request.Arguments["agent_id"].(string); ok && agentID != "" {
		payload["agent_id"] = agentID
	}
	if saga, ok := request.Arguments["saga"].(string); ok && strings.TrimSpace(saga) != "" {
		payload["saga"] = strings.TrimSpace(saga)
	}
	endpoint := fmt.Sprintf("%s/v1/intake/runs/%s/start-task", a.WorkspaceURL, url.PathEscape(request.RunID))
	body, status, err := a.workspaceDo(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: start_task failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: "Task started: " + body}, nil
}

// handleIntakeRunStatus answers progress questions: the sender's earlier
// routed messages with their run outcomes. The sender is the run actor —
// the intake run always runs as the identified sender (docs §10).
func (a *Activities) handleIntakeRunStatus(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.ActorID == "" {
		return ToolResult{Content: "error: sender is not set for this run"}, nil
	}
	limit := 10
	if requested, ok := numericArg(request.Arguments["limit"]); ok && requested > 0 && requested <= 50 {
		limit = int(requested)
	}
	endpoint := fmt.Sprintf("%s/v1/intake/runs?sender=%s&limit=%d", a.WorkspaceURL, url.QueryEscape(request.ActorID), limit)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: run_status failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: compactJSON(body, 6000)}, nil
}

// handleIntakeCancelRun stops a sender-owned run; the endpoint re-checks
// ownership against the sender's routed messages (docs §10).
func (a *Activities) handleIntakeCancelRun(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.RunID == "" {
		return ToolResult{Content: "error: run id is not set"}, nil
	}
	targetRun, _ := request.Arguments["run_id"].(string)
	if strings.TrimSpace(targetRun) == "" {
		return ToolResult{Content: "error: run_id is required (take it from run_status)"}, nil
	}
	endpoint := fmt.Sprintf("%s/v1/intake/runs/%s/cancel", a.WorkspaceURL, url.PathEscape(request.RunID))
	body, status, err := a.workspaceDo(ctx, http.MethodPost, endpoint, map[string]any{"run_id": targetRun})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: cancel_run failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: "Cancel requested: " + body}, nil
}

func (a *Activities) handleIntakeDecide(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.RunID == "" {
		return ToolResult{Content: "error: run id is not set"}, nil
	}
	classification, _ := request.Arguments["classification"].(string)
	switch classification {
	case "task", "amendment", "status", "cancel", "chatter", "reply":
	default:
		return ToolResult{Content: "error: classification must be one of task, amendment, status, cancel, chatter, reply"}, nil
	}
	payload := map[string]any{"classification": classification}
	if note, ok := request.Arguments["note"].(string); ok && note != "" {
		payload["note"] = note
	}
	endpoint := fmt.Sprintf("%s/v1/intake/runs/%s/decision", a.WorkspaceURL, url.PathEscape(request.RunID))
	body, status, err := a.workspaceDo(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: intake_decide failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: "Classification recorded: " + classification}, nil
}

// Skill tool handlers — read-only navigation over the skill registry. Skills
// change only through proposals and approval, never from a run
// (docs/living-skills.md §26).
func (a *Activities) handleSkillSearch(ctx context.Context, request ToolRequest) (ToolResult, error) {
	// Skills are workspace-global: the registry is shared across projects.
	url := fmt.Sprintf("%s/v1/workspace/skills", a.WorkspaceURL)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: skill search failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	query, _ := request.Arguments["query"].(string)
	if query == "" {
		return ToolResult{Content: compactJSON(body, 4000)}, nil
	}
	var page struct {
		Skills []skillIndexEntry `json:"skills"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	// Match per keyword instead of one literal substring: a multi-word query
	// like "code review quality gate" must still surface relevant skills.
	// The description carries the human wording — the most likely place a
	// natural-language query actually matches.
	keywords := strings.Fields(strings.ToLower(query))
	var scored []skillScoredMatch
	docs := make([]string, 0, len(page.Skills))
	for _, s := range page.Skills {
		docs = append(docs, s.searchText())
		haystack := strings.ToLower(s.searchText())
		score := 0
		for _, kw := range keywords {
			if strings.Contains(haystack, kw) {
				score++
			}
		}
		scored = append(scored, skillScoredMatch{item: map[string]any{"id": s.ID, "name": s.Name, "version": s.Version, "capabilities": s.Manifest.Capabilities, "tools": s.Manifest.Tools}, score: score})
	}
	// Semantic tier bridges the language gap: skills are usually written in
	// English while the model queries in the dialog's language (a Russian
	// «проверь код ревью» matches zero English keywords). The multilingual
	// e5 model ranks by meaning; lexical matches keep their primary status.
	// An unavailable embedder (model still loading, inference failure)
	// degrades to the pure lexical behavior.
	cosines := a.skillCosines(ctx, request.Project, query, page.Skills, docs)
	matches := rankSkillMatches(scored, cosines)
	if len(matches) == 0 {
		// Zero matches must not read as 'no skills exist': return the
		// registry's id+name index so the agent sees what IS available and can
		// re-query with better terms.
		index := make([]map[string]any, 0, len(page.Skills))
		for _, s := range page.Skills {
			index = append(index, map[string]any{"id": s.ID, "name": s.Name})
		}
		encoded, _ := json.Marshal(map[string]any{"skills": matches, "count": 0, "note": "no keyword matches; the registry itself is listed in 'all_skills'", "all_skills": index})
		return ToolResult{Content: string(encoded)}, nil
	}
	encoded, _ := json.Marshal(map[string]any{"skills": matches, "count": len(matches)})
	return ToolResult{Content: string(encoded)}, nil
}

// skillSemanticThreshold is the cosine above which an e5-small vector pair
// counts as a meaningful cross-language match (relevant pairs typically sit
// at 0.6–0.8; the bar sits low on purpose — recall over precision, the
// lexical tier stays the precision anchor).
const skillSemanticThreshold = 0.55

// skillScoredMatch pairs one registry row's rendered entry with its two
// search signals: keyword coverage (lexical, the precision anchor) and the
// embedding cosine against the query (the cross-language recall tier).
type skillScoredMatch struct {
	item   map[string]any
	score  int
	cosine float32
}

// rankSkillMatches orders the registry for skill_search: lexical coverage
// first, semantic proximity second, registry order on ties. Without any
// lexical hit, only skills above the semantic bar surface — zero-vector
// fallbacks (embedder unavailable) keep the pure lexical behavior. Empty
// output sends the caller to the all_skills index.
func rankSkillMatches(scored []skillScoredMatch, cosines []float32) []map[string]any {
	for i := range scored {
		if i < len(cosines) {
			scored[i].cosine = cosines[i]
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].cosine > scored[j].cosine
	})
	matches := make([]map[string]any, 0, len(scored))
	for _, m := range scored {
		// A lexical hit is in; a keyword-miss needs the semantic bar. When the
		// embedder is unavailable every cosine is zero and only lexical hits
		// survive — the pre-semantic behavior.
		if m.score == 0 && m.cosine < skillSemanticThreshold {
			continue
		}
		matches = append(matches, m.item)
	}
	return matches
}

// skillIndexEntry is one registry row as skill_search sees it.
type skillIndexEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Manifest    struct {
		Capabilities []string `json:"capabilities"`
		Tools        []string `json:"tools"`
	} `json:"manifest"`
}

// searchText is the full document behind both search tiers: the lexical
// haystack and the embedding passage (id keeps exact-name queries working,
// capabilities/tools carry the functional wording).
func (s skillIndexEntry) searchText() string {
	return s.ID + " " + s.Name + " " + s.Description + " " + strings.Join(s.Manifest.Capabilities, " ") + " " + strings.Join(s.Manifest.Tools, " ")
}

// skillCosines computes the query-to-document cosine for every skill, with a
// per-(skill,version) passage cache: the registry is small but re-embedding
// it on every search would still burn CPU. Any failure returns an all-zero
// slice — the caller falls back to pure lexical ranking.
func (a *Activities) skillCosines(ctx context.Context, project, query string, skills []skillIndexEntry, docs []string) []float32 {
	out := make([]float32, len(skills))
	if len(skills) == 0 || strings.TrimSpace(query) == "" {
		return out
	}
	a.skillEmbedMu.Lock()
	vectors := make([][]float32, len(skills))
	missing := []int{}
	for i, s := range skills {
		if c, ok := a.skillEmbedCache[s.ID]; ok && c.version == s.Version {
			vectors[i] = c.vec
		} else {
			missing = append(missing, i)
		}
	}
	a.skillEmbedMu.Unlock()
	if len(missing) > 0 {
		texts := make([]string, len(missing))
		for j, i := range missing {
			texts[j] = docs[i]
		}
		fresh, err := a.EmbedTexts(ctx, project, EmbedPassage, texts)
		if err != nil {
			return out // zeros: pure lexical fallback
		}
		a.skillEmbedMu.Lock()
		for j, i := range missing {
			vectors[i] = fresh[j]
			a.skillEmbedCache[skills[i].ID] = skillEmbedEntry{version: skills[i].Version, vec: fresh[j]}
		}
		a.skillEmbedMu.Unlock()
	}
	queryVecs, err := a.EmbedTexts(ctx, project, EmbedQuery, []string{query})
	if err != nil || len(queryVecs) != 1 {
		return out
	}
	q := queryVecs[0]
	for i, v := range vectors {
		if v == nil {
			continue
		}
		out[i] = cosine32(q, v)
	}
	return out
}

// cosine32 computes the cosine of two equal-length vectors; zero-length
// inputs yield 0.
func cosine32(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// handleKnowledgeLookupTool backs the agent-facing knowledge_lookup tool: a
// priming cue carries only a compact rule + knowledge_id, and this returns the
// full item — grounds, lifecycle and scope — for cues that matter to the task.
func (a *Activities) handleKnowledgeLookupTool(ctx context.Context, request ToolRequest) (ToolResult, error) {
	knowledgeID, _ := request.Arguments["knowledge_id"].(string)
	if knowledgeID == "" {
		return ToolResult{Content: "error: knowledge_id is required"}, nil
	}
	if request.Project == "" {
		return ToolResult{Content: "error: project is required"}, nil
	}
	url := fmt.Sprintf("%s/v1/observations/knowledge?project=%s&knowledge_id=%s", a.TemporalityURL, url.QueryEscape(request.Project), url.QueryEscape(knowledgeID))
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if token := a.apiToken(); token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.HTTP.Do(httpRequest)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	status := response.StatusCode
	if status == 404 {
		return ToolResult{Content: fmt.Sprintf("error: knowledge %q not found in this project", knowledgeID)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: knowledge lookup failed (HTTP %d): %s", status, truncate(string(body), 400))}, nil
	}
	return ToolResult{Content: compactJSON(string(body), 6000)}, nil
}

// handleKnowledgeExploreTool backs the agent-facing knowledge_explore tool:
// the research surface over the project knowledge base (orient/search/expand/
// trace/compare/synthesize). Read-only and project-scoped like knowledge_lookup;
// the journal enforces visibility before the engine sees anything.
func (a *Activities) handleKnowledgeExploreTool(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.Project == "" {
		return ToolResult{Content: "error: project is required"}, nil
	}
	// The caller may target ANOTHER project than the run's own — but ONLY an
	// intake dialog run may do that (the intake dialog lives in the system
	// 'intake' project and answers questions about the sender's real
	// projects). In ordinary runs the model naming a foreign project would
	// widen its own visibility — the journal enforces the run's project.
	if request.Project == IntakeProjectID {
		if project, _ := request.Arguments["project"].(string); strings.TrimSpace(project) != "" {
			request.Project = strings.TrimSpace(project)
		}
	}
	operation, _ := request.Arguments["operation"].(string)
	if operation == "" {
		return ToolResult{Content: "error: operation is required (orient, search, expand, trace, compare, synthesize)"}, nil
	}
	payload := map[string]any{"project": request.Project, "operation": operation}
	if question, _ := request.Arguments["question"].(string); strings.TrimSpace(question) != "" {
		payload["question"] = strings.TrimSpace(question)
	}
	payload["targets"] = toolStringList(request.Arguments["targets"])
	payload["relations"] = toolStringList(request.Arguments["relations"])
	if value, ok := toolInt(request.Arguments["max_depth"]); ok {
		payload["max_depth"] = value
	}
	if value, ok := toolInt(request.Arguments["max_items"]); ok {
		payload["max_items"] = value
	}
	if value, ok := request.Arguments["include_candidates"].(bool); ok {
		payload["include_candidates"] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TemporalityURL+"/v1/observations/knowledge/explore", bytes.NewReader(encoded))
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if token := a.apiToken(); token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.HTTP.Do(httpRequest)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status := response.StatusCode; status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: knowledge explore failed (HTTP %d): %s", status, truncate(string(body), 400))}, nil
	}
	return ToolResult{Content: compactJSON(string(body), 6000)}, nil
}

// toolStringList coerces a tool argument into a clean string slice; model
// tool calls deliver arrays as []any with string items.
func toolStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

// toolInt coerces a numeric tool argument (delivered as float64 by JSON)
// into an int when present.
func toolInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	return int(number), true
}

func (a *Activities) handleSkillAction(ctx context.Context, request ToolRequest) (ToolResult, error) {
	skillID, _ := request.Arguments["skill_id"].(string)
	if skillID == "" {
		return ToolResult{Content: "error: skill_id is required"}, nil
	}
	var url string
	switch request.Name {
	case "skill_inspect":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s", a.WorkspaceURL, skillID)
	case "skill_validate":
		return a.handleSkillValidate(ctx, skillID)
	case "skill_history":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/versions", a.WorkspaceURL, skillID)
	case "skill_executions":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/executions", a.WorkspaceURL, skillID)
	case "skill_memory":
		url = fmt.Sprintf("%s/v1/workspace/skills/%s/memory", a.WorkspaceURL, skillID)
	default:
		return ToolResult{Content: "error: unknown skill tool " + request.Name}, nil
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status == 404 {
		return ToolResult{Content: fmt.Sprintf("error: skill %q not found; use skill_search to list ids", skillID)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: %s failed (HTTP %d): %s", request.Name, status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: compactJSON(body, 6000)}, nil
}

// handleSkillValidate fetches the stored skill and re-checks its contract.
func (a *Activities) handleSkillValidate(ctx context.Context, skillID string) (ToolResult, error) {
	body, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s", a.WorkspaceURL, skillID), nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: skill %q not readable (HTTP %d)", skillID, status)}, nil
	}
	var skill struct {
		Markdown     string          `json:"markdown"`
		ManifestYAML json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(body), &skill); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	response, status, err := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/skills/%s/validate", a.WorkspaceURL, skillID), map[string]any{
		"markdown":      skill.Markdown,
		"manifest_yaml": string(skill.ManifestYAML),
	})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: validate failed (HTTP %d): %s", status, truncate(response, 400))}, nil
	}
	return ToolResult{Content: compactJSON(response, 4000)}, nil
}

// skillBuilderModel resolves the model for the skill builder: the run's
// project default when configured, else the supervisor/judge model — the
// installation's dedicated quality-work model (an agent asking to memorize a
// skill should not depend on the project default being set).
func (a *Activities) skillBuilderModel(ctx context.Context, project string) (*llm.Client, error) {
	if a.Model != nil {
		return a.Model, nil
	}
	if a.JudgeModel != nil {
		if client, err := a.JudgeModel(ctx); err == nil && client != nil {
			return client, nil
		}
	}
	if project != "" && a.WorkspaceURL != "" {
		if config, ok, err := a.modelConfigFor(ctx, project, ""); err == nil && ok {
			return llm.New(config), nil
		}
	}
	return nil, errors.New("no model available for the skill builder: set the project default model, or pick a judge model in Agents → Quality settings")
}

// handleSkillPropose turns a natural-language capability description into a
// structured skill draft (via the skill builder) and saves it as a proposal:
// a draft version for human review, never the live revision. Per
// docs/knowledge-evolution.md Wave A the proposal lands with full provenance
// (origin, source run, evidence) and goes live only when a human applies it
// in the Skills UI (skill.applied event).
func (a *Activities) handleSkillPropose(ctx context.Context, request ToolRequest) (ToolResult, error) {
	description, _ := request.Arguments["description"].(string)
	if strings.TrimSpace(description) == "" {
		return ToolResult{Content: "error: description is required"}, nil
	}
	model, modelErr := a.skillBuilderModel(ctx, request.Project)
	if modelErr != nil {
		return ToolResult{Content: "error: " + modelErr.Error()}, nil
	}
	draft, err := BuildSkillDraft(ctx, model, description, a.ToolsSummary(), false)
	if err != nil {
		return ToolResult{Content: "error: skill builder failed: " + err.Error()}, nil
	}
	var manifest struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	_ = json.Unmarshal(draft.Manifest, &manifest)
	id := manifest.ID
	if id == "" {
		id = skillSlug(draft.Name)
	}
	if id == "" {
		return ToolResult{Content: "error: could not derive a skill id from the draft"}, nil
	}
	// Provenance: the run and operation that produced this proposal. Version
	// policy stays server-side — the workspace API picks the next free patch
	// for existing skills and 0.x for new capabilities.
	sourceRuns := []string{}
	if request.RunID != "" {
		sourceRuns = append(sourceRuns, request.RunID)
	}
	evidenceRefs := []string{}
	if request.OperationID != "" {
		evidenceRefs = append(evidenceRefs, request.OperationID)
	}
	changeSummary := "Agent proposal: " + truncate(draft.Description, 180)
	// Normalize the manifest so identity fields agree with the stored row.
	manifestMap := map[string]any{}
	if err := json.Unmarshal(draft.Manifest, &manifestMap); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if _, ok := manifestMap["name"]; !ok {
		manifestMap["name"] = draft.Name
	}
	if _, ok := manifestMap["description"]; !ok {
		manifestMap["description"] = draft.Description
	}
	manifestMap["id"] = id
	delete(manifestMap, "version") // server picks the version, never the model
	normalizedManifest, err := json.Marshal(manifestMap)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	exists := false
	if current, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s", a.WorkspaceURL, id), nil); err == nil && status == 200 {
		var existing struct {
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(current), &existing) == nil && existing.Version != "" {
			exists = true
		}
	}
	var (
		version string
		action  string
	)
	if exists {
		// Skill exists — propose the draft as its next version; the current
		// revision keeps serving agents until a human applies the draft.
		payload := map[string]any{
			"markdown":       draft.Markdown,
			"manifest_yaml":  string(normalizedManifest),
			"origin":         "agent-proposal",
			"source_runs":    sourceRuns,
			"evidence_refs":  evidenceRefs,
			"change_summary": changeSummary,
		}
		body, status, err := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/skills/%s/versions", a.WorkspaceURL, id), payload)
		if err != nil {
			return ToolResult{Content: "error: " + err.Error()}, nil
		}
		if status >= 400 {
			return ToolResult{Content: fmt.Sprintf("error: propose skill version failed (HTTP %d): %s", status, truncate(body, 600))}, nil
		}
		var proposed struct {
			Version string `json:"version"`
		}
		_ = json.Unmarshal([]byte(body), &proposed)
		version = proposed.Version
		action = "draft version proposed for existing skill"
	} else {
		payload := map[string]any{
			"id":             id,
			"name":           draft.Name,
			"description":    draft.Description,
			"version":        "0.1.0",
			"markdown":       draft.Markdown,
			"manifest_yaml":  string(normalizedManifest),
			"draft":          true,
			"origin":         "agent-proposal",
			"source_runs":    sourceRuns,
			"evidence_refs":  evidenceRefs,
			"change_summary": changeSummary,
		}
		body, status, err := a.workspaceDo(ctx, http.MethodPost, a.WorkspaceURL+"/v1/workspace/skills", payload)
		if err != nil {
			return ToolResult{Content: "error: " + err.Error()}, nil
		}
		if status >= 400 {
			return ToolResult{Content: fmt.Sprintf("error: save skill draft failed (HTTP %d): %s", status, truncate(body, 600))}, nil
		}
		version = "0.1.0"
		action = "skill created as draft"
	}
	a.emitSkillProposed(ctx, request, id, draft.Name, version, changeSummary, evidenceRefs)
	response := map[string]any{
		"skill_id":  id,
		"name":      draft.Name,
		"version":   version,
		"state":     "draft",
		"action":    action,
		"next_step": "a human must review and apply the draft in the Skills UI before the skill becomes available to agents",
		"questions": draft.Questions,
	}
	encoded, _ := json.Marshal(response)
	return ToolResult{Content: string(encoded)}, nil
}

// handleAgentPropose files an evolution proposal for an agent definition
// (docs/plan-evaluable-agent.md §2.2 stage 10): an observed problem, the
// proposed change and evidence. It lands as a pending proposal a human
// applies in the Agents UI — never as a live definition change. Capabilities
// are deliberately not accepted: an agent must not be able to grant itself
// powers through a proposal.
func (a *Activities) handleAgentPropose(ctx context.Context, request ToolRequest) (ToolResult, error) {
	agentID, _ := request.Arguments["agent_id"].(string)
	if strings.TrimSpace(agentID) == "" {
		return ToolResult{Content: "error: agent_id is required"}, nil
	}
	problem, _ := request.Arguments["problem"].(string)
	change, _ := request.Arguments["change"].(string)
	if strings.TrimSpace(problem) == "" || strings.TrimSpace(change) == "" {
		return ToolResult{Content: "error: problem and change are required"}, nil
	}
	effect, _ := request.Arguments["effect"].(string)
	description, _ := request.Arguments["description"].(string)

	// Verify the target agent so the failure names the real problem.
	body, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/agents/%s", a.WorkspaceURL, agentID), nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status == http.StatusNotFound {
		return ToolResult{Content: fmt.Sprintf("error: agent %q not found", agentID)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: agent %q not readable (HTTP %d): %s", agentID, status, truncate(body, 300))}, nil
	}
	var target struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(body), &target)

	// Provenance is added automatically: the run and operation that produced
	// the proposal, plus any refs the agent supplied (run ids, knowledge ids).
	evidence := stringList(request.Arguments["evidence"])
	if request.RunID != "" {
		evidence = append([]string{"run:" + request.RunID}, evidence...)
	}
	if request.OperationID != "" {
		evidence = append(evidence, "op:"+request.OperationID)
	}
	author := "agent-run:" + request.RunID
	payload := map[string]any{
		"problem":  problem,
		"change":   change,
		"effect":   effect,
		"evidence": evidence,
		"author":   author,
	}
	if description != "" {
		payload["description"] = description
	}
	// The server copies capabilities from the current definition; the proposal
	// may only restate constraints, completion and the purpose text.
	definition := map[string]any{}
	if constraints := stringList(request.Arguments["constraints"]); constraints != nil {
		definition["constraints"] = constraints
	}
	if completion := stringList(request.Arguments["completion"]); completion != nil {
		definition["completion"] = completion
	}
	if len(definition) > 0 {
		payload["definition"] = definition
	}
	response, status, err := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/agents/%s/proposals", a.WorkspaceURL, agentID), payload)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: propose failed (HTTP %d): %s", status, truncate(response, 600))}, nil
	}
	var saved struct {
		ID          int64 `json:"id"`
		BaseVersion int   `json:"base_version"`
	}
	_ = json.Unmarshal([]byte(response), &saved)
	result := map[string]any{
		"agent_id":     agentID,
		"agent_name":   target.Name,
		"proposal_id":  saved.ID,
		"base_version": saved.BaseVersion,
		"state":        "pending",
		"next_step":    "a human must review and apply the proposal in the Agents UI (Evolution tab) before the definition changes",
	}
	encoded, _ := json.Marshal(result)
	return ToolResult{Content: string(encoded)}, nil
}

// emitSkillProposed records the skill.proposed event in the journal via the
// durable outbox. Best-effort: the draft row is already saved, so an outbox
// miss must not fail the tool call.
func (a *Activities) emitSkillProposed(ctx context.Context, request ToolRequest, skillID, skillName, version, changeSummary string, evidenceRefs []string) {
	if a.Events == nil {
		return
	}
	evidence := make([]observation.Evidence, 0, len(evidenceRefs))
	for _, ref := range evidenceRefs {
		evidence = append(evidence, observation.Evidence{Ref: ref})
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("%s/skill-proposed/%s", request.RunID, version),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: request.Project, Run: request.RunID,
			Actor: observation.Actor{ID: a.SourceID, Type: "agent"},
		},
		Type:     "skill.proposed",
		Data:     map[string]any{"skill_id": skillID, "skill_name": skillName, "version": version, "origin": "agent-proposal", "change_summary": changeSummary, "source_runs": []string{request.RunID}},
		Evidence: evidence,
	}
	if err := a.Events.Enqueue(ctx, event); err != nil {
		return
	}
}

// ToolsSummary renders builtin + MCP tools as a compact text list for the
// skill builder prompt, so drafts reference real tool names. It is shared by
// the UI wizard endpoint and the skill_propose agent tool.
func (a *Activities) ToolsSummary() string {
	var b strings.Builder
	for _, def := range a.BuiltinToolDefs() {
		if def.Description != "" {
			fmt.Fprintf(&b, "- %s — %s\n", def.Name, def.Description)
		} else {
			fmt.Fprintf(&b, "- %s\n", def.Name)
		}
	}
	for name, server := range a.MCP.ServerTools() {
		fmt.Fprintf(&b, "MCP server %q: ", name)
		tools := make([]string, 0, len(server.Tools))
		for _, tool := range server.Tools {
			tools = append(tools, tool.ModelName)
		}
		b.WriteString(strings.Join(tools, ", "))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// skillSlug derives a kebab-case id from a human skill name.
func skillSlug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// truncate shortens s for inclusion in a compact tool error message.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

func stringArgs(value any) ([]string, error) {
	switch values := value.(type) {
	case []string:
		return values, nil
	case []any:
		result := make([]string, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("command[%d] must be a string", i)
			}
			result[i] = text
		}
		return result, nil
	default:
		return nil, errors.New("command must be an array of strings")
	}
}

// faultDrilled tracks which runs already consumed the tool-name fault drill;
// entries live for the worker's lifetime, one per drilled run.
var (
	faultMu      sync.Mutex
	faultDrilled = map[string]bool{}
)

// faultAfterEffect reports whether this call should simulate a worker crash
// after its effect lands. KERNEL_FAULT_AFTER_EFFECT holds either an exact
// operation id, or a tool name ("run_command", "mcp__create_issue") which
// faults the first matching execution per run — operation ids embed
// model-generated call ids and cannot be predicted before the run starts,
// which is what a live drill needs.
func faultAfterEffect(runID, toolName, operationID string) bool {
	fault := strings.TrimSpace(os.Getenv("KERNEL_FAULT_AFTER_EFFECT"))
	if fault == "" || (fault != toolName && fault != operationID) {
		return false
	}
	if fault == operationID {
		return true
	}
	faultMu.Lock()
	defer faultMu.Unlock()
	if faultDrilled[runID] {
		return false
	}
	faultDrilled[runID] = true
	return true
}

func (a *Activities) KnowledgeHints(ctx context.Context, request HintRequest) ([]Hint, error) {
	// The journal caps query at 4096 runes; long prompts (a pasted document,
	// a big task brief) are truncated — the head of the task carries the
	// matching signal, and an unhandled 422 would silently strip the whole
	// run of hints.
	query := request.Query
	if len([]rune(query)) > 4000 {
		query = string([]rune(query)[:4000])
	}
	payload := map[string]any{"project": request.Project, "run": request.RunID, "task": request.TaskID, "actor": map[string]string{"id": request.ActorID, "type": "agent"}, "query": query, "limit": 8}
	// Semantic tier (docs/supervisor-model.md §6): embed the slot text with
	// the supervisor embeddings model. A failure degrades to the lexical
	// matcher — hints must never fail because semantics are misconfigured.
	if query != "" {
		if vectors, err := a.EmbedTexts(ctx, request.Project, EmbedQuery, []string{query}); err == nil && len(vectors) == 1 {
			payload["embedding"] = vectors[0]
			payload["embedding_model"] = embeddingModelID()
		}
	}
	if request.TeamRoot != "" {
		payload["team_root"] = request.TeamRoot
	}
	if len(request.Skills) > 0 {
		// Skill digests already ride in the agent prompt; the journal uses them
		// as novelty context so hints that restate a skill lose rank (§7).
		skills := make([]map[string]string, 0, len(request.Skills))
		for _, skill := range request.Skills {
			skills = append(skills, map[string]string{"id": skill.ID, "summary": skill.Digest})
		}
		payload["active_skills"] = skills
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TemporalityURL+"/v1/observations/hints", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if token := a.apiToken(); token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.HTTP.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Temporality hints returned HTTP %d", response.StatusCode)
	}
	var reply struct {
		Hints []Hint `json:"hints"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply.Hints, nil
}

// TeamContractRequest asks the kernel to persist a completed slot's contract
// into the team scratch (docs/agent-teams.md §8):
// <project workspace>/team/<run_id>/contracts/<task_id>.md. RunID is the team
// ROOT run (no path separators); TaskID is the slot id (or dag step id).
type TeamContractRequest struct {
	Project string
	RunID   string
	TaskID  string
	Answer  string
}

// TeamScratchDir returns the workspace-relative scratch directory of a team
// run: team/<root run id>/. The path is the contract between the runtime and
// the slot prompts — both sides derive it, nobody passes absolute paths.
func TeamScratchDir(rootRunID string) string { return "team/" + rootRunID }

// WriteTeamContract persists a slot contract file into the team scratch.
// Idempotent by construction: rework rounds rewrite the same path with the
// newest answer, retries rewrite the same content.
func (a *Activities) WriteTeamContract(ctx context.Context, request TeamContractRequest) (string, error) {
	if request.Project == "" || request.RunID == "" || request.TaskID == "" {
		return "", errors.New("project, run and task are required")
	}
	if !sandboxConfigured(a.Sandbox) {
		return "", errors.New("sandbox is not configured")
	}
	safeRun := sanitizeFileToken(request.RunID)
	safeTask := sanitizeFileToken(request.TaskID)
	if safeRun == "" || safeRun == "." || safeRun == ".." || safeTask == "" || safeTask == "." || safeTask == ".." {
		return "", fmt.Errorf("run or task id %q has no usable path token", request.RunID+"/"+request.TaskID)
	}
	projectDir := filepath.Join(a.Sandbox.SandboxRoot(), "projects", request.Project)
	if err := os.MkdirAll(projectDir, 0o777); err != nil {
		return "", fmt.Errorf("create project workspace: %w", err)
	}
	// Same resolution as PrepareRun: the kernel and the sandbox must agree on
	// the host path of the project workspace (symlinked roots included).
	projectDir, err := a.Sandbox.ResolveWorkspace(projectDir)
	if err != nil {
		return "", err
	}
	rel := path.Join(TeamScratchDir(safeRun), "contracts", safeTask+".md")
	abs := filepath.Join(projectDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o777); err != nil {
		return "", fmt.Errorf("create team scratch: %w", err)
	}
	body := "# Contract: " + request.TaskID + "\n\nTeam run: " + request.RunID + "\nProject: " + request.Project + "\n\n" + strings.TrimSpace(request.Answer) + "\n"
	if err := os.WriteFile(abs, []byte(body), 0o666); err != nil {
		return "", fmt.Errorf("write team contract: %w", err)
	}
	return rel, nil
}

// SweepTeamScratch removes team scratch directories whose newest change is
// older than ttl (docs/agent-teams.md §8): contracts outlive the run for
// inspection, not forever.
func (a *Activities) SweepTeamScratch(now time.Time, ttl time.Duration) (int, error) {
	if ttl <= 0 || !sandboxConfigured(a.Sandbox) {
		return 0, nil
	}
	projects := filepath.Join(a.Sandbox.SandboxRoot(), "projects")
	projectEntries, err := os.ReadDir(projects)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, project := range projectEntries {
		teams := filepath.Join(projects, project.Name(), "team")
		runEntries, err := os.ReadDir(teams)
		if err != nil {
			continue // no scratch for this project
		}
		for _, run := range runEntries {
			if !run.IsDir() {
				continue
			}
			info, err := run.Info()
			if err != nil || now.Sub(info.ModTime()) <= ttl {
				continue
			}
			if err := os.RemoveAll(filepath.Join(teams, run.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// sanitizeFileToken keeps only filename-safe characters so ids from manifests
// can never traverse out of the scratch directory.
func sanitizeFileToken(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type HintRequest struct {
	Project string `json:"project"`
	RunID   string `json:"run_id"`
	TaskID  string `json:"task_id"`
	ActorID string `json:"actor_id"`
	Query   string `json:"query"`
	// TeamRoot names the team program root run (docs/agent-teams.md §8): the
	// journal additionally admits knowledge confirmed inside that run tree to
	// every slot, even when the lexical matcher would not rank it.
	TeamRoot string `json:"team_root,omitempty"`
	// Skills are the run's resolved skills: their digests already sit in the
	// agent prompt, so the journal demotes knowledge that only restates them
	// (plan-priming-relevance.md §7) instead of spending hint budget on it.
	Skills []SkillRef `json:"skills,omitempty"`
}

// KnowledgeLookupResult reports the current projected state of one knowledge
// item. Exists is false when the item is unknown to the project. Proposition
// rides along so lifecycle events can carry the human-readable text instead
// of a bare id.
type KnowledgeLookupResult struct {
	Exists      bool
	State       string
	Proposition string
}

// KnowledgeLookup fetches the current state of one knowledge item so the
// workflow can reuse or confirm an existing observation instead of proposing
// a duplicate node.
func (a *Activities) KnowledgeLookup(ctx context.Context, request KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
	url := fmt.Sprintf("%s/v1/observations/knowledge?project=%s&knowledge_id=%s", a.TemporalityURL, url.QueryEscape(request.Project), url.QueryEscape(request.KnowledgeID))
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return KnowledgeLookupResult{}, err
	}
	if token := a.apiToken(); token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.HTTP.Do(httpRequest)
	if err != nil {
		return KnowledgeLookupResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return KnowledgeLookupResult{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return KnowledgeLookupResult{}, fmt.Errorf("Temporality knowledge lookup returned HTTP %d", response.StatusCode)
	}
	var reply struct {
		Knowledge []struct {
			State       string `json:"state"`
			Proposition string `json:"proposition"`
		} `json:"knowledge"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return KnowledgeLookupResult{}, err
	}
	if len(reply.Knowledge) == 0 {
		return KnowledgeLookupResult{}, nil
	}
	return KnowledgeLookupResult{Exists: true, State: reply.Knowledge[0].State, Proposition: reply.Knowledge[0].Proposition}, nil
}

type KnowledgeLookupQuery struct {
	Project     string
	KnowledgeID string
}

type Hint struct {
	KnowledgeID string   `json:"knowledge_id"`
	HintID      string   `json:"hint_id"`
	Proposition string   `json:"proposition"`
	State       string   `json:"state"`
	MatchedBy   []string `json:"matched_by"`
	Caution     string   `json:"caution,omitempty"`
}

// toObservationHints mirrors the wire hints into the observation type so the
// shared renderer (budgets, cue contract) works on the runtime path.
func toObservationHints(hints []Hint) []observation.Hint {
	out := make([]observation.Hint, 0, len(hints))
	for _, hint := range hints {
		out = append(out, observation.Hint{
			KnowledgeID: hint.KnowledgeID, HintID: hint.HintID, Proposition: hint.Proposition,
			State: hint.State, MatchedBy: hint.MatchedBy, Caution: hint.Caution,
		})
	}
	return out
}
