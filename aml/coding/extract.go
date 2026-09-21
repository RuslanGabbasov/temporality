package coding

import (
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/aml/memory"
)

// Extractor builds the coding-world experience extractor. It is deliberately
// deterministic and narrow (no LLM summarizer):
//
//   - command workaround: a failing shell command followed by a different
//     successful one → "X fails, use Y" (0.70);
//   - test command: a successful `go test` command → convention asset at the
//     what-worked baseline (0.40), matured by independent sessions (§18.3);
//   - navigation: the most-read file of a successful task → where the answer
//     lives (0.40).
func Extractor(repoName string, lexicon []string) memory.ExtractFunc {
	return func(log []memory.CallRecord, opt memory.ExtractOptions) []*memory.Asset {
		// Task-level success does not gate command experience: a successful
		// `go test` or a fail→success workaround is validated evidence on its
		// own, even when the task then fails on answer compliance (the marker
		// check). Discarding it starved memory exactly for tasks whose answers
		// format poorly. Navigation still requires task success: the claim
		// "the key file is X" is only justified by a solved task.
		if len(log) == 0 {
			return nil
		}
		var assets []*memory.Asset
		seen := map[string]bool{}
		add := func(a *memory.Asset) {
			if a == nil || seen[a.DedupKey] {
				return
			}
			seen[a.DedupKey] = true
			assets = append(assets, a)
		}

		var shells []memory.CallRecord
		for _, r := range log {
			if r.Tool == "shell" && commandOf(r) != "" {
				shells = append(shells, r)
			}
		}

		// Failure→success workarounds (max 2 per task).
		workarounds := 0
		for i := 0; i < len(shells); i++ {
			if shells[i].OK {
				continue
			}
			runEnd := i
			for runEnd+1 < len(shells) && !shells[runEnd+1].OK {
				runEnd++
			}
			if runEnd+1 >= len(shells) {
				break // never resolved in this task
			}
			if workarounds < 2 {
				add(workaroundAsset(repoName, lexicon, shells[runEnd], shells[runEnd+1], opt))
				workarounds++
			}
			i = runEnd + 1
		}

		// Successful test commands (max 2 per task) at the §18.3 baseline.
		testCommands := 0
		for _, r := range shells {
			if r.OK && isTestCommand(commandOf(r)) && testCommands < 2 {
				add(testCommandAsset(repoName, lexicon, r, opt))
				testCommands++
			}
		}

		// Navigation: the most-read file of a successful task.
		if path, ok := topReadFile(log); ok && opt.Success {
			add(navAsset(repoName, lexicon, path, opt))
		}
		return assets
	}
}

func commandOf(r memory.CallRecord) string {
	cmd, _ := r.Params["command"].(string)
	return normalizeCommand(cmd)
}

func normalizeCommand(cmd string) string {
	return strings.Join(strings.Fields(cmd), " ")
}

func isTestCommand(cmd string) bool {
	return strings.Contains(cmd, "go test")
}

func scopeOf(text, fallback string, lexicon []string) string {
	if svc := InferScope(text, lexicon); svc != "" {
		return svc
	}
	return fallback
}

func workaroundAsset(repoName string, lexicon []string, failure, success memory.CallRecord, opt memory.ExtractOptions) *memory.Asset {
	failCmd := commandOf(failure)
	okCmd := commandOf(success)
	if failCmd == "" || okCmd == "" || failCmd == okCmd || len(okCmd) > 120 {
		return nil
	}
	service := scopeOf(okCmd+" "+failCmd, opt.Service, lexicon)
	return &memory.Asset{
		DedupKey:    memory.ComputeDedupKey(opt.Environment, service, "", memory.KindCommandWorkaround, "command", okCmd),
		Service:     service,
		Problem:     "command",
		Kind:        memory.KindCommandWorkaround,
		Proposition: fmt.Sprintf("in %s (%s): `%s` failed (%s); use `%s` instead", repoName, service, truncate(failCmd, 80), causeLabel(failure.Cause), okCmd),
		Recommendation: memory.Recommendation{
			Service: service, Param: "command", Value: okCmd, ValueType: "string",
			Avoid: truncate(failCmd, 80), AvoidStatus: failure.Status,
		},
		Evidence: []memory.Evidence{
			evidenceOf(opt.SessionID, opt.TaskID, failure),
			evidenceOf(opt.SessionID, opt.TaskID, success),
		},
		Confidence:     0.70,
		Status:         memory.StatusActive,
		CreatedAt:      timeNowUTC(),
		Environment:    opt.Environment,
		VersionContext: opt.Version,
	}
}

