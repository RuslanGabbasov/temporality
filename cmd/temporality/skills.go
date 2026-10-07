package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// API shapes of the skill registry served by agent-kernel (workspace.Skill
// and friends). The CLI keeps its own copies so it stays a thin, separately
// testable client; field lists mirror the server structs exactly.

type skill struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Version       string          `json:"version"`
	Markdown      string          `json:"markdown"`
	Manifest      json.RawMessage `json:"manifest"`
	OrgUnitID     string          `json:"org_unit_id,omitempty"`
	VersionStatus string          `json:"version_status,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type skillVersion struct {
	SkillID  string          `json:"skill_id"`
	Version  string          `json:"version"`
	Markdown string          `json:"markdown"`
	Manifest json.RawMessage `json:"manifest"`
	// Provenance of the change that produced this version.
	Origin        string    `json:"origin"`
	SourceRuns    []string  `json:"source_runs,omitempty"`
	EvidenceRefs  []string  `json:"evidence_refs,omitempty"`
	KnowledgeIDs  []string  `json:"knowledge_ids,omitempty"`
	ChangeSummary string    `json:"change_summary,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type skillExecution struct {
	ID           int64     `json:"id"`
	SkillID      string    `json:"skill_id"`
	SkillVersion string    `json:"skill_version"`
	ProjectID    string    `json:"project_id"`
	RunID        string    `json:"run_id"`
	AgentID      string    `json:"agent_id"`
	StartedAt    time.Time `json:"started_at"`
}

// skillMemoryItem is one knowledge entry linked to a skill: a
// knowledge.proposed event carrying data.skill_id, enriched with the current
// knowledge state by the server.
type skillMemoryItem struct {
	KnowledgeID string `json:"knowledge_id"`
	Proposition string `json:"proposition"`
	Capability  string `json:"capability"`
	RunID       string `json:"run_id"`
	Project     string `json:"project"`
	EventID     string `json:"event_id"`
	OccurredAt  string `json:"occurred_at"`
	State       string `json:"state"`
}

// options carries the flags shared by every subcommand.
type options struct {
	url   string
	token string
	json  bool
}

// commonFlags registers --url/--token/--json with environment defaults on a
// flag set that reports parse errors to stderr instead of exiting.
func commonFlags(name string, env func(string) string, opts *options, stderr io.Writer) *flag.FlagSet {
	baseURL := env("TEMPORALITY_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	opts.url = baseURL
	opts.token = env("TEMPORALITY_API_TOKEN")
	fs := flag.NewFlagSet("temporality skill "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.url, "url", baseURL, "workspace API base URL (env TEMPORALITY_URL)")
	fs.StringVar(&opts.token, "token", opts.token, "API bearer token (env TEMPORALITY_API_TOKEN)")
	fs.BoolVar(&opts.json, "json", false, "machine-readable JSON output")
	return fs
}

// parseExit maps flag parse failures to exit codes; -h prints the flag
// usage and is not an error.
func parseExit(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func skillPath(id string) string {
	return "/v1/workspace/skills/" + url.PathEscape(id)
}

func runSkillList(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("list", env, &opts, stderr)
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	var payload struct {
		Skills []skill `json:"skills"`
		Count  int     `json:"count"`
	}
	if err := newClient(opts.url, opts.token).get("/v1/workspace/skills", nil, &payload); err != nil {
		fmt.Fprintln(stderr, "temporality skill list:", err)
		return 1
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, payload)
	}
	if len(payload.Skills) == 0 {
		fmt.Fprintln(stdout, "no skills")
		return 0
	}
	rows := [][]string{{"ID", "VERSION", "STATUS", "UPDATED", "NAME"}}
	for _, s := range payload.Skills {
		rows = append(rows, []string{s.ID, s.Version, statusLabel(s.VersionStatus), formatTime(s.UpdatedAt), s.Name})
	}
	printLines(stdout, alignRows(rows))
	return 0
}

func runSkillInspect(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("inspect", env, &opts, stderr)
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill inspect <skill-id>")
		return 2
	}
	var s skill
	if err := newClient(opts.url, opts.token).get(skillPath(fs.Arg(0)), nil, &s); err != nil {
		fmt.Fprintln(stderr, "temporality skill inspect:", err)
		return 1
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, s)
	}
	writeSkillDetail(stdout, s)
	return 0
}

