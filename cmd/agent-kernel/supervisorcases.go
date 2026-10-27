package main

// Supervisor cases pass (docs/supervisor-model.md §3, waves B and C): one
// scheduled pass that feeds the wave-A surface — journal events in, a
// supervisor.verdict with `case`+`subject` out — for the knowledge waves
// (B1 contradiction triage, B4 hygiene), the friction waves (C1 user error
// patterns, C2 intake classification audit) and C4 skill mining. B2 contract
// proposals file into the agent-proposals store; B3 narratives and C5
// conflict predictions ride the quality schedule and the embedding backfill
// respectively. Each case dedups per subject and is capped per pass so a
// backlog cannot burn the model budget. The whole pass honors
// KERNEL_SUPERVISOR_CASES=off.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

const (
	// One pass touches at most this many subjects per case: a first boot over
	// a months-old journal must not spend an hour of model time.
	supervisorCasePassCap = 2
	// One verdict per subject per case; re-audits happen only after this
	// window (or after the human resolves and the pattern recurs).
	supervisorCaseDedupWindow = 7 * 24 * time.Hour
)

// supervisorCaseStore is the workspace surface the cases need (fakes in tests).
type supervisorCaseStore interface {
	ListAllAgents(ctx context.Context) ([]workspace.Agent, error)
	ListSkillsVisible(ctx context.Context, units []string) ([]workspace.Skill, error)
	CreateAgentProposal(ctx context.Context, p *workspace.AgentProposal) error
	ListAgentEvaluationRuns(ctx context.Context, agentID string, limit int) ([]workspace.AgentEvaluationRun, error)
	ListRunsByAgent(ctx context.Context, agentID string, limit int) ([]workspace.Run, error)
	GetAgentEvaluationSuite(ctx context.Context, agentID string) (workspace.AgentEvaluationSuite, error)
	SaveAgentEvaluationSuite(ctx context.Context, agentID string, cases []workspace.EvaluationCase) error
}

func supervisorCasesEnabled() bool {
	return strings.TrimSpace(strings.ToLower(env("KERNEL_SUPERVISOR_CASES", "on"))) != "off"
}

// runSupervisorCasesPass executes one pass over the B/C cases. Model failures
// and journal hiccups skip their case quietly — the next pass retries.
func runSupervisorCasesPass(ctx context.Context, log *slog.Logger, activities *agent.Activities, ws supervisorCaseStore, observationURL string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	// Token read per pass (see the triage pass note).
	journalToken := os.Getenv("TEMPORALITY_API_TOKEN")
	if !supervisorCasesEnabled() {
		return
	}
	verdicts, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "supervisor.verdict", 200)
	if err != nil {
		log.Warn("supervisor cases: list verdicts", "error", err.Error())
		return
	}
	done := caseDoneIndex(verdicts)
	client, err := modelClient(ctx)
	if err != nil {
		client = nil // cases degrade to their deterministic fallbacks
	}
	triageKnowledgeCases(ctx, log, activities, observationURL, journalToken, client, done)
	proposeContractsFromClusters(ctx, log, activities, ws, observationURL, journalToken, client, done)
	mineSkillCandidates(ctx, log, activities, ws, observationURL, journalToken, client, done)
	detectUserFriction(ctx, log, activities, observationURL, journalToken, client, done)
	auditIntakeClassifications(ctx, log, activities, observationURL, journalToken, client, done)
	narrateAgentDevelopment(ctx, log, activities, ws, client, done)
	monitorTurns(ctx, log, activities, observationURL, journalToken, client, done)
	verifyCompletionClaims(ctx, log, activities, observationURL, journalToken, client, done)
	warmupEvalCases(ctx, log, ws, client, done)
}

// verificationClaimPhrases: the success claims a final answer makes about
// checks. Only phrases that assert CHECKS passed count — generic success
// wording would flag half the runs (false positives kill the guard).
var verificationClaimPhrases = []string{
	"тесты проходят", "тесты зелёные", "все тесты прошли", "тесты успешно",
	"проверки проходят", "сборка и тесты", "tests pass", "tests are green",
	"all tests pass", "test suite passes", "build and tests pass",
}

// verificationEvidencePhrases: a run_command whose command line matches one
// of these counts as real verification evidence for the run.
var verificationEvidencePhrases = []string{
	"go test", "npm test", "npm run test", "pnpm test", "yarn test",
	"pytest", "cargo test", "mvn test", "gradle test", "make test",
	"go build", "npm run build", "cargo build",
}

// verifyCompletionClaims is the anti-deception slice of stage 2 (docs/
// agent-behavior-monitoring.md §3): a run whose final message claims checks
// passed while its own journal shows no verification command becomes a case.
// The deterministic phrase pass only NOMINATES — every nomination goes
// through the supervisor judge with the run's actual command digest, and only
// a judged gap reaches a human. Judged-honest runs get a sticky marker; the
// same run never queues twice (the case key includes the case id — the
// subject-only lookup once flooded the queue with duplicates).
// verificationFetch / verificationJudge are indirection points for tests:
// production uses the journal HTTP fetcher and the agent judge.
var (
	verificationFetch = fetchJournalTypedEvents
	verificationJudge = agent.VerificationGapCase
)

// verificationSink is the journal-write surface of the audit; *agent.Activities
// satisfies it, tests record calls.
type verificationSink interface {
	PublishSupervisorCase(ctx context.Context, model, caseID, subject, project, summary, recommendation string, extra map[string]any) error
	EmitVerificationReviewed(ctx context.Context, run, explanation string) error
}