func testCommandAsset(repoName string, lexicon []string, r memory.CallRecord, opt memory.ExtractOptions) *memory.Asset {
	cmd := commandOf(r)
	if cmd == "" || len(cmd) > 120 {
		return nil
	}
	service := scopeOf(cmd, opt.Service, lexicon)
	return &memory.Asset{
		DedupKey:    memory.ComputeDedupKey(opt.Environment, service, "", memory.KindTestCommand, "command", cmd),
		Service:     service,
		Problem:     "tests",
		Kind:        memory.KindTestCommand,
		Proposition: fmt.Sprintf("tests for %s in %s pass with: %s", service, repoName, cmd),
		Recommendation: memory.Recommendation{
			Service: service, Param: "command", Value: cmd, ValueType: "string",
		},
		Evidence: []memory.Evidence{
			evidenceOf(opt.SessionID, opt.TaskID, r),
		},
		Confidence:     memory.WhatWorkedBaseline,
		Status:         memory.StatusActive,
		CreatedAt:      timeNowUTC(),
		Environment:    opt.Environment,
		VersionContext: opt.Version,
	}
}

func navAsset(repoName string, lexicon []string, path string, opt memory.ExtractOptions) *memory.Asset {
	service := scopeOf(path, opt.Service, lexicon)
	return &memory.Asset{
		DedupKey:    memory.ComputeDedupKey(opt.Environment, service, opt.TaskID, memory.KindNav, "path", path),
		Service:     service,
		Problem:     "navigation",
		Kind:        memory.KindNav,
		Proposition: fmt.Sprintf("when investigating %q in %s, the key file is %s", taskShort(opt.TaskText), repoName, path),
		Recommendation: memory.Recommendation{
			Service: service, Resource: opt.TaskID, Param: "path", Value: path, ValueType: "string",
		},
		Evidence:       []memory.Evidence{},
		Confidence:     memory.WhatWorkedBaseline,
		Status:         memory.StatusActive,
		CreatedAt:      timeNowUTC(),
		Environment:    opt.Environment,
		VersionContext: opt.Version,
	}
}

// topReadFile returns the most often read_file'd repository path (ties broken
// by first read).
func topReadFile(log []memory.CallRecord) (string, bool) {
	counts := map[string]int{}
	firstSeq := map[string]int{}
	for _, r := range log {
		if r.Tool != "read_file" {
			continue
		}
		path, _ := r.Params["path"].(string)
		if path == "" || strings.Contains(path, "..") {
			continue
		}
		counts[path]++
		if _, ok := firstSeq[path]; !ok {
			firstSeq[path] = r.Seq
		}
	}
	best, bestCount := "", 0
	for path, n := range counts {
		if n > bestCount || (n == bestCount && best != "" && firstSeq[path] < firstSeq[best]) {
			best, bestCount = path, n
		}
	}
	return best, bestCount > 0
}

func evidenceOf(sessionID, taskID string, r memory.CallRecord) memory.Evidence {
	return memory.Evidence{
		SessionID: sessionID,
		TaskID:    taskID,
		Seq:       r.Seq,
		Tool:      r.Tool,
		Status:    r.Status,
		Summary:   r.Summary,
	}
}

func causeLabel(cause string) string {
	if cause == "" {
		return "unknown error"
	}
	return cause
}

func taskShort(text string) string {
	words := strings.Fields(text)
	if len(words) > 10 {
		words = words[:10]
	}
	return strings.Join(words, " ")
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func timeNowUTC() time.Time { return time.Now().UTC() }
