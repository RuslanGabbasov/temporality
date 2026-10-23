package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/sandbox"
)

func newTestConsole(limit, maxLines int) *Console {
	c := newConsole(limit, maxLines)
	c.now = func() time.Time { return time.Unix(0, 0) }
	return c
}

// ── Tail budgets ─────────────────────────────────────────────────────────────

func TestConsoleTailByteOverflowKeepsNewestBytesAndCountsDropped(t *testing.T) {
	c := newTestConsole(8, 1000)
	c.Write("proj", "op-1", "stdout", []byte("aaaabbbb"))
	c.Write("proj", "op-1", "stdout", []byte("ccccdddd"))
	state, ok := c.Snapshot("proj", "op-1")
	if !ok {
		t.Fatal("operation must be registered")
	}
	if state.StdoutTail != "ccccdddd" {
		t.Fatalf("tail must keep the newest bytes, got %q", state.StdoutTail)
	}
	if state.DroppedBytes != 8 || !state.Truncated {
		t.Fatalf("dropped counter must report the 8 evicted bytes, got %d truncated=%v", state.DroppedBytes, state.Truncated)
	}
	if state.StderrTail != "" {
		t.Fatalf("stdout writes must not touch the stderr tail, got %q", state.StderrTail)
	}
}

func TestConsoleTailLineOverflowKeepsNewestWholeLines(t *testing.T) {
	c := newTestConsole(1<<20, 2)
	c.Write("proj", "op-1", "stdout", []byte("l1\nl2\nl3\nl4\n"))
	state, _ := c.Snapshot("proj", "op-1")
	// The line budget keeps whole lines only: the newest two complete lines.
	if state.StdoutTail != "l3\nl4\n" {
		t.Fatalf("tail must keep the two newest whole lines, got %q", state.StdoutTail)
	}
	if state.DroppedLines != 2 || state.DroppedBytes != 6 {
		t.Fatalf("dropped counters must report 2 lines / 6 bytes, got %d/%d", state.DroppedLines, state.DroppedBytes)
	}
	if state.Lines != 2 {
		t.Fatalf("lines must count the tail's lines, got %d", state.Lines)
	}
}

func TestConsolePartialTailLineStaysWithinLineBudget(t *testing.T) {
	// A tail without a trailing newline is one partial line; two writes
	// forming three newline-free fragments must not evict anything.
	c := newTestConsole(1<<20, 2)
	c.Write("proj", "op-1", "stdout", []byte("ab"))
	c.Write("proj", "op-1", "stdout", []byte("cd"))
	c.Write("proj", "op-1", "stdout", []byte("ef"))
	state, _ := c.Snapshot("proj", "op-1")
	if state.StdoutTail != "abcdef" {
		t.Fatalf("a newline-free tail is one partial line, got %q", state.StdoutTail)
	}
	if state.Lines != 1 || state.DroppedLines != 0 {
		t.Fatalf("partial line must count once and evict nothing, got lines=%d dropped=%d", state.Lines, state.DroppedLines)
	}
}

func TestConsoleStreamsBufferSeparately(t *testing.T) {
	c := newTestConsole(4, 100)
	c.Write("proj", "op-1", "stdout", []byte("aaaa"))
	c.Write("proj", "op-1", "stderr", []byte("bbbb"))
	state, _ := c.Snapshot("proj", "op-1")
	if state.StdoutTail != "aaaa" || state.StderrTail != "bbbb" {
		t.Fatalf("stdout and stderr must buffer separately, got %q / %q", state.StdoutTail, state.StderrTail)
	}
}

func TestConsoleRejectsUnknownStreamAndEmptyChunk(t *testing.T) {
	c := newTestConsole(64, 10)
	c.Write("proj", "op-1", "stdin", []byte("nope"))
	c.Write("proj", "op-1", "stdout", nil)
	if _, ok := c.Snapshot("proj", "op-1"); ok {
		t.Fatal("nothing must be registered for unknown streams or empty chunks")
	}
}

