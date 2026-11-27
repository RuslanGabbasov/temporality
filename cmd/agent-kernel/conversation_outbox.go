package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/presentation"
	"github.com/temporality-project/temporality/workspace"
)

// conversationTurnStore is the surface the runtime-first channel dispatch
// needs from the workspace store: the turn lifecycle plus the outbox write.
// The production *workspace.Store satisfies it wholesale; narrow test fakes
// opt out and keep the legacy intake path.
type conversationTurnStore interface {
	TryAcquireConversationTurn(context.Context, string, int64) (func(), bool, error)
	GetConversation(context.Context, string) (workspace.Conversation, error)
	GetAgent(context.Context, string) (workspace.Agent, error)
	ConversationTransportSource(context.Context, string) (string, string, error)
	ListConversationMessages(context.Context, string, int64, int) ([]workspace.ConversationMessage, error)
	AppendConversationMessage(context.Context, *workspace.ConversationMessage) (workspace.ConversationMessage, bool, error)
	AppendConversationMessageWithOutbox(context.Context, *workspace.ConversationMessage, *workspace.ConversationOutboxEvent) (workspace.ConversationMessage, bool, error)
	SetConversationMessageStatus(context.Context, string, string) error
	LinkConversationTask(context.Context, string, string, string, string, string, string) error
	FindConversationTaskByRunID(context.Context, string) (workspace.ConversationTask, error)
	ListConversationTasks(context.Context, string) ([]workspace.ConversationTask, error)
	AttachConversationLead(context.Context, string, string, string) error
}

// runAnswerPayload carries an outbox run_answer event: the durable promise to
// deliver one agent run's final answer to the chat it was requested from.
type runAnswerPayload struct {
	RunID         string `json:"run_id"`
	Project       string `json:"project,omitempty"`
	Channel       string `json:"channel"`
	Address       string `json:"address"`
	InboundRowID  string `json:"inbound_row_id,omitempty"`
	MarkAnswered  bool   `json:"mark_answered,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// conversationMessagePayload carries an outbox conversation_message event:
// one lead-agent direct turn answer for the transport chat of the talk.
type conversationMessagePayload struct {
	Text            string `json:"text"`
	Channel         string `json:"channel"`
	Address         string `json:"address"`
	Thread          string `json:"thread,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
}

// outboxStore is the delivery loop's persistence surface.
type outboxStore interface {
	ClaimDueConversationOutboxEvents(ctx context.Context, limit int, lease time.Duration) ([]workspace.ConversationOutboxEvent, error)
	CompleteConversationOutboxEvent(ctx context.Context, id string) error
	FailConversationOutboxEvent(ctx context.Context, id string, attempt int, reason error) error
	RetryConversationOutboxEvent(ctx context.Context, id string, delay time.Duration) error
	ListStaleConversationTurns(ctx context.Context, olderThan time.Duration, limit int) ([]workspace.ConversationMessage, error)
}

// outboxStep is the outcome of one delivery cycle.
type outboxStep int

const (
	outboxDone      outboxStep = iota // delivered — mark terminal
	outboxRetrySoon                   // not due yet (run still live) — recheck shortly
	outboxFailed                      // attempt failed — exponential backoff
)

// conversationOutboxLoop drains the durable outbox and drives the per-channel
// deliveries. It owns nothing transport-specific: the channel loops keep
// their drivers and settings, the outbox only sequences who delivers what
// and records how it went (docs/conversational-runtime.md §13.3 — повторная
// доставка, идемпотентность, фиксация ошибок, восстановление).
type conversationOutboxLoop struct {
	store  outboxStore
	lookup func(channel string) *channelInboundLoop
	onTurn func(message workspace.ConversationMessage) // stale-turn recovery hook
	log    *slog.Logger
}

// outboxPollEvery paces both the delivery claims and the live-run rechecks:
// the old in-process watcher polled every 5s, and channel users expect the
// same delivery freshness.
const outboxPollEvery = 5 * time.Second

// outboxLease bounds one claim: a crashed kernel's 'processing' rows become
// reclaimable after it, which is the restart-recovery guarantee.
const outboxLease = time.Minute

