package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/timetravel"
)

func (s *Store) CreateSnapshot(ctx context.Context, value timetravel.Snapshot) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Metadata.Through.EventSeq <= 0 {
		return errors.New("snapshot through event_seq must be positive")
	}
	m := value.Metadata
	_, err := s.pool.Exec(ctx, `INSERT INTO snapshots(snapshot_id,protocol,protocol_version,snapshot_version,episode_id,created_at,through_event_seq,through_event_id,through_tx_time,payload,sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, m.SnapshotID, m.Protocol, m.ProtocolVersion, m.SnapshotVersion, m.EpisodeID, m.CreatedAt, m.Through.EventSeq, m.Through.EventID, m.Through.TransactionTime, value.Content, m.ContentHash)
	return err
}

func scanSnapshot(row scanner) (timetravel.Snapshot, error) {
	var value timetravel.Snapshot
	err := row.Scan(&value.Metadata.SnapshotID, &value.Metadata.Protocol, &value.Metadata.ProtocolVersion, &value.Metadata.SnapshotVersion, &value.Metadata.EpisodeID, &value.Metadata.CreatedAt, &value.Metadata.Through.EventSeq, &value.Metadata.Through.EventID, &value.Metadata.Through.TransactionTime, &value.Content, &value.Metadata.ContentHash)
	return value, err
}

const snapshotFields = `snapshot_id::text,protocol,protocol_version,snapshot_version,episode_id::text,created_at,through_event_seq,through_event_id::text,through_tx_time,payload,sha256`

func (s *Store) GetSnapshot(ctx context.Context, id string) (timetravel.Snapshot, error) {
	value, err := scanSnapshot(s.pool.QueryRow(ctx, `SELECT `+snapshotFields+` FROM snapshots WHERE snapshot_id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, substrate.ErrNotFound
	}
	if err == nil {
		err = value.Validate()
	}
	return value, err
}

func (s *Store) SelectSnapshot(ctx context.Context, episodeID string, target timetravel.EventCursor) (timetravel.Snapshot, bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+snapshotFields+` FROM snapshots WHERE episode_id=$1 AND through_event_seq <= $2 ORDER BY through_event_seq DESC,created_at DESC,snapshot_id`, episodeID, target.EventSeq)
	if err != nil {
		return timetravel.Snapshot{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		value, scanErr := scanSnapshot(rows)
		if scanErr != nil {
			return timetravel.Snapshot{}, false, scanErr
		}
		// A snapshot is only an optimization. Corruption never prevents replay.
		if value.Validate() == nil {
			return value, true, nil
		}
	}
	return timetravel.Snapshot{}, false, rows.Err()
}

func (s *Store) GetReplayFrame(ctx context.Context, id string) (frame.Frame, timetravel.EventCursor, error) {
	var data []byte
	var cursor timetravel.EventCursor
	err := s.pool.QueryRow(ctx, `SELECT f.data,e.event_seq,e.tx_time,e.event_id::text FROM frames f JOIN events e ON e.event_id=f.created_event WHERE f.frame_id=$1`, id).Scan(&data, &cursor.EventSeq, &cursor.TransactionTime, &cursor.EventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return frame.Frame{}, cursor, frame.ErrFrameNotFound
	}
	if err != nil {
		return frame.Frame{}, cursor, err
	}
	var value frame.Frame
	if err = json.Unmarshal(data, &value); err != nil {
		return frame.Frame{}, cursor, err
	}
	return value, cursor, nil
}

