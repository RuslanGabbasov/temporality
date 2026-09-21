// Command aml-bench runs the Temporality adaptive-memory experiments on the
// synthetic ops environment. Experiment 2 fixtures:
//
//	main      — long horizon: arms A (no memory) and D (adaptive memory) over
//	            10 sessions × 5 tasks, with a prod auth flip at session 6 that
//	            invalidates learned billing-api knowledge;
//	conflict  — two valid conflicting memories (prod vs staging auth) that
//	            retrieval must separate by environment;
//	guardrail — a flaky service that tempts the agent into repeated identical
//	            failures, exercising the soft repeated-failure guardrail.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
	"github.com/temporality-project/temporality/aml/opsenv"
)

type taskMetric struct {
	Arm           string `json:"arm"`
	Session       int    `json:"session"`
	Epoch         int    `json:"epoch"`
	Task          string `json:"task"`
	Env           string `json:"env"`
	Success       bool   `json:"success"`
	Steps         int    `json:"steps"`
	ToolCalls     int    `json:"tool_calls"`
	FailedCalls   int    `json:"failed_calls"`
	RepeatedFail  int    `json:"repeated_failed"`
	FirstOKStep   int    `json:"first_ok_step"`
	PromptTok     int    `json:"prompt_tokens"`
	CompletionTok int    `json:"completion_tokens"`
	TotalTok      int    `json:"total_tokens"`
	WallMS        int64  `json:"wall_ms"`
	StoppedOn     string `json:"stopped_on"`
	Recalled      int    `json:"recalled"`
	HintsInjected int    `json:"hints_injected"`
	Acknowledged  int    `json:"acknowledged"`
	GuardrailHit  int    `json:"guardrail_hints"`
	Reused        int    `json:"reused"`
	Validated     int    `json:"validated"`
	Contradicted  int    `json:"contradicted"`
	Unresolved    int    `json:"unresolved"`
	Answer        string `json:"answer"`
}

type envAdapter struct{ world opsenv.World }

func (e envAdapter) Tools() []llm.ToolDef { return opsenv.ToolDefs() }

func (e envAdapter) Execute(call llm.ToolCall) harness.ToolResult {
	if call.Name == opsenv.ToolAPIDocs {
		service, _ := call.Args["service"].(string)
		return harness.ToolResult{OK: true, Status: 200, Text: opsenv.Docs(service)}
	}
	result := e.world.Execute(opsenv.ParseCall(call.Args))
	return harness.ToolResult{OK: result.OK(), Status: result.Status, Text: result.Text(), Cause: result.Cause}
}