// staleTurnGrace keeps live turns (a model call can take minutes) out of the
// recovery set; only turns that were dispatched and then orphaned requeue.
const staleTurnGrace = 5 * time.Minute

func (l *conversationOutboxLoop) run(ctx context.Context) {
	ticker := time.NewTicker(outboxPollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		l.cycle(ctx)
	}
}

func (l *conversationOutboxLoop) cycle(ctx context.Context) {
	events, err := l.store.ClaimDueConversationOutboxEvents(ctx, 10, outboxLease)
	if err != nil {
		l.log.Warn("outbox claim", "error", err)
		return
	}
	for _, event := range events {
		l.deliver(ctx, event)
	}
	if l.onTurn != nil {
		stale, err := l.store.ListStaleConversationTurns(ctx, staleTurnGrace, 5)
		if err != nil {
			l.log.Warn("stale conversation turns", "error", err)
			return
		}
		for _, message := range stale {
			l.onTurn(message)
		}
	}
}

func (l *conversationOutboxLoop) deliver(ctx context.Context, event workspace.ConversationOutboxEvent) {
	loop, payload, err := l.resolveEventTransport(event)
	if err != nil {
		l.fail(ctx, event, err)
		return
	}
	var step outboxStep
	var stepErr error
	switch typed := payload.(type) {
	case runAnswerPayload:
		step, stepErr = loop.deliverOutboxRunAnswer(ctx, typed)
	case conversationMessagePayload:
		step, stepErr = loop.deliverOutboxConversationMessage(ctx, typed)
	default:
		l.fail(ctx, event, fmt.Errorf("unknown outbox event payload for %s", event.EventType))
		return
	}
	switch step {
	case outboxDone:
		if err := l.store.CompleteConversationOutboxEvent(ctx, event.ID); err != nil {
			l.log.Warn("outbox complete", "event", event.ID, "error", err)
		}
	case outboxRetrySoon:
		// A live run must not poll forever: the old in-process watcher gave up
		// after two hours, and the durable path honors the same ceiling.
		if time.Since(event.CreatedAt) > 2*time.Hour {
			l.fail(ctx, event, errors.New("run answer delivery gave up after 2h"))
			return
		}
		if err := l.store.RetryConversationOutboxEvent(ctx, event.ID, outboxPollEvery); err != nil {
			l.log.Warn("outbox requeue", "event", event.ID, "error", err)
		}
	case outboxFailed:
		l.fail(ctx, event, stepErr)
	}
}

// resolveEventTransport finds the channel loop and decodes the payload for
// one event. An event without a live transport fails permanently only after
// the normal retry ladder — a channel configured a minute later must still
// get its backlog.
func (l *conversationOutboxLoop) resolveEventTransport(event workspace.ConversationOutboxEvent) (*channelInboundLoop, any, error) {
	var channel string
	var payload any
	switch event.EventType {
	case workspace.OutboxRunAnswer:
		var p runAnswerPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil, nil, fmt.Errorf("decode run_answer payload: %w", err)
		}
		channel, payload = p.Channel, p
	case workspace.OutboxConversationMessage:
		var p conversationMessagePayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil, nil, fmt.Errorf("decode conversation_message payload: %w", err)
		}
		channel, payload = p.Channel, p
	default:
		return nil, nil, fmt.Errorf("unknown outbox event_type %q", event.EventType)
	}
	loop := l.lookup(channel)
	if loop == nil {
		return nil, nil, fmt.Errorf("%w: %q", errOutboxChannelUnavailable, channel)
	}
	return loop, payload, nil
}

func (l *conversationOutboxLoop) fail(ctx context.Context, event workspace.ConversationOutboxEvent, reason error) {
	if err := l.store.FailConversationOutboxEvent(ctx, event.ID, event.Attempts+1, reason); err != nil {
		l.log.Warn("outbox fail", "event", event.ID, "error", err)
	}
	l.log.Warn("outbox delivery attempt failed", "event", event.ID, "type", event.EventType, "attempt", event.Attempts+1, "error", reason)
}

