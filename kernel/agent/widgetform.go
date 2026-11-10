package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Neutral widget model for schema asks delivered as interactive HTML widgets
// (Bot API 10.4, docs/interactive-HITL.md): the kernel owns the semantics —
// fields derived from the ask's JSON Schema subset (§7), the authoritative
// state, validation and the op protocol — while the driver only renders the
// model and ferries ops back. Agents stay untouched: this whole file is a
// pure function over the schema ask_human already produces.
//
// Size discipline (the transport's hard limits): the serialized model must
// fit the widget's data channel (1024 bytes) and one user op fits 64 bytes —
// values larger than that degrade the ask to the hosted-form URL, decided by
// the kernel, never visible to the model.

// WidgetDataLimit mirrors the widget data cap: an encoded model above it
// cannot ride the widget and must fall back to the hosted form.
const WidgetDataLimit = 1024

// WidgetOpLimit mirrors the per-query op cap (telegram: 1-64 bytes).
const WidgetOpLimit = 64

// Widget field kinds — the transport-independent projection of the §7 subset.
type widgetKind string

const (
	widgetText     widgetKind = "text"
	widgetInt      widgetKind = "int"
	widgetNum      widgetKind = "num"
	widgetBool     widgetKind = "bool"
	widgetChoice   widgetKind = "choice"   // string + enum
	widgetMulti    widgetKind = "multi"    // array + items.enum
	widgetDate     widgetKind = "date"     // string + format=date
	widgetDatePair widgetKind = "daterange" // 2-element date array (§7 range picker)
)

// WidgetField is one form field with its current validation error ("" = ok).
type WidgetField struct {
	Name     string   `json:"n"`
	Title    string   `json:"t"`
	Kind     string   `json:"k"`
	Required bool     `json:"rq,omitempty"`
	Options  []string `json:"o,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	MinLen   int      `json:"nl,omitempty"`
	MaxLen   int      `json:"xl,omitempty"`
	Pattern  string   `json:"p,omitempty"`
	Hint     string   `json:"h,omitempty"`
	Error    string   `json:"err,omitempty"`
}

// WidgetState is the authoritative user input plus the resolved marker.
type WidgetState struct {
	Values map[string]any `json:"v,omitempty"`
	Done   bool           `json:"done,omitempty"`
}

// WidgetModel is the full widget payload: what the driver renders and what
// rides back as the widget's data after every accepted op.
type WidgetModel struct {
	Question string        `json:"q"`
	Fields   []WidgetField `json:"f"`
	State    WidgetState   `json:"s"`
}

// WidgetOp is one user action from the widget: set a field value, submit the
// form, or reset it. Field references go by index (tokens stay ≤64 bytes).
type WidgetOp struct {
	Op    string `json:"op"`        // set | submit | reset
	Index int    `json:"i,omitempty"`
	Value any    `json:"v,omitempty"`
}

// ErrWidgetDataTooLarge reports a model the widget data channel cannot carry;
// the delivery falls back to the hosted form, the ask itself is unaffected.
var ErrWidgetDataTooLarge = errors.New("widget model exceeds the data limit")

// widgetSchemaField is the slice of the JSON Schema subset widgetform reads —
// the same subset workspace.ValidateFormValues enforces server-side (§7).
type widgetSchemaField struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	Desc       string `json:"description"`
	Format     string `json:"format"`
	MinItems   *int   `json:"minItems"`
	MaxItems   *int   `json:"maxItems"`
	Minimum    *float64
	Maximum    *float64
	MinLength  int    `json:"minLength"`
	MaxLength  int    `json:"maxLength"`
	Pattern    string `json:"pattern"`
	Enum       []any  `json:"enum"`
	ItemsEnum  []any  `json:"items"`
	RequiredTag bool
}

// BuildWidgetModel derives the widget fields from an ask's schema and seeds
// them with the current state. A schema outside the supported subset (or a
// broken one) errors: the caller degrades to the plain question — the same
// contract the web renderer uses (§7: нерабочая схема не роняет tool call).
func BuildWidgetModel(question string, schema json.RawMessage, state WidgetState) (WidgetModel, error) {
	var parsed struct {
		Type       string                          `json:"type"`
		Required   []string                        `json:"required"`
		Properties map[string]json.RawMessage      `json:"properties"`
		Order      map[string]int                  `json:"-"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return WidgetModel{}, fmt.Errorf("widget schema: %w", err)
	}
	if !strings.EqualFold(parsed.Type, "object") || len(parsed.Properties) == 0 {
		return WidgetModel{}, errors.New("widget schema: not an object with properties")
	}
	required := map[string]bool{}
	for _, name := range parsed.Required {
		required[name] = true
	}
	// Deterministic order: properties then alphabetical (json object order
	// is lost by the parser; the web form sorts the same way).
	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	model := WidgetModel{Question: strings.TrimSpace(question), State: state}
	if model.State.Values == nil {
		model.State.Values = map[string]any{}
	}
	for _, name := range names {
		var prop struct {
			widgetSchemaField
			MinimumAlias *float64 `json:"minimum"`
			MaximumAlias *float64 `json:"maximum"`
			Items        *struct {
				Enum []any `json:"enum"`
				Type string `json:"type"`
				Format string `json:"format"`
			} `json:"items"`
		}
		if err := json.Unmarshal(parsed.Properties[name], &prop); err != nil {
			return WidgetModel{}, fmt.Errorf("widget schema field %q: %w", name, err)
		}
		field := WidgetField{
			Name:     name,
			Title:    firstNonEmpty(prop.Title, name),
			Required: required[name],
			Pattern:  prop.Pattern,
			MinLen:   prop.MinLength,
			MaxLen:   prop.MaxLength,
		}
		field.Min = prop.MinimumAlias
		field.Max = prop.MaximumAlias
		if prop.Desc != "" {
			field.Hint = prop.Desc
		}
		switch {
		case prop.Type == "string" && len(prop.Enum) > 0:
			field.Kind = string(widgetChoice)
			field.Options = widgetStringEnum(prop.Enum)
		case prop.Type == "array" && prop.Items != nil && len(prop.Items.Enum) > 0:
			field.Kind = string(widgetMulti)
			field.Options = widgetStringEnum(prop.Items.Enum)
		case prop.Type == "array" && prop.Items != nil && prop.Items.Format == "date":
			field.Kind = string(widgetDatePair)
			// 0-1 required items may be partially filled; the canonical §7
			// range is minItems 2 / maxItems 2.
			if (prop.MinItems != nil && *prop.MinItems == 2) || (prop.MaxItems != nil && *prop.MaxItems == 2) {
				field.Required = field.Required || (prop.MinItems != nil && *prop.MinItems == 2)
			}
		case prop.Type == "string" && prop.Format == "date":
			field.Kind = string(widgetDate)
		case prop.Type == "boolean":
			field.Kind = string(widgetBool)
		case prop.Type == "integer":
			field.Kind = string(widgetInt)
		case prop.Type == "number":
			field.Kind = string(widgetNum)
		case prop.Type == "string":
			field.Kind = string(widgetText)
		default:
			return WidgetModel{}, fmt.Errorf("widget schema field %q: unsupported type %q", name, prop.Type)
		}
		model.Fields = append(model.Fields, field)
	}
	return model, nil
}

