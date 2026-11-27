package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

type conversationIngressStore interface {
	CreateConversation(context.Context, *workspace.Conversation) error
	GetConversation(context.Context, string) (workspace.Conversation, error)
	BindConversationSource(context.Context, string, string, string, string) error
	ResolveConversationSource(context.Context, string, string, string) (workspace.Conversation, error)
	AddConversationParticipant(context.Context, string, string, string) error
	AppendConversationMessage(context.Context, *workspace.ConversationMessage) (workspace.ConversationMessage, bool, error)
}

// persistChannelMessage puts a normalized channel event in the durable
// conversation log before routing or side effects. The source room mapping
// keeps one conversation per registered transport chat; message event IDs
// provide cross-restart idempotency.
func (l *channelInboundLoop) persistChannelMessage(ctx context.Context, message agent.InboundMessage, actorID string) error {
	store, ok := l.store.(conversationIngressStore)
	if !ok {
		if wrapper, wrapped := l.store.(interface {
			conversationStore() (conversationIngressStore, bool)
		}); wrapped {
			store, ok = wrapper.conversationStore()
		}
	}
	if !ok {
		return nil // compatibility for older test stores
	}
	if message.ID == "" || message.Channel == "" || message.ChatID == "" || actorID == "" {
		return errors.New("channel message is missing stable source, chat, event or actor identity")
	}
	conversation, err := store.ResolveConversationSource(ctx, "", message.Channel, message.ChatID)
	if errors.Is(err, workspace.ErrNotFound) {
		conversationID := stableConversationID(message.Channel, message.ChatID)
		candidate := &workspace.Conversation{ID: conversationID, OwnerID: actorID}
		if createErr := store.CreateConversation(ctx, candidate); createErr != nil && !errors.Is(createErr, workspace.ErrConflict) {
			return createErr
		}
		if bindErr := store.BindConversationSource(ctx, "", message.Channel, message.ChatID, conversationID); bindErr != nil {
			if !errors.Is(bindErr, workspace.ErrConflict) {
				return bindErr
			}
		}
		conversation, err = store.ResolveConversationSource(ctx, "", message.Channel, message.ChatID)
	}
	if err != nil {
		return err
	}
	if err := store.AddConversationParticipant(ctx, conversation.ID, "human", actorID); err != nil {
		return err
	}
	content, err := json.Marshal(map[string]any{
		"text":      message.Text,
		"encrypted": message.Encrypted,
	})
	if err != nil {
		return err
	}
	attachments, err := json.Marshal(message.Files)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]any{
		"transport_timestamp": message.Timestamp,
		"reply_to":            message.ReplyTo,
	})
	if err != nil {
		return err
	}
	_, _, err = store.AppendConversationMessage(ctx, &workspace.ConversationMessage{
		ID:              stableConversationMessageID(message.Channel, message.ChatID, message.ID),
		OrganizationID:  conversation.OrganizationID,
		ConversationID:  conversation.ID,
		SenderType:      "human",
		SenderID:        actorID,
		MessageType:     "text",
		Content:         content,
		Attachments:     attachments,
		Source:          message.Channel,
		SourceMessageID: message.ID,
		SourceThreadID:  message.SourceThreadID,
		Metadata:        metadata,
	})
	return err
}

func stableConversationID(source, sourceChatID string) string {
	return "conv-" + stableConversationKey(source, sourceChatID)
}

func stableConversationMessageID(source, sourceChatID, sourceMessageID string) string {
	return "msg-" + stableConversationKey(source, sourceChatID, sourceMessageID)
}

func stableConversationKey(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