// errOutboxChannelUnavailable marks a permanent transport gap; the retry
// ladder still applies (a channel registered after the event was enqueued
// must receive its backlog), but the error is logged distinctly.
var errOutboxChannelUnavailable = errors.New("no transport loop for channel")

// deliverOutboxRunAnswer runs ONE poll/deliver cycle for a run_answer event.
// A live run asks for a timed recheck; a terminal run delivers — the send is
// the shared channel send with its rich-text fallback, presentations and
// file links, and the conversation record lands before the transport attempt
// so the result is durable in the talk even when the chat hiccups.
func (l *channelInboundLoop) deliverOutboxRunAnswer(ctx context.Context, payload runAnswerPayload) (outboxStep, error) {
	stored, sErr := l.settings(ctx)
	if sErr != nil {
		return outboxFailed, sErr
	}
	settings := stored.OverlayEnv()
	project := payload.Project
	if project == "" {
		project = IntakeProjectID
	}
	run, err := l.currentRun(ctx, project, payload.RunID)
	if err != nil {
		// A missing run row means the workflow has not persisted state yet —
		// poll again; anything else is a resolver outage, also retryable.
		return outboxRetrySoon, nil
	}
	if liveRunStatus(run.Status) {
		return outboxRetrySoon, nil
	}
	if run.Answer == "" {
		l.log.Warn("run finished without an answer", "run", payload.RunID, "status", run.Status)
		l.recordConversationRunResult(ctx, payload, run)
		if payload.MarkAnswered && payload.InboundRowID != "" {
			_ = l.store.UpdateInboundMessage(ctx, payload.InboundRowID, workspace.InboundMessageUpdate{Status: workspace.InboundStatusAnswered})
		}
		return outboxDone, nil
	}
	l.recordConversationRunResult(ctx, payload, run)
	address := payload.Address
	if err := l.sendRunAnswer(ctx, settings, address, project, payload, run); err != nil {
		return outboxFailed, err
	}
	l.log.Info("run answer delivered", "channel", payload.Channel, "chat", address, "run", payload.RunID)
	return outboxDone, nil
}

// sendRunAnswer posts the final answer with the rich-text-then-plain
// fallback (docs/channels.md §13) and rides the visual/file extras. A send
// failure is retryable: the outbox redelivers with backoff instead of the
// old watcher's log-and-drop.
func (l *channelInboundLoop) sendRunAnswer(ctx context.Context, settings agent.ChannelSettings, address, project string, payload runAnswerPayload, run workspace.Run) error {
	answerBody := presentation.StripFences(run.Answer)
	deliverCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	var dErr error
	rich, canRich := l.driver.(agent.RichTextPoster)
	if canRich {
		_, dErr = rich.SendTextRich(deliverCtx, l.httpClient, settings.Channels[payload.Channel], address, agent.OutboundText{Body: answerBody})
		if dErr != nil {
			l.log.Warn("rich answer delivery, falling back to text", "channel", payload.Channel, "error", dErr)
		}
	}
	if !canRich || dErr != nil {
		dErr = l.driver.SendText(deliverCtx, l.httpClient, settings.Channels[payload.Channel], address, agent.OutboundText{Body: answerBody})
	}
	cancel()
	if dErr != nil {
		return dErr
	}
	l.deliverRunPresentations(ctx, settings, address, payload.RunID)
	l.deliverRunFileLinks(ctx, settings, address, project, answerBody)
	if payload.MarkAnswered && payload.InboundRowID != "" {
		_ = l.store.UpdateInboundMessage(ctx, payload.InboundRowID, workspace.InboundMessageUpdate{Status: workspace.InboundStatusAnswered})
	}
	return nil
}

