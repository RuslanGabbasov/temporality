package postgres

import (
	"context"

	"github.com/temporality-project/temporality/frp/projection"
)

func (s *Store) ReplaceEdges(ctx context.Context, episodeID, branchID string, edges []projection.Edge) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM region_edges WHERE episode_id=$1 AND branch_id=$2`, episodeID, branchID); err != nil {
		return err
	}
	for _, edge := range edges {
		if _, err = tx.Exec(ctx, `INSERT INTO region_edges(edge_id,episode_id,branch_id,source_region_id,target_region_id,edge_type,weight,evidence_count,projection_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, edge.EdgeID, edge.EpisodeID, edge.BranchID, edge.SourceRegionID, edge.TargetRegionID, edge.Type, edge.Weight, edge.EvidenceCount, edge.ProjectionVersion); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListEdges(ctx context.Context, filter projection.EdgeFilter) ([]projection.Edge, error) {
	rows, err := s.pool.Query(ctx, `SELECT edge_id::text,episode_id::text,branch_id::text,source_region_id::text,target_region_id::text,edge_type,weight,evidence_count,projection_version FROM region_edges WHERE ($1='' OR episode_id=$1::uuid) AND ($2='' OR branch_id=$2::uuid) ORDER BY source_region_id,target_region_id,edge_type,edge_id`, filter.EpisodeID, filter.BranchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]projection.Edge, 0)
	for rows.Next() {
		var edge projection.Edge
		if err = rows.Scan(&edge.EdgeID, &edge.EpisodeID, &edge.BranchID, &edge.SourceRegionID, &edge.TargetRegionID, &edge.Type, &edge.Weight, &edge.EvidenceCount, &edge.ProjectionVersion); err != nil {
			return nil, err
		}
		result = append(result, edge)
	}
	return result, rows.Err()
}
