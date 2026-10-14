package agent

import (
	"strings"
	"sync"
	"time"
)

// TokenBus is an ephemeral, in-memory pub/sub for live model output. Tokens
// are not durable facts and never enter the journal (the event model forbids
// per-token events); the journal keeps turn-level model.completed /
// model.text_delta records while this bus carries the live stream to
// subscribed UI clients. It exists for the single-process kernel: with
// multi-node workers the bus needs a real transport (see docs/ROADMAP.md).
type TokenBus struct {
	mu   sync.Mutex
	runs map[string]*tokenRun
}

// TokenEvent is one SSE frame of the ephemeral token stream.
type TokenEvent struct {
	Type      string `json:"type"` // "snapshot" | "delta" | "done"
	Turn      int    `json:"turn,omitempty"`
	Text      string `json:"text,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	// Tool/Args carry live tool-call streaming: most agent turns produce no
	// answer text at all — the model streams tool-call arguments (JSON) for the
	// whole call. Without publishing those the UI reports "waiting for the
	// first token" while tokens flow invisibly. Args is never rendered as
	// answer content; it feeds liveness/token-rate estimates only.
	Tool string `json:"tool,omitempty"`
	Args string `json:"args,omitempty"`
}

// maxLiveText bounds the replayed live text per run so a runaway generation
// cannot grow the bus without limit. The journal keeps the full record.
const maxLiveText = 256 * 1024

// idleSweep is how long an untouched run entry survives without subscribers;
// entries of crashed runs (nobody publishes, nobody subscribes) are dropped
// lazily on the next Subscribe.
const idleSweep = 30 * time.Minute

type tokenRun struct {
	mu        sync.Mutex
	turn      int
	text      strings.Builder
	reasoning strings.Builder
	tool      string
	args      strings.Builder
	subs      map[chan TokenEvent]struct{}
	closed    bool
	lastSeen  time.Time
}

func NewTokenBus() *TokenBus {
	return &TokenBus{runs: make(map[string]*tokenRun)}
}

func (b *TokenBus) run(runID string) *tokenRun {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[runID]
	if !ok {
		r = &tokenRun{subs: make(map[chan TokenEvent]struct{}), lastSeen: time.Now()}
		b.runs[runID] = r
	}
	r.lastSeen = time.Now()
	return r
}

// resetLocked starts a new live segment: a turn change drops the accumulated
// text, reasoning and tool args — the UI renders one live segment per model call.
func (r *tokenRun) resetLocked(turn int) {
	r.turn = turn
	r.text.Reset()
	r.reasoning.Reset()
	r.tool = ""
	r.args.Reset()
}

func (r *tokenRun) broadcastLocked(event TokenEvent) {
	for ch := range r.subs {
		select {
		case ch <- event:
		default: // a slow consumer must not stall the model stream
		}
	}
}

// Publish appends a live delta for the run. A turn change resets the
// accumulated text: the UI renders one live segment per model call.
func (b *TokenBus) Publish(runID string, turn int, text, reasoning string) {
	r := b.run(runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.turn != turn {
		r.resetLocked(turn)
	}
	if text != "" && r.text.Len() < maxLiveText {
		r.text.WriteString(text)
	}
	if reasoning != "" && r.reasoning.Len() < maxLiveText {
		r.reasoning.WriteString(reasoning)
	}
	r.broadcastLocked(TokenEvent{Type: "delta", Turn: turn, Text: text, Reasoning: reasoning})
}

// PublishTool appends a live tool-call delta. Name is set on the first chunk
// that carries it; later chunks stream argument fragments. Text turns and tool
// turns share the turn-reset semantics.
func (b *TokenBus) PublishTool(runID string, turn int, name, argsDelta string) {
	r := b.run(runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.turn != turn {
		r.resetLocked(turn)
	}
	if name != "" {
		r.tool = name
	}
	if argsDelta != "" && r.args.Len() < maxLiveText {
		r.args.WriteString(argsDelta)
	}
	r.broadcastLocked(TokenEvent{Type: "delta", Turn: turn, Tool: r.tool, Args: argsDelta})
}

// Close marks the run as finished: subscribers receive a done event and late
// subscribers get done immediately. The closed entry is kept for a while so
// reconnecting UIs learn the run ended; the idle sweep removes it later.
func (b *TokenBus) Close(runID string) {
	r := b.run(runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for ch := range r.subs {
		select {
		case ch <- TokenEvent{Type: "done"}:
		default:
		}
		close(ch)
	}
	r.subs = nil
	r.closed = true
}

// Subscribe returns the live channel, a snapshot of the current turn's text
// (so a reconnecting UI does not miss what already streamed) and a cancel
// func. Cancel always drops the entry's subscriber; Close terminates it.
func (b *TokenBus) Subscribe(runID string) (<-chan TokenEvent, TokenEvent, func()) {
	b.sweep()
	r := b.run(runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := TokenEvent{Type: "snapshot", Turn: r.turn, Text: r.text.String(), Reasoning: r.reasoning.String(), Tool: r.tool, Args: r.args.String()}
	if r.closed {
		return nil, TokenEvent{Type: "done"}, func() {}
	}
	ch := make(chan TokenEvent, 512)
	r.subs[ch] = struct{}{}
	cancel := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.subs == nil {
			return
		}
		if _, ok := r.subs[ch]; !ok {
			return
		}
		delete(r.subs, ch)
		close(ch)
	}
	return ch, snapshot, cancel
}

// sweep drops entries idle past idleSweep with no subscribers: dead runs of
// crashed or delegated-only workflows whose nobody will ever close.
func (b *TokenBus) sweep() {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, r := range b.runs {
		r.mu.Lock()
		idle := len(r.subs) == 0 && now.Sub(r.lastSeen) > idleSweep
		r.mu.Unlock()
		if idle {
			delete(b.runs, id)
		}
	}
}