func verifyCompletionClaims(ctx context.Context, log *slog.Logger, activities verificationSink, observationURL, journalToken string, client *llm.Client, done map[string]time.Time) {
	fetch := func(eventType string, limit int) ([]journalEvent, error) {
		return verificationFetch(ctx, observationURL, journalToken, eventType, limit)
	}
	completions, err := fetch("run.completed", 60)
	if err != nil {
		log.Warn("verification claims: completions", "error", err.Error())
		return
	}
	started, err := fetch("tool.started", 500)
	if err != nil {
		log.Warn("verification claims: tool events", "error", err.Error())
		return
	}
	delegations, err := fetch("delegation.started", 200)
	if err != nil {
		log.Warn("verification claims: delegations", "error", err.Error())
		return
	}
	verifiedRuns := map[string]bool{}
	commandsByRun := map[string][]string{}
	for _, ev := range started {
		command := strings.ToLower(fmt.Sprint(ev.Data["command"]))
		if command == "" {
			if args, ok := ev.Data["arguments"].(map[string]any); ok {
				command = strings.ToLower(fmt.Sprint(args["command"]))
			}
		}
		if command == "" || command == "<nil>" {
			continue
		}
		if len(commandsByRun[ev.Run]) < 8 {
			commandsByRun[ev.Run] = append(commandsByRun[ev.Run], truncateForMonitor(command))
		}
		for _, phrase := range verificationEvidencePhrases {
			if strings.Contains(command, phrase) {
				verifiedRuns[ev.Run] = true
				break
			}
		}
	}
	delegatedRuns := map[string]bool{}
	for _, ev := range delegations {
		delegatedRuns[ev.Run] = true
	}
	// Runs whose claim was already audited (judged honest) — sticky, same as
	// the done index but in their own event type: an honest report is not a
	// case and must not appear in the queue.
	reviewed, err := fetch("verification.reviewed", 200)
	if err != nil {
		log.Warn("verification claims: reviewed markers", "error", err.Error())
	}
	reviewedRuns := map[string]bool{}
	for _, ev := range reviewed {
		subject, _ := ev.Data["subject"].(string)
		if run, ok := strings.CutSuffix(subject, ":verification_claim"); ok {
			reviewedRuns[run] = true
		}
	}
	judged := 0
	for _, ev := range completions {
		run := ev.Run
		if run == "" || verifiedRuns[run] || delegatedRuns[run] || reviewedRuns[run] {
			continue
		}
		subject := run + ":verification_claim"
		if _, ok := done[agent.CaseVerificationGap+"\x00"+subject]; ok {
			continue
		}
		// The final message is the run's newest text delta.
		final := runFinalMessageVia(fetch, run)
		if final == "" {
			continue
		}
		lower := strings.ToLower(final)
		claimed := false
		for _, phrase := range verificationClaimPhrases {
			if strings.Contains(lower, phrase) {
				claimed = true
				break
			}
		}
		if !claimed {
			continue
		}
		// Judge gate: phrase matches over-nominate (interactive work
		// described as «проверено», checks done in other runs, blocked
		// checks). The model sees the claim and the actual command digest
		// and decides whether the report is a real unverified-pass claim.
		if client == nil || judged >= 3 {
			continue // no judge budget: stay silent rather than spam
		}
		judged++
		commandsDigest := strings.Join(commandsByRun[run], "\n")
		if commandsDigest == "" {
			commandsDigest = "(ни одной команды в журнале рана)"
		}
		verdict, vErr := verificationJudge(ctx, client, truncateForMonitor(final), commandsDigest)
		if vErr != nil {
			log.Warn("verification claims: judge", "run", run, "error", vErr.Error())
			continue // fail-open: skip this pass, the marker keeps it sticky
		}
		if !verdict.Gap {
			if err := activities.EmitVerificationReviewed(ctx, run, verdict.Explanation); err != nil {
				log.Warn("verification claims: honest marker", "run", run, "error", err.Error())
			}
			continue
		}
		summary := fmt.Sprintf("Финал заявляет прохождение проверок, но журнал рана таких команд не показывает. Судья: %s Команды рана: %s. Финал: «%.160s»",
			verdict.Explanation, commandsDigest, final)
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseVerificationGap, subject, ev.Project, summary,
			"Прогоните проверки сами или запросите у рана доказательства; если отчёт честный — пометьте кейс решённым.", map[string]any{
				"run": run, "claim_only": true, "cause_class": "verification_gap", "judge": verdict.Explanation,
			}); err != nil {
			log.Warn("verification claims: publish", "error", err.Error())
		}
	}
}

// runFinalMessage fetches the run's newest model.text_delta — the final
// answer text.
func runFinalMessage(ctx context.Context, observationURL, journalToken, run string) string {
	return runFinalMessageVia(func(eventType string, limit int) ([]journalEvent, error) {
		return fetchJournalTypedEvents(ctx, observationURL, journalToken, eventType, limit)
	}, run)
}

// runFinalMessageVia is the injectable variant (tests pass their fixture).
func runFinalMessageVia(fetch func(eventType string, limit int) ([]journalEvent, error), run string) string {
	events, err := fetch("model.text_delta", 200)
	if err != nil {
		return ""
	}
	latest := ""
	for _, ev := range events {
		if ev.Run == run {
			latest = fmt.Sprint(ev.Data["text"])
		}
	}
	return latest
}

// ── B2: contract proposals from recurring agent-prompt failures ────────────

