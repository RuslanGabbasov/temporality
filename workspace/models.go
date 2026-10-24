package workspace

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Project groups tasks and runs. Agents are top-level and reusable across projects.
type Project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	DefaultAgentID string `json:"default_agent_id,omitempty"`
	DefaultModel   string `json:"default_model,omitempty"`
	Archived       bool   `json:"archived,omitempty"`
	// IntakeEnabled opts the project into channel-initiated tasks
	// (docs/inbound-messages.md §12 phase 4): start_task refuses to route
	// here until an admin flips it. Default false — a new project must not
	// silently become a channel target.
	IntakeEnabled bool `json:"intake_enabled,omitempty"`
	// Org bindings (docs/org-structure.md §3.4): a project spans several org
	// units; it is visible where the project's units intersect the viewer's
	// ancestor chain. Empty = org-neutral, visible everywhere (transition).
	OrgUnitIDs []string  `json:"org_units,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ProjectMember is one explicit membership row (docs/org-structure.md §16):
// the user sees the project even when their org chain does not intersect the
// project's units. The role scopes what the member may do inside the project.
type ProjectMember struct {
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// OrgUnitRole is a role granted on an org unit (docs/org-structure.md §13):
// it acts on the unit and its whole subtree, upgrading the grantee's
// installation role inside that scope.
type OrgUnitRole struct {
	UserID    string    `json:"user_id"`
	OrgUnitID string    `json:"org_unit_id"`
	Role      string    `json:"role"`
	GrantedBy string    `json:"granted_by,omitempty"`
	GrantedAt time.Time `json:"granted_at"`
}

// ExecutionIdentity is the security context of automated runs
// (docs/org-structure.md §20): which agents, MCP servers, providers,
// projects and human request targets a trigger-driven run may touch. A
// "*" entry means "anything visible in the identity's org scope".
type ExecutionIdentity struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	OrgUnitID        string    `json:"org_unit_id,omitempty"`
	AllowedAgents    []string  `json:"allowed_agents"`
	AllowedMCP       []string  `json:"allowed_mcp"`
	AllowedProviders []string  `json:"allowed_providers"`
	AllowedProjects  []string  `json:"allowed_projects"`
	HumanTargets     []string  `json:"human_targets"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// OrgUnitKind values for OrgUnit.Kind.
const (
	OrgKindOrganization = "organization"
	OrgKindDepartment   = "department"
	OrgKindTeam         = "team"
)

// HumanRequest lifecycle statuses (docs/org-structure.md §28). The spec's
// "waiting" merges into "delivered": once the notification is out, the
// request is waiting for an answer — one state, not two.
const (
	HumanStatusPending   = "pending"
	HumanStatusDelivered = "delivered"
	HumanStatusAnswered  = "answered"
	HumanStatusExpired   = "expired"
	HumanStatusCancelled = "cancelled"
	HumanStatusRejected  = "rejected" // §30: the identity may not address this user
)

// HumanRequest is one ask_human escalation as a queryable entity
// (docs/org-structure.md §28, docs/triggers-and-escalations.md §10): the run
// pauses, the question is routed to a resolved user, and the answer flows
// back into the run. The id is the ask operation id, so the approval
// endpoint closes the row atomically.
type HumanRequest struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id,omitempty"`
	// AgentVersion pins the agent definition snapshot the ask was created
	// under (specs/quality-loop.md §3): filled from workspace_agent.
	// definition_version at creation, immutable afterwards. 0 = unknown
	// (rows predating the column); such rows never enter per-version metrics.
	AgentVersion int      `json:"agent_version,omitempty"`
	Recipient    string   `json:"recipient,omitempty"`
	ResolvedUser string   `json:"resolved_user,omitempty"`
	Question     string   `json:"question"`
	Context      string   `json:"context,omitempty"`
	Options      []string `json:"options"`
	Status       string   `json:"status"`
	Channel      string   `json:"channel,omitempty"`
	// ThreadRef is the conversation thread of the delivered question on
	// thread-capable transports (slack ts; docs/channels.md §9): follow-ups of
	// the same run join it and threaded replies match their ask exactly.
	// Empty on web delivery and non-threaded channels.
	ThreadRef           string     `json:"thread_ref,omitempty"`
	Response            string     `json:"response,omitempty"`
	AnsweredBy          string     `json:"answered_by,omitempty"`
	TimeoutSeconds      int        `json:"timeout_seconds,omitempty"`
	TimeoutPolicy       string     `json:"timeout_policy,omitempty"`
	ExecutionIdentityID string     `json:"execution_identity_id,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	DeliveredAt         *time.Time `json:"delivered_at,omitempty"`
	AnsweredAt          *time.Time `json:"answered_at,omitempty"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	// Deliveries is the team fan-out (docs/inbound-messages.md §8): when the
	// recipient is org:<unit-id>, one entry per active unit member the question
	// was delivered to. Empty for single-recipient requests (resolved_user).
	Deliveries []HumanRequestDelivery `json:"deliveries,omitempty"`
}

