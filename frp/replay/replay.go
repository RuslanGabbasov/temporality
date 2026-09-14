package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
)

type Result struct {
	Protocol string           `json:"protocol"`
	Version  string           `json:"version"`
	Events   []protocol.Event `json:"events"`
	Digest   string           `json:"digest"`
}

type Service struct { store substrate.EventStore }

func New(store substrate.EventStore) *Service { return &Service{store: store} }

func (s *Service) Replay(ctx context.Context, filter substrate.EventFilter) (Result, error) {
	events, err := s.store.List(ctx, filter)
	if err != nil { return Result{}, err }
	encoded, err := json.Marshal(events)
	if err != nil { return Result{}, err }
	sum := sha256.Sum256(encoded)
	return Result{Protocol: protocol.Name, Version: protocol.Version, Events: events, Digest: hex.EncodeToString(sum[:])}, nil
}
