package workspace

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustSchema(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	return json.RawMessage(raw)
}

func TestValidateFormValuesRequired(t *testing.T) {
	schema := mustSchema(t, `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	if err := ValidateFormValues(schema, nil); err == nil || !strings.Contains(err.Error(), "name: required") {
		t.Fatalf("expected required error, got %v", err)
	}
	if err := ValidateFormValues(schema, map[string]any{"name": "   "}); err == nil {
		t.Fatal("expected blank string to violate required")
	}
	if err := ValidateFormValues(schema, map[string]any{"name": "billing"}); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestValidateFormValuesFieldConstraints(t *testing.T) {
	schema := mustSchema(t, `{"type":"object","properties":{
		"name":{"type":"string","minLength":3,"maxLength":10},
		"priority":{"type":"string","enum":["high","normal"]},
		"count":{"type":"integer","minimum":1,"maximum":100},
		"ratio":{"type":"number","minimum":0,"maximum":1},
		"confirm":{"type":"boolean"}
	}}`)
	cases := []struct {
		name    string
		values  map[string]any
		wantErr string
	}{
		{"short string", map[string]any{"name": "ab"}, "shorter than 3"},
		{"long string", map[string]any{"name": "abcdefghijkl"}, "longer than 10"},
		{"off-enum", map[string]any{"name": "ok", "priority": "urgent"}, "must be one of"},
		{"non-integer", map[string]any{"name": "ok", "count": 1.5}, "expected an integer"},
		{"below minimum", map[string]any{"name": "ok", "count": float64(0)}, "must be ≥ 1"},
		{"above maximum", map[string]any{"name": "ok", "count": float64(101)}, "must be ≤ 100"},
		{"ratio bounds", map[string]any{"name": "ok", "ratio": 1.5}, "must be ≤ 1"},
		{"boolean type", map[string]any{"name": "ok", "confirm": "yes"}, "expected boolean"},
		{"string type", map[string]any{"name": 42}, "expected string"},
		{"unknown field", map[string]any{"name": "ok", "extra": 1}, "unknown field"},
	}
	for _, tc := range cases {
		err := ValidateFormValues(schema, tc.values)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: expected error containing %q, got %v", tc.name, tc.wantErr, err)
		}
	}
	// The happy path fills every field validly.
	ok := map[string]any{"name": "billing", "priority": "high", "count": float64(3), "ratio": 0.5, "confirm": true}
	if err := ValidateFormValues(schema, ok); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestValidateFormValuesOptionalEmptySkipsChecks(t *testing.T) {
	schema := mustSchema(t, `{"type":"object","properties":{"note":{"type":"string","minLength":5}}}`)
	if err := ValidateFormValues(schema, map[string]any{"note": ""}); err != nil {
		t.Fatalf("optional empty field must pass, got %v", err)
	}
}

func TestValidateFormValuesUnsupportedAndBrokenSchema(t *testing.T) {
	unsupported := mustSchema(t, `{"type":"object","properties":{"tree":{"type":"object"}}}`)
	if err := ValidateFormValues(unsupported, map[string]any{"tree": map[string]any{"x": 1}}); err == nil || !strings.Contains(err.Error(), "unsupported form field type") {
		t.Fatalf("expected unsupported type error, got %v", err)
	}
	broken := mustSchema(t, `{"type":"object"}`)
	if err := ValidateFormValues(broken, map[string]any{"x": 1}); err == nil || !strings.Contains(err.Error(), "no properties") {
		t.Fatalf("expected no-properties error, got %v", err)
	}
	malformed := mustSchema(t, `{oops`)
	if err := ValidateFormValues(malformed, nil); err == nil || !strings.Contains(err.Error(), "invalid form schema") {
		t.Fatalf("expected malformed schema error, got %v", err)
	}
}

func TestCanonicalFormValues(t *testing.T) {
	encoded, err := CanonicalFormValues(map[string]any{"name": "billing", "count": float64(2)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal([]byte(encoded), &round); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if round["name"] != "billing" || round["count"] != float64(2) {
		t.Fatalf("unexpected canonical values: %v", round)
	}
}

func TestValidateFormValuesPattern(t *testing.T) {
	// JSON Schema semantics are unanchored: a pattern without anchors matches
	// any substring.
	schema := mustSchema(t, `{"type":"object","properties":{"ticket":{"type":"string","pattern":"[A-Z]+-[0-9]+"}}}`)
	if err := ValidateFormValues(schema, map[string]any{"ticket": "fix the bug"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected pattern violation, got %v", err)
	}
	if err := ValidateFormValues(schema, map[string]any{"ticket": "see PROJ-123 please"}); err != nil {
		t.Fatalf("unanchored match must pass, got %v", err)
	}
	// An explicit anchor makes the whole-string match mandatory.
	anchored := mustSchema(t, `{"type":"object","properties":{"code":{"type":"string","pattern":"^[a-z]+-[0-9]+$"}}}`)
	if err := ValidateFormValues(anchored, map[string]any{"code": "see proj-1 please"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("anchored pattern must reject substrings, got %v", err)
	}
	broken := mustSchema(t, `{"type":"object","properties":{"x":{"type":"string","pattern":"([bad"}}}`)
	if err := ValidateFormValues(broken, map[string]any{"x": "y"}); err == nil || !strings.Contains(err.Error(), "invalid schema pattern") {
		t.Fatalf("expected invalid pattern error, got %v", err)
	}
}

func TestValidateFormValuesStringFormats(t *testing.T) {
	schema := mustSchema(t, `{"type":"object","properties":{
		"day":{"type":"string","format":"date"},
		"at":{"type":"string","format":"date-time"},
		"mail":{"type":"string","format":"email"},
		"link":{"type":"string","format":"url"},
		"secret":{"type":"string","format":"password"},
		"clock":{"type":"string","format":"time"}
	}}`)
	good := map[string]any{
		"day": "2026-03-01", "at": "2026-03-01T10:00:00Z", "mail": "a@b.co",
		"link": "https://x.example/y", "secret": "s3cret", "clock": "10:00:00",
	}
	if err := ValidateFormValues(schema, good); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
	// Browser-native shapes: datetime-local (no seconds, no zone) and bare
	// HH:MM are accepted — the UI normalizes to full RFC 3339 anyway.
	browser := map[string]any{"at": "2026-03-01T10:30", "clock": "14:30"}
	if err := ValidateFormValues(schema, browser); err != nil {
		t.Fatalf("browser-native shapes must pass, got %v", err)
	}
	bad := map[string]any{"day": "01.03.2026"}
	if err := ValidateFormValues(schema, bad); err == nil || !strings.Contains(err.Error(), "expected a date") {
		t.Fatalf("expected date format violation, got %v", err)
	}
	badMail := map[string]any{"mail": "not-a-mail"}
	if err := ValidateFormValues(schema, badMail); err == nil || !strings.Contains(err.Error(), "expected an email") {
		t.Fatalf("expected email format violation, got %v", err)
	}
	badDT := map[string]any{"at": "01.03.2026 10:30"}
	if err := ValidateFormValues(schema, badDT); err == nil || !strings.Contains(err.Error(), "expected a date-time") {
		t.Fatalf("expected date-time violation, got %v", err)
	}
}

func TestValidateFormValuesArray(t *testing.T) {
	schema := mustSchema(t, `{"type":"object","properties":{
		"services":{"type":"array","items":{"type":"string","enum":["api","web","db"]},"minItems":1,"maxItems":3},
		"window":{"type":"array","minItems":2,"maxItems":2,"items":{"type":"string","format":"date"}}
	}}`)
	ok := map[string]any{"services": []any{"api", "db"}, "window": []any{"2026-03-01", "2026-03-05"}}
	if err := ValidateFormValues(schema, ok); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
	if err := ValidateFormValues(schema, map[string]any{"services": []any{"api", "cache"}}); err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("expected enum violation inside array, got %v", err)
	}
	if err := ValidateFormValues(schema, map[string]any{"window": []any{"2026-03-01"}}); err == nil || !strings.Contains(err.Error(), "fewer than 2") {
		t.Fatalf("expected minItems violation, got %v", err)
	}
	if err := ValidateFormValues(schema, map[string]any{"window": []any{"2026-03-01", "oops"}}); err == nil || !strings.Contains(err.Error(), "window[1]") {
		t.Fatalf("expected per-element format violation, got %v", err)
	}
	if err := ValidateFormValues(schema, map[string]any{"services": "api"}); err == nil || !strings.Contains(err.Error(), "expected array") {
		t.Fatalf("expected array type violation, got %v", err)
	}
}
