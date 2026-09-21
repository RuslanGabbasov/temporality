package coding

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
)

// SystemPrompt is the coding-agent persona for the real-harness experiment.
// The reasoning loop itself is untouched; only the world description differs.
const SystemPrompt = `You are a coding agent investigating a real Go repository (the bleve search library, module github.com/blevesearch/bleve/v2) checked out at the current working directory.
Tools:
- shell(command): run a shell command in the repository root (macOS, bash; the go toolchain is on PATH; the environment is offline: GOPROXY=off).
- read_file(path, start_line?, end_line?): read a repository file (max 400 lines per call).
- list_files(path?): list one directory.
- grep(pattern, include?, path?): search file contents with a Go regular expression (include defaults to *.go).
Work economically: do not repeat a command that already failed identically; prefer the smallest command that answers the question.
When you have the requested information, reply with a final answer and no tool calls. Include the exact identifiers, values, file paths and the exact commands you used — reports that omit the requested items are incomplete.`

const (
	shellTimeoutDefault = 300 * time.Second
	maxOutputBytes      = 12000
	maxReadLines        = 400
	maxLineLength       = 500
	maxListEntries      = 300
	maxGrepMatches      = 50
	maxGrepFileBytes    = 2 << 20
	goBinPath           = "/usr/local/go/bin"
)

// Environment executes coding tools against one repository checkout. Shell
// commands are serialized so two concurrent go builds cannot fight over the
// same workspace.
type Environment struct {
	workdir        string
	shellTimeout   time.Duration
	mu             sync.Mutex
	skipGitFolders map[string]bool
}

// NewEnvironment builds the environment for a workspace directory.
func NewEnvironment(workdir string) *Environment {
	return &Environment{
		workdir:        workdir,
		shellTimeout:   shellTimeoutDefault,
		skipGitFolders: map[string]bool{".git": true, "node_modules": true},
	}
}

// Tools describes the four coding tools.
func (e *Environment) Tools() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name:        "shell",
			Description: "Run a shell command in the repository root (bash on macOS, go toolchain available, offline).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "the command to run"},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read a file from the repository (max 400 lines per call).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string", "description": "repository-relative path"},
					"start_line": map[string]any{"type": "integer", "description": "1-based first line (optional)"},
					"end_line":   map[string]any{"type": "integer", "description": "last line, inclusive (optional)"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "list_files",
			Description: "List the entries of one directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "repository-relative directory (default: root)"},
				},
			},
		},
		{
			Name:        "grep",
			Description: "Search file contents with a Go regular expression; returns file:line: text matches.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string", "description": "Go regexp"},
					"include": map[string]any{"type": "string", "description": "file-name glob, default *.go"},
					"path":    map[string]any{"type": "string", "description": "repository-relative search root (default: repo root)"},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

// Execute runs one tool call for real.
func (e *Environment) Execute(call llm.ToolCall) harness.ToolResult {
	switch call.Name {
	case "shell":
		command, _ := call.Args["command"].(string)
		if strings.TrimSpace(command) == "" {
			return harness.ToolResult{OK: false, Status: 2, Text: "shell: empty command", Cause: memory.CauseParameter}
		}
		return e.runShell(command)
	case "read_file":
		path, _ := call.Args["path"].(string)
		start, _ := call.Args["start_line"].(float64)
		end, _ := call.Args["end_line"].(float64)
		return e.readFile(path, int(start), int(end))
	case "list_files":
		path, _ := call.Args["path"].(string)
		return e.listFiles(path)
	case "grep":
		pattern, _ := call.Args["pattern"].(string)
		include, _ := call.Args["include"].(string)
		path, _ := call.Args["path"].(string)
		return e.grep(pattern, include, path)
	default:
		return harness.ToolResult{OK: false, Status: 2, Text: fmt.Sprintf("unknown tool %q", call.Name), Cause: memory.CauseParameter}
	}
}

func (e *Environment) runShell(command string) harness.ToolResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), e.shellTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = e.workdir
	cmd.Env = shellEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	text := capOutput(buf.String())
	if ctx.Err() == context.DeadlineExceeded {
		return harness.ToolResult{OK: false, Status: 124, Text: fmt.Sprintf("command timed out after %s\n%s", e.shellTimeout, text), Cause: memory.CauseTimeout}
	}
	status := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		status = exitErr.ExitCode()
	}
	ok := err == nil
	cause := ""
	if !ok {
		cause = ClassifyCause(text, status)
	}
	// Pipelines and trailing echo mask the go tool's exit code (`go test … |
	// tail; echo …` exits 0 even when tests fail), which silently disables
	// failure attribution. For go toolchain commands that the shell reports
	// as successful, fall back to output markers so a masked FAIL is still
	// classified as a failure.
	if ok && reGoTool.MatchString(command) {
		if c := ClassifyCause(text, 1); c != "" {
			ok = false
			cause = c
			status = 1
		}
	}
	if status == 0 && err != nil {
		status = 1
	}
	// Surface the exit code to the model so it does not invent `echo $?`
	// probes; classified above on the raw output so the marker cannot
	// perturb cause detection.
	text = strings.TrimRight(text, "\n") + fmt.Sprintf("\n[exit: %d]", status)
	return harness.ToolResult{OK: ok, Status: status, Text: text, Cause: cause}
}

func shellEnv() []string {
	env := os.Environ()
	env = append(env, "GOPROXY=off")
	found := false
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + goBinPath + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
			found = true
			break
		}
	}
	if !found {
		env = append(env, "PATH="+goBinPath)
	}
	return env
}