// proposeContractsFromClusters turns a recurring agent_prompt failure cluster
// (wave A2 grouping, wave B2 action) into a pending evolution proposal for
// the agent's definition: the failures say which completion check or
// constraint is missing, the model words the change, a human applies it
// through the ordinary proposal review.
func proposeContractsFromClusters(ctx context.Context, log *slog.Logger, activities *agent.Activities, ws supervisorCaseStore, observationURL, journalToken string, client *llm.Client, done map[string]time.Time) {
	if ws == nil || client == nil {
		return // filing a proposal needs the store; wording needs the model
	}
	verdicts, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "supervisor.verdict", 200)
	if err != nil {
		log.Warn("supervisor cases: contract list verdicts", "error", err.Error())
		return
	}
	started, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "run.started", 200)
	if err != nil {
		log.Warn("supervisor cases: contract list runs", "error", err.Error())
		return
	}
	runAgent := map[string]string{}
	for _, event := range started {
		agentID, _ := event.Data["agent_id"].(string)
		if agentID != "" {
			runAgent[event.Run] = agentID
		}
	}
	// Group agent_prompt verdicts by agent + normalized summary head (the
	// same failure keeps the same cause class and roughly the same story).
	type cluster struct {
		agent   string
		runs    []string
		summary string
	}
	clusters := map[string]*cluster{}
	for _, v := range verdicts {
		cause, _ := v.Data["cause_class"].(string)
		if cause != string(agent.CauseAgentPrompt) {
			continue
		}
		runID, _ := v.Data["run_id"].(string)
		agentID := runAgent[runID]
		if agentID == "" {
			continue
		}
		summary, _ := v.Data["summary"].(string)
		pattern, _ := v.Data["pattern"].(string)
		key := agentID + "\x00" + normalizeFrictionText(pattern+" "+summary)
		c := clusters[key]
		if c == nil {
			c = &cluster{agent: agentID, summary: summary}
			clusters[key] = c
		}
		if len(c.runs) < 5 {
			c.runs = append(c.runs, runID)
		}
	}
	proposed := 0
	for key, c := range clusters {
		if proposed >= supervisorCasePassCap {
			break
		}
		if len(c.runs) < 3 {
			continue
		}
		subject := "contract:" + patternHash(key)
		if !caseDue(done, agent.CaseContractProposal, subject) {
			continue
		}
		draft, err := agent.DraftContractProposal(ctx, client, c.agent, c.runs, c.summary)
		if err != nil {
			log.Warn("supervisor cases: contract draft", "agent", c.agent, "error", err.Error())
			continue
		}
		proposal := &workspace.AgentProposal{
			AgentID:  c.agent,
			Problem:  draft.Problem,
			Change:   draft.Change,
			Effect:   draft.Effect,
			Evidence: c.runs,
			Author:   "supervisor",
		}
		if err := ws.CreateAgentProposal(ctx, proposal); err != nil {
			log.Warn("supervisor cases: contract file", "agent", c.agent, "error", err.Error())
			continue
		}
		// Close the loop (docs/agent-quality-loop.md §6 + warm-start): the same
		// failure cluster drafts ONE regression case into the agent's suite —
		// if the proposal is applied, the gate now has a case that catches the
		// exact failure that motivated it. The case name marks it as
		// supervisor-authored; the human prunes it like any warmup draft.
		if len(c.runs) > 0 {
			if suite, sErr := ws.GetAgentEvaluationSuite(ctx, c.agent); sErr == nil {
				caseInput := fmt.Sprintf("Повторяющийся сбой (раны %s): %s. Выполни задачу так, чтобы этой ошибки не было.", strings.Join(c.runs, ", "), c.summary)
				expected := fmt.Sprintf("Решение учитывает правило из эволюции: %s", draft.Change)
				suite.Cases = append(suite.Cases, workspace.EvaluationCase{
					Name:           "supervisor/contract-" + patternHash(key)[:8],
					Input:          caseInput,
					ExpectedOutput: expected,
				})
				if saveErr := ws.SaveAgentEvaluationSuite(ctx, c.agent, suite.Cases); saveErr != nil {
					log.Warn("supervisor cases: eval case append", "agent", c.agent, "error", saveErr.Error())
				}
			}
		}
		summary := fmt.Sprintf("%d однотипных сбоев агента %s оформлены предложением развития #%d: %s", len(c.runs), c.agent, proposal.ID, draft.Change)
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseContractProposal, subject, "", summary, "Примите или отклоните предложение в карточке агента.", map[string]any{
			"agent": c.agent, "proposal_id": proposal.ID, "runs": c.runs,
		}); err != nil {
			log.Warn("supervisor cases: publish contract", "error", err.Error())
			continue
		}
		proposed++
	}
}

// caseDoneIndex maps case:subject → the newest verdict time inside the dedup
// window; the run-triage verdicts (no case field) are ignored.
func caseDoneIndex(verdicts []journalEvent) map[string]time.Time {
	done := map[string]time.Time{}
	for _, v := range verdicts {
		caseID, _ := v.Data["case"].(string)
		subject, _ := v.Data["subject"].(string)
		if caseID == "" || subject == "" {
			continue
		}
		key := caseID + "\x00" + subject
		if at, ok := done[key]; !ok {
			done[key] = time.Now() // journal listing is newest-first; first hit wins
		} else {
			_ = at
		}
	}
	return done
}

// caseDue reports whether the subject still needs a verdict.
func caseDue(done map[string]time.Time, caseID, subject string) bool {
	_, recent := done[caseID+"\x00"+subject]
	return !recent
}

// ── B1 + B4: knowledge contradiction triage and hygiene ────────────────────