func loadDotEnv(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

// fixture describes one experiment setup.
type fixture struct {
	name      string
	tasks     func() []opsenv.Task
	arms      string
	sessions  int
	flipAt    int
	maxSteps  int
	dbPrefix  string
	timeLimit time.Duration
}

func fixtureByName(name string) fixture {
	switch name {
	case "conflict":
		return fixture{name: "conflict", tasks: opsenv.ConflictTasks, arms: "B,D", sessions: 4, flipAt: 1,
			maxSteps: 10, dbPrefix: "aml2c", timeLimit: 30 * time.Minute}
	case "guardrail":
		return fixture{name: "guardrail", tasks: opsenv.GuardrailTasks, arms: "A,D", sessions: 3, flipAt: 0,
			maxSteps: 12, dbPrefix: "aml2g", timeLimit: 30 * time.Minute}
	default:
		return fixture{name: "main", tasks: opsenv.MainTasks, arms: "A,D", sessions: 10, flipAt: 6,
			maxSteps: 10, dbPrefix: "aml2", timeLimit: 60 * time.Minute}
	}
}

func main() {
	fixtureFlag := flag.String("fixture", "main", "main | conflict | guardrail")
	armsFlag := flag.String("arms", "", "comma-separated arms (default per fixture)")
	sessions := flag.Int("sessions", 0, "sessions per arm (default per fixture)")
	flipAt := flag.Int("flip-at", -1, "session where prod billing auth flips (default per fixture; 0 disables)")
	maxSteps := flag.Int("max-steps", 0, "model steps per task (default per fixture)")
	parallel := flag.Int("parallel", 2, "arms running concurrently")
	dbPrefix := flag.String("db-prefix", "", "per-arm database name prefix (default per fixture)")
	reportPath := flag.String("report", "", "markdown report path")
	dumpPath := flag.String("dump", "", "metrics JSON path")
	timeLimit := flag.Duration("time-limit", 0, "overall wall-clock limit (default per fixture)")
	taskFilter := flag.String("tasks", "", "comma-separated task ids (default all)")
	modelOverride := flag.String("model-id", "", "override the model id from env (second-model compliance runs)")
	flag.Parse()
	loadDotEnv(".env")

	fix := fixtureByName(*fixtureFlag)
	if *armsFlag != "" {
		fix.arms = *armsFlag
	}
	if *sessions > 0 {
		fix.sessions = *sessions
	}
	if *flipAt >= 0 {
		fix.flipAt = *flipAt
	}
	if *maxSteps > 0 {
		fix.maxSteps = *maxSteps
	}
	if *dbPrefix != "" {
		fix.dbPrefix = *dbPrefix
	}
	if *timeLimit > 0 {
		fix.timeLimit = *timeLimit
	}
	today := time.Now().Format("2006-01-02")
	if *reportPath == "" {
		*reportPath = fmt.Sprintf("docs/benchmarks/aml-%s-%s.md", fix.name, today)
	}
	if *dumpPath == "" {
		*dumpPath = fmt.Sprintf("benchmarks/aml-%s-%s.json", fix.name, time.Now().Format("2006-01-02-150405"))
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	modelCfg, err := llm.ConfigFromEnv()
	if err != nil {
		logger.Error("model config", "error", err)
		os.Exit(1)
	}
	if *modelOverride != "" {
		modelCfg.Model = *modelOverride
	}
	baseDSN := os.Getenv("DATABASE_URL")
	if baseDSN == "" {
		baseDSN = "postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable"
	}

	var arms []string
	for _, arm := range strings.Split(fix.arms, ",") {
		arm = strings.ToUpper(strings.TrimSpace(arm))
		if arm != "" {
			arms = append(arms, arm)
		}
	}
	tasks := fix.tasks()
	if *taskFilter != "" {
		wanted := map[string]bool{}
		for _, id := range strings.Split(*taskFilter, ",") {
			wanted[strings.TrimSpace(id)] = true
		}
		var filtered []opsenv.Task
		for _, task := range tasks {
			if wanted[task.ID] {
				filtered = append(filtered, task)
			}
		}
		tasks = filtered
	}

	logger.Info("experiment",
		"fixture", fix.name, "model", modelCfg.Model, "max_output_tokens", modelCfg.MaxOutputTokens,
		"temperature", modelCfg.Temperature, "reasoning", modelCfg.Reasoning,
		"arms", strings.Join(arms, ","), "sessions", fix.sessions, "tasks", len(tasks),
		"flip_at", fix.flipAt)

	ctx, cancel := context.WithTimeout(context.Background(), fix.timeLimit)
	defer cancel()

	var mu sync.Mutex
	metrics := []taskMetric{}

	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	for _, arm := range arms {
		arm := arm
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			runArm(ctx, arm, fix, tasks, baseDSN, modelCfg, logger, &mu, &metrics, *dumpPath)
		}()
	}
	wg.Wait()

	writeReport(*reportPath, *dumpPath, fix, arms, modelCfg, metrics, logger)
	logger.Info("done", "report", *reportPath, "dump", *dumpPath)
}

func runArm(ctx context.Context, arm string, fix fixture, tasks []opsenv.Task,
	baseDSN string, modelCfg llm.Config, logger *slog.Logger,
	mu *sync.Mutex, metrics *[]taskMetric, dumpPath string) {

	cfg, err := memory.ConfigForArm(arm)
	if err != nil {
		logger.Error("arm config", "arm", arm, "error", err)
		return
	}
	dbName := fmt.Sprintf("%s_arm_%s", fix.dbPrefix, strings.ToLower(arm))
	adminDSN := memory.DatabaseURL(baseDSN, "postgres")
	if err := memory.DropDatabase(ctx, adminDSN, dbName); err != nil {
		logger.Error("drop db", "db", dbName, "error", err)
		return
	}
	if err := memory.CreateDatabase(ctx, adminDSN, dbName); err != nil {
		logger.Error("create db", "db", dbName, "error", err)
		return
	}

	var layer *memory.Layer
	if arm != "A" {
		store, err := memory.OpenPg(ctx, memory.DatabaseURL(baseDSN, dbName))
		if err != nil {
			logger.Error("open store", "arm", arm, "error", err)
			return
		}
		defer store.Close()
		if err := memory.ApplyMigrations(ctx, store.Pool()); err != nil {
			logger.Error("migrate", "arm", arm, "error", err)
			return
		}
		layer = memory.NewLayer(store, cfg, opsenv.AllServices, logger)
	}

	client := llm.New(modelCfg)
	runner := &harness.Runner{Client: client, MaxSteps: fix.maxSteps}
	var hooks harness.Hooks
	if layer != nil {
		hooks = layer
	}

	for session := 1; session <= fix.sessions; session++ {
		world := opsenv.World{Session: session, FlipAt: fix.flipAt}
		env := envAdapter{world}
		sessionID := fmt.Sprintf("%s-s%d", arm, session)
		if layer != nil {
			layer.SetSession(sessionID, session)
		}
		for _, task := range tasks {
			if ctx.Err() != nil {
				logger.Warn("time limit reached; stopping arm", "arm", arm)
				return
			}
			taskEnv := task.Environment
			if taskEnv == "" {
				taskEnv = opsenv.EnvProd
			}
			if layer != nil {
				layer.SetTaskContext(taskEnv, world.VersionFor(task.Service, taskEnv))
			}
			check := func(answer string) bool { return harness.Check(answer, task.Markers) }
			result := runner.Run(ctx, env, hooks, sessionID, task.ID, task.Text, check)
			row := taskMetric{
				Arm: arm, Session: session, Epoch: world.Epoch(), Task: task.ID, Env: taskEnv,
				Success: result.Success, Steps: result.Steps, ToolCalls: result.ToolCalls,
				FailedCalls: result.FailedCalls, FirstOKStep: result.FirstOKStep,
				PromptTok: result.Usage.PromptTokens, CompletionTok: result.Usage.CompletionTokens,
				TotalTok: result.Usage.TotalTokens, WallMS: result.WallMS, StoppedOn: result.StoppedOn,
				Answer: firstLine(result.Answer, 200),
			}
			if layer != nil {
				stats := layer.Stats()
				row.Recalled = stats.Recalled
				row.HintsInjected = stats.HintsInjected
				row.Acknowledged = stats.Acknowledged
				row.GuardrailHit = stats.GuardrailHints
				row.Reused = stats.Reused
				row.Validated = stats.Validated
				row.Contradicted = stats.Contradicted
				row.Unresolved = stats.Unresolved
				row.RepeatedFail = stats.RepeatedFailedCall
			}
			logger.Info("task",
				"arm", arm, "session", session, "epoch", world.Epoch(), "task", task.ID, "env", taskEnv,
				"success", result.Success, "steps", result.Steps, "calls", result.ToolCalls,
				"failed", result.FailedCalls, "tokens", result.Usage.TotalTokens,
				"hints", row.HintsInjected, "reused", row.Reused, "validated", row.Validated,
				"contradicted", row.Contradicted, "stopped", result.StoppedOn)
			mu.Lock()
			*metrics = append(*metrics, row)
			writeDump(dumpPath, *metrics)
			mu.Unlock()
		}
	}
}

func firstLine(text string, limit int) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if len(line) > limit {
		return line[:limit] + "..."
	}
	return line
}