func TestConsoleWriteAfterDoneIsIgnored(t *testing.T) {
	c := newTestConsole(64, 10)
	c.Write("proj", "op-1", "stdout", []byte("before"))
	c.Done("proj", "op-1", 0)
	c.Write("proj", "op-1", "stdout", []byte("after"))
	state, _ := c.Snapshot("proj", "op-1")
	if state.StdoutTail != "before" {
		t.Fatalf("post-completion writes must be ignored, got %q", state.StdoutTail)
	}
	if !state.Done || state.ExitCode == nil || *state.ExitCode != 0 {
		t.Fatalf("done state with exit code must persist, got %+v", state)
	}
}

func TestConsoleSnapshotOfUnknownOperationFails(t *testing.T) {
	c := newTestConsole(64, 10)
	if _, ok := c.Snapshot("proj", "missing"); ok {
		t.Fatal("an unknown operation must not resolve")
	}
	// A different project's key must not resolve a foreign operation either.
	c.Write("proj-a", "shared-op", "stdout", []byte("x"))
	if _, ok := c.Snapshot("proj-b", "shared-op"); ok {
		t.Fatal("a foreign project must not see another project's console")
	}
}

func TestConsoleContractFields(t *testing.T) {
	c := newTestConsole(64, 10)
	c.Write("proj", "op-1", "stdout", []byte("hi\n"))
	code := 7
	c.Done("proj", "op-1", code)
	state, _ := c.Snapshot("proj", "op-1")
	if state.OperationID != "op-1" {
		t.Fatalf("operation_id mismatch: %q", state.OperationID)
	}
	if state.ConsoleURI != "console://op-1" {
		t.Fatalf("console_uri must be console://<operation_id>, got %q", state.ConsoleURI)
	}
	if state.Bytes != int64(len("hi\n")) || state.Lines != 1 {
		t.Fatalf("bytes/lines must describe the tails, got %d/%d", state.Bytes, state.Lines)
	}
	if state.ExitCode == nil || *state.ExitCode != code {
		t.Fatalf("exit_code must ride the snapshot, got %v", state.ExitCode)
	}
}

// ── Fan-out ──────────────────────────────────────────────────────────────────

func TestConsoleFanOutReachesLiveSubscribers(t *testing.T) {
	c := newTestConsole(64, 10)
	a, okA := c.Subscribe("proj", "op-1", false)
	b, okB := c.Subscribe("proj", "op-1", true)
	if !okA || !okB {
		t.Fatal("both subscriptions must attach to a live operation")
	}
	c.Write("proj", "op-1", "stdout", []byte("one"))
	c.Write("proj", "op-1", "stderr", []byte("two"))
	c.Done("proj", "op-1", 0)

	for name, sub := range map[string]ConsoleSubscription{"a": a, "b": b} {
		got := []consoleChunk{}
		for chunk := range sub.Events {
			got = append(got, chunk)
		}
		if len(got) != 2 ||
			got[0].Stream != "stdout" || got[0].Chunk != "one" ||
			got[1].Stream != "stderr" || got[1].Chunk != "two" {
			t.Fatalf("subscriber %s must see both chunks in order, got %+v", name, got)
		}
	}
}

func TestConsoleSubscribeTailTrueSkipsBacklog(t *testing.T) {
	c := newTestConsole(64, 10)
	c.Write("proj", "op-1", "stdout", []byte("old"))
	sub, ok := c.Subscribe("proj", "op-1", true)
	if !ok {
		t.Fatal("subscription must attach")
	}
	c.Write("proj", "op-1", "stdout", []byte("new"))
	c.Done("proj", "op-1", 0)
	frames := []consoleChunk{}
	for chunk := range sub.Events {
		frames = append(frames, chunk)
	}
	if len(frames) != 1 || frames[0].Chunk != "new" {
		t.Fatalf("tail=true must receive only new data, got %+v", frames)
	}
}

