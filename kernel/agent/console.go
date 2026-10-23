package agent

import (
	"bytes"
	"sync"
	"time"
)

// Console is the ephemeral, in-memory registry of run_command output
// (docs/specs/trassirovka-vkladka-zapuski/v2-consolidated.md §6). It exists
// for the live-console special scenario: subscribers watch stdout/stderr
// chunks while the command runs, late readers get the tail buffer, and
// everything evaporates with the process.
//
// Invariants this type owns:
//   - No persistence (§11.1, deferred by the customer): chunks never enter
//     the journal, the database or files — the registry is the only home of
//     the bytes, and it lives and dies with the kernel process.
//   - No redaction (§6, §7): the console stream is not a private channel —
//     sandboxes hold no secrets, so raw output passes through untouched.
//     The secret.* display invariant (P-2) is upheld upstream: secret values
//     never reach the sandbox environment's command output at all.
//   - stdout and stderr are buffered separately, matching the runner's own
//     split buffers (sandbox.Request.OnOutput).
//
// Topology note: the activity worker and the HTTP server share one process
// (the same single-process kernel assumption as TokenBus), so an in-memory
// registry keyed by operation_id connects them without a broker.
type Console struct {
	mu  sync.Mutex
	ops map[consoleKey]*consoleOp
	// limit is the per-stream tail budget in bytes; maxLines caps the tail in
	// whole lines. Defaults below carry the §6 proposal — the buffer size is
	// an open question (§11.2); the defaults are documented, not settled.
	limit    int
	maxLines int
	now      func() time.Time
}

// consoleTailBytes is the default per-stream tail budget: 256 KiB, the §6
// proposal (open question §11.2 — a config knob may revisit it later).
const consoleTailBytes = 256 << 10

// consoleTailLines is the default per-stream line cap: 2000 lines, the §6
// proposal (open question §11.2).
const consoleTailLines = 2000

// consoleIdleSweep is how long a finished, unsubscribed console entry
// survives so late readers still see the tail; idle running entries of
// crashed activities are dropped on the same clock (the TokenBus precedent).
const consoleIdleSweep = 6 * time.Hour

// consoleSubBuffer bounds one subscriber's pending chunks: a slow consumer
// must never stall the command's output path, so an overflow drops frames
// for that consumer only (the TokenBus broadcast policy).
const consoleSubBuffer = 512

// consoleKey scopes one operation to its project: the REST layer authorizes
// by ?project=, so two projects must never see each other's streams even
// if an operation id collided.
type consoleKey struct {
	project string
	op      string
}

// consoleOp is one run_command execution's ephemeral console state.
type consoleOp struct {
	mu       sync.Mutex
	stdout   consoleStream
	stderr   consoleStream
	subs     map[chan consoleChunk]struct{}
	done     bool
	exitCode *int
	// lastSeen is owned by mu (op.mu): Write, Done, Subscribe, op and sweep
	// touch it only while holding it — the registry lock c.mu is never used
	// for this field, the two locks must not protect the same data.
	lastSeen time.Time
}

// consoleChunk is one fan-out frame: a raw piece of one stream.
type consoleChunk struct {
	Stream string // "stdout" | "stderr"
	Chunk  string
}

// ConsoleState is the REST projection of one operation's console
// (GET /v1/agent/console/{operationID}). bytes/lines describe what the tails
// currently hold; dropped_* count what overflow already evicted — the §6
// requirement that overflow keeps counters, not silence.
type ConsoleState struct {
	OperationID  string `json:"operation_id"`
	ConsoleURI   string `json:"console_uri"`
	StdoutTail   string `json:"stdout_tail"`
	StderrTail   string `json:"stderr_tail"`
	Bytes        int64  `json:"bytes"`
	Lines        int    `json:"lines"`
	DroppedBytes int64  `json:"dropped_bytes"`
	DroppedLines int64  `json:"dropped_lines"`
	Truncated    bool   `json:"truncated"`
	Done         bool   `json:"done"`
	ExitCode     *int   `json:"exit_code"`
}

// ConsoleSubscription is a live tap into one operation's output. Events
// closes when the operation finishes — after every buffered chunk drained —
// so the SSE handler turns the close into the done frame.
type ConsoleSubscription struct {
	Events <-chan consoleChunk
	Cancel func()
}

