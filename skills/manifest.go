// Package skills implements the Living Skills manifest model: a machine-readable
// contract (skill.yaml) paired with the human-readable SKILL.md, validation, and
// a compact digest for prompt injection. See docs/living-skills.md.
package skills

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the parsed skill.yaml contract.
type Manifest struct {
	ID             string           `json:"id,omitempty" yaml:"id"`
	Version        string           `json:"version,omitempty" yaml:"version"`
	Name           string           `json:"name,omitempty" yaml:"name"`
	Description    string           `json:"description,omitempty" yaml:"description"`
	Inputs         map[string]Field `json:"inputs,omitempty" yaml:"inputs"`
	Outputs        map[string]Field `json:"outputs,omitempty" yaml:"outputs"`
	Capabilities   []string         `json:"capabilities,omitempty" yaml:"capabilities"`
	Tools          []string         `json:"tools,omitempty" yaml:"tools"`
	Runtime        Runtime          `json:"runtime,omitempty" yaml:"runtime"`
	Preconditions  []string         `json:"preconditions,omitempty" yaml:"preconditions"`
	Postconditions []string         `json:"postconditions,omitempty" yaml:"postconditions"`
	Evidence       EvidenceSpec     `json:"evidence,omitempty" yaml:"evidence"`
	Evaluation     *EvaluationSpec  `json:"evaluation,omitempty" yaml:"evaluation"`
	Inferred       []string         `json:"inferred,omitempty" yaml:"-"` // fields inferred from SKILL.md (legacy loading)
}

// Field is a typed input or output declaration.
type Field struct {
	Type        string `json:"type,omitempty" yaml:"type"`
	Description string `json:"description,omitempty" yaml:"description"`
	Required    bool   `json:"required,omitempty" yaml:"required"`
}

// Runtime describes what the skill needs from the execution environment.
type Runtime struct {
	Sandbox     string   `json:"sandbox,omitempty" yaml:"sandbox"` // required | optional | none
	Network     string   `json:"network,omitempty" yaml:"network"` // restricted | open | none
	Filesystem  FSAccess `json:"filesystem,omitempty" yaml:"filesystem"`
	Credentials []string `json:"credentials,omitempty" yaml:"credentials"`
	MCP         []string `json:"mcp,omitempty" yaml:"mcp"`
}

// FSAccess declares filesystem needs.
type FSAccess struct {
	Read  []string `json:"read,omitempty" yaml:"read"`
	Write []string `json:"write,omitempty" yaml:"write"`
}

// EvidenceSpec declares the minimal evidence a skill execution must produce.
type EvidenceSpec struct {
	Required []string `json:"required,omitempty" yaml:"required"`
}

// EvaluationSpec references an evaluation suite.
type EvaluationSpec struct {
	Suite string `json:"suite,omitempty" yaml:"suite"`
}

// Issue is a single validation finding.
type Issue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	versionRe   = regexp.MustCompile(`^\d+\.\d+\.\d+([+-][0-9A-Za-z.-]+)?$`)
	headingRe   = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	sectionRe   = regexp.MustCompile(`(?mi)^##\s+(.+)$`)
	procedureRe = regexp.MustCompile(`(?m)^\s*\d+[.)]\s+(.+)$`)
)

// ParseManifest decodes a manifest from YAML or JSON text.
func ParseManifest(raw string) (Manifest, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Manifest{}, nil
	}
	var m Manifest
	if err := yaml.Unmarshal([]byte(trimmed), &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest must be valid YAML (or JSON): %w", err)
	}
	return m, nil
}

// Validate returns a list of issues; empty means the manifest is valid.
func (m Manifest) Validate() []Issue {
	var issues []Issue
	if m.ID == "" {
		issues = append(issues, Issue{Field: "id", Message: "id is required"})
	} else if !idPattern.MatchString(m.ID) {
		issues = append(issues, Issue{Field: "id", Message: "id must be lowercase letters, digits and dashes (e.g. deploy-service)"})
	}
	if m.Version == "" {
		issues = append(issues, Issue{Field: "version", Message: "version is required (semver, e.g. 1.0.0)"})
	} else if !versionRe.MatchString(m.Version) {
		issues = append(issues, Issue{Field: "version", Message: "version must be semver (e.g. 1.2.0)"})
	}
	if m.Name == "" {
		issues = append(issues, Issue{Field: "name", Message: "name is required"})
	}
	if m.Description == "" {
		issues = append(issues, Issue{Field: "description", Message: "description is recommended so agents can select the skill"})
	}
	if len(m.Capabilities) == 0 {
		issues = append(issues, Issue{Field: "capabilities", Message: "declare at least one capability (stable identifier for memory and analytics)"})
	}
	if len(m.Tools) == 0 {
		issues = append(issues, Issue{Field: "tools", Message: "declare the tools this skill uses (e.g. run_command, mcp servers)"})
	}
	for name, field := range m.Inputs {
		if field.Required && field.Type == "" {
			issues = append(issues, Issue{Field: "inputs." + name, Message: "type is required for declared inputs"})
		}
	}
	switch m.Runtime.Sandbox {
	case "", "required", "optional", "none":
	default:
		issues = append(issues, Issue{Field: "runtime.sandbox", Message: "sandbox must be one of: required, optional, none"})
	}
	switch m.Runtime.Network {
	case "", "restricted", "open", "none":
	default:
		issues = append(issues, Issue{Field: "runtime.network", Message: "network must be one of: restricted, open, none"})
	}
	return issues
}