func writeDump(path string, metrics []taskMetric) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	raw, err := json.MarshalIndent(metrics, "", " ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

type armAggregate struct {
	Arm          string
	Tasks        int
	Success      int
	Steps        int
	ToolCalls    int
	Failed       int
	RepeatedFail int
	PromptTok    int
	CompletionTk int
	TotalTok     int
	WallMS       int64
	Recalled     int
	Hints        int
	Acknowledged int
	Guardrails   int
	Reused       int
	Validated    int
	Contradicted int
	Unresolved   int
}

func aggregate(metrics []taskMetric, filter func(taskMetric) bool) armAggregate {
	var agg armAggregate
	for _, m := range metrics {
		if !filter(m) {
			continue
		}
		agg.Arm = m.Arm
		agg.Tasks++
		if m.Success {
			agg.Success++
		}
		agg.Steps += m.Steps
		agg.ToolCalls += m.ToolCalls
		agg.Failed += m.FailedCalls
		agg.RepeatedFail += m.RepeatedFail
		agg.PromptTok += m.PromptTok
		agg.CompletionTk += m.CompletionTok
		agg.TotalTok += m.TotalTok
		agg.WallMS += m.WallMS
		agg.Recalled += m.Recalled
		agg.Hints += m.HintsInjected
		agg.Acknowledged += m.Acknowledged
		agg.Guardrails += m.GuardrailHit
		agg.Reused += m.Reused
		agg.Validated += m.Validated
		agg.Contradicted += m.Contradicted
		agg.Unresolved += m.Unresolved
	}
	return agg
}

func writeReport(path, dumpPath string, fix fixture, arms []string, modelCfg llm.Config, metrics []taskMetric, logger *slog.Logger) {
	var b strings.Builder
	fmt.Fprintf(&b, "# AML %s experiment — %s\n\n", fix.name, time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "Dump: `%s`\n\n", dumpPath)
	fmt.Fprintf(&b, "## Configuration\n\n")
	fmt.Fprintf(&b, "- model: `%s` (temperature %v, max_output_tokens %d, reasoning %q)\n", modelCfg.Model, modelCfg.Temperature, modelCfg.MaxOutputTokens, modelCfg.Reasoning)
	fmt.Fprintf(&b, "- fixture: %s; arms: %s; sessions: %d; tasks per session: %d; flip at session: %d\n", fix.name, strings.Join(arms, ", "), fix.sessions, len(uniqueTasks(metrics)), fix.flipAt)
	fmt.Fprintf(&b, "- arm mapping: A = no memory; B = semantic ranking only; C = semantic+temporal ranking; D = C + pre-action activation + guardrail + adaptive feedback with cause-level attribution\n\n")

	fmt.Fprintf(&b, "## Per-arm totals\n\n")
	fmt.Fprintf(&b, "| arm | tasks | success | steps | calls | failed | repeated | tokens | hints | reused | validated | contradicted | unresolved |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, arm := range arms {
		agg := aggregate(metrics, func(m taskMetric) bool { return m.Arm == arm })
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			arm, agg.Tasks, agg.Success, agg.Steps, agg.ToolCalls, agg.Failed, agg.RepeatedFail,
			agg.TotalTok, agg.Hints, agg.Reused, agg.Validated, agg.Contradicted, agg.Unresolved)
	}

	fmt.Fprintf(&b, "\n## Per-session detail\n\n")
	fmt.Fprintf(&b, "| arm | session | epoch | success | failed | repeated | steps | tokens | hints | reused | validated | contradicted |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, arm := range arms {
		for session := 1; session <= fix.sessions; session++ {
			agg := aggregate(metrics, func(m taskMetric) bool { return m.Arm == arm && m.Session == session })
			if agg.Tasks == 0 {
				continue
			}
			epoch := 1
			for _, m := range metrics {
				if m.Arm == arm && m.Session == session {
					epoch = m.Epoch
					break
				}
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d/%d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
				arm, session, epoch, agg.Success, agg.Tasks, agg.Failed, agg.RepeatedFail, agg.Steps, agg.TotalTok, agg.Hints, agg.Reused, agg.Validated, agg.Contradicted)
		}
	}

	if fix.flipAt > 0 && fix.flipAt <= fix.sessions {
		fmt.Fprintf(&b, "\n## Post-flip window (from session %d)\n\n", fix.flipAt)
		fmt.Fprintf(&b, "| arm | tasks | success | failed | repeated | contradicted | unresolved | tokens |\n")
		fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|---:|\n")
		for _, arm := range arms {
			agg := aggregate(metrics, func(m taskMetric) bool { return m.Arm == arm && m.Session >= fix.flipAt })
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d |\n", arm, agg.Tasks, agg.Success, agg.Failed, agg.RepeatedFail, agg.Contradicted, agg.Unresolved, agg.TotalTok)
		}
	}

	fmt.Fprintf(&b, "\n## Per-task answers\n\n")
	sorted := append([]taskMetric(nil), metrics...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Arm != sorted[j].Arm {
			return sorted[i].Arm < sorted[j].Arm
		}
		if sorted[i].Session != sorted[j].Session {
			return sorted[i].Session < sorted[j].Session
		}
		return sorted[i].Task < sorted[j].Task
	})
	fmt.Fprintf(&b, "| arm | session | task | env | ok | steps | failed | stopped | answer |\n")
	fmt.Fprintf(&b, "|---|---:|---|---|---|---:|---:|---|---|\n")
	for _, m := range sorted {
		ok := "no"
		if m.Success {
			ok = "yes"
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %d | %d | %s | %s |\n", m.Arm, m.Session, m.Task, m.Env, ok, m.Steps, m.FailedCalls, m.StoppedOn, m.Answer)
	}
	fmt.Fprintf(&b, "\n_Analysis to be filled after inspecting the run._\n")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Error("report dir", "error", err)
		return
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		logger.Error("write report", "error", err)
	}
}

func uniqueTasks(metrics []taskMetric) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range metrics {
		if !seen[m.Task] {
			seen[m.Task] = true
			out = append(out, m.Task)
		}
	}
	return out
}