// NewConsole builds the registry with the §6 default budgets (open question
// §11.2: 256 KiB / 2000 lines per stream).
func NewConsole() *Console {
	return newConsole(consoleTailBytes, consoleTailLines)
}

func newConsole(limit, maxLines int) *Console {
	return &Console{
		ops:      make(map[consoleKey]*consoleOp),
		limit:    limit,
		maxLines: maxLines,
		now:      time.Now,
	}
}

// Hook returns the sandbox.Request.OnOutput callback for one run_command
// execution, or nil when there is nothing to stream to (no registry, no
// operation id) — nil keeps the classic wait-for-completion behavior.
func (c *Console) Hook(project, operationID string) func(stream string, chunk []byte) {
	if c == nil || operationID == "" {
		return nil
	}
	return func(stream string, chunk []byte) {
		c.Write(project, operationID, stream, chunk)
	}
}

// Write appends one chunk to the operation's named stream and fans it out to
// live subscribers. Unknown stream names and writes after completion are
// ignored — the registry never guesses where a byte belongs.
func (c *Console) Write(project, operationID, stream string, chunk []byte) {
	if c == nil || operationID == "" || len(chunk) == 0 {
		return
	}
	if stream != "stdout" && stream != "stderr" {
		return
	}
	op := c.op(project, operationID)
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.done {
		return
	}
	if stream == "stdout" {
		op.stdout.append(chunk, c.limit, c.maxLines)
	} else {
		op.stderr.append(chunk, c.limit, c.maxLines)
	}
	op.lastSeen = c.now()
	frame := consoleChunk{Stream: stream, Chunk: string(chunk)}
	for ch := range op.subs {
		// A slow consumer is dropped, never waited on: the command's output
		// path must not block on a stalled SSE reader.
		select {
		case ch <- frame:
		default:
		}
	}
}

// Done marks the operation finished: every subscriber's channel closes (the
// SSE handler emits the done frame with the exit code when it observes the
// close) and late subscribers/GETs see the final state. Idempotent.
func (c *Console) Done(project, operationID string, exitCode int) {
	if c == nil || operationID == "" {
		return
	}
	op := c.op(project, operationID)
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.done {
		return
	}
	op.done = true
	code := exitCode
	op.exitCode = &code
	for ch := range op.subs {
		// Closing wakes every reader at once; buffered chunks still drain
		// first, so the done frame always lands after the tail chunks.
		close(ch)
	}
	op.subs = nil
	op.lastSeen = c.now()
}