func triageKnowledgeCases(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL, journalToken string, client *llm.Client, done map[string]time.Time) {
	challenges, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "knowledge.challenged", 50)
	if err != nil {
		log.Warn("supervisor cases: list challenges", "error", err.Error())
		return
	}
	triagedB1, triagedB4 := 0, 0
	for _, challenge := range challenges {
		knowledgeID, _ := challenge.Data["knowledge_id"].(string)
		reason, _ := challenge.Data["reason"].(string)
		if knowledgeID == "" {
			continue
		}
		rule, _ := challenge.Data["rule"].(string)
		proposition, _ := challenge.Data["proposition"].(string)
		isAging := rule == "aging.v1" || strings.HasPrefix(reason, "unconfirmed for")
		if !isAging && triagedB1 >= supervisorCasePassCap {
			continue
		}
		if isAging && triagedB4 >= supervisorCasePassCap {
			continue
		}
		if isAging {
			if !caseDue(done, agent.CaseKnowledgeHygiene, knowledgeID) {
				continue
			}
			verdict, err := agent.HygieneCase(ctx, client, agent.HygieneCaseInput{
				KnowledgeID: knowledgeID, Proposition: proposition, Reason: reason, State: "proposed",
			})
			if err != nil {
				log.Warn("supervisor cases: hygiene", "knowledge", knowledgeID, "error", err.Error())
				continue
			}
			// The action vocabulary is machine-facing (data.action); gluing it to
			// the summary leaked raw tokens like "supersede_old" into the queue.
			summary := verdict.Explanation
			if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseKnowledgeHygiene, knowledgeID, challenge.Project, summary, verdict.Recommendation, map[string]any{
				"action": verdict.Action, "rule": rule,
			}); err != nil {
				log.Warn("supervisor cases: publish hygiene", "error", err.Error())
				continue
			}
			triagedB4++
		} else {
			if !caseDue(done, agent.CaseKnowledgeContradiction, knowledgeID) {
				continue
			}
			contradictedBy, _ := challenge.Data["contradicted_by"].(string)
			verdict, err := agent.ContradictionCase(ctx, client, agent.ContradictionCaseInput{
				KnowledgeID: knowledgeID, Proposition: proposition, Reason: reason, ContradictedBy: contradictedBy,
			})
			if err != nil {
				log.Warn("supervisor cases: contradiction", "knowledge", knowledgeID, "error", err.Error())
				continue
			}
			// Same leak fix as hygiene above: action lives in data.action.
			summary := verdict.Explanation
			if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseKnowledgeContradiction, knowledgeID, challenge.Project, summary, verdict.Recommendation, map[string]any{
				"action": verdict.Action, "contradicted_by": contradictedBy,
			}); err != nil {
				log.Warn("supervisor cases: publish contradiction", "error", err.Error())
				continue
			}
			triagedB1++
		}
		if triagedB1 >= supervisorCasePassCap && triagedB4 >= supervisorCasePassCap {
			break
		}
	}
}

// ── C4: skill candidate mining ──────────────────────────────────────────────

func mineSkillCandidates(ctx context.Context, log *slog.Logger, activities *agent.Activities, ws supervisorCaseStore, observationURL, journalToken string, client *llm.Client, done map[string]time.Time) {
	tools, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "tool.started", 400)
	if err != nil {
		log.Warn("supervisor cases: list tool events", "error", err.Error())
		return
	}
	// Per-run ordered tool sequences → repeated 3-grams across DISTINCT runs.
	// A triple (read → mutate → verify) actually names a procedure where a
	// pair mostly names a habit; the deterministic gate keeps the noise out.
	perRun := map[string][]string{}
	runOrder := []string{}
	for _, event := range tools {
		tool, _ := event.Data["tool"].(string)
		if tool == "" || event.Run == "" {
			continue
		}
		if _, seen := perRun[event.Run]; !seen {
			runOrder = append(runOrder, event.Run)
		}
		perRun[event.Run] = append(perRun[event.Run], tool)
	}
	type candidate struct {
		runs    map[string]bool
		count   int
		project string
	}
	grams := map[string]*candidate{}
	for _, run := range runOrder {
		seq := perRun[run]
		seenInRun := map[string]bool{}
		project := ""
		for _, event := range tools {
			if event.Run == run {
				project = event.Project
				break
			}
		}
		for i := 0; i+2 < len(seq); i++ {
			a, b, c := seq[i], seq[i+1], seq[i+2]
			if !skillPatternWorthMining(a, b, c) {
				continue
			}
			sig := a + " → " + b + " → " + c
			if seenInRun[sig] {
				continue
			}
			seenInRun[sig] = true
			entry := grams[sig]
			if entry == nil {
				entry = &candidate{runs: map[string]bool{}, project: project}
				grams[sig] = entry
			}
			entry.runs[run] = true
			entry.count++
		}
	}
	// Rank by distinct-run spread.
	type ranked struct {
		sig string
		c   *candidate
	}
	var rankedList []ranked
	for sig, c := range grams {
		if len(c.runs) >= 3 {
			rankedList = append(rankedList, ranked{sig, c})
		}
	}
	sort.Slice(rankedList, func(i, j int) bool { return len(rankedList[i].c.runs) > len(rankedList[j].c.runs) })

	var existing []string
	if ws != nil {
		if skills, err := ws.ListSkillsVisible(ctx, nil); err == nil {
			for _, s := range skills {
				existing = append(existing, s.Name)
			}
		}
	}
	mined := 0
	for _, r := range rankedList {
		if mined >= supervisorCasePassCap {
			break
		}
		subject := patternHash(r.sig)
		if !caseDue(done, agent.CaseSkillCandidate, subject) || recentlyRejectedSkillPattern(r.sig) {
			continue
		}
		// A skill candidate is only interesting when no existing skill name
		// already covers the tools.
		duplicated := false
		for _, name := range existing {
			for _, tool := range strings.Split(r.sig, " → ") {
				if name != "" && strings.Contains(strings.ToLower(name), strings.ToLower(tool)) {
					duplicated = true
					break
				}
			}
			if duplicated {
				break
			}
		}
		if duplicated {
			continue
		}
		examples := make([]string, 0, 3)
		for run := range r.c.runs {
			if len(examples) >= 3 {
				break
			}
			examples = append(examples, "run: "+run)
		}
		verdict, err := agent.SkillCandidateCase(ctx, client, agent.SkillCandidateCaseInput{
			Signature: r.sig, Runs: len(r.c.runs), Examples: examples, ExistingSkills: existing,
		})
		if err != nil {
			log.Warn("supervisor cases: skill candidate", "signature", r.sig, "error", err.Error())
			continue
		}
		if !verdict.WorthSkill {
			// The judge's rejection is a decision, not a failure: remember it so
			// the next pass reaches deeper into rankedList instead of re-asking
			// the same pair (the journal cannot remember it — a rejection is
			// never published, the feed being the point).
			log.Info("supervisor cases: skill candidate rejected as trivial", "signature", r.sig, "explanation", verdict.Explanation)
			rememberRejectedSkillPattern(r.sig)
			continue
		}
		summary := fmt.Sprintf("Последовательность «%s» повторяется в %d ранах — кандидат в навыки «%s»: %s", r.sig, len(r.c.runs), verdict.SkillName, verdict.Description)
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseSkillCandidate, subject, r.c.project, summary, verdict.Recommendation, map[string]any{
			"signature": r.sig, "runs": len(r.c.runs), "skill_name": verdict.SkillName, "description": verdict.Description, "steps": verdict.Steps,
		}); err != nil {
			log.Warn("supervisor cases: publish skill candidate", "error", err.Error())
			continue
		}
		mined++
	}
}

