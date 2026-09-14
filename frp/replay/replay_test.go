package replay_test

import (
	"context"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/replay"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestReplayIsDeterministicAndRespectsAsOf(t *testing.T) {
	store := memory.New()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range []protocol.Event{
		{Protocol:"frp",Version:"0.3",EventID:"b",TransactionTime:base.Add(time.Second),ValidTime:base.Add(2*time.Second),EpisodeID:"episode",Type:"frame.created",Payload:map[string]any{},Provenance:map[string]any{}},
		{Protocol:"frp",Version:"0.3",EventID:"a",TransactionTime:base,ValidTime:base,EpisodeID:"episode",Type:"episode.started",Payload:map[string]any{},Provenance:map[string]any{}},
	} { if err := store.Append(context.Background(), e); err != nil { t.Fatal(err) } }

	service := replay.New(store)
	asOf := base.Add(time.Second)
	first, err := service.Replay(context.Background(), substrate.EventFilter{EpisodeID:"episode", AsOf:&asOf})
	if err != nil { t.Fatal(err) }
	second, err := service.Replay(context.Background(), substrate.EventFilter{EpisodeID:"episode", AsOf:&asOf})
	if err != nil { t.Fatal(err) }
	if len(first.Events) != 1 || first.Events[0].EventID != "a" { t.Fatalf("unexpected replay: %#v", first.Events) }
	if first.Digest != second.Digest { t.Fatalf("non-deterministic digest: %s != %s", first.Digest, second.Digest) }
}

func TestDuplicateEventIsRejected(t *testing.T) {
	store := memory.New()
	e := protocol.Event{Protocol:"frp",Version:"0.3",EventID:"same",TransactionTime:time.Now(),ValidTime:time.Now(),Type:"episode.started",Payload:map[string]any{},Provenance:map[string]any{}}
	if err := store.Append(context.Background(), e); err != nil { t.Fatal(err) }
	if err := store.Append(context.Background(), e); err == nil { t.Fatal("expected duplicate rejection") }
}