// HumanRequestDelivery is one member's copy of a team human request.
type HumanRequestDelivery struct {
	RequestID string    `json:"request_id"`
	UserID    string    `json:"user_id"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"` // delivered | failed | superseded
	CreatedAt time.Time `json:"created_at"`
}

// Valid timeout policies (docs/org-structure.md §33): what happens when a
// human request times out. The policy lives in the execution model, never in
// the transport adapter.
var ValidHumanTimeoutPolicies = map[string]bool{
	"fail":     true,
	"retry":    true,
	"fallback": true,
	"escalate": true,
	"cancel":   true,
}

// ValidOrgKinds lists the allowed org unit kinds.
var ValidOrgKinds = map[string]bool{
	OrgKindOrganization: true,
	OrgKindDepartment:   true,
	OrgKindTeam:         true,
}

// OrgUnit is one node of the org-structure tree (docs/org-structure.md §3.1).\n// ParentID empty marks a root (one per company). Path is the materialized
// ancestor-id path ("root.dept" for a team under dept); the unit itself is NOT
// part of its own path. Resources bound to a unit are visible to the unit and
// its whole subtree.
type OrgUnit struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	// HeadUserID is the explicit manager edge (docs/inbound-messages.md §7):
	// the escalation target and the default addressee for team requests. The
	// effective head resolves bottom-up — a unit without a head inherits the
	// nearest ancestor's head. Empty = none assigned (legacy proximity rules).
	HeadUserID string    `json:"head_user_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SelfPath returns the unit's own materialized path prefix: the path its
// descendants carry. For a root it is just the id.
func (u OrgUnit) SelfPath() string { return joinPath(u.Path, u.ID) }

// Ancestors returns the unit's ancestor chain including itself, oldest first.
// For a root it is [root].
func (u OrgUnit) Ancestors() []string {
	chain := make([]string, 0, 4)
	if u.Path != "" {
		chain = append(chain, strings.Split(u.Path, ".")...)
	}
	return append(chain, u.ID)
}

// joinPath appends id to an ancestor path.
func joinPath(path, id string) string {
	if path == "" {
		return id
	}
	return path + "." + id
}

// Policy is an inheritable execution constraint attached to an org unit
// (docs/org-structure.md §24). A row with an empty OrgUnitID applies to the
// whole installation; a row bound to a unit constrains the unit's subtree.
// Every dimension is optional: zero values mean "no restriction". The
// effective policy for a run merges restrictively along the ancestor chain.
type Policy struct {
	ID        string `json:"id"`
	OrgUnitID string `json:"org_unit_id,omitempty"` // '' = installation-wide
	Name      string `json:"name"`
	// AllowedModels/AllowedMCP: empty = unrestricted, "*" = unrestricted;
	// otherwise an exact-name allowlist.
	AllowedModels []string `json:"allowed_models"`
	AllowedMCP    []string `json:"allowed_mcp"`
	// MaxTokens/MaxBudgetUSD/TimeoutSeconds cap a single run; NULL = uncapped.
	MaxTokens      *int     `json:"max_tokens,omitempty"`
	MaxBudgetUSD   *float64 `json:"max_budget_usd,omitempty"`
	TimeoutSeconds *int     `json:"timeout_seconds,omitempty"`
	// Modes: '' keeps the inherited/default behavior, the listed restrictive
	// value wins during the merge.
	NetworkMode  string `json:"network_mode,omitempty"`  // 'deny' restricts
	SandboxMode  string `json:"sandbox_mode,omitempty"`  // 'read_only' restricts
	ApprovalMode string `json:"approval_mode,omitempty"` // 'tools' restricts
	// IntakeMode denies channel-initiated tasks for the subtree
	// (docs/inbound-messages.md §12 phase 4); enabling is per project —
	// there is no 'allow' overlay, a child cannot loosen an ancestor's deny.
	IntakeMode string    `json:"intake_mode,omitempty"` // 'deny' restricts
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Restrictive merge helpers. PolicyAllowsList distinguishes nil (the
// dimension was never restricted — everything passes) from an empty non-nil
// list (allowlists intersected to nothing — nothing passes). The store
// normalizes user input so an empty submitted list reads back as nil.
func PolicyAllowsList(list []string, value string) bool {
	if list == nil {
		return true
	}
	for _, v := range list {
		if v == "*" || v == value {
			return true
		}
	}
	return false
}

// AgentCapabilities is the user-facing toggle set from the agent form
// (docs/evaluable-agent.md §3.2). Enforcement is hard: a disabled capability
// removes the corresponding tools and environment access at run start — it is
// never just a line in the prompt.
type AgentCapabilities struct {
	ReadFiles   *bool `json:"read_files,omitempty"`   // read_file / search / grep
	ModifyFiles *bool `json:"modify_files,omitempty"` // write tools; false ⇒ read-only run
	RunCommands *bool `json:"run_commands,omitempty"` // run_command in sandbox
	Network     *bool `json:"network,omitempty"`      // sandbox network access
	Skills      *bool `json:"skills,omitempty"`       // skill injection for this run
	Knowledge   *bool `json:"knowledge,omitempty"`    // prior knowledge hints
	// Delegation lets the agent hand subtasks to other agents via the delegate
	// tool (docs/agent-delegation.md). Inverted default: nil or false ⇒ denied,
	// because delegation must be granted explicitly by a human.
	Delegation *bool `json:"delegation,omitempty"`
}

// Cap returns the effective value of a capability: nil (unset) means allowed,
// matching pre-definition agent behavior.
func (c AgentCapabilities) Cap(enabled *bool) bool { return enabled == nil || *enabled }

// AgentCapabilityNames are the canonical capability names usable in team slot
// requirements (docs/agent-teams.md §7). "write_files" is accepted as a
// historical alias of modify_files (the spec example used it) and normalizes
// to the same flag.
var AgentCapabilityNames = []string{
	"read_files", "modify_files", "run_commands", "network", "skills", "knowledge", "delegation",
}

// NormalizeAgentCapability maps a requirement name onto the canonical name;
// the second return reports whether the name is known at all.
func NormalizeAgentCapability(name string) (string, bool) {
	if name == "write_files" {
		return "modify_files", true
	}
	for _, known := range AgentCapabilityNames {
		if name == known {
			return name, true
		}
	}
	return "", false
}

// SatisfiesCapability reports whether the agent fills one capability
// requirement. Matching follows the roster card semantics (kernel/agent
// roster.go): unset means allowed, an explicit false opts out, delegation
// additionally requires an explicit grant because it is the inverted-default
// exception, and a legacy agent without a structured definition reports no
// capabilities at all (it satisfies only requirement-free slots).
func (a Agent) SatisfiesCapability(name string) bool {
	canonical, known := NormalizeAgentCapability(name)
	if !known || a.Definition == nil {
		return false
	}
	caps := a.Definition.Capabilities
	if canonical == "delegation" {
		return caps.Delegation != nil && *caps.Delegation
	}
	switch canonical {
	case "read_files":
		return caps.Cap(caps.ReadFiles)
	case "modify_files":
		return caps.Cap(caps.ModifyFiles)
	case "run_commands":
		return caps.Cap(caps.RunCommands)
	case "network":
		return caps.Cap(caps.Network)
	case "skills":
		return caps.Cap(caps.Skills)
	case "knowledge":
		return caps.Cap(caps.Knowledge)
	}
	return false
}

// SatisfiesRequirements reports whether the agent can fill a team role slot:
// every required capability enabled and every required skill attached
// (docs/agent-teams.md §7, wave C).
func (a Agent) SatisfiesRequirements(requirements TeamRequirements) bool {
	for _, capability := range requirements.Capabilities {
		if !a.SatisfiesCapability(capability) {
			return false
		}
	}
	for _, skill := range requirements.Skills {
		if !containsStringIn(a.Skills, skill) {
			return false
		}
	}
	return true
}

func containsStringIn(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// AgentDefinition is the structured, human-edited part of an agent
// (docs/evaluable-agent.md). The system prompt is compiled from it; users never
// see YAML. Description (the agent's purpose) lives on Agent.Description.
type AgentDefinition struct {
	Capabilities   AgentCapabilities `json:"capabilities,omitempty"`
	Constraints    []string          `json:"constraints,omitempty"`     // what the agent must never do
	Completion     []string          `json:"completion,omitempty"`      // what to verify before declaring done
	PromptOverride string            `json:"prompt_override,omitempty"` // optional manual replacement of the compiled prompt
}

// AgentVersion is an immutable snapshot of the agent definition: what changed,
// who changed it, and the exact prompt compiled from it (docs/evaluable-agent.md §14).
type AgentVersion struct {
	AgentID        string          `json:"agent_id"`
	Version        int             `json:"version"`
	Definition     AgentDefinition `json:"definition"`
	Description    string          `json:"description"` // purpose snapshot
	CompiledPrompt string          `json:"compiled_prompt"`
	PromptSource   string          `json:"prompt_source"` // manual | ai | builtin
	GeneratorModel string          `json:"generator_model,omitempty"`
	Author         string          `json:"author,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Agent is a top-level entity with full configuration. Agents are reusable
