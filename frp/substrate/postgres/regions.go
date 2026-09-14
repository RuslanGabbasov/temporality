package postgres

import (
	"context"

	"github.com/temporality-project/temporality/frp/projection"
)

func (s *Store) ReplaceRegions(ctx context.Context, episodeID, branchID string, regions []projection.Region) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM regions WHERE episode_id=$1 AND branch_id=$2`, episodeID, branchID); err != nil {
		return err
	}
	for _, region := range regions {
		if _, err = tx.Exec(ctx, `INSERT INTO regions(region_id,episode_id,branch_id,kind,label,created_ts,projection_version) VALUES($1,$2,$3,$4,$5,$6,$7)`, region.RegionID, region.EpisodeID, region.BranchID, region.Kind, region.Label, region.ValidFrom, region.ProjectionVersion); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO region_versions(region_id,version,valid_from,label,activation,projection_version) VALUES($1,$2,$3,$4,$5,$6)`, region.RegionID, region.Version, region.ValidFrom, region.Label, region.Activation, region.ProjectionVersion); err != nil {
			return err
		}
		for _, eventID := range region.MemberEventIDs {
			if _, err = tx.Exec(ctx, `INSERT INTO region_memberships(region_id,region_version,event_id) VALUES($1,$2,$3)`, region.RegionID, region.Version, eventID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) ListRegions(ctx context.Context, filter projection.RegionFilter) ([]projection.Region, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.region_id::text,r.episode_id::text,r.branch_id::text,r.kind,v.label,v.version,v.valid_from,v.activation,v.projection_version FROM regions r JOIN region_versions v ON v.region_id=r.region_id WHERE ($1='' OR r.episode_id=$1::uuid) AND ($2='' OR r.branch_id=$2::uuid) AND ($3::timestamptz IS NULL OR v.valid_from <= $3) ORDER BY r.region_id,v.version`, filter.EpisodeID, filter.BranchID, filter.AsOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]projection.Region, 0)
	for rows.Next() {
		var region projection.Region
		if err = rows.Scan(&region.RegionID, &region.EpisodeID, &region.BranchID, &region.Kind, &region.Label, &region.Version, &region.ValidFrom, &region.Activation, &region.ProjectionVersion); err != nil {
			return nil, err
		}
		memberRows, queryErr := s.pool.Query(ctx, `SELECT event_id::text FROM region_memberships WHERE region_id=$1 AND region_version=$2 ORDER BY event_id`, region.RegionID, region.Version)
		if queryErr != nil {
			return nil, queryErr
		}
		for memberRows.Next() {
			var id string
			if queryErr = memberRows.Scan(&id); queryErr != nil {
				memberRows.Close()
				return nil, queryErr
			}
			region.MemberEventIDs = append(region.MemberEventIDs, id)
		}
		queryErr = memberRows.Err()
		memberRows.Close()
		if queryErr != nil {
			return nil, queryErr
		}
		result = append(result, region)
	}
	return result, rows.Err()
}
