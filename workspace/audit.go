package workspace

import (
	"context"
	"encoding/json"
	"time"
)

// Access audit (docs/org-structure.md §36): the minimal journal of who changed
// which access-relevant entity and when. Append-only, written by the kernel
// after org-tree, binding and membership mutations. Audit failures are logged
// by the caller but never block the mutation itself.

// AccessAuditEvent is one recorded access change.
type AccessAuditEvent struct {
	ID         int64          `json:"id"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	EntityKind string         `json:"entity_kind"`
	EntityID   string         `json:"entity_id"`
	Details    map[string]any `json:"details,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Audit action names.
const (
	AuditProjectMemberAdded   = "project.member_added"
	AuditProjectMemberRemoved = "project.member_removed"
	AuditResourceBound        = "resource.bound"
	AuditResourceUnbound      = "resource.unbound"
	AuditOrgUnitCreated       = "org_unit.created"
	AuditOrgUnitUpdated       = "org_unit.updated"
	AuditOrgUnitMoved         = "org_unit.moved"
	AuditOrgUnitDeleted       = "org_unit.deleted"
	AuditRoleGranted          = "role.granted"
	AuditRoleRevoked          = "role.revoked"
	AuditUserOrgUnitChanged   = "user.org_unit_changed"
	AuditExecIdentityCreated  = "execution_identity.created"
	AuditExecIdentityUpdated  = "execution_identity.updated"
	AuditExecIdentityDeleted  = "execution_identity.deleted"
	AuditTriggerCreated       = "trigger.created"
	AuditTriggerUpdated       = "trigger.updated"
	AuditTriggerDeleted       = "trigger.deleted"
	AuditHumanCreated         = "human_request.created"
	AuditHumanDelivered       = "human_request.delivered"
	AuditHumanAnswered        = "human_request.answered"
	AuditHumanExpired         = "human_request.expired"
	AuditHumanCancelled       = "human_request.cancelled"
	AuditHumanRejected        = "human_request.rejected"
	AuditPolicyCreated        = "policy.created"
	AuditPolicyUpdated        = "policy.updated"
	AuditPolicyDeleted        = "policy.deleted"
	AuditTeamCreated          = "team.created"
	AuditTeamUpdated          = "team.updated"
	AuditTeamDeleted          = "team.deleted"
	AuditTeamVersionProposed  = "team_version.proposed"
	AuditTeamVersionApplied   = "team_version.applied"
	AuditTeamVersionRejected  = "team_version.rejected"
)

// RecordAccessAudit appends one event. Nil details is stored as {}.
func (s *Store) RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil || raw == nil {
		raw = []byte("{}")
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO access_audit_log (actor, action, entity_kind, entity_id, details) VALUES ($1, $2, $3, $4, $5)`,
		actor, action, entityKind, entityID, raw)
	return err
}

// ListAccessAudit returns the most recent events, newest first.
func (s *Store) ListAccessAudit(ctx context.Context, limit int) ([]AccessAuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, actor, action, entity_kind, entity_id, details, created_at
		 FROM access_audit_log ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AccessAuditEvent{}
	for rows.Next() {
		var e AccessAuditEvent
		var details []byte
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.EntityKind, &e.EntityID, &details, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(details) > 0 {
			_ = json.Unmarshal(details, &e.Details)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