// across projects and tasks — they define HOW the agent behaves.
type Agent struct {
	ID string `json:"id"`
	// Identity
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // purpose: what this agent is for
	// Scope (optional — agents can be global or project-scoped)
	ProjectID string `json:"project_id,omitempty"`
	// Model configuration
	Model        string   `json:"model"`
	Provider     string   `json:"provider,omitempty"` // provider ID (see Provider)
	SystemPrompt string   `json:"system_prompt"`      // legacy/manual prompt; superseded by Definition
	Temperature  *float64 `json:"temperature,omitempty"`
	MaxTokens    *int     `json:"max_tokens,omitempty"`
	// Tools & capabilities
	Skills     []string `json:"skills,omitempty"`
	MCPServers []string `json:"mcp_servers,omitempty"`
	Tools      []string `json:"tools,omitempty"` // model tool names; empty = all available tools
	// Secrets granted to this agent (docs/secrets-management.md §8): names of
	// workspace secrets resolved into SECRET_<NAME> sandbox env vars at the
	// execution boundary. The model never sees the values.
	Secrets []string `json:"secrets,omitempty"`
	// Structured definition (docs/evaluable-agent.md); nil ⇒ legacy prompt-only agent
	Definition        *AgentDefinition `json:"definition,omitempty"`
	DefinitionVersion int              `json:"definition_version,omitempty"`
	// Sandbox constraints
	SandboxProfile string `json:"sandbox_profile,omitempty"` // restricted, standard, privileged
	NetworkAccess  *bool  `json:"network_access,omitempty"`
	ReadOnly       *bool  `json:"read_only,omitempty"`
	// Execution limits
	MaxTurns     *int    `json:"max_turns,omitempty"`
	ApprovalMode *string `json:"approval_mode,omitempty"` // auto, prompt, manual
	// Egress allowlist (docs/agent-behavior-monitoring.md §7): network
	// destinations (domains, globs, CIDRs, host:port) the agent may reach
	// beyond the public internet. Private addresses are denied by default —
	// only explicit entries open them. Enforced by the egress proxy.
	EgressAllowlist []string `json:"egress_allowlist,omitempty"`
	// Metadata
	Labels map[string]string `json:"labels,omitempty"`
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string    `json:"org_unit_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Task is scoped to a project and optionally bound to an agent.
type Task struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	AgentID   string    `json:"agent_id,omitempty"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Run is one execution of a task.
type Run struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id,omitempty"`
	// AgentVersion pins the agent definition snapshot this run used
	// (docs/evaluable-agent.md §15); 0 = legacy run without a definition.
	AgentVersion int `json:"agent_version,omitempty"`
	// ExecContext is the authorization snapshot at start time
	// (docs/org-structure.md §34-35): which agent, skills, MCP servers,
	// model/project and identity the run was authorized to use, and by whom.
	ExecContext map[string]any `json:"exec_context,omitempty"`
	RunID       string         `json:"run_id"`
	Status      string         `json:"status"`
	Model       string         `json:"model,omitempty"`
	Answer      string         `json:"answer,omitempty"`
	Turns       int            `json:"turns"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Trigger is a configurable mechanism to launch agent runs.
type Trigger struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	AgentID   string          `json:"agent_id,omitempty"`
	Name      string          `json:"name"`
	Type      string          `json:"type"` // schedule, webhook, event
	Enabled   bool            `json:"enabled"`
	Config    json.RawMessage `json:"config"`
	// Org binding (docs/org-structure.md §18): empty = whole installation.
	OrgUnitID string `json:"org_unit_id,omitempty"`
	// Execution identity the automated runs execute under (§20-21): the run
	// never inherits the creator's rights. Empty = legacy trigger behaviour
	// (pre-wave-D triggers keep running as before).
	ExecutionIdentityID string    `json:"execution_identity_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// ScheduleConfig is the config JSON for schedule triggers.
type ScheduleConfig struct {
	Cron     string `json:"cron"`
	Prompt   string `json:"prompt"`
	Timezone string `json:"timezone,omitempty"`
}

// WebhookConfig is the config JSON for webhook triggers.
type WebhookConfig struct {
	Path string `json:"path"`
	// Secret, when set, requires every delivery to be signed with
	// X-Temporality-Signature: sha256-<HMAC-SHA256 of the body>.
	Secret string `json:"secret,omitempty"`
	// AuthTokenHash is the sha256 hex of a per-trigger delivery token
	// ("whk_…", generated by POST …/webhook-token). A delivery presenting
	// that token (Bearer, Basic password or ?token=) is accepted for THIS
	// trigger's endpoint only — no workspace principal is involved, so the
	// credential is safe to hand to external systems like TFS. Only the hash
	// is stored; the raw token is shown once at generation.
	AuthTokenHash  string `json:"auth_token_hash,omitempty"`
	PromptTemplate string `json:"prompt_template"`
}

// EventConfig is the config JSON for event triggers.
type EventConfig struct {
	EventType string            `json:"event_type"`
	Filter    map[string]string `json:"filter,omitempty"`
	Prompt    string            `json:"prompt"`
}

// Provider is an OpenAI-compatible model endpoint (e.g. z.ai, xiaomi).
type Provider struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	BaseURL   string            `json:"base_url"`
	APIKeyRef string            `json:"api_key_ref,omitempty"` // env var name or secret ref
	Models    []string          `json:"models,omitempty"`      // model IDs discovered or listed
	Labels    map[string]string `json:"labels,omitempty"`
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string    `json:"org_unit_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserChannel is one delivery transport owned by the user profile
// (docs/triggers-and-escalations.md §6). The agent never picks transports;
// the kernel resolves the recipient and their preferred channel. The type is
// a free slug on purpose: the deliverable set lives in the kernel's channel
// registry (kernel/agent/channels.go), so new transports need no storage
// migration — the API layer checks types against the registry.
type UserChannel struct {
	Type    string `json:"type"`    // matrix, telegram, slack, webhook, …
	Address string `json:"address"` // room id, chat id, webhook URL, …
	Enabled bool   `json:"enabled"`
	// SenderID is the owner's own identity inside the channel (mxid for
	// matrix, chat id for telegram) — the closed-world check of the intake
	// pipeline (docs/inbound-messages.md §5): an initiative message must come
	// from exactly this identity to create work in the owner's name. Empty
	// means unregistered: the channel delivers to the address but cannot
	// originate tasks. Replies to open questions stay on the room rule.
	SenderID string `json:"sender_id,omitempty"`
}

// User is an operator who can log in and use the platform.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`                // viewer, operator, admin
	Token    string `json:"token,omitempty"`     // bearer token; only set in create/regenerate responses, never in list
	HasToken bool   `json:"has_token,omitempty"` // list responses: whether a token exists (value is never exposed)
	// Password login (optional per user): bcrypt hash, never serialized;
	// HasPassword reports in list/get responses whether it is configured.
	PasswordHash string `json:"-"`
	HasPassword  bool   `json:"has_password,omitempty"`
	// Org position (docs/org-structure.md §3.3): the single unit the user
	// belongs to; empty = unassigned, sees everything (transition semantics).
	OrgUnitID string `json:"org_unit_id,omitempty"`
	Active    bool   `json:"active"`
	// Federation identity (docs/auth-federation.md §6): the (issuer, external_id)
	// pair links the user to its IdP identity — the JIT key. sourced is "local"
	// for manually created users and "federated" for JIT-provisioned ones.
	ExternalIssuer string     `json:"external_issuer,omitempty"`
	ExternalID     string     `json:"external_id,omitempty"`
	Sourced        string     `json:"sourced,omitempty"`
	LastLoginAt    *time.Time `json:"last_login_at,omitempty"`
	// Communication channels + preferred delivery. Preferred "" or "web"
	// means the UI inbox; otherwise it must reference an enabled channel.
	Channels         []UserChannel `json:"channels,omitempty"`
	PreferredChannel string        `json:"preferred_channel,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

// InboundMessage is one user-authored channel message recorded for the
// intake pipeline (docs/inbound-messages.md §4.1). Same pattern as
// HumanRequest: a durable workspace row, journal events, state derived from
// the row. The ID is deterministic — inbound/{channel}/{message_id} — and
// (channel, message_id) is unique, so redeliveries (matrix /sync replay,
// kernel restart) never reprocess a message.
type InboundMessage struct {
	ID             string   `json:"id"` // inbound/{channel}/{message_id}
	Channel        string   `json:"channel"`
	MessageID      string   `json:"message_id"` // matrix event_id, telegram update_id
	SenderRaw      string   `json:"sender_raw"` // mxid / chat id as seen by the channel
	SenderUserID   string   `json:"sender_user_id,omitempty"`
	Room           string   `json:"room"` // room/chat the message arrived in
	Body           string   `json:"body"`
	Attachments    []string `json:"attachments,omitempty"`    // saved inbox paths
	Status         string   `json:"status"`                   // received|classified|clarifying|routed|rejected|expired
	Classification string   `json:"classification,omitempty"` // task|amendment|status|cancel|chatter|reply
	IntakeRunID    string   `json:"intake_run_id,omitempty"`
	TargetRunID    string   `json:"target_run_id,omitempty"` // run spawned by the message
	ProjectID      string   `json:"project_id,omitempty"`    // resolved target project
	// RetiredAt marks a body scrubbed by the retention policy (PII): the row
	// survives as the audit chain link, the payload does not.
	RetiredAt *time.Time `json:"retired_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Inbound message statuses (docs/inbound-messages.md §4.1).
const (
	InboundStatusReceived   = "received"
	InboundStatusClassified = "classified"
	InboundStatusClarifying = "clarifying"
	InboundStatusRouted     = "routed"
	InboundStatusRejected   = "rejected"
	InboundStatusExpired    = "expired"
	// Answered closes the loop on a chatter/status intake: the agent's verdict
	// was delivered back to the sender's chat — no work was created by design.
	InboundStatusAnswered = "answered"
)

// Inbound message classifications (docs/inbound-messages.md §4.3).
const (
	InboundClassTask      = "task"
	InboundClassAmendment = "amendment"
	InboundClassStatus    = "status"
	InboundClassCancel    = "cancel"
	InboundClassChatter   = "chatter"
	InboundClassReply     = "reply"
)

// ValidInboundClassification reports whether s is one of the known intake
// classifications.
func ValidInboundClassification(s string) bool {
	switch s {
	case InboundClassTask, InboundClassAmendment, InboundClassStatus,
		InboundClassCancel, InboundClassChatter, InboundClassReply:
		return true
	}
	return false
}

// channelTypePattern is the shape every channel type slug must have. Which
// slugs actually deliver is the kernel registry's business; storage only
// refuses nonsense.
var channelTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidateChannels normalizes a channel list: plausible type slugs, non-empty
// addresses, at most one entry per type. It reports the first problem found.
func ValidateChannels(channels []UserChannel) error {
	seen := make(map[string]bool, len(channels))
	for _, channel := range channels {
		if !channelTypePattern.MatchString(channel.Type) {
			return fmt.Errorf("invalid channel type %q", channel.Type)
		}
		if strings.TrimSpace(channel.Address) == "" {
			return fmt.Errorf("channel %q needs an address", channel.Type)
		}
		// The sender identity is optional but must not be whitespace and is
		// stored trimmed: the closed-world intake check compares it exactly.
		if len(strings.TrimSpace(channel.SenderID)) != len(channel.SenderID) {
			return fmt.Errorf("channel %q sender id must not have surrounding whitespace", channel.Type)
		}
		if seen[channel.Type] {
			return fmt.Errorf("channel %q is configured twice", channel.Type)
		}
		seen[channel.Type] = true
	}
	return nil
}

// CreateProjectRequest is the payload for creating a project.
type CreateProjectRequest struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	DefaultAgentID string   `json:"default_agent_id"`
	DefaultModel   string   `json:"default_model"`
	OrgUnits       []string `json:"org_units,omitempty"` // org unit ids the project spans
	// IntakeEnabled opts into channel-initiated tasks; a pointer so a PUT
	// without the field keeps the stored value (same convention as OrgUnits).
	IntakeEnabled *bool `json:"intake_enabled,omitempty"`
}