func TestConsoleSubscribeTailFalseReplaysBacklog(t *testing.T) {
	c := newTestConsole(64, 10)
	c.Write("proj", "op-1", "stdout", []byte("old-out"))
	c.Write("proj", "op-1", "stderr", []byte("old-err"))
	sub, ok := c.Subscribe("proj", "op-1", false)
	if !ok {
		t.Fatal("subscription must attach")
	}
	c.Write("proj", "op-1", "stdout", []byte("new"))
	c.Done("proj", "op-1", 0)
	frames := []consoleChunk{}
	for chunk := range sub.Events {
		frames = append(frames, chunk)
	}
	// Backlog first (one frame per stream with content), then the live chunk.
	if len(frames) != 3 ||
		frames[0].Stream != "stdout" || frames[0].Chunk != "old-out" ||
		frames[1].Stream != "stderr" || frames[1].Chunk != "old-err" ||
		frames[2].Stream != "stdout" || frames[2].Chunk != "new" {
		t.Fatalf("tail=false must replay the backlog then go live, got %+v", frames)
	}
}

func TestConsoleSubscribeToFinishedOperationDeliversDoneClosure(t *testing.T) {
	c := newTestConsole(64, 10)
	c.Write("proj", "op-1", "stdout", []byte("out"))
	c.Done("proj", "op-1", 3)
	sub, ok := c.Subscribe("proj", "op-1", false)
	if !ok {
		t.Fatal("a finished operation still resolves (the tail is readable)")
	}
	frames := []consoleChunk{}
	for chunk := range sub.Events {
		frames = append(frames, chunk)
	}
	if len(frames) != 1 || frames[0].Chunk != "out" {
		t.Fatalf("backlog must replay on a finished operation, got %+v", frames)
	}
}

func TestConsoleCancelStopsDelivery(t *testing.T) {
	c := newTestConsole(64, 10)
	sub, _ := c.Subscribe("proj", "op-1", true)
	sub.Cancel()
	c.Write("proj", "op-1", "stdout", []byte("after-cancel"))
	c.Done("proj", "op-1", 0)
	select {
	case _, ok := <-sub.Events:
		if ok {
			t.Fatal("a canceled subscription must not deliver chunks")
		}
	default:
	}
	// Cancel is idempotent and must not panic on an already-removed channel.
	sub.Cancel()
}

func TestConsoleSlowSubscriberIsDroppedNotBlocking(t *testing.T) {
	c := newTestConsole(64, 10)
	sub, _ := c.Subscribe("proj", "op-1", true)
	// Fill the subscriber buffer, then keep writing: the write path must
	// never block on the stalled consumer and must never corrupt the tail.
	for i := 0; i < consoleSubBuffer*2; i++ {
		c.Write("proj", "op-1", "stdout", []byte("x\n"))
	}
	c.Done("proj", "op-1", 0)
	state, _ := c.Snapshot("proj", "op-1")
	if state.StdoutTail != strings.Repeat("x\n", 10) || state.Lines != 10 {
		t.Fatalf("the tail budget stays intact for stalled readers, got %d lines: %q", state.Lines, state.StdoutTail)
	}
	<-sub.Events // first buffered frame arrives; the rest were dropped
	for range sub.Events {
	}
}

func TestConsoleConcurrentWritesAreRaceFree(t *testing.T) {
	c := newTestConsole(1<<10, 500)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.Write("proj", "op-1", "stdout", []byte(strings.Repeat("a", 100)))
			}
		}()
	}
	wg.Wait()
	if _, ok := c.Snapshot("proj", "op-1"); !ok {
		t.Fatal("the operation must survive concurrent writes")
	}
}

// ── Sweep ────────────────────────────────────────────────────────────────────

func TestConsoleSweepDropsIdleUnsubscribedEntries(t *testing.T) {
	c := newTestConsole(64, 10)
	clock := 0
	c.now = func() time.Time { return time.Unix(int64(clock), 0) }
	c.Write("proj", "op-idle", "stdout", []byte("x"))
	clock = int(consoleIdleSweep.Seconds()) + 1
	c.sweep()
	if _, ok := c.Snapshot("proj", "op-idle"); ok {
		t.Fatal("an idle, unsubscribed entry must be swept")
	}
	c.Write("proj", "op-fresh", "stdout", []byte("y"))
	clock = int(consoleIdleSweep.Seconds()) + 1
	c.Write("proj", "op-fresh", "stdout", []byte("z")) // refreshes lastSeen
	c.sweep()
	if _, ok := c.Snapshot("proj", "op-fresh"); !ok {
		t.Fatal("an entry with fresh writes must survive the sweep")
	}
}