// Snapshot returns the current REST projection of one operation's console.
// ok=false means nobody registered it under this project — an unknown
// operation, a foreign project's id, or an entry already swept.
func (c *Console) Snapshot(project, operationID string) (ConsoleState, bool) {
	if c == nil {
		return ConsoleState{}, false
	}
	c.mu.Lock()
	op, ok := c.ops[consoleKey{project, operationID}]
	c.mu.Unlock()
	if !ok {
		return ConsoleState{}, false
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	state := ConsoleState{
		OperationID:  operationID,
		ConsoleURI:   "console://" + operationID,
		StdoutTail:   string(op.stdout.tail),
		StderrTail:   string(op.stderr.tail),
		Bytes:        int64(len(op.stdout.tail) + len(op.stderr.tail)),
		Lines:        consoleLineCount(op.stdout.tail) + consoleLineCount(op.stderr.tail),
		DroppedBytes: op.stdout.droppedBytes + op.stderr.droppedBytes,
		DroppedLines: op.stdout.droppedLines + op.stderr.droppedLines,
		Truncated:    op.stdout.droppedBytes > 0 || op.stderr.droppedBytes > 0,
		Done:         op.done,
		ExitCode:     op.exitCode,
	}
	return state, true
}

// Subscribe taps one operation's live output. With tail=false the current
// tails replay first (a reconnecting UI catches up); tail=true receives only
// new data written after the subscription. Subscribing to an operation that
// has not produced output yet is the normal lazy-UI flow (§6: the watcher
// attaches when the run_command row expands, often before the first chunk),
// so Subscribe registers the entry instead of failing it — the live frames
// start with the command's first output. ok=false is reserved for a nil
// registry; a foreign project still cannot observe the operation because
// the registry key is (project, operation_id).
func (c *Console) Subscribe(project, operationID string, tail bool) (ConsoleSubscription, bool) {
	if c == nil {
		return ConsoleSubscription{}, false
	}
	c.sweep()
	key := consoleKey{project, operationID}
	c.mu.Lock()
	op := c.ops[key]
	if op == nil {
		op = &consoleOp{subs: make(map[chan consoleChunk]struct{}), lastSeen: c.now()}
		c.ops[key] = op
	}
	c.mu.Unlock()
	op.mu.Lock()
	op.lastSeen = c.now()
	defer op.mu.Unlock()
	ch := make(chan consoleChunk, consoleSubBuffer)
	if !tail {
		if len(op.stdout.tail) > 0 {
			ch <- consoleChunk{Stream: "stdout", Chunk: string(op.stdout.tail)}
		}
		if len(op.stderr.tail) > 0 {
			ch <- consoleChunk{Stream: "stderr", Chunk: string(op.stderr.tail)}
		}
	}
	if op.done {
		close(ch)
		return ConsoleSubscription{Events: ch, Cancel: func() {}}, true
	}
	op.subs[ch] = struct{}{}
	cancel := func() {
		op.mu.Lock()
		defer op.mu.Unlock()
		if _, ok := op.subs[ch]; ok {
			delete(op.subs, ch)
			close(ch)
		}
	}
	return ConsoleSubscription{Events: ch, Cancel: cancel}, true
}

// op returns (creating on demand) the registry entry for one operation.
func (c *Console) op(project, operationID string) *consoleOp {
	key := consoleKey{project, operationID}
	c.mu.Lock()
	op, ok := c.ops[key]
	if !ok {
		op = &consoleOp{subs: make(map[chan consoleChunk]struct{}), lastSeen: c.now()}
		c.ops[key] = op
	}
	c.mu.Unlock()
	// The lastSeen refresh belongs under op.mu: sweeping and every other
	// reader expect it there, and taking op.mu under c.mu would invert the
	// lock order used by Write/Done/Snapshot.
	op.mu.Lock()
	op.lastSeen = c.now()
	op.mu.Unlock()
	return op
}

// sweep drops entries idle past consoleIdleSweep with no subscribers:
// finished operations whose tails nobody asked for and orphans of crashed
// activities. Live entries are refreshed by every Write and Subscribe.
func (c *Console) sweep() {
	limit := c.now().Add(-consoleIdleSweep)
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, op := range c.ops {
		op.mu.Lock()
		idle := len(op.subs) == 0 && op.lastSeen.Before(limit)
		op.mu.Unlock()
		if idle {
			delete(c.ops, key)
		}
	}
}

// consoleStream is one stream's tail: the last ≤ budget bytes that also fit
// in ≤ maxLines whole lines, plus counters of everything evicted.
type consoleStream struct {
	tail []byte
	// droppedBytes counts every byte evicted from the head (byte-budget
	// trims count raw evicted bytes; line evictions count the whole line
	// they remove). droppedLines counts evicted whole lines only — a
	// byte-trimmed head fragment was never a complete line.
	droppedBytes int64
	droppedLines int64
}

// append folds one chunk into the tail, keeping the newest bytes and whole
// lines within the budgets. The tail is always a private copy — the writer
// (the docker runner's pipe buffer) reuses its scratch, and fan-out
// snapshots must not alias it.
func (s *consoleStream) append(chunk []byte, limit, maxLines int) {
	buf := make([]byte, 0, len(s.tail)+len(chunk))
	buf = append(buf, s.tail...)
	buf = append(buf, chunk...)
	// Byte budget: keep the newest limit bytes.
	if len(buf) > limit {
		s.droppedBytes += int64(len(buf) - limit)
		buf = buf[len(buf)-limit:]
	}
	// Line budget: evict whole lines from the head while the tail holds more
	// than maxLines. A tail without a newline is one partial line — always
	// within budget, so the index below cannot fail when the loop runs.
	for consoleLineCount(buf) > maxLines {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			break
		}
		s.droppedBytes += int64(idx + 1)
		s.droppedLines++
		buf = buf[idx+1:]
	}
	s.tail = buf
}

// consoleLineCount counts lines in a tail: newline-terminated lines plus one
// for a trailing partial line (its beginning is still user-visible output).
func consoleLineCount(tail []byte) int {
	if len(tail) == 0 {
		return 0
	}
	lines := bytes.Count(tail, []byte("\n"))
	if tail[len(tail)-1] != '\n' {
		lines++
	}
	return lines
}
