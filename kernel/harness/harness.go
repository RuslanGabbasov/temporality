// Package harness centralizes the installation-wide settings that shape the
// whole agent harness: model-request budgets, compaction knobs, output caps
// and activity timeouts. Every value has a documented default and a
// TEMPORALITY_* env override; kernel/agent reads workflow-affecting values
// through the per-run pinned snapshot (see Budgets) and activity-side values
// through Current() (docs/harness-settings.md).
//
// Model CHOICES (which model serves which role) live in the workspace
// settings (supervisor panel): roles are DB-backed, provider-validated and
// resolvable at call time — this package only names the roles.
package harness

import (
	"os"
	"strconv"
	"time"
)

// ModelRole names a model slot in the installation settings. The run's own
// model is chosen per agent; these roles cover the harness's own work.
type ModelRole string

const (
	// RoleJudge — supervisor/judge work: evaluations, conflict judging, area
	// summaries (setting eval.judge.model).
	RoleJudge ModelRole = "judge"
	// RoleVision — image-bearing calls when the run's model is not multimodal
	// (setting supervisor.vision.model).
	RoleVision ModelRole = "vision"
	// RoleCompaction — the context-compaction state card (docs/context-compaction.md).
	RoleCompaction ModelRole = "compaction"
	// RoleTitle — short run titles for the runs list.
	RoleTitle ModelRole = "title"
	// RoleExtraction — end-of-run knowledge extraction.
	RoleExtraction ModelRole = "extraction"
)

// Budgets are the numeric knobs that steer workflow code paths: they decide
// when a request is too big to schedule, when the conversation compacts and
// how much of a tool output stays in history. Because such a decision must
// replay identically for a run already in flight, AgentRun pins one snapshot
// per run (the harness runtime activity) instead of reading the process
// settings on every turn.
type Budgets struct {
	// MaxModelRequestBytes is the serialized call_model activity-input
	// ceiling: Temporal caps a single event payload at limit.blobSize.error
	// (2 MiB), so anything bigger would wedge the run in TMPRL1103 retries.
	MaxModelRequestBytes int `json:"max_model_request_bytes"`
	// CompactionTriggerBytes is the request size that starts a context
	// compaction (docs/context-compaction.md).
	CompactionTriggerBytes int `json:"compaction_trigger_bytes"`
	// CompactionCardMaxBytes bounds the state card itself; the card rides
	// every later request.
	CompactionCardMaxBytes int `json:"compaction_card_max_bytes"`
	// CompactionTailBytes / CompactionKeepGroupsMin/Max bound the verbatim
	// tail kept after the card (whole turn groups only).
	CompactionTailBytes     int `json:"compaction_tail_bytes"`
	CompactionKeepGroupsMin int `json:"compaction_keep_groups_min"`
	CompactionKeepGroupsMax int `json:"compaction_keep_groups_max"`
	// CompactionDigestHeadBytes/CompactionDigestTailBytes bound one message
	// inside the digest sent for summarization.
	CompactionDigestHeadBytes int `json:"compaction_digest_head_bytes"`
	CompactionDigestTailBytes int `json:"compaction_digest_tail_bytes"`
	// ToolOutputMaxBytes/ToolOutputTailBytes bound one tool result as it
	// joins the conversation history (head and tail survive).
	ToolOutputMaxBytes  int `json:"tool_output_max_bytes"`
	ToolOutputTailBytes int `json:"tool_output_tail_bytes"`
}

// Settings is the whole harness surface: budgets plus the activity ceilings
// and image caps (activity-side values — they shape results, not workflow
// commands, and are read through Current()).
type Settings struct {
	Budgets Budgets `json:"budgets"`
	// ModelCallActivityTimeout bounds one kernel.call_model activity
	// execution (the streaming attempt plus the blocking fallback ladder).
	ModelCallActivityTimeout time.Duration `json:"model_call_activity_timeout"`
	// ToolActivityTimeout bounds one kernel.run_tool activity execution; it
	// must cover the sandbox command ceiling.
	ToolActivityTimeout time.Duration `json:"tool_activity_timeout"`
	// ImageMaxSide is the long-side pixel budget for vision blocks;
	// ImageEncodedMaxBytes bounds the encoded block (base64 expansion is
	// ×4/3 on the wire); ImageSourceMaxBytes bounds the raw download/file
	// before decoding.
	ImageMaxSide         int `json:"image_max_side"`
	ImageEncodedMaxBytes int `json:"image_encoded_max_bytes"`
	ImageSourceMaxBytes  int `json:"image_source_max_bytes"`
}