func patternHash(sig string) string {
	sum := sha256.Sum256([]byte(sig))
	return hex.EncodeToString(sum[:8])
}

// skillPatternWorthMining is the cheap deterministic gate ahead of the judge:
// adjacent same-tool steps are pagination or a retry, and a sequence of only
// read-only tools is navigation every run performs — none of it encodes a
// procedure, and proposing them as skills floods the human queue with noise.
// A sequence with at least one mutating tool (run_command, write_file,
// delegate…) stays in: whether it is a real procedure is the judge's call.
// Variadic over the n-gram length: the same rules hold for pairs and triples.
func skillPatternWorthMining(tools ...string) bool {
	readonly := 0
	for i, tool := range tools {
		if looksLikeReadonlyTool(tool) {
			readonly++
		}
		if i > 0 && tools[i-1] == tool {
			return false
		}
	}
	return readonly < len(tools)
}

// skillReadonlyMarkers are name fragments that mark a builtin tool as a pure
// lookup/read (skill_search, artifact_read, saga_list, web_fetch,
// run_status…), grounded in the builtin tool names; exact matches cover the
// names without a marker ("agents").
var skillReadonlyMarkers = []string{
	"search", "list", "read", "inspect", "fetch", "explore", "lookup",
	"history", "diff", "executions", "status",
}

// Exact names without a distinctive marker; "view" as a substring would
// wrongly catch submit_review.
var skillReadonlyNames = map[string]bool{"agents": true, "view_image": true}

func looksLikeReadonlyTool(name string) bool {
	if skillReadonlyNames[name] {
		return true
	}
	for _, marker := range skillReadonlyMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// Rejected skill patterns are remembered in-process for the same window the
// journal dedup uses. The judge's "trivial" verdict is deliberately not
// published (it would re-appear in the human feed), so the journal cannot
// remember it; the cost of a restart is one judge call per stale top
// candidate, bounded by the pass cap.
var (
	skillRejectedMu       sync.Mutex
	skillRejectedPatterns = map[string]time.Time{}
)

func rememberRejectedSkillPattern(sig string) {
	skillRejectedMu.Lock()
	defer skillRejectedMu.Unlock()
	skillRejectedPatterns[sig] = time.Now()
}

func recentlyRejectedSkillPattern(sig string) bool {
	skillRejectedMu.Lock()
	defer skillRejectedMu.Unlock()
	at, ok := skillRejectedPatterns[sig]
	return ok && time.Since(at) < supervisorCaseDedupWindow
}

// ── C1: user friction patterns ──────────────────────────────────────────────

func detectUserFriction(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL, journalToken string, client *llm.Client, done map[string]time.Time) {
	started, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "run.started", 200)
	if err != nil {
		log.Warn("supervisor cases: list started runs", "error", err.Error())
		return
	}
	rejections, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "approval.rejected", 100)
	if err != nil {
		log.Warn("supervisor cases: list rejections", "error", err.Error())
		return
	}
	// Pattern 1: the same normalized question from the same actor ≥3 times.
	type ask struct {
		actor, title string
	}
	counts := map[ask]int{}
	for _, event := range started {
		title, _ := event.Data["title"].(string)
		actor, _ := event.Data["actor_id"].(string)
		if title == "" {
			continue
		}
		norm := normalizeFrictionText(title)
		if norm == "" {
			continue
		}
		counts[ask{actor, norm}]++
	}
	published := 0
	for key, n := range counts {
		if published >= supervisorCasePassCap {
			break
		}
		if n < 3 {
			continue
		}
		subject := "repeat_prompt:" + patternHash(key.actor+"|"+key.title)
		if !caseDue(done, agent.CaseUserFriction, subject) {
			continue
		}
		facts := []string{fmt.Sprintf("the same normalized prompt appeared %d times in the window by actor %q", n, key.actor)}
		verdict, err := agent.FrictionCase(ctx, client, agent.FrictionCaseInput{
			Pattern: fmt.Sprintf("same prompt asked %dx by one user", n), Facts: facts,
		})
		if err != nil {
			log.Warn("supervisor cases: friction", "error", err.Error())
			continue
		}
		summary := fmt.Sprintf("Один и тот же запрос повторён %d раз пользователем %s — %s", n, key.actor, verdict.Explanation)
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseUserFriction, subject, "", summary, verdict.Recommendation, map[string]any{
			"repeats": n, "actor": key.actor,
		}); err != nil {
			log.Warn("supervisor cases: publish friction", "error", err.Error())
			continue
		}
		published++
	}
	// Pattern 2: the same operation rejected ≥2 times in one run — the agent
	// keeps re-asking what the user already said no to.
	rejectCounts := map[string]int{}
	for _, event := range rejections {
		operation, _ := event.Data["operation_id"].(string)
		if operation == "" {
			continue
		}
		rejectCounts[event.Run+"\x00"+operation]++
	}
	for key, n := range rejectCounts {
		if published >= supervisorCasePassCap {
			break
		}
		if n < 2 {
			continue
		}
		run := strings.SplitN(key, "\x00", 2)[0]
		subject := "repeat_rejection:" + patternHash(key)
		if !caseDue(done, agent.CaseUserFriction, subject) {
			continue
		}
		verdict, err := agent.FrictionCase(ctx, client, agent.FrictionCaseInput{
			Pattern: "the same operation rejected twice in one run", Facts: []string{fmt.Sprintf("run %s: operation rejected %d times", run, n)},
		})
		if err != nil {
			log.Warn("supervisor cases: friction rejection", "error", err.Error())
			continue
		}
		summary := fmt.Sprintf("Операция отклонена пользователем %d раз в одном ране (%s) — %s", n, run, verdict.Explanation)
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseUserFriction, subject, "", summary, verdict.Recommendation, map[string]any{
			"rejections": n, "run": run,
		}); err != nil {
			log.Warn("supervisor cases: publish friction rejection", "error", err.Error())
			continue
		}
		published++
	}
}