func runSkillHistory(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("history", env, &opts, stderr)
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill history <skill-id>")
		return 2
	}
	var payload struct {
		Versions []skillVersion `json:"versions"`
	}
	if err := newClient(opts.url, opts.token).get(skillPath(fs.Arg(0))+"/versions", nil, &payload); err != nil {
		fmt.Fprintln(stderr, "temporality skill history:", err)
		return 1
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, payload)
	}
	if len(payload.Versions) == 0 {
		fmt.Fprintln(stdout, "no versions")
		return 0
	}
	rows := [][]string{{"VERSION", "STATUS", "ORIGIN", "CREATED", "CHANGE"}}
	for _, v := range payload.Versions {
		rows = append(rows, []string{v.Version, statusLabel(v.Status), v.Origin, formatTime(v.CreatedAt), dash(v.ChangeSummary)})
	}
	printLines(stdout, alignRows(rows))
	return 0
}

func runSkillExecutions(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("executions", env, &opts, stderr)
	project := fs.String("project", "", "only executions in this project")
	limit := fs.Int("limit", 0, "maximum executions to fetch (0 = server default)")
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill executions <skill-id> [--project id] [--limit n]")
		return 2
	}
	if *limit < 0 {
		fmt.Fprintln(stderr, "temporality skill executions: --limit must be >= 0")
		return 2
	}
	query := url.Values{}
	if *limit > 0 {
		query.Set("limit", strconv.Itoa(*limit))
	}
	var payload struct {
		Executions []skillExecution `json:"executions"`
	}
	if err := newClient(opts.url, opts.token).get(skillPath(fs.Arg(0))+"/executions", query, &payload); err != nil {
		fmt.Fprintln(stderr, "temporality skill executions:", err)
		return 1
	}
	if *project != "" {
		filtered := payload.Executions[:0]
		for _, e := range payload.Executions {
			if e.ProjectID == *project {
				filtered = append(filtered, e)
			}
		}
		payload.Executions = filtered
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, payload)
	}
	if len(payload.Executions) == 0 {
		fmt.Fprintln(stdout, "no executions")
		return 0
	}
	rows := [][]string{{"ID", "RUN", "AGENT", "PROJECT", "VERSION", "STARTED"}}
	for _, e := range payload.Executions {
		rows = append(rows, []string{
			strconv.FormatInt(e.ID, 10), dash(e.RunID), dash(e.AgentID), dash(e.ProjectID), dash(e.SkillVersion), formatTime(e.StartedAt),
		})
	}
	printLines(stdout, alignRows(rows))
	return 0
}

func runSkillMemory(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("memory", env, &opts, stderr)
	project := fs.String("project", "", "only knowledge from this project")
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill memory <skill-id> [--project id]")
		return 2
	}
	var payload struct {
		Memory []skillMemoryItem `json:"memory"`
	}
	if err := newClient(opts.url, opts.token).get(skillPath(fs.Arg(0))+"/memory", nil, &payload); err != nil {
		fmt.Fprintln(stderr, "temporality skill memory:", err)
		return 1
	}
	if *project != "" {
		filtered := payload.Memory[:0]
		for _, item := range payload.Memory {
			if item.Project == *project {
				filtered = append(filtered, item)
			}
		}
		payload.Memory = filtered
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, payload)
	}
	if len(payload.Memory) == 0 {
		fmt.Fprintln(stdout, "no linked knowledge")
		return 0
	}
	writeMemoryList(stdout, payload.Memory)
	return 0
}
