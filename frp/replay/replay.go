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
	Manifest Manifest         `json:"manifest"`
	Events   []protocol.Event `json:"events"`
	Digest   string           `json:"digest"`
}

type Service struct{ store substrate.EventStore }

func New(store substrate.EventStore) *Service { return &Service{store: store} }

func (s *Service) Replay(ctx context.Context, filter substrate.EventFilter) (Result, error) {
	return s.ReplayWithManifest(ctx, filter, DefaultManifest())
}

func (s *Service) ReplayWithManifest(ctx context.Context, filter substrate.EventFilter, manifest Manifest) (Result, error) {
	events, err := s.store.List(ctx, filter)
	if err != nil {
		return Result{}, err
	}
	manifest = manifest.withDefaults()
	digestInput := struct {
		Manifest Manifest         `json:"manifest"`
		Events   []protocol.Event `json:"events"`
	}{Manifest: manifest, Events: events}
	encoded, err := json.Marshal(digestInput)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(encoded)
	return Result{Protocol: protocol.Name, Version: protocol.Version, Manifest: manifest, Events: events, Digest: hex.EncodeToString(sum[:])}, nil
}