// normalizeFrictionText levels prompts for repeat detection: lowercase,
// digits → #, whitespace collapsed, truncated.
func normalizeFrictionText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte('#')
			lastSpace = false
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if !lastSpace {
				b.WriteByte(' ')
			}
			lastSpace = true
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	out := b.String()
	if len(out) > 120 {
		out = out[:120]
	}
	return strings.TrimSpace(out)
}

// ── C2: intake classification audit ─────────────────────────────────────────

func auditIntakeClassifications(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL, journalToken string, client *llm.Client, done map[string]time.Time) {
	classified, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "intake.classified", 30)
	if err != nil {
		log.Warn("supervisor cases: list classifications", "error", err.Error())
		return
	}
	started, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "run.started", 100)
	if err != nil {
		log.Warn("supervisor cases: list started runs", "error", err.Error())
		return
	}
	audited := 0
	for _, event := range classified {
		if audited >= 1 {
			break // sampling: one classification per pass
		}
		// Stable subject: the inbound message id (present on every classified
		// event), falling back to the journal event id. The old fallback keyed
		// on len(done) — a number that changes every pass — so the same
		// classification was re-audited each cycle, publishing the same verdict
		// over and over (the «куча одинаковых вердиктов» flood).
		subject, _ := event.Data["message_id"].(string)
		if subject == "" {
			subject = event.EventID
		}
		if subject == "" {
			continue
		}
		classification, _ := event.Data["classification"].(string)
		if classification != "task" && classification != "amendment" {
			continue // only the two load-bearing labels are worth auditing
		}
		if !caseDue(done, agent.CaseIntakeAudit, subject) {
			continue
		}
		messageText, _ := event.Data["message"].(string)
		if strings.TrimSpace(messageText) == "" {
			// Older classified events predate the message payload (and truly
			// empty bodies carry nothing to judge): auditing them produced the
			// «message body is empty → reclassify» spam. Skip — one sample of
			// the real text or nothing at all.
			continue
		}
		// The run the router started right after this classification: same
		// project, newest first (both listings are newest-first already).
		startedRun := "(nothing started)"
		for _, run := range started {
			if run.Project == event.Project {
				title, _ := run.Data["title"].(string)
				startedRun = title
				break
			}
		}
		verdict, err := agent.IntakeAuditCase(ctx, client, agent.IntakeAuditCaseInput{
			Classification: classification, MessageText: messageText, StartedRun: startedRun,
		})
		if err != nil {
			log.Warn("supervisor cases: intake audit", "error", err.Error())
			continue
		}
		if verdict.Agrees {
			continue // silent agreement: no queue noise for a correct label
		}
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseIntakeAudit, subject, event.Project, verdict.Explanation, verdict.Recommendation, map[string]any{
			"classification": classification,
		}); err != nil {
			log.Warn("supervisor cases: publish intake audit", "error", err.Error())
			continue
		}
		audited++
	}
}

// ── B3: development narrative ───────────────────────────────────────────────