// ── Hook ─────────────────────────────────────────────────────────────────────

func TestConsoleHookWiring(t *testing.T) {
	c := newTestConsole(64, 10)
	hook := c.Hook("proj", "op-1")
	if hook == nil {
		t.Fatal("a registry with an operation id must produce a hook")
	}
	hook("stdout", []byte("via-hook"))
	c.Write("proj", "op-1", "stderr", []byte("err"))
	state, _ := c.Snapshot("proj", "op-1")
	if state.StdoutTail != "via-hook" || state.StderrTail != "err" {
		t.Fatalf("hook chunks must land in the right streams, got %q / %q", state.StdoutTail, state.StderrTail)
	}
	if c.Hook("proj", "") != nil {
		t.Fatal("no operation id means no hook — nil keeps the classic behavior")
	}
	var nilConsole *Console
	if nilConsole.Hook("proj", "op-1") != nil {
		t.Fatal("a nil registry must produce a nil hook (tests, legacy path)")
	}
}

func TestConsoleNilReceiverIsSafe(t *testing.T) {
	var c *Console
	c.Write("proj", "op-1", "stdout", []byte("x"))
	c.Done("proj", "op-1", 0)
	if _, ok := c.Snapshot("proj", "op-1"); ok {
		t.Fatal("a nil registry resolves nothing")
	}
	if _, ok := c.Subscribe("proj", "op-1", false); ok {
		t.Fatal("a nil registry subscribes nobody")
	}
}

// ── Journal isolation (§11.1: output never becomes an event) ─────────────────

// streamingSandbox feeds the registry the way the docker runner does — via
// sandbox.Request.OnOutput while the command runs — then answers with the
// final result.
type streamingSandbox struct{ root string }

func (s *streamingSandbox) SandboxRoot() string                          { return s.root }
func (s *streamingSandbox) ResolveWorkspace(path string) (string, error) { return path, nil }

func (s *streamingSandbox) Execute(_ context.Context, request sandbox.Request) (sandbox.Result, error) {
	if request.OnOutput != nil {
		request.OnOutput("stdout", []byte("hello-from-command\n"))
		request.OnOutput("stderr", []byte("warn-from-command\n"))
	}
	return sandbox.Result{Output: "hello-from-command\n", ExitCode: 0}, nil
}

func TestConsoleOutputNeverEntersTheJournal(t *testing.T) {
	outbox := &capturedOutbox{}
	activities := &Activities{
		Events:  outbox,
		Sandbox: &streamingSandbox{root: t.TempDir()},
		Console: newTestConsole(64, 10),
	}
	if _, err := activities.RunTool(t.Context(), ToolRequest{
		RunID: "run-console", OperationID: "frame-1/call-5", Project: "proj",
		Name:      "run_command",
		Arguments: map[string]any{"command": []string{"echo", "hi"}},
	}); err != nil {
		t.Fatalf("run_command must succeed: %v", err)
	}
	// §11.1: the console stream adds no journaling of its own — at the
	// activity layer run_command enqueues nothing (tool envelopes are the
	// workflow's business, pre-existing), so the registry is the only place
	// the output bytes exist while the command runs.
	if len(outbox.events) != 0 {
		t.Fatalf("run_command must not journal anything at the activity layer, got %d events", len(outbox.events))
	}
	// The registry still holds the tail for the live console; the done state
	// is resolved with the exit code.
	state, ok := activities.Console.Snapshot("proj", "frame-1/call-5")
	if !ok {
		t.Fatal("the operation's console must exist while the process lives")
	}
	if state.StdoutTail != "hello-from-command\n" || !state.Done || state.ExitCode == nil || *state.ExitCode != 0 {
		t.Fatalf("the tail and done state must serve the console endpoints, got %+v", state)
	}
}
