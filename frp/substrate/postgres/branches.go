package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/branch"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/timetravel"
)

func (s *Store) Fork(ctx context.Context, sourceFrameID string, request branch.ForkRequest) (branch.ForkGroup, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return branch.ForkGroup{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var data []byte
	var cursor timetravel.EventCursor
	err = tx.QueryRow(ctx, `SELECT f.data,e.event_seq,e.event_id::text,e.tx_time FROM frames f JOIN events e ON e.event_id=f.created_event WHERE f.frame_id=$1 FOR SHARE`, sourceFrameID).Scan(&data, &cursor.EventSeq, &cursor.EventID, &cursor.TransactionTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return branch.ForkGroup{}, frame.ErrFrameNotFound
	}
	if err != nil {
		return branch.ForkGroup{}, err
	}
	var source frame.Frame
	if err = json.Unmarshal(data, &source); err != nil {
		return branch.ForkGroup{}, err
	}
	group, roots, err := branch.Fork(source, request)
	if err != nil {
		return group, err
	}
	group.SourceCursor = cursor
	for i := range group.Branches {
		group.Branches[i].SourceCursor = cursor
	}
	groupData, _ := json.Marshal(group)
	_, err = tx.Exec(ctx, `INSERT INTO fork_groups(fork_group_id,episode_id,objective_id,source_frame_id,source_branch_id,source_event_seq,source_event_id,source_tx_time,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, group.ForkGroupID, group.EpisodeID, group.ObjectiveID, group.SourceFrameID, group.SourceBranchID, cursor.EventSeq, cursor.EventID, cursor.TransactionTime, groupData)
	if err != nil {
		return branch.ForkGroup{}, err
	}
	now := time.Now().UTC()
	for i, b := range group.Branches {
		created := pgBranchEvent(branch.EventBranchCreated, group, b.BranchID, b, now)
		forked := pgBranchEvent(branch.EventFrameForked, group, b.BranchID, roots[i], now)
		if err = insertEvent(ctx, tx, created); err != nil {
			return branch.ForkGroup{}, err
		}
		if err = insertEvent(ctx, tx, forked); err != nil {
			return branch.ForkGroup{}, err
		}
		if err = insertFrame(ctx, tx, roots[i], forked.EventID); err != nil {
			return branch.ForkGroup{}, err
		}
		bd, _ := json.Marshal(b)
		mc, _ := json.Marshal(b.ModelConfig)
		_, err = tx.Exec(ctx, `INSERT INTO branches(branch_id,fork_group_id,parent_branch_id,episode_id,objective_id,source_frame_id,source_event_seq,source_event_id,source_tx_time,root_frame_id,head_frame_id,status,model_config,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, b.BranchID, b.ForkGroupID, b.ParentBranchID, b.EpisodeID, b.ObjectiveID, b.SourceFrameID, cursor.EventSeq, cursor.EventID, cursor.TransactionTime, b.RootFrameID, b.HeadFrameID, b.Status, mc, bd)
		if err != nil {
			return branch.ForkGroup{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return branch.ForkGroup{}, err
	}
	return group, nil
}

func pgBranchEvent(kind string, group branch.ForkGroup, branchID string, payload any, at time.Time) protocol.Event {
	data, _ := json.Marshal(struct{ Kind, Group, Branch string }{kind, group.ForkGroupID, branchID})
	sum := sha256.Sum256(data)
	raw := append([]byte(nil), sum[:16]...)
	raw[6] = raw[6]&15 | 80
	raw[8] = raw[8]&63 | 128
	h := hex.EncodeToString(raw)
	id := h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	encoded, _ := json.Marshal(payload)
	var body map[string]any
	_ = json.Unmarshal(encoded, &body)
	return protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: group.EpisodeID, BranchID: branchID, Type: kind, Payload: body, Provenance: map[string]any{"source": "temporality-branch-store"}}
}
func scanBranch(row scanner) (branch.Branch, error) {
	var data []byte
	err := row.Scan(&data)
	var v branch.Branch
	if err == nil {
		err = json.Unmarshal(data, &v)
	}
	return v, err
}
func (s *Store) GetBranch(ctx context.Context, id string) (branch.Branch, error) {
	v, err := scanBranch(s.pool.QueryRow(ctx, `SELECT jsonb_set(data,'{head_frame_id}',to_jsonb(head_frame_id::text)) FROM branches WHERE branch_id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = branch.ErrNotFound
	}
	return v, err
}
func (s *Store) GetForkGroup(ctx context.Context, id string) (branch.ForkGroup, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM fork_groups WHERE fork_group_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return branch.ForkGroup{}, branch.ErrNotFound
	}
	var g branch.ForkGroup
	if err == nil {
		err = json.Unmarshal(data, &g)
	}
	if err != nil {
		return g, err
	}
	rows, err := s.pool.Query(ctx, `SELECT jsonb_set(data,'{head_frame_id}',to_jsonb(head_frame_id::text)) FROM branches WHERE fork_group_id=$1 ORDER BY branch_id`, id)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	g.Branches = nil
	for rows.Next() {
		b, e := scanBranch(rows)
		if e != nil {
			return g, e
		}
		g.Branches = append(g.Branches, b)
	}
	return g, rows.Err()
}
func (s *Store) UpdateHead(ctx context.Context, id, expected, next string) (branch.Branch, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE branches b SET head_frame_id=$3 WHERE b.branch_id=$1 AND b.head_frame_id=$2 AND EXISTS(SELECT 1 FROM frames f WHERE f.frame_id=$3 AND f.branch_id=b.branch_id AND f.episode_id=b.episode_id AND f.objective_id=b.objective_id)`, id, expected, next)
	if err != nil {
		return branch.Branch{}, err
	}
	if tag.RowsAffected() != 1 {
		if _, e := s.GetBranch(ctx, id); e != nil {
			return branch.Branch{}, e
		}
		return branch.Branch{}, branch.ErrCASConflict
	}
	return s.GetBranch(ctx, id)
}
func (s *Store) PutComparison(ctx context.Context, g branch.ForkGroup, left, right branch.Trajectory) (branch.ComparisonResult, error) {
	stored, err := s.GetForkGroup(ctx, g.ForkGroupID)
	if err != nil {
		return branch.ComparisonResult{}, err
	}
	result, err := branch.Compare(stored, left, right)
	if err != nil {
		return result, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	event := pgBranchEvent(branch.EventBranchCompared, stored, "", result, time.Now().UTC())
	if err = insertEvent(ctx, tx, event); err != nil {
		var old branch.ComparisonResult
		if e := s.pool.QueryRow(ctx, `SELECT data FROM branch_comparisons WHERE comparison_id=$1`, result.ComparisonID).Scan(scanJSON(&old)); e == nil {
			return old, nil
		}
		return result, err
	}
	data, _ := json.Marshal(result)
	_, err = tx.Exec(ctx, `INSERT INTO branch_comparisons(comparison_id,fork_group_id,left_branch_id,right_branch_id,left_frame_id,right_frame_id,created_event,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, result.ComparisonID, result.ForkGroupID, result.LeftBranchID, result.RightBranchID, result.LeftFrameID, result.RightFrameID, event.EventID, data)
	if err != nil {
		return result, err
	}
	err = tx.Commit(ctx)
	return result, err
}
func (s *Store) GetComparison(ctx context.Context, id string) (branch.ComparisonResult, error) {
	var v branch.ComparisonResult
	err := s.pool.QueryRow(ctx, `SELECT data FROM branch_comparisons WHERE comparison_id=$1`, id).Scan(scanJSON(&v))
	if errors.Is(err, pgx.ErrNoRows) {
		err = branch.ErrNotFound
	}
	return v, err
}
