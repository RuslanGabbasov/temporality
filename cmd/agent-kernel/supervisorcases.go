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
	"strings"
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
	// Per-run ordered tool sequences → repeated 2-grams across DISTINCT runs.
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
		for i := 0; i+1 < len(seq); i++ {
			sig := seq[i] + " → " + seq[i+1]
			if seenInRun[sig] {
				continue
			}
			seenInRun[sig] = true
			c := grams[sig]
			if c == nil {
				c = &candidate{runs: map[string]bool{}, project: project}
				grams[sig] = c
			}
			c.runs[run] = true
			c.count++
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
		if !caseDue(done, agent.CaseSkillCandidate, subject) {
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
