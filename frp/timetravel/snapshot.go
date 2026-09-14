package timetravel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

const (
	Protocol        = "frp"
	ProtocolVersion = "0.3"
	SnapshotVersion = "1"
)

// EventCursor identifies a stable position in the event order. EventID is the
// tie-breaker for events sharing a transaction timestamp.
type EventCursor struct {
	TransactionTime time.Time `json:"tx_time"`
	EventID         string    `json:"event_id"`
}

type SnapshotMetadata struct {
	Protocol        string      `json:"protocol"`
	ProtocolVersion string      `json:"protocol_version"`
	SnapshotVersion string      `json:"snapshot_version"`
	SnapshotID      string      `json:"snapshot_id"`
	EpisodeID       string      `json:"episode_id"`
	CreatedAt       time.Time   `json:"created_at"`
	Through         EventCursor `json:"through"`
	ContentHash     string      `json:"content_hash"`
}

// Snapshot contains JSON inline by design. Persistence and object-store access
// are outside this pure package.
type Snapshot struct {
	Metadata SnapshotMetadata `json:"metadata"`
	Content  json.RawMessage  `json:"content"`
}

func NewSnapshot(metadata SnapshotMetadata, content json.RawMessage) (Snapshot, error) {
	metadata.Protocol = Protocol
	metadata.ProtocolVersion = ProtocolVersion
	metadata.SnapshotVersion = SnapshotVersion
	hash, err := CanonicalJSONHash(content)
	if err != nil {
		return Snapshot{}, err
	}
	metadata.ContentHash = hash
	snapshot := Snapshot{Metadata: metadata, Content: append(json.RawMessage(nil), content...)}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (s Snapshot) Validate() error {
	m := s.Metadata
	if m.Protocol != Protocol || m.ProtocolVersion != ProtocolVersion || m.SnapshotVersion != SnapshotVersion {
		return fmt.Errorf("unsupported snapshot version %q/%q/%q", m.Protocol, m.ProtocolVersion, m.SnapshotVersion)
	}
	if m.SnapshotID == "" || m.EpisodeID == "" {
		return errors.New("snapshot_id and episode_id are required")
	}
	if m.CreatedAt.IsZero() || m.Through.TransactionTime.IsZero() || m.Through.EventID == "" {
		return errors.New("created_at and complete through cursor are required")
	}
	hash, err := CanonicalJSONHash(s.Content)
	if err != nil {
		return fmt.Errorf("snapshot content: %w", err)
	}
	if m.ContentHash != hash {
		return fmt.Errorf("content_hash mismatch: got %q, want %q", m.ContentHash, hash)
	}
	return nil
}

// CanonicalJSONHash hashes a whitespace-independent JSON encoding with object
// keys sorted recursively and equivalent JSON number spellings normalized.
func CanonicalJSONHash(content json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", err
	}
	value, err := normalizeJSONNumbers(value)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonical JSON: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeJSONNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON number %q: %w", typed, err)
		}
		return number, nil
	case []any:
		for i := range typed {
			normalized, err := normalizeJSONNumbers(typed[i])
			if err != nil {
				return nil, err
			}
			typed[i] = normalized
		}
	case map[string]any:
		for key, item := range typed {
			normalized, err := normalizeJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			typed[key] = normalized
		}
	}
	return value, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON: multiple values")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

type SnapshotSelection struct {
	AsOf        time.Time   `json:"as_of"`
	Cursor      EventCursor `json:"cursor"`
	AvailableAt time.Time   `json:"available_at"`
}

func (s SnapshotSelection) Validate() error {
	if s.AsOf.IsZero() || s.Cursor.TransactionTime.IsZero() || s.Cursor.EventID == "" || s.AvailableAt.IsZero() {
		return errors.New("as_of, available_at, and complete cursor are required")
	}
	return nil
}

// SelectSnapshot chooses the furthest valid snapshot visible at the supplied
// wall-clock time and not beyond either the as-of time or event cursor.
func SelectSnapshot(snapshots []Snapshot, query SnapshotSelection) (Snapshot, bool, error) {
	if err := query.Validate(); err != nil {
		return Snapshot{}, false, err
	}
	eligible := make([]Snapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if err := snapshot.Validate(); err != nil {
			return Snapshot{}, false, fmt.Errorf("snapshot %q: %w", snapshot.Metadata.SnapshotID, err)
		}
		m := snapshot.Metadata
		if !m.CreatedAt.After(query.AvailableAt) && !m.Through.TransactionTime.After(query.AsOf) && compareCursor(m.Through, query.Cursor) <= 0 {
			eligible = append(eligible, snapshot)
		}
	}
	if len(eligible) == 0 {
		return Snapshot{}, false, nil
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := eligible[i].Metadata, eligible[j].Metadata
		if comparison := compareCursor(a.Through, b.Through); comparison != 0 {
			return comparison > 0
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.SnapshotID < b.SnapshotID
	})
	return eligible[0], true, nil
}

func compareCursor(a, b EventCursor) int {
	if a.TransactionTime.Before(b.TransactionTime) {
		return -1
	}
	if a.TransactionTime.After(b.TransactionTime) {
		return 1
	}
	if a.EventID < b.EventID {
		return -1
	}
	if a.EventID > b.EventID {
		return 1
	}
	return 0
}
