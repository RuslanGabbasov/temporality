// loadgen — the load-scenario driver for docs/resilience-and-scale.md §НТ.
// Creates a project/agent/task trio against a running kernel, starts N runs
// spread over a ramp, polls each to its terminal state and prints a
// percentile report (start latency, run duration, status distribution).
//
// The model side must point at the mock provider (examples/mockllm), so the
// measured latencies belong to the harness, not to a real LLM:
//
//	TEMPORALITY_MODEL_BASE_URL=http://mockllm:8081/v1
//	TEMPORALITY_MODEL_ID=loadgen-model
//
// Example (50 runs, ramped over 60s):
//
//	go run ./examples/loadgen -kernel http://localhost:8090 -runs 50 -ramp 60s -report report.md
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	kernel    = flag.String("kernel", "http://localhost:8090", "kernel API base URL")
	token     = flag.String("token", "", "bearer token (empty = anonymous fresh install)")
	project   = flag.String("project", "loadgen", "project id (created when missing)")
	agent     = flag.String("agent", "loadgen-agent", "agent id (created when missing)")
	model     = flag.String("model", "loadgen-model", "model id the load agent uses")
	runs      = flag.Int("runs", 50, "total runs to start")
	ramp      = flag.Duration("ramp", 60*time.Second, "spread the starts over this duration")
	pollEvery = flag.Duration("poll", 1*time.Second, "run-status poll interval")
	perRunTO  = flag.Duration("timeout", 15*time.Minute, "give up on a run after this long")
	// HITL profile: when the mock answers ask_human (see -ask-human-after-tools),
	// the driver plays the human — waits humanDelay after the question event,
	// then signals the approval with a response. Delay is what makes the HWT
	// numbers in the report realistic (queue wait, not reflex).
	askHuman   = flag.Bool("hitl", false, "answer ask_human pauses (use with mockllm -ask-human-after-tools)")
	humanDelay = flag.Duration("human-delay", 5*time.Second, "simulated human think time before answering")
	report     = flag.String("report", "", "write a markdown report to this path (in addition to stdout)")
	journal    = flag.String("journal", "http://localhost:8080", "journal API base URL (human.requested lookup)")
	// Cleanup (docs/resilience-and-scale.md §НТ): delete every task created by
	// this run after the report. The cascade removes the runs; the journal is
	// append-only, so the loadgen project's events stay as an archive of the
	// profile — acceptable by design.
	cleanup = flag.Bool("cleanup", true, "delete the created tasks after the report")
)

type api struct {
	base   string
	token  string
	client *http.Client
}