// DefaultBudgets returns the compile-time defaults. This exact snapshot is
// what pre-settings runs replay with — the literals are part of the workflow
// history contract and must keep matching the documented defaults.
func DefaultBudgets() Budgets {
	return Budgets{
		MaxModelRequestBytes:      (2 << 20) - (32 << 10),
		CompactionTriggerBytes:    1200 << 10,
		CompactionCardMaxBytes:    8 << 10,
		CompactionTailBytes:       256 << 10,
		CompactionKeepGroupsMin:   2,
		CompactionKeepGroupsMax:   8,
		CompactionDigestHeadBytes: 3584,
		CompactionDigestTailBytes: 512,
		ToolOutputMaxBytes:        512 << 10,
		ToolOutputTailBytes:       16 << 10,
	}
}

// Defaults returns the full compile-time settings snapshot.
func Defaults() Settings {
	return Settings{
		Budgets:                  DefaultBudgets(),
		ModelCallActivityTimeout: 20 * time.Minute,
		ToolActivityTimeout:      16 * time.Minute,
		ImageMaxSide:             1568,
		ImageEncodedMaxBytes:     512 << 10,
		ImageSourceMaxBytes:      5 << 20,
	}
}

// FromEnv resolves the settings: documented defaults overlaid with
// TEMPORALITY_* overrides. Unknown values fall back to the default so a typo
// degrades instead of breaking runs.
func FromEnv() Settings {
	s := Defaults()
	b := &s.Budgets
	intEnv("TEMPORALITY_MAX_MODEL_REQUEST_BYTES", &b.MaxModelRequestBytes)
	intEnv("TEMPORALITY_COMPACTION_TRIGGER_BYTES", &b.CompactionTriggerBytes)
	intEnv("TEMPORALITY_COMPACTION_CARD_MAX_BYTES", &b.CompactionCardMaxBytes)
	intEnv("TEMPORALITY_COMPACTION_TAIL_BYTES", &b.CompactionTailBytes)
	intEnv("TEMPORALITY_COMPACTION_KEEP_GROUPS_MIN", &b.CompactionKeepGroupsMin)
	intEnv("TEMPORALITY_COMPACTION_KEEP_GROUPS_MAX", &b.CompactionKeepGroupsMax)
	intEnv("TEMPORALITY_COMPACTION_DIGEST_HEAD_BYTES", &b.CompactionDigestHeadBytes)
	intEnv("TEMPORALITY_COMPACTION_DIGEST_TAIL_BYTES", &b.CompactionDigestTailBytes)
	intEnv("TEMPORALITY_TOOL_OUTPUT_MAX_BYTES", &b.ToolOutputMaxBytes)
	intEnv("TEMPORALITY_TOOL_OUTPUT_TAIL_BYTES", &b.ToolOutputTailBytes)
	durationEnv("TEMPORALITY_MODEL_ACTIVITY_TIMEOUT", &s.ModelCallActivityTimeout)
	durationEnv("TEMPORALITY_TOOL_ACTIVITY_TIMEOUT", &s.ToolActivityTimeout)
	intEnv("TEMPORALITY_IMAGE_MAX_SIDE", &s.ImageMaxSide)
	intEnv("TEMPORALITY_IMAGE_ENCODED_MAX_BYTES", &s.ImageEncodedMaxBytes)
	intEnv("TEMPORALITY_IMAGE_SOURCE_MAX_BYTES", &s.ImageSourceMaxBytes)
	return s
}

func intEnv(name string, target *int) {
	raw := os.Getenv(name)
	if raw == "" {
		return
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return
	}
	*target = value
}

func durationEnv(name string, target *time.Duration) {
	raw := os.Getenv(name)
	if raw == "" {
		return
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return
	}
	*target = value
}

// current is resolved once: the process environment does not change under a
// running kernel, and a single snapshot keeps every reader consistent.
var current = FromEnv()

// Current returns the process-wide harness settings.
func Current() Settings {
	return current
}
