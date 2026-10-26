package workspace

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// HITL form validation (docs/interactive-HITL.md §7). The agent attaches an
// optional JSON Schema to an ask_human request; the submission endpoint
// validates the human's values against it server-side before the answer
// closes the row. Client-side checks are convenience only — this file is the
// authority.
//
// Supported subset — deliberately small, so every UI (chat card, runs page)
// and every future channel renderer can implement it faithfully:
//
//	type        string | number | integer | boolean | array
//	enum        string enums (single: radio/select; array: checkbox group)
//	required    top-level list of property names
//	minLength / maxLength   strings
//	pattern     RE2 regexp on strings, unanchored (JSON Schema semantics)
//	minimum / maximum       numbers
//	format      password, date, date-time, time, email, url
//	items       array element schema (string); with enum — multi-select,
//	            with format date and minItems=maxItems=2 — a date range
//	minItems / maxItems     arrays (bounded at 100 elements)
//
// Unknown keywords are ignored; unknown types reject the value (an
// unrecognized constraint must not silently pass). Patterns must compile as
// Go RE2 — the same subset is valid in browsers, so both validators agree.

// formSchemaMaxProperties bounds a form's width — a card with a hundred
// fields is unreadable and the canonical values would bloat the answer.
const formSchemaMaxProperties = 20

// formValuesMaxBytes bounds the canonical values JSON stored as the response.
const formValuesMaxBytes = 4 * 1024

// formPatternMaxLen bounds a field's regex source; formArrayMaxItems bounds
// one array value — both keep a hostile or careless schema from bloating the
// validation path and the canonical answer.
const (
	formPatternMaxLen = 256
	formArrayMaxItems = 100
)

