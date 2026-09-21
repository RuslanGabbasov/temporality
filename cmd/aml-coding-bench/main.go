// Command aml-coding-bench drives experiment 3: the adaptive memory layer as
// an HTTP sidecar over a real coding-agent trajectory on the bleve repository.
//
// Phases:
//
//	3A — stable repository, N sessions × 10 tasks, arms A (no memory) and D
//	     (adaptive memory): maturation and repeated-discovery avoidance;
//	3B — the repository evolves (analysis tests require BLEVE_ANALYSIS_TESTMODE=1):
//	     old memories must be contradicted and replaced;
//	3C — context conflict: the same task on branches main (evolved) and legacy
//	     (base): no cross-environment memory injection.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/aml/coding"
	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
	"github.com/temporality-project/temporality/aml/sidecar"
)

type unit struct {
	phase   string
	arm     string
	session int
	task    coding.Task
	branch  string
	run     int
}

type runRecord struct {
	Phase      string `json:"phase"`
	Arm        string `json:"arm"`
	Session    int    `json:"session"`
	SessionID  string `json:"session_id"`
	TaskID     string `json:"task_id"`
	Subsystem  string `json:"subsystem"`
	Branch     string `json:"branch"`
	Run        int    `json:"run"` // 3C repeat index
	Success    bool   `json:"success"`
	StoppedOn  string `json:"stopped_on"`
	Steps      int    `json:"steps"`
	ToolCalls  int    `json:"tool_calls"`
	FailedCall int    `json:"failed_calls"`
	FirstOKSte int    `json:"first_ok_step"`

	PromptTokens     int   `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	TotalTokens      int   `json:"total_tokens"`
	WallMS           int64 `json:"wall_ms"`

	FilesRead      int  `json:"files_read"`
	UniqueFiles    int  `json:"unique_files"`
	RepeatedReads  int  `json:"repeated_reads"`
	DiscoveryCalls int  `json:"discovery_calls"`
	TestCalls      int  `json:"test_calls"`
	FirstTestOK    bool `json:"first_test_ok"`

	Recalled     int `json:"recalled"`
	Injected     int `json:"injected"`
	Acknowledged int `json:"acknowledged"`
	Reused       int `json:"reused"`
	Validated    int `json:"validated"`
	Contradicted int `json:"contradicted"`
	Unresolved   int `json:"unresolved"`
	Guardrail    int `json:"guardrail"`

	AnswerSnippet string `json:"answer_snippet"`
}

func main() {
	phase := flag.String("phase", "all", "3a|3b|3c|all")
	arms := flag.String("arms", "A,D", "comma-separated arms")
	sessions3A := flag.Int("sessions-3a", 4, "3A sessions")
	sessions3B := flag.Int("sessions-3b", 2, "3B sessions (after evolution)")
	runs3C := flag.Int("runs-3c", 2, "3C repeats per context")
	repo := flag.String("repo", os.ExpandEnv("$HOME/Projects/temporality-bench/repos/bleve"), "pristine clone (pinned SHA)")
	work := flag.String("work", "/tmp/aml3", "work directory")
	dbName := flag.String("db", "aml3_memory", "memory database name")
	out := flag.String("out", "benchmarks/aml3", "output directory")
	sidecarPort := flag.Int("sidecar-port", 18190, "sidecar port")
	maxSteps := flag.Int("max-steps", 14, "max cognitive steps per task")
	taskTimeout := flag.Duration("task-timeout", 12*time.Minute, "per-task wall limit")
	timeLimit := flag.Duration("time-limit", 300*time.Minute, "global time limit")
	parallel := flag.Int("parallel", 1, "parallel tasks within a session")
	tasksFilter := flag.String("tasks", "", "comma-separated task IDs (smoke runs); empty = all")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	modelCfg, err := llm.ConfigFromEnv()
	if err != nil {
		fatal(err.Error())
	}
	baseDSN := os.Getenv("DATABASE_URL")
	if baseDSN == "" {
		baseDSN = "postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable"
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		fatal(err.Error())
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err.Error())
	}

	// Fresh memory database.
	ctx := context.Background()
	adminDSN := memory.DatabaseURL(baseDSN, "postgres")
	_ = memory.DropDatabase(ctx, adminDSN, *dbName)
	if err := memory.CreateDatabase(ctx, adminDSN, *dbName); err != nil {
		fatal("create db: " + err.Error())
	}
	dbURL := memory.DatabaseURL(baseDSN, *dbName)

	// Build + start sidecar.
	bin := filepath.Join(*work, "aml-sidecar")
	if outb, err := exec.Command("go", "build", "-o", bin, "./cmd/aml-sidecar").CombinedOutput(); err != nil {
		fatal("build sidecar: " + string(outb))
	}
	addr := fmt.Sprintf("127.0.0.1:%d", *sidecarPort)
	sidecarCmd := exec.Command(bin, "--dsn", dbURL, "--world", "coding", "--addr", addr)
	sidecarCmd.Stderr = os.Stderr
	if err := sidecarCmd.Start(); err != nil {
		fatal("start sidecar: " + err.Error())
	}
	defer func() {
		_ = sidecarCmd.Process.Kill()
		_ = sidecarCmd.Wait()
	}()
	if err := waitHealth(addr, 15*time.Second); err != nil {
		fatal("sidecar health: " + err.Error())
	}
	logger.Info("sidecar up", "addr", addr, "model", modelCfg.Model)

	// Workspaces per (arm, worker).
	armList := strings.Split(*arms, ",")
	wsBase := func(arm string, worker int) string { return filepath.Join(*work, fmt.Sprintf("ws-%s-%d", arm, worker)) }
	for _, arm := range armList {
		for w := 0; w < *parallel; w++ {
			if err := coding.PrepareWorkspace(*repo, wsBase(arm, w)); err != nil {
				fatal("workspace: " + err.Error())
			}
		}
	}

	llmClient := llm.New(modelCfg)
	deadline := time.Now().Add(*timeLimit)

	var units []unit

	tasks3A := coding.Tasks3A()
	tasks3B := coding.Tasks3B()
	task3C := coding.Task3C()
	if *tasksFilter != "" {
		want := strings.Split(*tasksFilter, ",")
		match := func(t coding.Task) bool {
			for _, prefix := range want {
				prefix = strings.TrimSpace(prefix)
				if prefix != "" && strings.HasPrefix(t.ID, prefix) {
					return true
				}
			}
			return false
		}
		var filtered []coding.Task
		for _, t := range tasks3A {
			if match(t) {
				filtered = append(filtered, t)
			}
		}
		tasks3A = filtered
		var filteredB []coding.Task
		for _, t := range tasks3B {
			if match(t) {
				filteredB = append(filteredB, t)
			}
		}
		tasks3B = filteredB
	}

	include := func(p string) bool { return *phase == "all" || *phase == p }

	if include("3a") {
		for s := 1; s <= *sessions3A; s++ {
			for _, t := range tasks3A {
				for _, arm := range armList {
					units = append(units, unit{phase: "3a", arm: arm, session: s, task: t, branch: "main"})
				}
			}
		}
	}
	if include("3b") {
		for s := 1; s <= *sessions3B; s++ {
			for _, t := range tasks3B {
				for _, arm := range armList {
					units = append(units, unit{phase: "3b", arm: arm, session: *sessions3A + s, task: t, branch: "main"})
				}
			}
		}
	}
	if include("3c") {
		for r := 1; r <= *runs3C; r++ {
			units = append(units, unit{phase: "3c", arm: "D", session: *sessions3A + *sessions3B + 1, task: task3C, branch: "legacy", run: r})
			units = append(units, unit{phase: "3c", arm: "D", session: *sessions3A + *sessions3B + 1, task: task3C, branch: "main", run: r})
			units = append(units, unit{phase: "3c", arm: "A", session: *sessions3A + *sessions3B + 1, task: task3C, branch: "main", run: r})
		}
	}

	jsonl, err := os.Create(filepath.Join(*out, "run.jsonl"))
	if err != nil {
		fatal(err.Error())
	}
	defer jsonl.Close()

	// Evolution is applied lazily before 3B/3C main work happens; for 3A the
	// workspaces stay on base main.
	evolveIfNeeded := func(arm string) error {
		for w := 0; w < *parallel; w++ {
			ws := wsBase(arm, w)
			if err := coding.CheckoutBranch(ws, "main", coding.BaseSHA); err != nil {
				return err
			}
			if err := coding.EnsureEvolved(ws); err != nil {
				return err
			}
		}
		return nil
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	workerFor := func(i int) int { return i % *parallel }

	// Group units by (phase, session) — barrier between sessions keeps memory
	// maturation semantics clean.
	groupKey := func(u unit) string { return fmt.Sprintf("%s-s%d", u.phase, u.session) }
	groups := map[string][]unit{}
	var order []string
	for _, u := range units {
		k := groupKey(u)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], u)
	}

	idx := 0
	for _, k := range order {
		if time.Now().After(deadline) {
			logger.Warn("global time limit reached, stopping", "remaining_groups", len(order)-len(strings.Split(k, ",")))
			break
		}
		// Ensure repo state for the group's phase.
		if strings.HasPrefix(k, "3b") || strings.HasPrefix(k, "3c") {
			for _, arm := range armList {
				if err := evolveIfNeeded(arm); err != nil {
					fatal("evolve: " + err.Error())
				}
			}
		}
		if strings.HasPrefix(k, "3c") {
			// legacy branch from base for D workspaces.
			for w := 0; w < *parallel; w++ {
				ws := wsBase("D", w)
				if err := coding.CheckoutBranch(ws, "legacy", coding.BaseSHA); err != nil {
					fatal("legacy branch: " + err.Error())
				}
			}
		}

		group := groups[k]
		logger.Info("session start", "group", k, "tasks", len(group))
		sem := make(chan struct{}, *parallel)
		for _, u := range group {
			wg.Add(1)
			sem <- struct{}{}
			go func(u unit, i int) {
				defer wg.Done()
				defer func() { <-sem }()
				if time.Now().After(deadline) {
					return
				}
				rec := runUnit(ctx, runParams{
					client: llmClient, addr: addr, u: u,
					ws: wsBase(u.arm, workerFor(i)), maxSteps: *maxSteps,
					taskTimeout: *taskTimeout, logger: logger,
				})
				mu.Lock()
				line, _ := json.Marshal(rec)
				fmt.Fprintln(jsonl, string(line))
				jsonl.Sync()
				mu.Unlock()
				logger.Info("run done",
					"phase", rec.Phase, "arm", rec.Arm, "session", rec.Session, "task", rec.TaskID, "branch", rec.Branch,
					"success", rec.Success, "steps", rec.Steps, "calls", rec.ToolCalls,
					"tokens", rec.TotalTokens, "reused", rec.Reused, "contradicted", rec.Contradicted,
					"wall_s", rec.WallMS/1000)
			}(u, idx)
			idx++
		}
		wg.Wait()
	}

	logger.Info("all groups finished", "out", *out)
}

type runParams struct {
	client      *llm.Client
	addr        string
	u           unit
	ws          string
	maxSteps    int
	taskTimeout time.Duration
	logger      *slog.Logger
}

func runUnit(ctx context.Context, p runParams) runRecord {
	u := p.u
	sessionID := fmt.Sprintf("%s-%s-s%d", u.phase, u.arm, u.session)

	// Deterministic workspace state.
	if err := coding.ResetWorkspace(p.ws); err != nil {
		p.logger.Warn("reset workspace", "error", err)
	}
	branch := u.branch
	if branch == "" {
		branch = "main"
	}
	if err := coding.CheckoutBranch(p.ws, branch, coding.BaseSHA); err != nil {
		p.logger.Warn("checkout branch", "error", err, "branch", branch)
	}
	if branch == "main" && (u.phase == "3b" || u.phase == "3c") {
		if err := coding.EnsureEvolved(p.ws); err != nil {
			p.logger.Warn("ensure evolved", "error", err)
		}
	}

	env := coding.NewEnvironment(p.ws)
	runner := harness.Runner{Client: p.client, MaxSteps: p.maxSteps, SystemPrompt: coding.SystemPrompt}

	var hooks harness.Hooks
	var client *sidecar.Client
	if u.arm == "D" {
		client = sidecar.NewClient("http://"+p.addr, sessionID, branch, coding.RepoName, branch)
		hooks = client
	}

	taskCtx, cancel := context.WithTimeout(ctx, p.taskTimeout)
	defer cancel()

	taskRunID := u.task.ID
	if u.run > 0 {
		taskRunID = fmt.Sprintf("%s-r%d", u.task.ID, u.run)
	}
	// 3C runs the same task on two branches concurrently; the sidecar keys
	// task state by task_id, so the branch must be part of the id or the
	// legacy and main layers overwrite each other.
	if u.phase == "3c" {
		taskRunID += "-" + branch
	}
	res := runner.Run(taskCtx, env, hooks, sessionID, taskRunID, u.task.Text, func(answer string) bool {
		return harness.Check(answer, u.task.Markers)
	})

	rec := runRecord{
		Phase: u.phase, Arm: u.arm, Session: u.session, SessionID: sessionID,
		TaskID: taskRunID, Subsystem: u.task.Subsystem, Branch: branch, Run: u.run,
		Success: res.Success, StoppedOn: res.StoppedOn, Steps: res.Steps,
		ToolCalls: res.ToolCalls, FailedCall: res.FailedCalls, FirstOKSte: res.FirstOKStep,
		PromptTokens: res.Usage.PromptTokens, CompletionTokens: res.Usage.CompletionTokens,
		TotalTokens: res.Usage.TotalTokens, WallMS: res.WallMS,
		AnswerSnippet: snippet(res.Answer, 300),
	}

	// Trace-derived discovery metrics.
	fileCount := map[string]int{}
	var order []string
	testCalls, firstTestOK := 0, false
	totalCalls := 0
	callsBeforeFirstOKTest := -1
	for _, step := range res.Trace {
		for i, call := range step.ToolCalls {
			totalCalls++
			status := 0
			if i < len(step.ResultStatus) {
				status = step.ResultStatus[i]
			}
			switch call.Name {
			case "read_file":
				path, _ := call.Args["path"].(string)
				if path != "" {
					if fileCount[path] == 0 {
						order = append(order, path)
					}
					fileCount[path]++
				}
			case "shell":
				command, _ := call.Args["command"].(string)
				if strings.Contains(command, "go test") {
					testCalls++
					if status == 0 && !firstTestOK {
						firstTestOK = true
						callsBeforeFirstOKTest = totalCalls - 1
					}
				}
			}
		}
	}
	rec.FilesRead = 0
	for _, n := range fileCount {
		rec.FilesRead += n
	}
	rec.UniqueFiles = len(order)
	rec.RepeatedReads = rec.FilesRead - rec.UniqueFiles
	rec.TestCalls = testCalls
	rec.FirstTestOK = firstTestOK
	if callsBeforeFirstOKTest >= 0 {
		rec.DiscoveryCalls = callsBeforeFirstOKTest
	} else {
		rec.DiscoveryCalls = totalCalls
	}

	if client != nil {
		if stats, ok := client.Stats(); ok {
			rec.Recalled = stats.Recalled
			rec.Injected = stats.HintsInjected
			rec.Acknowledged = stats.Acknowledged
			rec.Reused = stats.Reused
			rec.Validated = stats.Validated
			rec.Contradicted = stats.Contradicted
			rec.Unresolved = stats.Unresolved
			rec.Guardrail = stats.GuardrailHints
		}
	}
	return rec
}

func snippet(text string, limit int) string {
	clean := strings.Join(strings.Fields(text), " ")
	if len(clean) > limit {
		return clean[:limit] + "…"
	}
	return clean
}

func waitHealth(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("sidecar not healthy after %s", timeout)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "aml-coding-bench: "+msg)
	os.Exit(1)
}