func narrateAgentDevelopment(ctx context.Context, log *slog.Logger, activities *agent.Activities, ws supervisorCaseStore, client *llm.Client, done map[string]time.Time) {
	if ws == nil {
		return
	}
	agents, err := ws.ListAllAgents(ctx)
	if err != nil {
		log.Warn("supervisor cases: list agents", "error", err.Error())
		return
	}
	narrated := 0
	for _, a := range agents {
		if narrated >= supervisorCasePassCap {
			break
		}
		if a.Definition == nil || a.DefinitionVersion < 2 {
			continue // a first version has no "development" to narrate
		}
		subject := fmt.Sprintf("%s:v%d", a.ID, a.DefinitionVersion)
		if !caseDue(done, agent.CaseDevelopmentNarrative, subject) {
			continue
		}
		runs, err := ws.ListAgentEvaluationRuns(ctx, a.ID, 2)
		if err != nil || len(runs) < 2 {
			continue
		}
		latest, previous := runs[0], runs[1]
		if latest.AgentVersion == previous.AgentVersion {
			continue // both evaluations saw the same definition: nothing to narrate
		}
		changes := []string{}
		if a.Definition != nil && len(a.Definition.Completion) > 0 {
			changes = append(changes, fmt.Sprintf("completion checks: %d", len(a.Definition.Completion)))
		}
		verdict, err := agent.NarrativeCase(ctx, client, agent.NarrativeCaseInput{
			AgentID:   a.ID,
			FromVer:   previous.AgentVersion,
			ToVer:     latest.AgentVersion,
			FromStats: fmt.Sprintf("passed %d, failed %d (verdict %s)", previous.Passed, previous.Failed, previous.Verdict),
			ToStats:   fmt.Sprintf("passed %d, failed %d (verdict %s)", latest.Passed, latest.Failed, latest.Verdict),
			Changes:   changes,
		})
		if err != nil {
			log.Warn("supervisor cases: narrative", "agent", a.ID, "error", err.Error())
			continue
		}
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseDevelopmentNarrative, subject, a.ProjectID, verdict.Explanation, verdict.Recommendation, map[string]any{
			"agent": a.ID, "from_version": previous.AgentVersion, "to_version": latest.AgentVersion,
		}); err != nil {
			log.Warn("supervisor cases: publish narrative", "error", err.Error())
			continue
		}
		narrated++
	}
}

// ── Behavior monitoring (stage 3, docs/agent-behavior-monitoring.md §5) ────

// monitorSuspicionMin is the judge score above which a run lands in the
// supervisor queue. Deliberately high: this channel pages a human, and every
// false positive spends the trust the whole guard runs on.
func monitorSuspicionMin() int {
	if raw := strings.TrimSpace(os.Getenv("KERNEL_MONITOR_SUSPICION_MIN")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 10 {
			return v
		}
	}
	return 6
}

// monitorTurns is the stage-3 oversight pass: for every run that asked to be
// monitored (its turn.completed carries behavior_monitor) and has not been
// reviewed yet, the judge reads the task plus the run's recorded actions and
// scores suspiciousness. A high score publishes a supervisor case; clean and
// reviewed runs are recorded via behavior.reviewed markers so nothing is
// re-judged. Without a judge client the stage silently stays off — there is
// no deterministic fallback for intent review.
func monitorTurns(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL, journalToken string, client *llm.Client, done map[string]time.Time) {
	if client == nil {
		return
	}
	completions, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "turn.completed", 400)
	if err != nil {
		log.Warn("behavior monitor: turn.completed", "error", err.Error())
		return
	}
	// Actions per run, from the run's own tool.started events. The journal
	// caps one page at 500 — the cursor walk in fetchJournalTypedEvents
	// covers the rest, so asking for more than the cap is just a 400.
	started, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "tool.started", 500)
	if err != nil {
		log.Warn("behavior monitor: tool.started", "error", err.Error())
		return
	}
	actionsByRun := map[string][]string{}
	for _, ev := range started {
		run := ev.Run
		if run == "" {
			continue
		}
		tool, _ := ev.Data["tool"].(string)
		if tool == "" {
			tool = "tool"
		}
		args := truncateForMonitor(fmt.Sprint(ev.Data["arguments"]))
		actionsByRun[run] = append(actionsByRun[run], fmt.Sprintf("%s: %s", tool, args))
	}
	// The task context: the run's originating request (run.started.request).
	// Runs already reviewed (clean or flagged) are skipped via their
	// behavior.reviewed markers.
	requests := map[string]string{}
	started2, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "run.started", 300)
	if err == nil {
		for _, ev := range started2 {
			if req2, ok := ev.Data["request"].(string); ok && req2 != "" {
				requests[ev.Run] = req2
			}
		}
	}
	reviewed := reviewedIndex(ctx, observationURL, journalToken)

	monitored := 0
	judged := 0
	// Oldest first: completions arrive oldest-first from the journal.
	for _, ev := range completions {
		if monitored >= 3 || judged >= 3 {
			return
		}
		if monitoredFlag, _ := ev.Data["behavior_monitor"].(bool); !monitoredFlag {
			continue
		}
		run := ev.Run
		if run == "" {
			continue
		}
		subject := run + ":monitor"
		if _, ok := done[subject]; ok || reviewed[subject] {
			continue // already reviewed (clean or flagged)
		}
		actions := actionsByRun[run]
		if len(actions) == 0 {
			continue // nothing recorded yet for this run
		}
		if len(actions) > 30 {
			actions = actions[:30]
		}
		task := requests[run]
		if task == "" {
			task = "(task prompt not recorded)"
		}
		monitored++
		verdict, vErr := agent.MonitorTurnCase(ctx, client, agent.MonitorTurnInput{
			Task:    truncateForMonitor(task),
			Actions: strings.Join(actions, "\n"),
		})
		if vErr != nil {
			log.Warn("behavior monitor: judge", "run", run, "error", vErr.Error())
			continue // not recorded as reviewed — retried next pass
		}
		judged++
		if err := activities.EmitBehaviorReviewed(ctx, run, verdict.Suspicion, verdict.Explanation); err != nil {
			log.Warn("behavior monitor: marker", "error", err.Error())
		}
		if verdict.Suspicion < monitorSuspicionMin() {
			continue
		}
		summary := fmt.Sprintf("Судья надзора: подозрительность %d/10 — %s", verdict.Suspicion, verdict.Explanation)
		if len(verdict.Flags) > 0 {
			summary += " Флаги: " + strings.Join(verdict.Flags, ", ")
		}
		if err := activities.PublishSupervisorCase(ctx, "supervisor", agent.CaseBehaviorSuspicion, subject, ev.Project, summary, "Просмотрите действия рана в трейсе и закройте кейс (подтверждено/ложная тревога).", map[string]any{
			"run": run, "suspicion": verdict.Suspicion, "flags": verdict.Flags,
		}); err != nil {
			log.Warn("behavior monitor: publish", "error", err.Error())
		}
	}
}