func (a api) call(method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

type runResult struct {
	id       string
	startMS  int64 // start-API latency
	totalMS  int64 // start → terminal
	status   string
	terminal string
	hits     int   // answered HITL pauses
	hwtMS    int64 // summed question→answer wait
}

func main() {
	flag.Parse()
	a := api{base: strings.TrimRight(*kernel, "/"), token: *token, client: &http.Client{Timeout: 30 * time.Second}}
	a.ensureSetup()

	started := time.Now()
	var (
		mu       sync.Mutex
		results  []runResult
		failures atomic.Int64
		tasksMu  sync.Mutex
		tasks    []string // every task created by this run — for -cleanup
	)
	var wg sync.WaitGroup

	step := time.Duration(0)
	if *runs > 1 {
		step = *ramp / time.Duration(*runs-1)
	}
	for i := 0; i < *runs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			time.Sleep(step * time.Duration(idx))

			// A task per run: the run's conversation transcript lives on the
			// task, so runs never share journal scope.
			tStart := time.Now()
			code, body, _ := a.call(http.MethodPost, "/v1/workspace/tasks", map[string]any{
				"project_id": *project, "agent_id": *agent,
				"title":  fmt.Sprintf("loadgen %d", idx),
				"prompt": fmt.Sprintf("Load profile run %d: run the two scripted steps and finish.", idx),
			})
			if code != 201 && code != 200 {
				failures.Add(1)
				mu.Lock()
				results = append(results, runResult{id: fmt.Sprintf("task-%d", idx), startMS: time.Since(tStart).Milliseconds(), status: fmt.Sprintf("task_create_failed(%d)", code), terminal: truncate(body)})
				mu.Unlock()
				return
			}
			var task struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(body, &task)
			if task.ID != "" {
				tasksMu.Lock()
				tasks = append(tasks, task.ID)
				tasksMu.Unlock()
			}
			startMS := time.Since(tStart).Milliseconds()

			code, body, _ = a.call(http.MethodPost, "/v1/workspace/tasks/"+task.ID+"/runs", map[string]any{
				"agent_id": *agent, "model": *model, "max_turns": 3,
			})
			if code != 202 && code != 200 {
				failures.Add(1)
				mu.Lock()
				results = append(results, runResult{id: task.ID, startMS: startMS, status: fmt.Sprintf("run_start_failed(%d)", code), terminal: truncate(body)})
				mu.Unlock()
				return
			}
			var startedRun struct {
				RunID string `json:"run_id"`
			}
			_ = json.Unmarshal(body, &startedRun)

			// Poll to terminal; the status endpoint enriches with the live
			// Temporal status, so a run that died silently still settles.
			// With the HITL profile on, human.requested events found in the
			// run's journal are answered after the simulated think time.
			deadline := time.Now().Add(*perRunTO)
			answered := map[string]bool{}
			hits, hwtMS := 0, int64(0)
			for time.Now().Before(deadline) {
				time.Sleep(*pollEvery)
				if *askHuman {
					if asked, ok := a.findOpenQuestion(startedRun.RunID, answered); ok {
						time.Sleep(*humanDelay)
						if a.answerQuestion(startedRun.RunID, *project, asked) == nil {
							hits++
							hwtMS += time.Since(asked.raisedAt).Milliseconds()
							answered[asked.operationID] = true
						}
					}
				}
				code, body, err := a.call(http.MethodGet, "/v1/workspace/runs/"+startedRun.RunID+"?project="+url.QueryEscape(*project), nil)
				if code != 200 || err != nil {
					continue // transient (restart window, journal lag)
				}
				var run struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(body, &run) != nil {
					continue
				}
				switch run.Status {
				case "completed", "failed", "cancelled":
					mu.Lock()
					results = append(results, runResult{
						id: task.ID, startMS: startMS,
						totalMS: time.Since(tStart).Milliseconds(), status: run.Status,
						hits: hits, hwtMS: hwtMS,
					})
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			results = append(results, runResult{id: task.ID, startMS: startMS, totalMS: time.Since(tStart).Milliseconds(), status: "timeout", hits: hits, hwtMS: hwtMS})
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(started)

	durations, startLat, done, statuses, hits, hwt := collect(results)
	fmt.Printf("\n=== Load report (%d runs, ramp %s) ===\n", *runs, ramp)
	fmt.Printf("wall clock: %s · start failures: %d · terminal: %d/%d\n", elapsed.Round(time.Second), failures.Load(), done, *runs)
	printTable("run duration (start → terminal)", durations)
	printTable("start-API latency", startLat)
	if hits > 0 {
		avg := int64(0)
		if hits > 0 && hwt > 0 {
			avg = hwt / int64(hits)
		}
		fmt.Printf("%-32s %d пауз · средняя пауза %d ms\n", "HITL (human-in-the-loop)", hits, avg)
	}
	printStatuses(statuses)
	if *report != "" {
		writeReport(*report, *runs, *ramp, elapsed, failures.Load(), durations, startLat, statuses, done, hits, hwt)
		fmt.Printf("report written to %s\n", *report)
	}
	if failures.Load() > 0 || done < *runs {
		if *cleanup {
			cleanupTasks(a, tasks)
		}
		os.Exit(1)
	}
	if *cleanup {
		cleanupTasks(a, tasks)
	}
}

// cleanupTasks deletes the tasks this run created; workspace_run rows go
// with them by cascade. The journal stays append-only by design.
func cleanupTasks(a api, tasks []string) {
	deleted, failed := 0, 0
	for _, id := range tasks {
		code, _, err := a.call(http.MethodDelete, "/v1/workspace/tasks/"+url.PathEscape(id), nil)
		if err != nil || code == 200 {
			deleted++
		} else {
			failed++
		}
	}
	fmt.Printf("cleanup: tasks deleted %d, failed %d\n", deleted, failed)
}

func collect(results []runResult) (durations, startLat []int64, done int, statuses map[string]int, hits int, hwt int64) {
	statuses = map[string]int{}
	for _, r := range results {
		startLat = append(startLat, r.startMS)
		if r.totalMS > 0 {
			durations = append(durations, r.totalMS)
		}
		hits += r.hits
		hwt += r.hwtMS
		statuses[r.status]++
		switch r.status {
		case "completed", "failed", "cancelled":
			done++
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	sort.Slice(startLat, func(i, j int) bool { return startLat[i] < startLat[j] })
	return
}

func printTable(title string, samples []int64) {
	if len(samples) == 0 {
		fmt.Printf("%-32s — no samples\n", title)
		return
	}
	pct := func(p int) int64 { return samples[p*len(samples)/100] }
	fmt.Printf("%-32s p50 %6d ms · p95 %6d ms · p99 %6d ms · max %6d ms (n=%d)\n",
		title, pct(50), pct(95), pct(99), samples[len(samples)-1], len(samples))
}

func printStatuses(statuses map[string]int) {
	keys := make([]string, 0, len(statuses))
	for k := range statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, statuses[k]))
	}
	fmt.Printf("statuses: %s\n", strings.Join(parts, " "))
}

func writeReport(path string, runs int, ramp time.Duration, elapsed time.Duration, failures int64, durations, startLat []int64, statuses map[string]int, done int, hits int, hwtMS int64) {
	var b strings.Builder
	fmt.Fprintf(&b, "## Нагрузочный прогон\n\n- ранов: %d, ramp: %s, wall clock: %s\n- сбоев старта: %d, завершившихся: %d/%d\n\n| Метрика | p50 | p95 | p99 | max |\n|---|---|---|---|---|\n", runs, ramp, elapsed.Round(time.Second), failures, done, runs)
	row := func(title string, s []int64) {
		if len(s) == 0 {
			fmt.Fprintf(&b, "| %s | — | — | — | — |\n", title)
			return
		}
		pct := func(p int) int64 { return s[p*len(s)/100] }
		fmt.Fprintf(&b, "| %s | %d ms | %d ms | %d ms | %d ms |\n", title, pct(50), pct(95), pct(99), s[len(s)-1])
	}
	row("Длительность рана", durations)
	row("Латентность старта (API)", startLat)
	if hits > 0 {
		fmt.Fprintf(&b, "\nHITL: отвечено пауз %d, средняя пауза (HWT): %d ms\n", hits, hwtMS/int64(hits))
	}
	keys := make([]string, 0, len(statuses))
	for k := range statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&b, "\nСтатусы: ")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%d ", k, statuses[k])
	}
	b.WriteString("\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
	}
}

