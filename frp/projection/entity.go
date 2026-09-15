// Package projection: entity.go builds the M14 knowledge graph. Entities and
// typed relations are derived exclusively from claims that carry a
// subject/predicate/object triple, so the graph stays a rebuildable projection
// over the substrate instead of a separately-maintained knowledge base.
package projection

import (
	"sort"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/entity"
)

// EntityProjectorVersion is kept in sync with entity.ProjectorVersion; both
// must change together if triple semantics change.
const EntityProjectorVersion = entity.ProjectorVersion

// projectiveClaimStatuses lists claim statuses that assert their triple.
// Refuted and superseded claims no longer project into the graph.
var projectiveClaimStatuses = map[cognition.ClaimStatus]struct{}{
	cognition.ClaimCandidate: {},
	cognition.ClaimSupported: {},
}

// BuildEntities projects claims into the global entity graph. It is pure and
// deterministic: identical claim sets always produce identical entities and
// relations (sorted, content-derived ids, deduplicated).
func BuildEntities(claims []cognition.Claim) ([]entity.Entity, []entity.EntityRelation) {
	type node struct {
		ref       entity.Ref
		mentions  int
		confident float32
	}
	nodes := map[string]*node{}
	relations := make([]entity.EntityRelation, 0)
	touch := func(ref entity.Ref, confidence float32) *node {
		id := ref.ID()
		existing, ok := nodes[id]
		if !ok {
			existing = &node{ref: ref}
			nodes[id] = existing
		}
		existing.mentions++
		if confidence > existing.confident {
			existing.confident = confidence
		}
		return existing
	}
	for _, claim := range claims {
		if _, asserts := projectiveClaimStatuses[claim.Status]; !asserts || !claim.HasTriple() {
			continue
		}
		subject, predicate, object, err := claim.Triple()
		if err != nil {
			// Validation should have caught this; skip defensively rather than
			// letting a malformed triple corrupt the projection.
			continue
		}
		touch(subject, claim.Confidence)
		touch(object, claim.Confidence)
		relations = append(relations, entity.EntityRelation{
			SourceID:          subject.ID(),
			TargetID:          object.ID(),
			Predicate:         predicate,
			ClaimID:           claim.ClaimID,
			Confidence:        claim.Confidence,
			ProjectionVersion: EntityProjectorVersion,
		})
	}
	entityList := make([]entity.Entity, 0, len(nodes))
	for id, n := range nodes {
		entityList = append(entityList, entity.Entity{
			EntityID:          id,
			Type:              n.ref.Type,
			Name:              n.ref.Name,
			MentionCount:      n.mentions,
			Confidence:        n.confident,
			ProjectionVersion: EntityProjectorVersion,
		})
	}
	sort.Slice(entityList, func(i, j int) bool {
		if entityList[i].Type != entityList[j].Type {
			return entityList[i].Type < entityList[j].Type
		}
		return entityList[i].Name < entityList[j].Name
	})
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].SourceID != relations[j].SourceID {
			return relations[i].SourceID < relations[j].SourceID
		}
		if relations[i].TargetID != relations[j].TargetID {
			return relations[i].TargetID < relations[j].TargetID
		}
		if relations[i].Predicate != relations[j].Predicate {
			return relations[i].Predicate < relations[j].Predicate
		}
		return relations[i].ClaimID < relations[j].ClaimID
	})
	return entityList, relations
}