// CreateOrgUnitRequest is the payload for creating or updating an org unit.
type CreateOrgUnitRequest struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"` // empty = new root
	Kind     string `json:"kind"`                // organization | department | team
	Name     string `json:"name"`
	// HeadUserID is a pointer so PUT can tell "absent" (keep the current
	// head, e.g. a move-only request) from "" (clear the head).
	HeadUserID *string `json:"head_user_id,omitempty"`
}

// CreateAgentRequest is the payload for creating an agent.
type CreateAgentRequest struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"project_id,omitempty"`
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Model          string            `json:"model"`
	Provider       string            `json:"provider,omitempty"`
	SystemPrompt   string            `json:"system_prompt"`
	Temperature    *float64          `json:"temperature,omitempty"`
	MaxTokens      *int              `json:"max_tokens,omitempty"`
	Skills         []string          `json:"skills,omitempty"`
	MCPServers     []string          `json:"mcp_servers,omitempty"`
	Secrets        []string          `json:"secrets,omitempty"`
	Tools          []string          `json:"tools,omitempty"`
	SandboxProfile string            `json:"sandbox_profile,omitempty"`
	NetworkAccess  *bool             `json:"network_access,omitempty"`
	ReadOnly       *bool             `json:"read_only,omitempty"`
	MaxTurns       *int              `json:"max_turns,omitempty"`
	ApprovalMode   *string           `json:"approval_mode,omitempty"`
	// EgressAllowlist (docs/agent-behavior-monitoring.md §7): destinations the
	// agent may reach beyond the public internet. Entries: domain, *.glob,
	// CIDR, host:port.
	EgressAllowlist []string `json:"egress_allowlist,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	OrgUnitID      string            `json:"org_unit_id,omitempty"`
	// Structured definition (docs/evaluable-agent.md). PromptSource marks the
	// origin of the definition for version history: manual | ai | builtin;
	// GeneratorModel records the model behind AI drafts.
	Definition     *AgentDefinition `json:"definition,omitempty"`
	PromptSource   string           `json:"prompt_source,omitempty"`
	GeneratorModel string           `json:"generator_model,omitempty"`
}

// CreateTaskRequest is the payload for creating a task.
type CreateTaskRequest struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
}

// StartRunRequest starts a new run for a task.
type StartRunRequest struct {
	AgentID string `json:"agent_id,omitempty"`
	Model   string `json:"model,omitempty"`
}

// CreateProviderRequest is the payload for creating a model provider.
type CreateProviderRequest struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	BaseURL   string            `json:"base_url"`
	APIKeyRef string            `json:"api_key_ref,omitempty"`
	Models    []string          `json:"models,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	OrgUnitID string            `json:"org_unit_id,omitempty"`
}

// CreateUserRequest is the payload for creating a user.
type CreateUserRequest struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Email            string        `json:"email,omitempty"`
	Role             string        `json:"role"`
	Token            string        `json:"token,omitempty"`       // auto-generated if empty
	Password         *string       `json:"password,omitempty"`    // optional; set/replace the bcrypt password login
	OrgUnitID        *string       `json:"org_unit_id,omitempty"` // nil on update = keep; "" = unassign
	Active           *bool         `json:"active,omitempty"`
	Channels         []UserChannel `json:"channels,omitempty"`
	PreferredChannel string        `json:"preferred_channel,omitempty"`
}
