package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Conversation is the durable identity for a human-agent dialogue. It is
// independent of rooms, projects and the task/run lifecycles.
type Conversation struct {
	ID                  string          `json:"id"`
	OrganizationID      string          `json:"organization_id"`
	OwnerID             string          `json:"owner_id,omitempty"`
	AgentID             string          `json:"agent_id,omitempty"`
	Status              string          `json:"status"`
	Title               string          `json:"title,omitempty"`
	LastMessageSequence int64           `json:"last_message_sequence"`
	ContextVersion      int64           `json:"context_version"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// ConversationMessage is one normalized, ordered event in a conversation.
// Content and attachments remain JSON so transports can preserve rich payloads.
type ConversationMessage struct {
	ID              string          `json:"id"`
	OrganizationID  string          `json:"organization_id"`
	ConversationID  string          `json:"conversation_id"`
	Sequence        int64           `json:"sequence"`
	ParentMessageID string          `json:"parent_message_id,omitempty"`
	SenderType      string          `json:"sender_type"`
	SenderID        string          `json:"sender_id,omitempty"`
	MessageType     string          `json:"message_type"`
	Content         json.RawMessage `json:"content"`
	Attachments     json.RawMessage `json:"attachments,omitempty"`
	Source          string          `json:"source,omitempty"`
	SourceMessageID string          `json:"source_message_id,omitempty"`
	SourceThreadID  string          `json:"source_thread_id,omitempty"`
	Status          string          `json:"status"`
	CreatedAt       time.Time       `json:"created_at"`
	ProcessedAt     *time.Time      `json:"processed_at,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

// ConversationInteraction is a routing projection of a domain operation.
// HumanRequest and ArtifactReview remain the source of truth for decisions.
type ConversationInteraction struct {
	ID               string               `json:"id"`
	ConversationID   string               `json:"conversation_id"`
	OriginMessageID  string               `json:"origin_message_id,omitempty"`
	SourceMessageID  string               `json:"source_message_id,omitempty"`
	InteractionType  string               `json:"interaction_type"`
	DomainObjectType string               `json:"domain_object_type"`
	DomainObjectID   string               `json:"domain_object_id"`
	TargetVersion    *int                 `json:"target_version,omitempty"`
	AssignedTo       string               `json:"assigned_to,omitempty"`
	Status           string               `json:"status"`
	CreatedAt        time.Time            `json:"created_at"`
	ExpiresAt        *time.Time           `json:"expires_at,omitempty"`
	ResolvedAt       *time.Time           `json:"resolved_at,omitempty"`
	Reference        InteractionReference `json:"reference"`
}

type InteractionReference struct {
	Type            string `json:"type"`
	ID              string `json:"id"`
	Version         *int   `json:"version,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
}

func (interaction ConversationInteraction) InteractionReference() InteractionReference {
	return InteractionReference{
		Type: interaction.InteractionType, ID: interaction.DomainObjectID,
		Version: interaction.TargetVersion, SourceMessageID: interaction.SourceMessageID,
	}
}

type ConversationTask struct {
	ID              string     `json:"id"`
	ConversationID  string     `json:"conversation_id"`
	OriginMessageID string     `json:"origin_message_id,omitempty"`
	TaskID          string     `json:"task_id"`
	RunID           string     `json:"run_id,omitempty"`
	ProjectID       string     `json:"project_id,omitempty"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

func (s *Store) ListConversationTasks(ctx context.Context, conversationID string) ([]ConversationTask, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, conversation_id, COALESCE(origin_message_id,''), task_id, run_id, project_id,
		 status, created_at, completed_at FROM conversation_task WHERE conversation_id=$1 ORDER BY created_at DESC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ConversationTask, 0)
	for rows.Next() {
		var item ConversationTask
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.OriginMessageID, &item.TaskID,
			&item.RunID, &item.ProjectID, &item.Status, &item.CreatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// UpsertConversationInteraction keeps the routing projection aligned with
// its domain operation while retaining the original identity and creation time.
func (s *Store) UpsertConversationInteraction(ctx context.Context, interaction ConversationInteraction) error {
	if interaction.ID == "" || interaction.ConversationID == "" || interaction.InteractionType == "" || interaction.DomainObjectType == "" || interaction.DomainObjectID == "" {
		return errors.New("interaction identity, conversation and domain object are required")
	}
	if interaction.Status == "" {
		interaction.Status = "pending"
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO conversation_interaction
		 (id, conversation_id, origin_message_id, source_message_id, interaction_type, domain_object_type, domain_object_id,
		  target_version, assigned_to, status, expires_at, resolved_at)
		 VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT (interaction_type, domain_object_id, conversation_id) DO UPDATE SET
		   origin_message_id = COALESCE(conversation_interaction.origin_message_id, EXCLUDED.origin_message_id),
		   source_message_id = COALESCE(NULLIF(EXCLUDED.source_message_id,''), conversation_interaction.source_message_id),
		   target_version = COALESCE(EXCLUDED.target_version, conversation_interaction.target_version),
		   assigned_to = NULLIF(EXCLUDED.assigned_to,''), status = EXCLUDED.status,
		   expires_at = COALESCE(EXCLUDED.expires_at, conversation_interaction.expires_at),
		   resolved_at = EXCLUDED.resolved_at`,
		interaction.ID, interaction.ConversationID, interaction.OriginMessageID, interaction.SourceMessageID,
		interaction.InteractionType, interaction.DomainObjectType, interaction.DomainObjectID, interaction.TargetVersion,
		interaction.AssignedTo, interaction.Status, interaction.ExpiresAt, interaction.ResolvedAt)
	return err
}

func (s *Store) ListConversationInteractions(ctx context.Context, conversationID string) ([]ConversationInteraction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, conversation_id, COALESCE(origin_message_id,''), source_message_id, interaction_type, domain_object_type,
		 domain_object_id, target_version, assigned_to, status, created_at, expires_at, resolved_at
		 FROM conversation_interaction WHERE conversation_id=$1 ORDER BY created_at DESC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ConversationInteraction, 0)
	for rows.Next() {
		var item ConversationInteraction
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.OriginMessageID, &item.SourceMessageID, &item.InteractionType,
			&item.DomainObjectType, &item.DomainObjectID, &item.TargetVersion, &item.AssignedTo,
			&item.Status, &item.CreatedAt, &item.ExpiresAt, &item.ResolvedAt); err != nil {
			return nil, err
		}
		item.Reference = item.InteractionReference()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) SetConversationInteractionStatus(ctx context.Context, conversationID, interactionType, domainObjectID, status string) error {
	if status == "" {
		return errors.New("interaction status is required")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE conversation_interaction SET status=$4,
		 resolved_at=CASE WHEN $4 IN ('answered','approved','rejected','cancelled','expired','resolved') THEN now() ELSE NULL END
		 WHERE conversation_id=$1 AND interaction_type=$2 AND domain_object_id=$3`,
		conversationID, interactionType, domainObjectID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const conversationColumns = `id, organization_id, owner_id, agent_id, status, title,
 last_message_sequence, context_version, metadata, created_at, updated_at`

// CreateConversation inserts a new durable conversation. IDs are supplied by
// the caller so API and transport idempotency policies remain explicit.
func (s *Store) CreateConversation(ctx context.Context, c *Conversation) error {
	if c == nil || strings.TrimSpace(c.ID) == "" {
		return errors.New("conversation id is required")
	}
	if c.Status == "" {
		c.Status = "active"
	}
	if c.Status != "active" && c.Status != "archived" {
		return fmt.Errorf("invalid conversation status %q", c.Status)
	}
	if len(c.Metadata) == 0 {
		c.Metadata = json.RawMessage(`{}`)
	}
	if !json.Valid(c.Metadata) {
		return errors.New("conversation metadata must be valid JSON")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx,
		`INSERT INTO conversation (id, organization_id, owner_id, agent_id, status, title, metadata)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO NOTHING`,
		c.ID, c.OrganizationID, c.OwnerID, c.AgentID, c.Status, c.Title, []byte(c.Metadata))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	for _, participant := range []struct{ kind, id string }{{"human", c.OwnerID}, {"agent", c.AgentID}} {
		if participant.id == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO conversation_participant (conversation_id, participant_type, participant_id)
			 VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, c.ID, participant.kind, participant.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.pool.QueryRow(ctx, `SELECT `+conversationColumns+` FROM conversation WHERE id=$1`, c.ID).
		Scan(&c.ID, &c.OrganizationID, &c.OwnerID, &c.AgentID, &c.Status, &c.Title,
			&c.LastMessageSequence, &c.ContextVersion, &c.Metadata, &c.CreatedAt, &c.UpdatedAt)
}

// GetConversation returns one conversation. Authorization is enforced by the
// API/adapter using the organization and participant fields.
func (s *Store) GetConversation(ctx context.Context, id string) (Conversation, error) {
	var c Conversation
	err := s.pool.QueryRow(ctx, `SELECT `+conversationColumns+` FROM conversation WHERE id=$1`, id).
		Scan(&c.ID, &c.OrganizationID, &c.OwnerID, &c.AgentID, &c.Status, &c.Title,
			&c.LastMessageSequence, &c.ContextVersion, &c.Metadata, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// BindConversationSource associates a stable transport chat/room with a
// conversation. A transport identity cannot silently be rebound to another
// conversation; callers must explicitly migrate the mapping if needed.
func (s *Store) BindConversationSource(ctx context.Context, organizationID, source, sourceConversationID, conversationID string) error {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(sourceConversationID) == "" || strings.TrimSpace(conversationID) == "" {
		return errors.New("source, source_conversation_id and conversation_id are required")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO conversation_transport (organization_id, source, source_conversation_id, conversation_id)
		 VALUES ($1,$2,$3,$4) ON CONFLICT (organization_id, source, source_conversation_id) DO NOTHING`,
		organizationID, source, sourceConversationID, conversationID)
	if err != nil {
		return err
	}
	var boundID string
	err = s.pool.QueryRow(ctx,
		`SELECT conversation_id FROM conversation_transport
		 WHERE organization_id=$1 AND source=$2 AND source_conversation_id=$3`,
		organizationID, source, sourceConversationID).Scan(&boundID)
	if err != nil {
		return err
	}
	if boundID != conversationID {
		return fmt.Errorf("%w: transport conversation already bound", ErrConflict)
	}
	return nil
}

// ResolveConversationSource returns the conversation bound to one transport
// room or chat identity.
func (s *Store) ResolveConversationSource(ctx context.Context, organizationID, source, sourceConversationID string) (Conversation, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT conversation_id FROM conversation_transport
		 WHERE organization_id=$1 AND source=$2 AND source_conversation_id=$3`,
		organizationID, source, sourceConversationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	return s.GetConversation(ctx, id)
}

// AddConversationParticipant records a participant the first time they speak
// in a transport conversation.
func (s *Store) AddConversationParticipant(ctx context.Context, conversationID, participantType, participantID string) error {
	if !validConversationParticipantType(participantType) || strings.TrimSpace(participantID) == "" {
		return errors.New("valid participant_type and participant_id are required")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO conversation_participant (conversation_id, participant_type, participant_id)
		 VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, conversationID, participantType, participantID)
	return err
}

// LinkConversationTask records the existing Task Runtime identity and the
// message that initiated it. Repeated updates (for example, follow-up runs)
// keep one task link while refreshing its latest run and state.
func (s *Store) LinkConversationTask(ctx context.Context, conversationID, originMessageID, taskID, runID, projectID, status string) error {
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(taskID) == "" {
		return errors.New("conversation_id and task_id are required")
	}
	if status == "" {
		status = "active"
	}
	linkID := "ctask-" + stableConversationKey(conversationID, taskID)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO conversation_task (id, conversation_id, origin_message_id, task_id, run_id, project_id, status)
		 VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7)
		 ON CONFLICT (conversation_id, task_id) DO UPDATE SET
		   origin_message_id = COALESCE(conversation_task.origin_message_id, EXCLUDED.origin_message_id),
		   run_id = NULLIF(EXCLUDED.run_id,''), project_id = NULLIF(EXCLUDED.project_id,''), status = EXCLUDED.status,
		   completed_at = CASE WHEN EXCLUDED.status IN ('completed','failed','cancelled') THEN now() ELSE NULL END`,
		linkID, conversationID, originMessageID, taskID, runID, projectID, status)
	return err
}

const conversationMessageColumns = `id, organization_id, conversation_id, sequence, parent_message_id,
 sender_type, sender_id, message_type, content, attachments, source, source_message_id,
 source_thread_id, status, created_at, processed_at, metadata`

func scanConversationMessage(row interface{ Scan(...any) error }) (ConversationMessage, error) {
	var m ConversationMessage
	var parent *string
	err := row.Scan(&m.ID, &m.OrganizationID, &m.ConversationID, &m.Sequence, &parent,
		&m.SenderType, &m.SenderID, &m.MessageType, &m.Content, &m.Attachments, &m.Source,
		&m.SourceMessageID, &m.SourceThreadID, &m.Status, &m.CreatedAt, &m.ProcessedAt, &m.Metadata)
	if parent != nil {
		m.ParentMessageID = *parent
	}
	return m, err
}

// AppendConversationMessage atomically allocates the next per-conversation
// sequence and persists the event. Repeated transport events return the
// original message with duplicate=true, including concurrent retries.
func (s *Store) AppendConversationMessage(ctx context.Context, m *ConversationMessage) (ConversationMessage, bool, error) {
	if m == nil || strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.ConversationID) == "" {
		return ConversationMessage{}, false, errors.New("message id and conversation_id are required")
	}
	if !validConversationParticipantType(m.SenderType) {
		return ConversationMessage{}, false, fmt.Errorf("invalid sender_type %q", m.SenderType)
	}
	if m.MessageType == "" {
		m.MessageType = "text"
	}
	if m.Status == "" {
		m.Status = "received"
	}
	if !json.Valid(m.Content) || (len(m.Attachments) > 0 && !json.Valid(m.Attachments)) || (len(m.Metadata) > 0 && !json.Valid(m.Metadata)) {
		return ConversationMessage{}, false, errors.New("message content, attachments and metadata must be valid JSON")
	}
	if len(m.Attachments) == 0 {
		m.Attachments = json.RawMessage(`[]`)
	}
	if len(m.Metadata) == 0 {
		m.Metadata = json.RawMessage(`{}`)
	}

	if m.Source != "" && m.SourceMessageID != "" {
		if existing, err := s.getConversationMessageBySource(ctx, m.OrganizationID, m.Source, m.SourceMessageID); err == nil {
			if existing.ConversationID != m.ConversationID {
				return ConversationMessage{}, false, ErrConflict
			}
			return existing, true, nil
		} else if !errors.Is(err, ErrNotFound) {
			return ConversationMessage{}, false, err
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ConversationMessage{}, false, err
	}
	defer tx.Rollback(ctx)
	// Row update is the cross-process serialization point for sequence
	// allocation. It does not hold a lock during model execution.
	err = tx.QueryRow(ctx,
		`UPDATE conversation SET last_message_sequence=last_message_sequence+1, updated_at=now()
		 WHERE id=$1 RETURNING last_message_sequence, organization_id`, m.ConversationID).
		Scan(&m.Sequence, &m.OrganizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversationMessage{}, false, ErrNotFound
	}
	if err != nil {
		return ConversationMessage{}, false, err
	}
	var parent any
	if m.ParentMessageID != "" {
		parent = m.ParentMessageID
	}
	if m.Status == "received" {
		m.Status = "persisted"
	}
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO conversation_message
		 (id, organization_id, conversation_id, sequence, parent_message_id, sender_type, sender_id,
		  message_type, content, attachments, source, source_message_id, source_thread_id, status, created_at, metadata)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		 RETURNING `+conversationMessageColumns,
		m.ID, m.OrganizationID, m.ConversationID, m.Sequence, parent, m.SenderType, m.SenderID,
		m.MessageType, []byte(m.Content), []byte(m.Attachments), m.Source, m.SourceMessageID,
		m.SourceThreadID, m.Status, createdAt, []byte(m.Metadata))
	created, err := scanConversationMessage(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(ctx)
			if m.Source == "" || m.SourceMessageID == "" {
				return ConversationMessage{}, false, ErrConflict
			}
			existing, lookupErr := s.getConversationMessageBySource(ctx, m.OrganizationID, m.Source, m.SourceMessageID)
			if lookupErr == nil {
				if existing.ConversationID != m.ConversationID {
					return ConversationMessage{}, false, ErrConflict
				}
				return existing, true, nil
			}
			return ConversationMessage{}, false, lookupErr
		}
		return ConversationMessage{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConversationMessage{}, false, err
	}
	return created, false, nil
}

func validConversationParticipantType(value string) bool {
	switch value {
	case "human", "agent", "system", "external_identity":
		return true
	default:
		return false
	}
}

func (s *Store) getConversationMessageBySource(ctx context.Context, organizationID, source, sourceMessageID string) (ConversationMessage, error) {
	m, err := scanConversationMessage(s.pool.QueryRow(ctx,
		`SELECT `+conversationMessageColumns+` FROM conversation_message
		 WHERE organization_id=$1 AND source=$2 AND source_message_id=$3`, organizationID, source, sourceMessageID))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// FindConversationMessageBySource resolves a transport reply reference to
// the normalized message ID used by parent_message_id.
func (s *Store) FindConversationMessageBySource(ctx context.Context, organizationID, source, sourceMessageID string) (ConversationMessage, error) {
	if strings.TrimSpace(sourceMessageID) == "" {
		return ConversationMessage{}, ErrNotFound
	}
	return s.getConversationMessageBySource(ctx, organizationID, source, sourceMessageID)
}

// ListConversationMessages returns messages in causal sequence. afterSequence
// is an exclusive cursor suitable for pagination and reconnects.
func (s *Store) ListConversationMessages(ctx context.Context, conversationID string, afterSequence int64, limit int) ([]ConversationMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+conversationMessageColumns+` FROM conversation_message
		 WHERE conversation_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, conversationID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]ConversationMessage, 0)
	for rows.Next() {
		m, err := scanConversationMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *Store) GetConversationMessage(ctx context.Context, conversationID, messageID string) (ConversationMessage, error) {
	message, err := scanConversationMessage(s.pool.QueryRow(ctx,
		`SELECT `+conversationMessageColumns+` FROM conversation_message WHERE conversation_id=$1 AND id=$2`, conversationID, messageID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversationMessage{}, ErrNotFound
	}
	return message, err
}

// TryAcquireConversationTurn serializes direct turns per conversation and
// admits them in message-sequence order. The session advisory lock remains
// pinned to one pool connection until release is called.
func (s *Store) TryAcquireConversationTurn(ctx context.Context, conversationID string, sequence int64) (release func(), acquired bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, conversationID).Scan(&locked); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !locked {
		conn.Release()
		return nil, false, nil
	}
	var earlierPending bool
	err = conn.QueryRow(ctx,
		`SELECT EXISTS (
		 SELECT 1 FROM conversation_message
		 WHERE conversation_id=$1 AND sequence<$2 AND sender_type='human' AND source='workspace_chat'
		   AND status IN ('persisted','queued','processing')
		)`, conversationID, sequence).Scan(&earlierPending)
	if err != nil || earlierPending {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, conversationID)
		conn.Release()
		return nil, false, err
	}
	release = func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, conversationID)
		conn.Release()
	}
	return release, true, nil
}

// SetConversationMessageStatus advances the durable processing lifecycle.
// Terminal states stamp processed_at; callers should persist the outbound
// response as a separate message before marking the inbound event completed.
func (s *Store) SetConversationMessageStatus(ctx context.Context, id, status string) error {
	switch status {
	case "received", "persisted", "queued", "processing", "completed", "retryable_failure", "failed", "cancelled":
	default:
		return fmt.Errorf("invalid conversation message status %q", status)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE conversation_message SET status=$2,
		 processed_at=CASE WHEN $2 IN ('completed','failed','cancelled') THEN now() ELSE processed_at END
		 WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncLegacyChat imports the Workspace's opaque transcript into normalized
// conversation messages. It is idempotent by the client message ID (or a
// stable legacy fallback), so existing clients can dual-write safely during
// the migration.
func (s *Store) SyncLegacyChat(ctx context.Context, chat Chat) error {
	metadata, err := json.Marshal(map[string]string{"project_id": chat.ProjectID})
	if err != nil {
		return err
	}
	conversation := &Conversation{
		ID: chat.ID, OwnerID: chat.OwnerUserID, AgentID: chat.AgentID, Title: chat.Title,
		Metadata: metadata,
	}
	if err := s.CreateConversation(ctx, conversation); err != nil && !errors.Is(err, ErrConflict) {
		return err
	}
	if err := s.BindConversationSource(ctx, "", "workspace", chat.ID, chat.ID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE conversation SET owner_id=CASE WHEN owner_id='' THEN $2 ELSE owner_id END,
		 agent_id=CASE WHEN $3='' THEN agent_id ELSE $3 END, title=CASE WHEN $4='' THEN title ELSE $4 END,
		 metadata=jsonb_set(metadata, '{project_id}', to_jsonb($5::text), true), updated_at=now()
		 WHERE id=$1`, chat.ID, chat.OwnerUserID, chat.AgentID, chat.Title, chat.ProjectID); err != nil {
		return err
	}
	var entries []struct {
		ID          string          `json:"id"`
		Role        string          `json:"role"`
		Content     json.RawMessage `json:"content"`
		At          time.Time       `json:"at"`
		Status      string          `json:"status"`
		RunID       string          `json:"runId"`
		Attachments json.RawMessage `json:"attachments"`
		Addenda     json.RawMessage `json:"addenda"`
	}
	if err := json.Unmarshal(chat.Transcript, &entries); err != nil {
		return fmt.Errorf("decode workspace chat transcript: %w", err)
	}
	lastUserMessageID := ""
	for i, entry := range entries {
		if entry.Role != "user" && entry.Role != "assistant" && entry.Role != "system" {
			continue
		}
		if entry.Status == "running" {
			continue // the live placeholder is not a completed conversational event
		}
		if len(entry.Content) == 0 {
			entry.Content = json.RawMessage(`""`)
		}
		sourceID := strings.TrimSpace(entry.ID)
		if sourceID == "" {
			sourceID = legacyConversationSourceID(chat.ID, i, entry.Role, string(entry.Content), entry.At)
		}
		messageID := "msg-" + stableConversationKey(chat.ID, sourceID)
		content, err := json.Marshal(map[string]json.RawMessage{"text": entry.Content, "addenda": nonNullJSON(entry.Addenda)})
		if err != nil {
			return err
		}
		attachments := nonNullJSON(entry.Attachments)
		senderType, senderID := "human", chat.OwnerUserID
		if entry.Role == "assistant" {
			senderType, senderID = "agent", chat.AgentID
		} else if entry.Role == "system" {
			senderType, senderID = "system", ""
		}
		parent := ""
		if entry.Role == "user" {
			lastUserMessageID = messageID
		} else if entry.Role == "assistant" {
			parent = lastUserMessageID
		}
		meta, _ := json.Marshal(map[string]string{"legacy_status": entry.Status, "run_id": entry.RunID})
		_, _, err = s.AppendConversationMessage(ctx, &ConversationMessage{
			ID: messageID, OrganizationID: conversation.OrganizationID, ConversationID: chat.ID,
			ParentMessageID: parent, SenderType: senderType, SenderID: senderID,
			MessageType: "text", Content: content, Attachments: attachments,
			Source: "workspace_chat", SourceMessageID: sourceID, Status: "completed",
			CreatedAt: entry.At, Metadata: meta,
		})
		if err != nil {
			return err
		}
	}
	if chat.TaskID != "" {
		linkID := "ctask-" + stableConversationKey(chat.ID, chat.TaskID)
		_, err := s.pool.Exec(ctx,
			`INSERT INTO conversation_task (id, conversation_id, origin_message_id, task_id, run_id, project_id, status)
			 VALUES ($1,$2,NULLIF($3,''),$4,'',$5,'active')
			 ON CONFLICT (conversation_id, task_id) DO NOTHING`,
			linkID, chat.ID, lastUserMessageID, chat.TaskID, chat.ProjectID)
		if err != nil {
			return err
		}
	}
	return nil
}

func nonNullJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(`[]`)
	}
	return raw
}

func legacyConversationSourceID(conversationID string, index int, role, content string, at time.Time) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s", conversationID, index, role, at.UTC().Format(time.RFC3339Nano), content)))
	return "legacy-" + hex.EncodeToString(digest[:16])
}

func stableConversationKey(parts ...string) string {
	joined := strings.Join(parts, "\x00")
	digest := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(digest[:16])
}