// Encode serializes the model for the widget's data channel, enforcing the
// transport cap.
func (m WidgetModel) Encode() (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if len(raw) > WidgetDataLimit {
		return "", ErrWidgetDataTooLarge
	}
	return string(raw), nil
}

// ParseWidgetModel restores a model from the widget data channel.
func ParseWidgetModel(raw string) (WidgetModel, error) {
	var m WidgetModel
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return WidgetModel{}, fmt.Errorf("widget model: %w", err)
	}
	if m.State.Values == nil {
		m.State.Values = map[string]any{}
	}
	return m, nil
}

// DecodeWidgetOp parses one ≤64-byte op payload from the widget.
func DecodeWidgetOp(data string) (WidgetOp, error) {
	if len(data) > WidgetOpLimit {
		return WidgetOp{}, fmt.Errorf("widget op exceeds %d bytes", WidgetOpLimit)
	}
	var op WidgetOp
	if err := json.Unmarshal([]byte(data), &op); err != nil {
		return WidgetOp{}, fmt.Errorf("widget op: %w", err)
	}
	switch op.Op {
	case "set", "submit", "reset":
	default:
		return WidgetOp{}, fmt.Errorf("widget op %q unsupported", op.Op)
	}
	return op, nil
}

// ApplyWidgetOp folds one user action into the model: set/update a value,
// reset, or submit (validate → mark done). The resolved case returns the
// canonical response JSON — the exact shape the model would have received
// from a web-form submission (§7), so nothing downstream changes.
func ApplyWidgetOp(model WidgetModel, op WidgetOp) (WidgetModel, string, bool, json.RawMessage, error) {
	if model.State.Done {
		return model, "closed", false, nil, nil
	}
	switch op.Op {
	case "reset":
		model.State.Values = map[string]any{}
		model.clearErrors()
		return model, "reset", false, nil, nil
	case "set":
		if op.Index < 0 || op.Index >= len(model.Fields) {
			return model, "bad field", false, nil, fmt.Errorf("widget op: field index %d out of range", op.Index)
		}
		field := model.Fields[op.Index]
		value, vErr := widgetCoerce(field, op.Value)
		if vErr != nil {
			return model, "rejected", false, nil, vErr
		}
		model.State.Values[field.Name] = value
		model.Fields[op.Index].Error = ""
		return model, "ok", false, nil, nil
	case "submit":
		failed := model.validate()
		if failed {
			encoded, _ := model.Encode()
			_ = encoded // size unchanged by errors alone in practice; safe to push
			return model, "invalid", false, nil, nil
		}
		model.State.Done = true
		model.clearErrors()
		canonical, err := json.Marshal(model.State.Values)
		if err != nil {
			return model, "error", false, nil, err
		}
		return model, "accepted", true, canonical, nil
	}
	return model, "", false, nil, fmt.Errorf("widget op %q unsupported", op.Op)
}

