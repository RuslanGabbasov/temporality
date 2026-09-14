package projection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
)

const RegionProjectorVersion = "region-event-type.v1"

type Region struct {
	RegionID          string    `json:"region_id"`
	EpisodeID         string    `json:"episode_id"`
	BranchID          string    `json:"branch_id"`
	Kind              string    `json:"kind"`
	Label             string    `json:"label"`
	Version           int       `json:"version"`
	ValidFrom         time.Time `json:"valid_from"`
	Activation        float64   `json:"activation"`
	ProjectionVersion string    `json:"projection_version"`
	MemberEventIDs    []string  `json:"member_event_ids"`
}
type RegionFilter struct {
	EpisodeID string
	BranchID  string
	AsOf      *time.Time
}
type RegionStore interface {
	ReplaceRegions(context.Context, string, string, []Region) error
	ListRegions(context.Context, RegionFilter) ([]Region, error)
}

func BuildRegions(episodeID, branchID string, events []protocol.Event) []Region {
	groups := map[string][]protocol.Event{}
	for _, event := range events {
		groups[event.Type] = append(groups[event.Type], event)
	}
	labels := make([]string, 0, len(groups))
	for label := range groups {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	regions := make([]Region, 0, len(labels))
	total := len(events)
	for _, label := range labels {
		members := groups[label]
		ids := make([]string, 0, len(members))
		validFrom := members[0].ValidTime
		for _, event := range members {
			ids = append(ids, event.EventID)
			if event.ValidTime.Before(validFrom) {
				validFrom = event.ValidTime
			}
		}
		sort.Strings(ids)
		activation := 0.0
		if total > 0 {
			activation = float64(len(members)) / float64(total)
		}
		regions = append(regions, Region{RegionID: regionID(episodeID, branchID, label), EpisodeID: episodeID, BranchID: branchID, Kind: "event_type", Label: label, Version: 1, ValidFrom: validFrom, Activation: activation, ProjectionVersion: RegionProjectorVersion, MemberEventIDs: ids})
	}
	return regions
}
func regionID(episodeID, branchID, label string) string {
	sum := sha256.Sum256([]byte(RegionProjectorVersion + "\x00" + episodeID + "\x00" + branchID + "\x00" + label))
	raw := sum[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := hex.EncodeToString(raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
