package observation

import "strings"

// PostgreSQL cannot represent the NUL byte in text or JSONB: a string whose
// bytes carry \x00 makes every JSONB insert fail with «unsupported Unicode
// escape sequence» (SQLSTATE 22P05) — even though encoding/json happily
// escapes it as \u0000. Tool outputs are the usual source (git/tfs listings
// with -z separators, binary file reads), and the journal event is
// observability: scrubbing the byte must always beat failing the run that
// produced it.

// ScrubNULEvent returns the event with NUL bytes removed from its Data values
// and keys and its Evidence strings. The scrubbing is deterministic — the same
// input always yields the same output — so idempotent re-ingestion and the
// outbox's payload-equality conflict checks stay stable. The Data map is
// scrubbed in place (the same contract RedactEvent follows), so maps the
// caller still holds see the cleaned strings too.
func ScrubNULEvent(event Event) Event {
	for key, value := range event.Data {
		event.Data[key] = ScrubNULValue(value)
	}
	for i, evidence := range event.Evidence {
		evidence.Ref = scrubNULString(evidence.Ref)
		evidence.Type = scrubNULString(evidence.Type)
		event.Evidence[i] = evidence
	}
	return event
}

// ScrubNULValue walks the value recursively, removing NUL bytes from every
// string it finds — including map keys, which json.Marshal would otherwise
// turn into \u0000 escapes and which PostgreSQL rejects just as loudly.
// Scalars (numbers, bools, nil, structs) pass through untouched.
func ScrubNULValue(value any) any {
	switch typed := value.(type) {
	case string:
		return scrubNULString(typed)
	case map[string]any:
		scrubbed := make(map[string]any, len(typed))
		for key, inner := range typed {
			scrubbed[scrubNULString(key)] = ScrubNULValue(inner)
		}
		return scrubbed
	case []any:
		for i, inner := range typed {
			typed[i] = ScrubNULValue(inner)
		}
		return typed
	default:
		return value
	}
}

func scrubNULString(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}