// validate checks every field against its constraints (mirroring the §7
// subset semantics), recording per-field errors. Returns true when any field
// failed; missing required fields are the common case.
func (m *WidgetModel) validate() bool {
	failed := false
	for i := range m.Fields {
		field := &m.Fields[i]
		value, present := m.State.Values[field.Name]
		err := widgetFieldError(field, value, present)
		field.Error = err
		if err != "" {
			failed = true
		}
	}
	return failed
}

func (m *WidgetModel) clearErrors() {
	for i := range m.Fields {
		m.Fields[i].Error = ""
	}
}

// widgetCoerce normalizes one incoming op value into the field's type before
// it enters the state (the widget posts JSON scalars/arrays; a hostile client
// may post anything).
func widgetCoerce(field WidgetField, value any) (any, error) {
	switch widgetKind(field.Kind) {
	case widgetBool:
		if v, ok := value.(bool); ok {
			return v, nil
		}
	case widgetInt:
		if f, ok := widgetAsFloat(value); ok && f == math.Trunc(f) {
			return f, nil
		}
	case widgetNum:
		if f, ok := widgetAsFloat(value); ok {
			return f, nil
		}
	case widgetChoice, widgetDate, widgetText:
		if v, ok := value.(string); ok {
			return v, nil
		}
	case widgetMulti:
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("widget field %q: expected a list", field.Name)
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("widget field %q: list items must be strings", field.Name)
			}
			out = append(out, s)
		}
		return out, nil
	case widgetDatePair:
		items, ok := value.([]any)
		if !ok || len(items) > 2 {
			return nil, fmt.Errorf("widget field %q: expected two dates", field.Name)
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok || !widgetIsDate(s) {
				return nil, fmt.Errorf("widget field %q: bad date %q", field.Name, item)
			}
		}
		return items, nil
	}
	return nil, fmt.Errorf("widget field %q: value does not match %s", field.Name, field.Kind)
}

// widgetFieldError validates one field value ("" = valid).
func widgetFieldError(field *WidgetField, value any, present bool) string {
	if !present || value == nil {
		if field.Required {
			return "required"
		}
		return ""
	}
	switch widgetKind(field.Kind) {
	case widgetChoice:
		s, _ := value.(string)
		for _, option := range field.Options {
			if option == s {
				return ""
			}
		}
		return "choose one of the options"
	case widgetMulti:
		items, _ := value.([]any)
		if field.Required && len(items) == 0 {
			return "required"
		}
		for _, item := range items {
			s, _ := item.(string)
			known := false
			for _, option := range field.Options {
				if option == s {
					known = true
					break
				}
			}
			if !known {
				return "unknown option " + s
			}
		}
		return ""
	case widgetDate:
		s, _ := value.(string)
		if !widgetIsDate(s) {
			return "expected YYYY-MM-DD"
		}
	case widgetDatePair:
		items, _ := value.([]any)
		if field.Required && len(items) < 2 {
			return "required"
		}
		if len(items) == 2 && widgetDateOf(items[0]) != "" && widgetDateOf(items[1]) != "" {
			if widgetDateOf(items[0]) > widgetDateOf(items[1]) {
				return "start must not be after the end"
			}
		}
	case widgetText:
		s, _ := value.(string)
		if field.MinLen > 0 && utf8.RuneCountInString(s) < field.MinLen {
			return fmt.Sprintf("at least %d characters", field.MinLen)
		}
		if field.MaxLen > 0 && utf8.RuneCountInString(s) > field.MaxLen {
			return fmt.Sprintf("at most %d characters", field.MaxLen)
		}
		if field.Pattern != "" {
			if re, err := regexp.Compile(field.Pattern); err == nil && !re.MatchString(s) {
				return "does not match the required format"
			}
		}
	case widgetInt, widgetNum:
		f, ok := widgetAsFloat(value)
		if !ok {
			return "expected a number"
		}
		if widgetKind(field.Kind) == widgetInt && f != math.Trunc(f) {
			return "expected a whole number"
		}
		if field.Min != nil && f < *field.Min {
			return fmt.Sprintf("minimum %s", widgetNumString(*field.Min))
		}
		if field.Max != nil && f > *field.Max {
			return fmt.Sprintf("maximum %s", widgetNumString(*field.Max))
		}
	case widgetBool:
		if _, ok := value.(bool); !ok {
			return "expected yes/no"
		}
	}
	return ""
}

func widgetAsFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		// The widget may post numbers as strings in narrow ops; parse
		// strictly so a typo never silently becomes a value.
		var f float64
		_, err := fmt.Sscanf(strings.TrimSpace(v), "%g", &f)
		return f, err == nil
	}
	return 0, false
}

func widgetNumString(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

func widgetIsDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func widgetDateOf(value any) string {
	s, _ := value.(string)
	if widgetIsDate(s) {
		return s
	}
	return ""
}

// widgetStringEnum converts a JSON enum to its string labels; non-strings
// render their canonical form (§7 keeps enums string-typed, this is a guard).
func widgetStringEnum(values []any) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok {
			out = append(out, s)
			continue
		}
		raw, _ := json.Marshal(v)
		out = append(out, string(raw))
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
