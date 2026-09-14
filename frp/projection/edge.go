package projection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
)

const EdgeProjectorVersion = "region-edges.v1"

type EdgeType string

const (
	EdgeSharedEvent      EdgeType = "shared_event"
	EdgeSequenceAdjacent EdgeType = "sequence_adjacency"
)

type Edge struct {
	EdgeID            string   `json:"edge_id"`
	EpisodeID         string   `json:"episode_id"`
	BranchID          string   `json:"branch_id"`
	SourceRegionID    string   `json:"source_region_id"`
	TargetRegionID    string   `json:"target_region_id"`
	Type              EdgeType `json:"type"`
	Weight            float64  `json:"weight"`
	EvidenceCount     int      `json:"evidence_count"`
	ProjectionVersion string   `json:"projection_version"`
}

type EdgeFilter struct {
	EpisodeID string
	BranchID  string
}

type EdgeStore interface {
	ReplaceEdges(context.Context, string, string, []Edge) error
	ListEdges(context.Context, EdgeFilter) ([]Edge, error)
}

type EdgeProjectionStore interface {
	substrate.EventStore
	RegionStore
	EdgeStore
}

func RebuildEdges(ctx context.Context, store EdgeProjectionStore, episodeID, branchID string) ([]Edge, error) {
	events, err := store.List(ctx, substrate.EventFilter{EpisodeID: episodeID, BranchID: branchID})
	if err != nil {
		return nil, err
	}
	regions, err := store.ListRegions(ctx, RegionFilter{EpisodeID: episodeID, BranchID: branchID})
	if err != nil {
		return nil, err
	}
	edges := BuildEdges(episodeID, branchID, events, regions)
	if err = store.ReplaceEdges(ctx, episodeID, branchID, edges); err != nil {
		return nil, err
	}
	return edges, nil
}

type edgeKey struct {
	source, target string
	typeName       EdgeType
}

// BuildEdges creates an undirected, canonical region graph. Shared membership and
// adjacency in the deterministic event timeline are retained as distinct edge types.
func BuildEdges(episodeID, branchID string, events []protocol.Event, regions []Region) []Edge {
	memberships := make(map[string][]string)
	for _, region := range regions {
		for _, eventID := range region.MemberEventIDs {
			memberships[eventID] = append(memberships[eventID], region.RegionID)
		}
	}
	for eventID := range memberships {
		sort.Strings(memberships[eventID])
	}

	counts := make(map[edgeKey]int)
	addPairs := func(regionIDs []string, edgeType EdgeType) {
		for i := 0; i < len(regionIDs); i++ {
			for j := i + 1; j < len(regionIDs); j++ {
				if regionIDs[i] == regionIDs[j] {
					continue
				}
				source, target := canonicalPair(regionIDs[i], regionIDs[j])
				counts[edgeKey{source: source, target: target, typeName: edgeType}]++
			}
		}
	}
	for _, regionIDs := range memberships {
		addPairs(regionIDs, EdgeSharedEvent)
	}

	ordered := append([]protocol.Event(nil), events...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].ValidTime.Equal(ordered[j].ValidTime) {
			return ordered[i].ValidTime.Before(ordered[j].ValidTime)
		}
		if !ordered[i].TransactionTime.Equal(ordered[j].TransactionTime) {
			return ordered[i].TransactionTime.Before(ordered[j].TransactionTime)
		}
		return ordered[i].EventID < ordered[j].EventID
	})
	for start := 0; start < len(ordered); {
		end := start + 1
		for end < len(ordered) && ordered[end].ValidTime.Equal(ordered[start].ValidTime) {
			end++
		}
		if end-start > 1 {
			cooccurring := make(map[string]struct{})
			for _, event := range ordered[start:end] {
				for _, regionID := range memberships[event.EventID] {
					cooccurring[regionID] = struct{}{}
				}
			}
			regionIDs := make([]string, 0, len(cooccurring))
			for regionID := range cooccurring {
				regionIDs = append(regionIDs, regionID)
			}
			sort.Strings(regionIDs)
			addPairs(regionIDs, EdgeSharedEvent)
		}
		start = end
	}
	for i := 1; i < len(ordered); i++ {
		left, right := memberships[ordered[i-1].EventID], memberships[ordered[i].EventID]
		for _, sourceID := range left {
			for _, targetID := range right {
				if sourceID == targetID {
					continue
				}
				source, target := canonicalPair(sourceID, targetID)
				counts[edgeKey{source: source, target: target, typeName: EdgeSequenceAdjacent}]++
			}
		}
	}

	keys := make([]edgeKey, 0, len(counts))
	maxByType := make(map[EdgeType]int)
	for key, count := range counts {
		keys = append(keys, key)
		if count > maxByType[key.typeName] {
			maxByType[key.typeName] = count
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].source != keys[j].source {
			return keys[i].source < keys[j].source
		}
		if keys[i].target != keys[j].target {
			return keys[i].target < keys[j].target
		}
		return keys[i].typeName < keys[j].typeName
	})

	result := make([]Edge, 0, len(keys))
	for _, key := range keys {
		count := counts[key]
		result = append(result, Edge{EdgeID: edgeID(episodeID, branchID, key), EpisodeID: episodeID, BranchID: branchID, SourceRegionID: key.source, TargetRegionID: key.target, Type: key.typeName, Weight: float64(count) / float64(maxByType[key.typeName]), EvidenceCount: count, ProjectionVersion: EdgeProjectorVersion})
	}
	return result
}

func canonicalPair(left, right string) (string, string) {
	if left < right {
		return left, right
	}
	return right, left
}

func edgeID(episodeID, branchID string, key edgeKey) string {
	sum := sha256.Sum256([]byte(EdgeProjectorVersion + "\x00" + episodeID + "\x00" + branchID + "\x00" + key.source + "\x00" + key.target + "\x00" + string(key.typeName)))
	raw := append([]byte(nil), sum[:16]...)
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := hex.EncodeToString(raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
