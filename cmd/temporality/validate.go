package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/temporality-project/temporality/skills"
)

// validationResult is the outcome of a local manifest/contract validation.
type validationResult struct {
	Valid    bool           `json:"valid"`
	Path     string         `json:"path"`
	Sources  []string       `json:"sources,omitempty"`
	ID       string         `json:"id,omitempty"`
	Version  string         `json:"version,omitempty"`
	Inferred []string       `json:"inferred,omitempty"`
	Issues   []skills.Issue `json:"issues"`
}

func runSkillValidate(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("validate", env, &opts, stderr)
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill validate <path>   (skill.yaml, SKILL.md or directory)")
		return 2
	}
	result, err := validateSkillPath(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "temporality skill validate:", err)
		return 1
	}
	if opts.json {
		if code := writeJSONOrFail(stdout, stderr, result); code != 0 {
			return code
		}
	} else {
		writeValidationHuman(stdout, result)
	}
	if !result.Valid {
		return 1
	}
	return 0
}

// validateSkillPath loads a skill from a local path and validates it with the
// shared skills package — the same parse-and-validate rules the kernel's
// skill endpoints apply (skillRequest.parse in cmd/agent-kernel): manifest
// YAML first, legacy inference from SKILL.md when the manifest carries no
// identity, then defaulting and validation. Unlike the server there is no
// separate name/description input: everything comes from the files.
func validateSkillPath(path string) (validationResult, error) {
	manifestYAML, markdown, sources, err := readSkillFiles(path)
	if err != nil {
		return validationResult{}, err
	}
	result := validationResult{Path: path, Sources: sources, Issues: nil}
	manifest, err := skills.ParseManifest(manifestYAML)
	if err != nil {
		result.Issues = append(result.Issues, skills.Issue{Field: "manifest_yaml", Message: err.Error()})
		return result, nil
	}
	if manifest.ID == "" && manifest.Name == "" {
		manifest = skills.InferFromMarkdown(markdown)
	}
	if manifest.ID == "" {
		manifest.ID = slugify(manifest.Name)
	}
	if manifest.Version == "" {
		manifest.Version = "1.0.0"
	}
	result.ID = manifest.ID
	result.Version = manifest.Version
	result.Inferred = manifest.Inferred
	result.Issues = manifest.Validate()
	result.Valid = len(result.Issues) == 0
	return result, nil
}

// readSkillFiles resolves the manifest YAML and SKILL.md text from a path:
// a directory (skill.yaml|skill.yml + SKILL.md) or a single file.
func readSkillFiles(path string) (manifestYAML, markdown string, sources []string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", nil, err
	}
	if !info.IsDir() {
		name := strings.ToLower(filepath.Base(path))
		switch {
		case name == "skill.md" || strings.HasSuffix(name, ".md"):
			raw, err := os.ReadFile(path)
			if err != nil {
				return "", "", nil, err
			}
			return "", string(raw), []string{filepath.Base(path)}, nil
		case strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml"):
			raw, err := os.ReadFile(path)
			if err != nil {
				return "", "", nil, err
			}
			return string(raw), "", []string{filepath.Base(path)}, nil
		default:
			return "", "", nil, fmt.Errorf("%s is not a skill file (expected skill.yaml, SKILL.md or a directory)", path)
		}
	}
	read := func(name string) (string, bool) {
		raw, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			return "", false
		}
		return string(raw), true
	}
	for _, name := range []string{"skill.yaml", "skill.yml"} {
		if raw, ok := read(name); ok {
			manifestYAML = raw
			sources = append(sources, name)
			break
		}
	}
	if raw, ok := read("SKILL.md"); ok {
		markdown = raw
		sources = append(sources, "SKILL.md")
	}
	if manifestYAML == "" && markdown == "" {
		return "", "", nil, fmt.Errorf("no skill.yaml or SKILL.md found in %s", path)
	}
	return manifestYAML, markdown, sources, nil
}

func writeValidationHuman(w io.Writer, result validationResult) {
	if result.Valid {
		fmt.Fprintf(w, "valid    %s %s (%s)\n", result.ID, result.Version, strings.Join(result.Sources, " + "))
	} else {
		fmt.Fprintf(w, "invalid  %s\n", result.Path)
	}
	if len(result.Inferred) > 0 {
		fmt.Fprintf(w, "inferred from SKILL.md: %s\n", strings.Join(result.Inferred, ", "))
	}
	for _, issue := range result.Issues {
		fmt.Fprintf(w, "  %s: %s\n", issue.Field, issue.Message)
	}
}

// slugify mirrors cmd/agent-kernel's slugify so local validation accepts the
// same names the server normalizes on create.
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