func (e *Environment) resolve(path string) (string, error) {
	clean := filepath.Clean("/" + strings.TrimPrefix(path, "/"))
	abs := filepath.Join(e.workdir, clean)
	if !strings.HasPrefix(abs, filepath.Clean(e.workdir)) {
		return "", fmt.Errorf("path escapes repository: %s", path)
	}
	return abs, nil
}

func (e *Environment) readFile(path string, start, end int) harness.ToolResult {
	if path == "" {
		return harness.ToolResult{OK: false, Status: 2, Text: "read_file: path required", Cause: memory.CauseParameter}
	}
	abs, err := e.resolve(path)
	if err != nil {
		return harness.ToolResult{OK: false, Status: 2, Text: err.Error(), Cause: memory.CauseParameter}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return harness.ToolResult{OK: false, Status: 1, Text: fmt.Sprintf("read_file: %v", err), Cause: memory.CauseNotFound}
	}
	lines := strings.Split(string(data), "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if end > start+maxReadLines-1 {
		end = start + maxReadLines - 1
	}
	if start > len(lines) {
		return harness.ToolResult{OK: true, Status: 0, Text: fmt.Sprintf("file has %d lines; requested range starts beyond EOF", len(lines))}
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		line := lines[i-1]
		if len(line) > maxLineLength {
			line = line[:maxLineLength] + "…"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", i, line)
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "…[%d more lines; use start_line=%d]\n", len(lines)-end, end+1)
	}
	return harness.ToolResult{OK: true, Status: 0, Text: strings.TrimRight(b.String(), "\n")}
}

func (e *Environment) listFiles(path string) harness.ToolResult {
	if path == "" {
		path = "."
	}
	abs, err := e.resolve(path)
	if err != nil {
		return harness.ToolResult{OK: false, Status: 2, Text: err.Error(), Cause: memory.CauseParameter}
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return harness.ToolResult{OK: false, Status: 1, Text: fmt.Sprintf("list_files: %v", err), Cause: memory.CauseNotFound}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > maxListEntries {
		names = append(names[:maxListEntries], fmt.Sprintf("…[%d more entries]", len(names)-maxListEntries))
	}
	rel := strings.TrimPrefix(path, "./")
	return harness.ToolResult{OK: true, Status: 0, Text: rel + "\n" + strings.Join(names, "\n")}
}

func (e *Environment) grep(pattern, include, path string) harness.ToolResult {
	if pattern == "" {
		return harness.ToolResult{OK: false, Status: 2, Text: "grep: pattern required", Cause: memory.CauseParameter}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return harness.ToolResult{OK: false, Status: 2, Text: fmt.Sprintf("grep: invalid pattern: %v", err), Cause: memory.CauseParameter}
	}
	if include == "" {
		include = "*.go"
	}
	if path == "" {
		path = "."
	}
	abs, err := e.resolve(path)
	if err != nil {
		return harness.ToolResult{OK: false, Status: 2, Text: err.Error(), Cause: memory.CauseParameter}
	}
	var matches []string
	total := 0
	filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if e.skipGitFolders[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if matched, _ := filepath.Match(include, d.Name()); !matched {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxGrepFileBytes {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(e.workdir, p)
		for i, line := range strings.Split(string(data), "\n") {
			if len(matches) >= maxGrepMatches {
				return nil
			}
			if re.MatchString(line) {
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 200 {
					trimmed = trimmed[:200] + "…"
				}
				matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, i+1, trimmed))
				total++
			}
		}
		return nil
	})
	if len(matches) == 0 {
		return harness.ToolResult{OK: true, Status: 0, Text: fmt.Sprintf("no matches for /%s/ in %s (%s)", pattern, path, include)}
	}
	text := strings.Join(matches, "\n")
	if total > len(matches) {
		text += fmt.Sprintf("\n…[%d matches total, showing first %d]", total, len(matches))
	}
	return harness.ToolResult{OK: true, Status: 0, Text: text}
}

// ---------------------------------------------------------------------------
// Cause classification (§8): a failed call must carry the reason it failed so
// outcome attribution can separate "the memory is wrong" from "the world had
// an unrelated problem".

var (
	reGoTool   = regexp.MustCompile(`\bgo (test|build|vet)\b`)
	reBuild    = regexp.MustCompile(`build failed|setup failed|# github\.com/|cannot use |undefined: |syntax error|vet: `)
	reTestFail = regexp.MustCompile(`--- FAIL: |(?m)^FAIL`)
	reNotFound = regexp.MustCompile(`command not found|no such file or directory|unknown primary or operator|invalid option|cannot find package|can't load package|matched no packages|no required module|does not exist`)
)

// ClassifyCause maps combined command output to a failure cause. Compile
// failures are checked before test failures because `go test` prints a FAIL
// line for both; a TestMain that exits non-zero (the 3B guard) carries no
// build markers and lands on test-fail, which is the semantics the memory
// attribution needs.
func ClassifyCause(output string, status int) string {
	if reBuild.MatchString(output) {
		return memory.CauseBuild
	}
	if reTestFail.MatchString(output) {
		return memory.CauseTestFail
	}
	if reNotFound.MatchString(output) {
		return memory.CauseNotFound
	}
	return ""
}

func capOutput(text string) string {
	if len(text) <= maxOutputBytes {
		return text
	}
	head := text[:7000]
	tail := text[len(text)-4000:]
	return head + "\n…[output truncated]…\n" + tail
}
