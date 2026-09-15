package postgres

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/entity"
)

// ReplaceAllEntities rebuilds the global entity projection atomically: the
// previous projection is deleted and the new one inserted in a single
// transaction, so readers never observe a partial graph.
func (s *Store) ReplaceAllEntities(ctx context.Context, entities []entity.Entity, relations []entity.EntityRelation) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM entity_relations`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM entities`); err != nil {
		return err
	}
	for _, e := range entities {
		if _, err = tx.Exec(ctx, `INSERT INTO entities (entity_id,type,name,mention_count,confidence,projection_version) VALUES ($1,$2,$3,$4,$5,$6)`, e.EntityID, e.Type, e.Name, e.MentionCount, e.Confidence, e.ProjectionVersion); err != nil {
			return err
		}
	}
	for _, r := range relations {
		if _, err = tx.Exec(ctx, `INSERT INTO entity_relations (source_id,target_id,predicate,claim_id,confidence,projection_version) VALUES ($1,$2,$3,$4,$5,$6)`, r.SourceID, r.TargetID, r.Predicate, r.ClaimID, r.Confidence, r.ProjectionVersion); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListEntities(ctx context.Context, filter entity.Filter) ([]entity.Entity, error) {
	query := `SELECT entity_id::text,type,name,mention_count,confidence,projection_version FROM entities`
	args := make([]any, 0, 2)
	if filter.Type != "" {
		args = append(args, filter.Type)
		query += ` WHERE type=$` + strconv.Itoa(len(args))
	}
	if filter.Name != "" {
		if len(args) > 0 {
			query += ` AND`
		} else {
			query += ` WHERE`
		}
		args = append(args, filter.Name)
		query += ` name=$` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY type,name`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]entity.Entity, 0)
	for rows.Next() {
		var e entity.Entity
		if err = rows.Scan(&e.EntityID, &e.Type, &e.Name, &e.MentionCount, &e.Confidence, &e.ProjectionVersion); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) GetEntity(ctx context.Context, id string) (entity.Entity, error) {
	var e entity.Entity
	err := s.pool.QueryRow(ctx, `SELECT entity_id::text,type,name,mention_count,confidence,projection_version FROM entities WHERE entity_id=$1`, id).Scan(&e.EntityID, &e.Type, &e.Name, &e.MentionCount, &e.Confidence, &e.ProjectionVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, entity.ErrEntityNotFound
	}
	return e, err
}

func (s *Store) ListEntityRelations(ctx context.Context, filter entity.RelationFilter) ([]entity.EntityRelation, error) {
	query := `SELECT source_id::text,target_id::text,predicate,claim_id::text,confidence,projection_version FROM entity_relations`
	args := make([]any, 0, 1)
	if filter.EntityID != "" {
		args = append(args, filter.EntityID)
		query += ` WHERE source_id=$1 OR target_id=$1`
	}
	query += ` ORDER BY source_id,target_id,predicate,claim_id`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]entity.EntityRelation, 0)
	for rows.Next() {
		var r entity.EntityRelation
		if err = rows.Scan(&r.SourceID, &r.TargetID, &r.Predicate, &r.ClaimID, &r.Confidence, &r.ProjectionVersion); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
