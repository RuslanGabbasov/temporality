package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/skills"
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

// runSkillApply promotes a version: drafts become the current revision,
// applying an older active version is the rollback path.
func runSkillApply(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("apply", env, &opts, stderr)
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: temporality skill apply <skill-id> <version>")
		return 2
	}
	var s skill
	if err := newClient(opts.url, opts.token).post(skillPath(fs.Arg(0))+"/versions/"+url.PathEscape(fs.Arg(1))+"/apply", struct{}{}, &s); err != nil {
		fmt.Fprintln(stderr, "temporality skill apply:", err)
		return 1
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, s)
	}
	fmt.Fprintf(stdout, "applied %s %s (now current)\n", s.ID, s.Version)
	return 0
}

// runSkillReject dismisses a draft proposal forever.
func runSkillReject(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("reject", env, &opts, stderr)
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: temporality skill reject <skill-id> <version>")
		return 2
	}
	var v skillVersion
	if err := newClient(opts.url, opts.token).post(skillPath(fs.Arg(0))+"/versions/"+url.PathEscape(fs.Arg(1))+"/reject", struct{}{}, &v); err != nil {
		fmt.Fprintln(stderr, "temporality skill reject:", err)
		return 1
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, v)
	}
	fmt.Fprintf(stdout, "rejected %s %s\n", v.SkillID, v.Version)
	return 0
}

// runSkillDiff prints the manifest + SKILL.md changes between two versions.
// Defaults mirror the agent tool: current revision → newest draft.
func runSkillDiff(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("diff", env, &opts, stderr)
	from := fs.String("from", "", "base version (default: current revision)")
	to := fs.String("to", "", "target version (default: newest draft)")
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill diff <skill-id> [--from v] [--to v]")
		return 2
	}
	var payload struct {
		Versions []skillVersion `json:"versions"`
	}
	if err := newClient(opts.url, opts.token).get(skillPath(fs.Arg(0))+"/versions", nil, &payload); err != nil {
		fmt.Fprintln(stderr, "temporality skill diff:", err)
		return 1
	}
	if len(payload.Versions) == 0 {
		fmt.Fprintln(stdout, "no versions")
		return 0
	}
	pick := func(version string) int {
		if version != "" {
			for i, v := range payload.Versions {
				if v.Version == version {
					return i
				}
			}
			fmt.Fprintf(stderr, "temporality skill diff: version %q not found\n", version)
			return -1
		}
		return -1
	}
	fromIdx := pick(*from)
	if fromIdx == -1 && *from != "" {
		return 2
	}
	toIdx := pick(*to)
	if toIdx == -1 && *to != "" {
		return 2
	}
	if fromIdx < 0 {
		for i, v := range payload.Versions {
			if v.Status == "active" {
				fromIdx = i
				break
			}
		}
		if fromIdx < 0 {
			fromIdx = len(payload.Versions) - 1
		}
	}
	if toIdx < 0 {
		toIdx = 0
		for i, v := range payload.Versions {
			if v.Status == "draft" {
				toIdx = i
				break
			}
		}
	}
	if fromIdx == toIdx {
		fmt.Fprintln(stdout, "no different versions to compare")
		return 0
	}
	fromV, toV := payload.Versions[fromIdx], payload.Versions[toIdx]
	fromManifest, err1 := skills.ParseManifest(string(fromV.Manifest))
	toManifest, err2 := skills.ParseManifest(string(toV.Manifest))
	if err1 != nil || err2 != nil {
		fmt.Fprintln(stderr, "temporality skill diff: stored manifest is not parseable")
		return 1
	}
	entries := skills.DiffManifests(fromManifest, toManifest)
	added, removed := skills.DiffMarkdown(fromV.Markdown, toV.Markdown)
	fmt.Fprint(stdout, skills.RenderDiff(fromV.Version, toV.Version, entries, added, removed))
	return 0
}

type evaluationCaseResult struct {
	Name       string   `json:"name"`
	Passed     bool     `json:"passed"`
	Missed     []string `json:"missed"`
	Unexpected []string `json:"unexpected"`
}

type evaluationRun struct {
	ID           int64                  `json:"id"`
	SkillVersion string                 `json:"skill_version"`
	Passed       int                    `json:"passed"`
	Failed       int                    `json:"failed"`
	Cases        []evaluationCaseResult `json:"cases"`
	CreatedAt    time.Time              `json:"created_at"`
}

// runSkillEvals lists evaluation run history.
func runSkillEvals(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("evals", env, &opts, stderr)
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill evals <skill-id>")
		return 2
	}
	var payload struct {
		Runs []evaluationRun `json:"runs"`
	}
	if err := newClient(opts.url, opts.token).get(skillPath(fs.Arg(0))+"/evaluations", nil, &payload); err != nil {
		fmt.Fprintln(stderr, "temporality skill evals:", err)
		return 1
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, payload)
	}
	if len(payload.Runs) == 0 {
		fmt.Fprintln(stdout, "no evaluation runs")
		return 0
	}
	rows := [][]string{{"RUN", "VERSION", "PASSED", "FAILED", "CREATED"}}
	for _, run := range payload.Runs {
		rows = append(rows, []string{
			strconv.FormatInt(run.ID, 10), run.SkillVersion,
			strconv.Itoa(run.Passed), strconv.Itoa(run.Failed), formatTime(run.CreatedAt),
		})
	}
	printLines(stdout, alignRows(rows))
	return 0
}

// runSkillEvalRun executes the stored suite now (synchronous model calls).
func runSkillEvalRun(args []string, env func(string) string, stdout, stderr io.Writer) int {
	var opts options
	fs := commonFlags("eval-run", env, &opts, stderr)
	version := fs.String("version", "", "test a specific version (e.g. a pending draft)")
	if code := parseExit(fs.Parse(args)); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: temporality skill eval-run <skill-id> [--version v]")
		return 2
	}
	payload := map[string]any{}
	if *version != "" {
		payload["version"] = *version
	}
	var run evaluationRun
	api := newClient(opts.url, opts.token)
	api.http.Timeout = 10 * time.Minute
	if err := api.post(skillPath(fs.Arg(0))+"/evaluations", payload, &run); err != nil {
		fmt.Fprintln(stderr, "temporality skill eval-run:", err)
		return 1
	}
	if opts.json {
		return writeJSONOrFail(stdout, stderr, run)
	}
	fmt.Fprintf(stdout, "run #%d on %s: %d passed, %d failed\n", run.ID, run.SkillVersion, run.Passed, run.Failed)
	for _, c := range run.Cases {
		mark := "✓"
		if !c.Passed {
			mark = "✗"
		}
		fmt.Fprintf(stdout, "  %s %s", mark, dash(c.Name))
		if len(c.Missed) > 0 || len(c.Unexpected) > 0 {
			fmt.Fprintf(stdout, " (missed: %s; unexpected: %s)", strings.Join(c.Missed, ", "), strings.Join(c.Unexpected, ", "))
		}
		fmt.Fprintln(stdout)
	}
	if run.Failed > 0 {
		return 1
	}
	return 0
}