func (s *Store) ListEventsThrough(ctx context.Context, episodeID, branchID string, target timetravel.EventCursor) ([]timetravel.CursorEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.event_seq,`+selectFields+` FROM events e WHERE e.episode_id=$1 AND e.event_seq <= $3 AND ($2='' OR e.branch_id IS NULL OR e.branch_id=$2::uuid OR EXISTS (SELECT 1 FROM branches b WHERE b.branch_id=$2::uuid AND e.branch_id=b.parent_branch_id AND e.event_seq <= b.source_event_seq)) ORDER BY e.event_seq`, episodeID, branchID, target.EventSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]timetravel.CursorEvent, 0)
	for rows.Next() {
		var item timetravel.CursorEvent
		if err = rows.Scan(&item.Cursor.EventSeq, &item.Event.EventID, &item.Event.Protocol, &item.Event.Version, &item.Event.TransactionTime, &item.Event.ValidTime, &item.Event.AgentID, &item.Event.EpisodeID, &item.Event.BranchID, &item.Event.ParentID, &item.Event.Type, scanJSON(&item.Event.Payload), scanJSON(&item.Event.Provenance), &item.Event.SourceTrust, &item.Event.EvidenceStrength, &item.Event.Freshness, &item.Event.AgentTrust, &item.Event.Consensus, &item.Event.Contradiction); err != nil {
			return nil, err
		}
		item.Cursor.TransactionTime, item.Cursor.EventID = item.Event.TransactionTime, item.Event.EventID
		result = append(result, item)
	}
	return result, rows.Err()
}

type jsonScanner struct{ destination any }

func scanJSON(destination any) *jsonScanner { return &jsonScanner{destination: destination} }
func (s *jsonScanner) Scan(src any) error {
	var data []byte
	switch value := src.(type) {
	case []byte:
		data = value
	case string:
		data = []byte(value)
	default:
		return fmt.Errorf("expected JSON bytes or string, got %T", src)
	}
	return json.Unmarshal(data, s.destination)
}

func (s *Store) BuildBlame(ctx context.Context, rootID string, maxDepth int) (timetravel.BlameGraph, error) {
	rows, err := s.pool.Query(ctx, `
SELECT claim_id::text,'claim',created_event::text,'event','produced' FROM claims
UNION ALL SELECT src_claim::text,'claim',evidence_event::text,'event','derived_from' FROM claim_relations
UNION ALL SELECT dst_claim::text,'claim',evidence_event::text,'event','derived_from' FROM claim_relations
UNION ALL SELECT request_id::text,'state',requested_event::text,'event','produced' FROM affordance_requests
UNION ALL SELECT execution_id::text,'side_effect',created_event::text,'event','produced' FROM executions
UNION ALL SELECT execution_id::text,'side_effect',request_id::text,'state','derived_from' FROM executions
UNION ALL SELECT frame_id::text,'state',created_event::text,'event','produced' FROM frames
UNION ALL SELECT emission_id,'decision',parent_frame_id::text,'state','read_from' FROM cognitive_steps
UNION ALL SELECT next_frame_id::text,'state',emission_id,'decision','produced' FROM cognitive_steps
UNION ALL SELECT event_id::text,'event',parent_id::text,'event','caused_by' FROM events WHERE parent_id IS NOT NULL`)
	if err != nil {
		return timetravel.BlameGraph{}, err
	}
	defer rows.Close()
	nodes := map[string]timetravel.BlameNode{}
	edges := make([]timetravel.BlameEdge, 0)
	for rows.Next() {
		var effect, effectType, cause, causeType, edgeType string
		if err = rows.Scan(&effect, &effectType, &cause, &causeType, &edgeType); err != nil {
			return timetravel.BlameGraph{}, err
		}
		nodes[effect] = timetravel.BlameNode{ID: effect, Type: timetravel.BlameNodeType(effectType)}
		nodes[cause] = timetravel.BlameNode{ID: cause, Type: timetravel.BlameNodeType(causeType)}
		edges = append(edges, timetravel.BlameEdge{From: cause, To: effect, Type: timetravel.BlameEdgeType(edgeType)})
	}
	list := make([]timetravel.BlameNode, 0, len(nodes))
	for _, node := range nodes {
		list = append(list, node)
	}
	return timetravel.BuildBlameGraph(rootID, list, edges, maxDepth)
}
