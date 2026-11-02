package agent

import (
	"strings"
	"testing"
)

func TestParseSupervisorReconcileReplyCoversEveryOp(t *testing.T) {
	content := `Here is my analysis: {"needs_attention": true, "summary": "all 30 ops died in one worker crash at 10:02", "causes": ["worker crash window"], "remedies": ["check the deploy"], "verdicts": [{"operation_id": "a", "effect": "occurred", "why": "ran before crash"}, {"operation_id": "b", "effect": "WEIRD", "why": "unclear"}, {"operation_id": "ghost-op", "effect": "none"}]}`
	verdict, err := parseSupervisorReconcileReply(content, map[string]bool{"a": true, "b": true, "c": true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !verdict.NeedsAttention {
		t.Fatal("needs_attention must survive")
	}
	if len(verdict.Causes) != 1 || len(verdict.Remedies) != 1 {
		t.Fatalf("causes/remedies = %v/%v", verdict.Causes, verdict.Remedies)
	}
	byID := map[string]SupervisorOpVerdict{}
	for _, v := range verdict.Verdicts {
		byID[v.OperationID] = v
	}
	if len(byID) != 3 {
		t.Fatalf("verdicts = %d, want 3 (every known op covered)", len(byID))
	}
	if byID["a"].Effect != "occurred" {
		t.Fatalf("a = %v", byID["a"])
	}
	// A hallucinated effect degrades to the honest unknown.
	if byID["b"].Effect != "unknown" {
		t.Fatalf("b = %v, want unknown", byID["b"])
	}
	// A hallucinated id is dropped; a skipped op gets the unknown default.
	if _, ok := byID["ghost-op"]; ok {
		t.Fatal("ghost-op must be dropped")
	}
	if byID["c"].Effect != "unknown" || byID["c"].Why == "" {
		t.Fatalf("c = %v, want the uncovered default", byID["c"])
	}
}

func TestParseSupervisorReconcileRejectsEmptySummary(t *testing.T) {
	if _, err := parseSupervisorReconcileReply(`{"needs_attention": false, "summary": "", "verdicts": []}`, map[string]bool{"a": true}); err == nil {
		t.Fatal("an empty summary must be rejected")
	}
	if _, err := parseSupervisorReconcileReply("no json at all", map[string]bool{"a": true}); err == nil {
		t.Fatal("no JSON must be rejected")
	}
}

func TestBuildWallDigestBoundsDetail(t *testing.T) {
	long := strings.Repeat("x", 500)
	digest := BuildWallDigest("run-1", []UncertainOperation{{
		OperationID: "run-1/turn/07/call-3", Tool: "run_command", Reason: "failed_uncertain",
		AgentID: "coder", ErrorDetail: long,
	}}, "boom")
	if len(digest.Lines) != 1 {
		t.Fatalf("lines = %d", len(digest.Lines))
	}
	line := digest.Lines[0]
	if !strings.Contains(line, "turn: 07") || !strings.Contains(line, "agent: coder") || !strings.Contains(line, "run-1/turn/07/call-3") {
		t.Fatalf("line = %q", line)
	}
	if idx := strings.Index(line, "detail: "); idx >= 0 && len(line)-idx > 200 {
		t.Fatalf("detail not bounded: %d chars", len(line)-idx)
	}
	if digest.Error != "boom" {
		t.Fatalf("error = %q", digest.Error)
	}
}

// The lesson is the supervisor's path into the knowledge layer: only an
// attention-worthy pile may teach, the proposition is self-contained and
// bounded, topics are capped.
func TestParseSupervisorReconcileLesson(t *testing.T) {
	content := `{"needs_attention": true, "summary": "timeouts", "lesson": {"proposition": "Long builds in run_command need timeout_sec >= 600; the model-guessed budget is not enough for cold dependency installs", "topics": ["timeouts", "run_command"]}, "verdicts": [{"operation_id": "a", "effect": "occurred"}]}`
	verdict, err := parseSupervisorReconcileReply(content, map[string]bool{"a": true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verdict.Lesson == nil {
		t.Fatal("the lesson must survive the parse")
	}
	if !strings.Contains(verdict.Lesson.Proposition, "timeout_sec >= 600") {
		t.Fatalf("proposition lost: %q", verdict.Lesson.Proposition)
	}
	if len(verdict.Lesson.Topics) != 2 {
		t.Fatalf("topics = %v", verdict.Lesson.Topics)
	}
}

func TestParseSupervisorReconcileLessonRequiresAttention(t *testing.T) {
	// A quiet pile taught nothing: the lesson is dropped even when the model
	// offered one — only real problems may write into the knowledge layer.
	content := `{"needs_attention": false, "summary": "quiet", "lesson": {"proposition": "always do X"}, "verdicts": [{"operation_id": "a", "effect": "none"}]}`
	verdict, err := parseSupervisorReconcileReply(content, map[string]bool{"a": true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verdict.Lesson != nil {
		t.Fatalf("a needs_attention=false verdict must not carry a lesson: %+v", verdict.Lesson)
	}
}

func TestParseSupervisorReconcileLessonBounds(t *testing.T) {
	long := make([]string, 600)
	for i := range long {
		long[i] = "x"
	}
	content := `{"needs_attention": true, "summary": "s", "lesson": {"proposition": "` + strings.Join(long, "") + `", "topics": ["a", "b", "c", "d", "e", "f", "g"]}, "verdicts": [{"operation_id": "a", "effect": "occurred"}]}`
	verdict, err := parseSupervisorReconcileReply(content, map[string]bool{"a": true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verdict.Lesson == nil {
		t.Fatal("lesson lost")
	}
	if len(verdict.Lesson.Proposition) > 400 {
		t.Fatalf("proposition must be bounded to 400, got %d", len(verdict.Lesson.Proposition))
	}
	if len(verdict.Lesson.Topics) > 5 {
		t.Fatalf("topics must be capped to 5, got %d", len(verdict.Lesson.Topics))
	}
}