// recordConversationRunResult closes the conversation side of a linked run
// (docs/conversational-runtime.md Phase 4 — task events, результаты в том же
// Conversation): the link flips to its terminal status, the answer lands as
// an agent message (idempotent by the run id), and the lead agent that did
// the work becomes the conversation's voice for future direct turns.
func (l *channelInboundLoop) recordConversationRunResult(ctx context.Context, payload runAnswerPayload, run workspace.Run) {
	conversation := workspace.Conversation{}
	resolver, canResolve := l.store.(conversationIngressStore)
	if canResolve {
		if resolved, err := resolver.ResolveConversationSource(ctx, "", payload.Channel, payload.Address); err == nil {
			conversation = resolved
		}
	}
	if payload.ConversationID != "" && conversation.ID == "" {
		conversation.ID = payload.ConversationID
	}
	if conversation.ID == "" {
		return
	}
	store, canStore := l.store.(conversationTurnStore)
	if !canStore {
		return
	}
	// The lead binding is fill-only: the first agent that served this chat
	// keeps the voice (a later routed run from a different project must not
	// silently re-target the conversation).
	if run.AgentID != "" {
		if err := store.AttachConversationLead(ctx, conversation.ID, run.AgentID, run.ProjectID); err != nil && !errors.Is(err, workspace.ErrNotFound) {
			l.log.Warn("conversation lead attach", "conversation", conversation.ID, "run", payload.RunID, "error", err)
		}
	}
	task, err := store.FindConversationTaskByRunID(ctx, payload.RunID)
	if err == nil {
		status := "completed"
		switch run.Status {
		case "failed":
			status = "failed"
		case "cancelled":
			status = "cancelled"
		}
		if task.Status != status {
			if err := store.LinkConversationTask(ctx, conversation.ID, task.OriginMessageID, task.TaskID, task.RunID, task.ProjectID, status); err != nil {
				l.log.Warn("conversation task settle", "conversation", conversation.ID, "run", payload.RunID, "error", err)
			}
		}
	} else if !errors.Is(err, workspace.ErrNotFound) {
		l.log.Warn("conversation task lookup", "conversation", conversation.ID, "run", payload.RunID, "error", err)
		return
	}
	// The result message: the answer itself when the run produced one, an
	// honest status line when it did not. Idempotent — the (source,
	// source_message_id) pair pins it to the run.
	text := run.Answer
	if text == "" {
		text = fmt.Sprintf("Задача завершилась со статусом %s", run.Status)
		if run.Error != "" {
			text += ": " + run.Error
		}
	}
	content, err := json.Marshal(map[string]string{"text": presentation.StripFences(text)})
	if err != nil {
		return
	}
	if _, _, err := store.AppendConversationMessage(ctx, &workspace.ConversationMessage{
		ID:              stableConversationMessageID("task-result", conversation.ID, payload.RunID),
		OrganizationID:  conversation.OrganizationID,
		ConversationID:  conversation.ID,
		SenderType:      "agent",
		SenderID:        run.AgentID,
		MessageType:     "text",
		Content:         content,
		Source:          "task_result",
		SourceMessageID: payload.RunID,
		Status:          "completed",
	}); err != nil && !errors.Is(err, workspace.ErrConflict) {
		l.log.Warn("conversation result message", "conversation", conversation.ID, "run", payload.RunID, "error", err)
	}
}

// deliverOutboxConversationMessage posts one lead-agent answer to the chat,
// continuing the thread the human wrote in when the transport carries one.
func (l *channelInboundLoop) deliverOutboxConversationMessage(ctx context.Context, payload conversationMessagePayload) (outboxStep, error) {
	stored, sErr := l.settings(ctx)
	if sErr != nil {
		return outboxFailed, sErr
	}
	settings := stored.OverlayEnv()
	channelSettings := settings.Channels[payload.Channel]
	if poster, ok := l.driver.(agent.ThreadedTextPoster); ok {
		if _, err := poster.SendTextThreaded(ctx, l.httpClient, channelSettings, payload.Address, agent.OutboundText{Body: payload.Text, Thread: payload.Thread}); err != nil {
			return outboxFailed, err
		}
	} else if err := l.driver.SendText(ctx, l.httpClient, channelSettings, payload.Address, agent.OutboundText{Body: payload.Text}); err != nil {
		return outboxFailed, err
	}
	l.log.Info("conversation message delivered", "channel", payload.Channel, "chat", payload.Address)
	return outboxDone, nil
}
