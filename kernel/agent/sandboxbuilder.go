package agent

// Sandbox profile builder (docs/sandbox-profiles.md, wizard surface): the
// supervisor model turns a plain-language description of the agent's tasks
// into a sandbox profile draft — base image, toolchains, packages and
// resource limits. Pure model call + parsing + validation; persistence and
// the Docker build live at the caller (Sandbox UI → existing create/build
// endpoints), mirroring the team/agent wizards.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// SandboxDraft is the model's proposal for one sandbox profile. Resource
// strings use the same forms the runner validates ("512m", "2", "1g").
type SandboxDraft struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Base        string   `json:"base"`
	Packages    []string `json:"packages,omitempty"`
	Toolchains  []string `json:"toolchains,omitempty"`
	Memory      string   `json:"memory,omitempty"`
	CPUs        string   `json:"cpus,omitempty"`
	ScratchSize string   `json:"scratch_size,omitempty"`
	TmpSize     string   `json:"tmp_size,omitempty"`
	// Rationale is the model's one-paragraph "why these limits" — surfaced in
	// the UI so the operator can sanity-check the draft before saving.
	Rationale string `json:"rationale,omitempty"`
}

// KnownSandboxBase describes one base image the builder may pick. The caller
// builds the list from kernel/sandbox ProfileDefinitions + the embedded base
// so the draft can never reference an image the platform cannot assemble.
type KnownSandboxBase struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Descript string `json:"description"`
}

// sandboxDraftPrompt grounds the supervisor: the known bases, the task
// description and the sizing rules.
func sandboxDraftPrompt(description, bases string) string {
	var b strings.Builder
	b.WriteString(`You configure a Docker sandbox profile for an AI agent. The operator describes in plain language what tasks the agent will perform; you propose the sandbox.

Pick ONE base image from the list below (do not invent bases). Add only OS packages the described tasks genuinely need (apk names for alpine bases, apt names for debian bases). Size resources by workload class:
- light scripts/config edits: memory 512m, cpus 0.5, scratch 512m, tmp 256m
- builds/tests of a typical service: memory 2g, cpus 2, scratch 1g, tmp 512m
- heavy e2e suites, browsers, large compiles: memory 4g, cpus 4, scratch 2g, tmp 1g
Never exceed memory 8g / cpus 8 / scratch 4g / tmp 2g. id: 2-32 chars, lowercase latin/digits/dash. name: short human label. description: 1-2 sentences.

Known bases:
`)
	b.WriteString(bases)
	b.WriteString(`

Operator's description of the tasks:
`)
	b.WriteString(strings.TrimSpace(description))
	b.WriteString(`

Respond with one JSON object:
{"id": "...", "name": "...", "description": "...", "base": "<base id>", "packages": ["..."], "toolchains": ["..."], "memory": "...", "cpus": "...", "scratch_size": "...", "tmp_size": "...", "rationale": "<why these choices, 1-2 sentences>"}`)
	return b.String()
}

// SandboxDraftParseArgs is the parse/validation context: known base ids.
type SandboxDraftLimits struct {
	Bases []KnownSandboxBase
}

// parseSandboxDraft validates and normalizes the model's reply against the
// known bases. Unknown bases, bad resource forms and bad ids are validation
// errors — the caller surfaces them, the model retries via the standard
// JSON-repair path in completeParsed.
func parseSandboxDraft(knownBases []string) func(string) (SandboxDraft, error) {
	return func(content string) (SandboxDraft, error) {
		payload, ok := extractJSONObject(content)
		if !ok {
			return SandboxDraft{}, fmt.Errorf("model returned no JSON object")
		}
		var draft SandboxDraft
		if err := json.Unmarshal([]byte(payload), &draft); err != nil {
			return SandboxDraft{}, fmt.Errorf("model returned invalid JSON: %w", err)
		}
		draft.ID = strings.ToLower(strings.TrimSpace(draft.ID))
		draft.ID = strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
				return r
			case r == ' ' || r == '_':
				return '-'
			}
			return -1
		}, draft.ID)
		draft.ID = strings.Trim(draft.ID, "-")
		if len(draft.ID) < 2 || len(draft.ID) > 32 {
			return SandboxDraft{}, fmt.Errorf("model returned an unusable id %q (want 2-32 chars, lowercase/digits/dash)", draft.ID)
		}
		draft.Name = strings.TrimSpace(draft.Name)
		if draft.Name == "" {
			return SandboxDraft{}, fmt.Errorf("model returned an empty name")
		}
		base := strings.ToLower(strings.TrimSpace(draft.Base))
		known := false
		for _, b := range knownBases {
			if b == base {
				known = true
				break
			}
		}
		if !known {
			return SandboxDraft{}, fmt.Errorf("model returned unknown base %q (known: %s)", draft.Base, strings.Join(knownBases, ", "))
		}
		draft.Base = base
		// Resource sanity clamps: the runner validates exact syntax later; here
		// we only reject nonsense the model may have invented.
		for _, check := range []struct{ field, value string }{
			{"memory", draft.Memory}, {"cpus", draft.CPUs},
			{"scratch_size", draft.ScratchSize}, {"tmp_size", draft.TmpSize},
		} {
			if check.value == "" {
				continue
			}
			if strings.ContainsAny(check.value, "/\\ ") {
				return SandboxDraft{}, fmt.Errorf("model returned malformed %s %q", check.field, check.value)
			}
		}
		return draft, nil
	}
}

// BuildSandboxDraft asks the supervisor model for a sandbox profile draft
// for the described tasks. knownBases is the caller's base catalog (ids +
// human names) — the model may only pick from it.
func BuildSandboxDraft(ctx context.Context, model *llm.Client, description string, knownBases []KnownSandboxBase) (SandboxDraft, error) {
	if model == nil {
		return SandboxDraft{}, fmt.Errorf("model client is not configured")
	}
	if strings.TrimSpace(description) == "" {
		return SandboxDraft{}, fmt.Errorf("description is required")
	}
	ids := make([]string, 0, len(knownBases))
	lines := make([]string, 0, len(knownBases))
	for _, base := range knownBases {
		ids = append(ids, base.ID)
		lines = append(lines, fmt.Sprintf("- %s: %s", base.ID, base.Descript))
	}
	_ = ids
	draft, err := completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You configure Docker sandbox profiles for AI agents. You answer with JSON only."},
		{Role: "user", Content: sandboxDraftPrompt(description, strings.Join(lines, "\n"))},
	}, parseSandboxDraft(ids))
	if err != nil {
		return SandboxDraft{}, err
	}
	return draft, nil
}