// ValidateFormValues checks submitted values against the schema. It returns
// the values unchanged; the error names every violating field.
func ValidateFormValues(schema json.RawMessage, values map[string]any) error {
	var parsed struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if len(schema) == 0 {
		return nil
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return fmt.Errorf("invalid form schema: %w", err)
	}
	if parsed.Type != "" && parsed.Type != "object" {
		return fmt.Errorf("invalid form schema: top-level type must be object, got %q", parsed.Type)
	}
	if len(parsed.Properties) == 0 {
		return fmt.Errorf("invalid form schema: no properties")
	}
	if len(parsed.Properties) > formSchemaMaxProperties {
		return fmt.Errorf("invalid form schema: more than %d properties", formSchemaMaxProperties)
	}
	var problems []string
	seen := map[string]bool{}
	for _, name := range parsed.Required {
		value, ok := values[name]
		if !ok || value == nil || (isEmptyString(value)) {
			problems = append(problems, fmt.Sprintf("%s: required", name))
			continue
		}
		if err := validateFormField(name, parsed.Properties[name], value); err != nil {
			problems = append(problems, err.Error())
		}
		seen[name] = true
	}
	for name, value := range values {
		if seen[name] {
			continue
		}
		propertySchema, ok := parsed.Properties[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: unknown field", name))
			continue
		}
		if value == nil || isEmptyString(value) {
			continue // optional field left empty
		}
		if err := validateFormField(name, propertySchema, value); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("form validation failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// isEmptyString treats a blank string as an unfilled field — required must
// reject it, optional may skip validation.
func isEmptyString(value any) bool {
	s, ok := value.(string)
	return ok && strings.TrimSpace(s) == ""
}

func validateFormField(name string, propertySchema, value any) error {
	var field struct {
		Type      string          `json:"type"`
		Enum      []string        `json:"enum"`
		MinLength *int            `json:"minLength"`
		MaxLength *int            `json:"maxLength"`
		Pattern   string          `json:"pattern"`
		Format    string          `json:"format"`
		Minimum   *float64        `json:"minimum"`
		Maximum   *float64        `json:"maximum"`
		Items     json.RawMessage `json:"items"`
		MinItems  *int            `json:"minItems"`
		MaxItems  *int            `json:"maxItems"`
	}
	if raw, err := json.Marshal(propertySchema); err == nil {
		_ = json.Unmarshal(raw, &field)
	}
	switch field.Type {
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: expected string", name)
		}
		if field.MinLength != nil && len([]rune(s)) < *field.MinLength {
			return fmt.Errorf("%s: shorter than %d characters", name, *field.MinLength)
		}
		if field.MaxLength != nil && len([]rune(s)) > *field.MaxLength {
			return fmt.Errorf("%s: longer than %d characters", name, *field.MaxLength)
		}
		if err := validateStringFormat(name, field.Format, s); err != nil {
			return err
		}
		if err := validateStringPattern(name, field.Pattern, s); err != nil {
			return err
		}
		if len(field.Enum) > 0 && !slicesContains(field.Enum, s) {
			return fmt.Errorf("%s: must be one of: %s", name, strings.Join(field.Enum, ", "))
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array", name)
		}
		if len(items) > formArrayMaxItems {
			return fmt.Errorf("%s: more than %d elements", name, formArrayMaxItems)
		}
		if field.MinItems != nil && len(items) < *field.MinItems {
			return fmt.Errorf("%s: fewer than %d elements", name, *field.MinItems)
		}
		if field.MaxItems != nil && len(items) > *field.MaxItems {
			return fmt.Errorf("%s: more than %d elements", name, *field.MaxItems)
		}
		for i, elem := range items {
			if len(field.Enum) > 0 {
				es, ok := elem.(string)
				if !ok || !slicesContains(field.Enum, es) {
					return fmt.Errorf("%s[%d]: must be one of: %s", name, i, strings.Join(field.Enum, ", "))
				}
				continue
			}
			if len(field.Items) > 0 {
				if err := validateFormField(fmt.Sprintf("%s[%d]", name, i), json.RawMessage(field.Items), elem); err != nil {
					return err
				}
			}
		}
	case "number", "integer":
		n, ok := numericValue(value)
		if !ok {
			return fmt.Errorf("%s: expected number", name)
		}
		if field.Type == "integer" && n != math.Trunc(n) {
			return fmt.Errorf("%s: expected an integer", name)
		}
		if field.Minimum != nil && n < *field.Minimum {
			return fmt.Errorf("%s: must be ≥ %v", name, *field.Minimum)
		}
		if field.Maximum != nil && n > *field.Maximum {
			return fmt.Errorf("%s: must be ≤ %v", name, *field.Maximum)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected boolean", name)
		}
	case "":
		// A field without a type accepts anything — the agent opted out of
		// validation for it.
	default:
		return fmt.Errorf("%s: unsupported form field type %q", name, field.Type)
	}
	return nil
}

// validateStringPattern applies the field's regex with JSON Schema semantics:
// unanchored — the pattern matches when any substring matches.
func validateStringPattern(name, pattern, s string) error {
	if pattern == "" {
		return nil
	}
	if len(pattern) > formPatternMaxLen {
		return fmt.Errorf("%s: pattern longer than %d characters", name, formPatternMaxLen)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("%s: invalid schema pattern: %v", name, err)
	}
	if !re.MatchString(s) {
		return fmt.Errorf("%s: does not match the required format", name)
	}
	return nil
}

// validateStringFormat checks the declared string format. "password" is a
// rendering hint and needs no check; the rest are structural formats.
func validateStringFormat(name, format, s string) error {
	switch format {
	case "", "password":
		return nil
	case "date":
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return fmt.Errorf("%s: expected a date (YYYY-MM-DD)", name)
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			// Browser datetime-local controls emit "2006-01-02T15:04" — no
			// seconds, no zone. The submission UI normalizes to full RFC 3339
			// with the user's offset, but direct API callers may send the naive
			// shape; accept it and treat it as UTC.
			naive := false
			for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
				if _, err := time.Parse(layout, s); err == nil {
					naive = true
					break
				}
			}
			if !naive {
				return fmt.Errorf("%s: expected a date-time (RFC 3339)", name)
			}
		}
	case "time":
		if _, err := time.Parse("15:04:05", s); err != nil {
			if _, err := time.Parse("15:04", s); err != nil {
				return fmt.Errorf("%s: expected a time (HH:MM:SS)", name)
			}
		}
	case "email":
		if !formEmailRe.MatchString(s) {
			return fmt.Errorf("%s: expected an email address", name)
		}
	case "url":
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("%s: expected a URL", name)
		}
	default:
		// Unknown declared format: the value stays a plain string. Rejecting
		// it here would break forms rendered by older UIs for no reason — the
		// format is a hint, not a constraint the renderer must know.
	}
	return nil
}

// formEmailRe is deliberately loose: it catches typos and garbage, not
// deliverability — that belongs to confirmation, not to a form validator.
var formEmailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// CanonicalFormValues serializes validated values into the canonical response
// JSON the workflow receives as the tool result. Key order is deterministic,
// the size is bounded.
func CanonicalFormValues(values map[string]any) (string, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	if len(encoded) > formValuesMaxBytes {
		return "", fmt.Errorf("form values exceed %d bytes", formValuesMaxBytes)
	}
	return string(encoded), nil
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