// InferFromMarkdown builds a legacy manifest from SKILL.md content when no
// skill.yaml exists. Inferred fields are recorded in Inferred so consumers can
// mark them for human review (docs/living-skills.md §40-41).
func InferFromMarkdown(markdown string) Manifest {
	m := Manifest{
		Version:  "1.0.0",
		Inferred: []string{"name", "description", "capabilities"},
	}
	if match := headingRe.FindStringSubmatch(markdown); match != nil {
		m.Name = strings.TrimSpace(match[1])
	} else {
		m.Name = "Unnamed skill"
	}
	m.ID = slug(m.Name)
	if section, ok := markdownSection(markdown, "purpose"); ok {
		m.Description = firstSentences(section, 2)
	}
	// A numbered list under a "Procedure"/"When to use"/"How to" heading is the
	// best capability signal available without a manifest.
	for _, title := range []string{"procedure", "when to use", "how to"} {
		if section, ok := markdownSection(markdown, title); ok {
			for _, step := range procedureRe.FindAllStringSubmatch(section, -1) {
				capability := slug(firstWords(step[1], 3))
				if capability != "" && !contains(m.Capabilities, capability) {
					m.Capabilities = append(m.Capabilities, capability)
				}
			}
			if len(m.Capabilities) > 0 {
				break
			}
		}
	}
	m.Tools = []string{"run_command"}
	m.Runtime = Runtime{Sandbox: "optional"}
	return m
}

// Digest renders a compact skill description for system prompt injection,
// bounded by the budget (in characters). Skills are canonical capabilities, so
// injecting their digest is intentional — unlike memory, which is filtered
// per execution (docs/living-skills.md §15).
func Digest(name, version, markdown string, m Manifest, budget int) string {
	if budget <= 0 {
		budget = 1500
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Skill: %s (v%s)", name, version)
	if m.Description != "" {
		fmt.Fprintf(&b, "\n%s", m.Description)
	}
	if len(m.Capabilities) > 0 {
		fmt.Fprintf(&b, "\nCapabilities: %s", strings.Join(m.Capabilities, ", "))
	}
	if len(m.Tools) > 0 {
		fmt.Fprintf(&b, "\nTools: %s", strings.Join(m.Tools, ", "))
	}
	if section, ok := markdownSection(markdown, "when to use"); ok {
		fmt.Fprintf(&b, "\nWhen to use: %s", firstSentences(section, 2))
	}
	if section, ok := markdownSection(markdown, "procedure"); ok {
		steps := procedureRe.FindAllStringSubmatch(section, -1)
		if len(steps) > 0 {
			b.WriteString("\nProcedure:")
			for i, step := range steps {
				if i >= 8 {
					fmt.Fprintf(&b, "\n%d. … (%d more steps in skill_inspect)", i+1, len(steps)-i)
					break
				}
				fmt.Fprintf(&b, "\n%d. %s", i+1, firstWords(step[1], 18))
			}
		}
	}
	if section, ok := markdownSection(markdown, "constraints"); ok {
		fmt.Fprintf(&b, "\nConstraints: %s", firstSentences(section, 3))
	}
	out := b.String()
	if len(out) > budget {
		out = out[:budget] + "… (truncated, use skill_inspect)"
	}
	return out
}

func markdownSection(markdown, title string) (string, bool) {
	lines := strings.Split(markdown, "\n")
	var collected []string
	inSection := false
	for _, line := range lines {
		if match := sectionRe.FindStringSubmatch(line); match != nil {
			if inSection {
				break // next ## section
			}
			if strings.EqualFold(strings.TrimSpace(match[1]), title) {
				inSection = true
				continue
			}
		}
		if inSection {
			collected = append(collected, line)
		}
	}
	if !inSection || len(collected) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(collected, "\n")), true
}

func firstSentences(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	sentences := strings.SplitAfter(text, ". ")
	if len(sentences) <= max {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(strings.Join(sentences[:max], ""))
}

func firstWords(text string, max int) string {
	words := strings.Fields(text)
	if len(words) > max {
		words = words[:max]
	}
	return strings.Join(words, " ")
}

func slug(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '/', r == '.', r == ',', r == ':':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// MarshalJSONForStorage normalizes the manifest for JSONB storage.
func MarshalJSONForStorage(m Manifest) json.RawMessage {
	encoded, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(encoded)
}