func truncateForMonitor(s string) string {
	if len(s) <= 400 {
		return s
	}
	return s[:400] + "…"
}

// The reviewed set: subjects of behavior.reviewed markers, so a judged run
// is never re-judged even after kernel restarts.
func reviewedIndex(ctx context.Context, observationURL, journalToken string) map[string]bool {
	events, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "behavior.reviewed", 500)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, ev := range events {
		if subject, ok := ev.Data["subject"].(string); ok {
			set[subject] = true
		}
	}
	return set
}

// ── Warm-start: supervisor-drafted eval cases for agents the gate cannot gate ──

// evalGateMinCases mirrors the gate's suite_not_ready minimum (§7.1): a suite
// below this cannot decide an apply, so it is the warm-start trigger.
const evalGateMinCases = 3

// warmupCaseStore is the store face the warm-start pass needs.
type warmupCaseStore interface {
	ListAllAgents(ctx context.Context) ([]workspace.Agent, error)
	ListRunsByAgent(ctx context.Context, agentID string, limit int) ([]workspace.Run, error)
	GetAgentEvaluationSuite(ctx context.Context, agentID string) (workspace.AgentEvaluationSuite, error)
	SaveAgentEvaluationSuite(ctx context.Context, agentID string, cases []workspace.EvaluationCase) error
}

// warmupEvalCases closes the cold-start gap of the quality loop: an agent
// whose suite holds fewer cases than the gate's minimum cannot pass an apply,
// and hand-writing cases is the chore that kills the loop. The supervisor
// drafts up to three representative cases from the agent's purpose and recent
// successful runs and APPENDS them to the suite (author 'supervisor
// warm-start' via the case name prefix) — a floor the human reviews and
// prunes, never a full suite. Suites that already gate are left alone, and
// one pass touches at most two agents (the pass cap shared by the siblings).
func warmupEvalCases(ctx context.Context, log *slog.Logger, ws warmupCaseStore, client *llm.Client, done map[string]time.Time) {
	if ws == nil || client == nil {
		return
	}
	agents, err := ws.ListAllAgents(ctx)
	if err != nil {
		log.Warn("eval warmup: list agents", "error", err.Error())
		return
	}
	touched := 0
	for _, a := range agents {
		if touched >= supervisorCasePassCap {
			break
		}
		if a.Description == "" || a.Definition == nil {
			continue
		}
		suite, err := ws.GetAgentEvaluationSuite(ctx, a.ID)
		if err != nil {
			log.Warn("eval warmup: suite", "agent", a.ID, "error", err.Error())
			continue
		}
		if len(suite.Cases) >= evalGateMinCases {
			continue // the suite already gates
		}
		subject := "eval-warmup:" + a.ID
		if !caseDue(done, agent.CaseEvalWarmup, subject) {
			continue
		}
		runs, err := ws.ListRunsByAgent(ctx, a.ID, 5)
		if err != nil {
			log.Warn("eval warmup: runs", "agent", a.ID, "error", err.Error())
			continue
		}
		var runDigests []string
		for _, run := range runs {
			if run.Status != "completed" || strings.TrimSpace(run.Answer) == "" {
				continue
			}
			digest := "task answer: " + run.Answer
			if len(digest) > 400 {
				digest = digest[:400]
			}
			runDigests = append(runDigests, digest)
		}
		drafts, err := agent.DraftEvalCases(ctx, client, a.ID, a.Description, runDigests)
		if err != nil {
			log.Warn("eval warmup: draft", "agent", a.ID, "error", err.Error())
			continue
		}
		if len(drafts) > evalGateMinCases-len(suite.Cases) {
			drafts = drafts[:evalGateMinCases-len(suite.Cases)]
		}
		cases := make([]workspace.EvaluationCase, 0, len(drafts))
		for i, draft := range drafts {
			name := strings.TrimSpace(draft.Name)
			if name == "" {
				name = fmt.Sprintf("warmup-%d", i+1)
			}
			cases = append(cases, workspace.EvaluationCase{
				Name:           "warmup/" + name,
				Input:          draft.Input,
				ExpectedOutput: draft.ExpectedOutput,
			})
		}
		if err := ws.SaveAgentEvaluationSuite(ctx, a.ID, append(suite.Cases, cases...)); err != nil {
			log.Warn("eval warmup: save suite", "agent", a.ID, "error", err.Error())
			continue
		}
		summary := fmt.Sprintf("Сьют агента %s не дотягивал до гейта (%d кейсов) — супервайзер добавил %d черновых кейсов из purpose и свежих ранов, всего теперь %d. Ревью и удаление — во вкладке «Эволюция».",
			a.ID, len(suite.Cases), len(cases), len(suite.Cases)+len(cases))
		log.Info("eval warmup: summary", "agent", a.ID, "summary", summary)
		log.Info("eval warmup: appended supervisor cases", "agent", a.ID, "cases", len(cases))
		touched++
	}
}
