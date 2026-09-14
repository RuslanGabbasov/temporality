package projection_test

import (
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	"reflect"
	"testing"
	"time"
)

func TestRegionProjectionIsDeterministicAndRebuildable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []protocol.Event{{EventID: "b", Type: "frame.created", ValidTime: now}, {EventID: "a", Type: "frame.created", ValidTime: now}, {EventID: "c", Type: "claim.candidate", ValidTime: now}}
	first := projection.BuildRegions("episode", "branch", events)
	second := projection.BuildRegions("episode", "branch", []protocol.Event{events[2], events[0], events[1]})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("projection depends on event order: %#v / %#v", first, second)
	}
	if len(first) != 2 || first[1].Activation != 2.0/3.0 || !reflect.DeepEqual(first[1].MemberEventIDs, []string{"a", "b"}) {
		t.Fatalf("unexpected regions: %#v", first)
	}
}