// openQuestion is one unanswered ask_human of a run, as seen in the journal.
type openQuestion struct {
	operationID   string
	argumentsHash string
	raisedAt      time.Time
}

// findOpenQuestion returns the run's newest human.requested event that the
// driver has not answered yet. Answers ride the approval signal with the
// operation id + arguments hash from that event.
func (a api) findOpenQuestion(runID string, answered map[string]bool) (openQuestion, bool) {
	req, err := http.NewRequest(http.MethodGet,
		strings.TrimRight(*journal, "/")+"/v1/observations/events?run="+url.QueryEscape(runID)+"&type=human.requested&limit=20", nil)
	if err != nil {
		return openQuestion{}, false
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return openQuestion{}, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || err != nil {
		return openQuestion{}, false
	}
	var page struct {
		Events []struct {
			OccurredAt time.Time      `json:"occurred_at"`
			Data       map[string]any `json:"data"`
		} `json:"events"`
	}
	if json.Unmarshal(body, &page) != nil {
		return openQuestion{}, false
	}
	for _, e := range page.Events {
		op, _ := e.Data["operation_id"].(string)
		hash, _ := e.Data["arguments_hash"].(string)
		if op == "" || answered[op] {
			continue
		}
		fmt.Fprintf(os.Stderr, "loadgen: HITL question found (run %s, op %s)\n", runID, op[:min2(len(op), 40)])
		return openQuestion{operationID: op, argumentsHash: hash, raisedAt: e.OccurredAt}, true
	}
	return openQuestion{}, false
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// answerQuestion signals the approval with a response — the same move the
// UI's question card makes (docs/triggers-and-escalations.md §5-13).
func (a api) answerQuestion(runID, project string, q openQuestion) error {
	code, body, _ := a.call(http.MethodPost,
		"/v1/agent/runs/"+url.PathEscape(runID)+"/approval?project="+url.QueryEscape(project),
		map[string]any{
			"operation_id": q.operationID, "arguments_hash": q.argumentsHash,
			"approved": true, "response": "loadgen: approved", "actor_id": "loadgen-human",
		})
	if code != 200 {
		return fmt.Errorf("approval %d: %s", code, truncate(body))
	}
	return nil
}

// ensureSetup creates the project and the load agent when missing. Both are
// idempotent: an existing id answers 409/200, which the runner treats as OK.
func (a api) ensureSetup() {
	code, _, err := a.call(http.MethodPost, "/v1/workspace/projects", map[string]any{"id": *project, "name": "LoadGen"})
	if err != nil {
		fmt.Printf("project create: %v\n", err)
	} else if code == 201 || code == 200 {
		fmt.Printf("project %s created\n", *project)
	} else {
		fmt.Printf("project %s exists or rejected (code %d)\n", *project, code)
	}
	code, body, err := a.call(http.MethodPost, "/v1/workspace/agents", map[string]any{
		"id": *agent, "name": "LoadGen Agent", "model": *model,
		"system_prompt": "You are a load-test fixture: run the two scripted commands and finish.",
		"max_turns":     3,
	})
	if err != nil {
		fmt.Printf("agent create: %v\n", err)
	} else if code == 201 || code == 200 {
		fmt.Printf("agent %s created\n", *agent)
	} else {
		fmt.Printf("agent %s exists or rejected (code %d): %s\n", *agent, code, truncate(body))
	}
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
