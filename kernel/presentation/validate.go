package presentation

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validation turns an untrusted IR payload into a normalized, proven-safe
// document (docs/presentation-engine.md §14). The renderer contract treats
// everything here as hostile input: unknown fields are rejected, URLs are
// scheme-checked, string fields are control-character checked and every
// collection is bounded — no executable instruction can survive validation
// because the IR has no field where code could hide.

// Validate parses and validates one raw IR payload, returning the normalized
// document (aliases resolved to canonical component types).
func Validate(raw []byte, policy Policy) (Document, error) {
	if len(raw) == 0 {
		return Document{}, fmt.Errorf("presentation IR is empty")
	}
	limits := policy.effective()
	if len(raw) > limits.MaxDocumentBytes {
		return Document{}, fmt.Errorf("presentation IR exceeds %d bytes", limits.MaxDocumentBytes)
	}
	doc, err := ParseDocument(raw)
	if err != nil {
		return Document{}, err
	}
	if err := ValidateDocument(&doc, policy); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// ValidateDocument validates an already-parsed document in place: component
// alias types are rewritten to canonical slugs and structural errors are
// reported with the offending path.
func ValidateDocument(doc *Document, policy Policy) error {
	v := &validation{
		policy: policy,
		limits: policy.effective(),
		ids:    map[string]bool{},
	}
	if doc.Version != IRVersion {
		v.failf("version: unsupported IR version %q (want %q)", doc.Version, IRVersion)
	}
	if doc.Type != DocumentType {
		v.failf("type: unsupported document type %q (want %q)", doc.Type, DocumentType)
	}
	checkShortString(v, "id", doc.ID, 128)
	checkLabel(v, "title", doc.Title)
	if len(doc.Content) == 0 {
		v.failf("content: document has no components")
	}
	for i := range doc.Content {
		v.validateComponent(&doc.Content[i], fmt.Sprintf("content[%d]", i), 1)
	}
	if v.count > v.limits.MaxComponents {
		v.failf("content: document holds %d components, limit is %d", v.count, v.limits.MaxComponents)
	}
	if len(v.errs) > 0 {
		return fmt.Errorf("presentation IR validation failed:\n%s", strings.Join(v.errs, "\n"))
	}
	return nil
}

// validation carries one document validation pass.
type validation struct {
	policy Policy
	limits Limits
	path   string
	count  int
	ids    map[string]bool
	errs   []string
}

// failf records one violation with the current component path.
func (v *validation) failf(format string, args ...any) {
	if len(v.errs) >= 10 {
		return // the first ten violations are enough to fix a document
	}
	v.errs = append(v.errs, v.path+": "+fmt.Sprintf(format, args...))
}

// validateComponent checks one component against its registry spec, its
// per-type shape and the safety policy, normalizing aliases in place.
func (v *validation) validateComponent(c *Component, path string, depth int) {
	v.path = path
	if depth > v.limits.MaxDepth {
		v.failf("nesting deeper than %d levels", v.limits.MaxDepth)
		return
	}
	if c.Type == "" {
		v.failf("component type is required")
		return
	}
	spec := Default().Lookup(c.Type)
	if spec == nil {
		v.failf("unknown component type %q", c.Type)
		return
	}
	if v.policy.Disabled(spec.Type) {
		v.failf("component type %q is disabled by policy", spec.Type)
		return
	}
	// Normalize the alias slug to its canonical type before dispatching to
	// the per-type validator, then let the alias imply its display props
	// ("pie-chart" -> chart with chartType pie).
	canonical := Default().NormalizeType(c.Type)
	if canonical != c.Type {
		aliasDefaults(c, canonical)
		planningAliasDefaults(c, canonical)
	}
	c.Type = canonical
	v.count++
	checkShortString(v, "id", c.ID, 128)
	if c.ID != "" {
		if v.ids[c.ID] {
			v.failf("duplicate component id %q", c.ID)
		}
		v.ids[c.ID] = true
	}
	// Declared interactive actions are checked against the spec's allowlist
	// and the action safety rules (§11.2) before the type body.
	v.validateActions(c, spec)
	if entry, isCustom := customLookup(c.Type); isCustom {
		entry.validator(v, c)
	} else if validator := componentValidators[c.Type]; validator != nil {
		validator(v, c)
	} else {
		// Every registry entry must ship a validator: registration without
		// one is a registry bug, not a document problem.
		v.failf("component type %q has no validator (registry bug)", c.Type)
		return
	}
	// Children are container-only: a text component must not smuggle nested
	// structure past its validator.
	if len(c.Children) > 0 {
		if len(spec.Containers) == 0 {
			v.failf("component type %q does not accept children", c.Type)
		}
		if len(c.Children) > v.limits.MaxChildren {
			v.failf("more than %d children", v.limits.MaxChildren)
			c.Children = nil
			return
		}
		savedPath := v.path
		for i := range c.Children {
			childPath := fmt.Sprintf("%s.children[%d]", path, i)
			if len(spec.Containers) > 0 && !spec.acceptsChild(canonicalChildType(&c.Children[i])) {
				v.failf("%s: child type %q is not allowed in %q", childPath, c.Children[i].Type, c.Type)
				continue
			}
			v.validateComponent(&c.Children[i], childPath, depth+1)
		}
		v.path = savedPath
	}
}

// canonicalChildType resolves a child's alias before the allowlist check so
// "pie-chart" passes a "chart" allowlist.
func canonicalChildType(c *Component) string {
	return Default().NormalizeType(c.Type)
}

// validateActions enforces the action safety rules (docs/presentation-engine.md
// §11.2): registered type, spec allowlist, unambiguous target, object payload
// of bounded size. Payload is data — never shell, JavaScript or SQL; those
// languages simply have no channel into the IR.
func (v *validation) validateActions(c *Component, spec *Spec) {
	if len(c.Actions) == 0 {
		return
	}
	if len(c.Actions) > 8 {
		v.failf("more than %d actions", 8)
		c.Actions = nil
		return
	}
	allowed := map[string]bool{}
	for _, name := range spec.Actions {
		allowed[name] = true
	}
	for i := range c.Actions {
		action := &c.Actions[i]
		actionPath := fmt.Sprintf("actions[%d]", i)
		if action.Type == "" {
			v.failf("%s: action type is required", actionPath)
			continue
		}
		if known := Default().Lookup(action.Type); known == nil && !actionTypes[action.Type] {
			v.failf("%s: unknown action type %q", actionPath, action.Type)
			continue
		}
		// An empty spec allowlist means the component declares no actions at
		// all — only the interactive types carry them (§6.7).
		if len(allowed) == 0 || !allowed[action.Type] {
			v.failf("%s: action type %q is not supported by component %q", actionPath, action.Type, c.Type)
		}
		checkLabel(v, actionPath+".label", action.Label)
		if action.Target == "" && action.Type != "open" {
			v.failf("%s: action target is required", actionPath)
		} else if action.Type == "open" || action.Target != "" {
			if strings.Contains(action.Target, "://") && !safeURL(action.Target) {
				v.failf("%s: target %q is not an allowed URL", actionPath, action.Target)
			}
		}
		if len(action.Payload) > actionPayloadMaxBytes {
			v.failf("%s: payload exceeds %d bytes", actionPath, actionPayloadMaxBytes)
			action.Payload = nil
			continue
		}
		if len(action.Payload) > 0 && !rawObject(action.Payload) {
			v.failf("%s: payload must be a JSON object", actionPath)
			action.Payload = nil
		}
	}
}

// actionPayloadMaxBytes bounds one declared action payload: decisions and
// form summaries, never data dumps.
const actionPayloadMaxBytes = 4 << 10

// actionTypes is the global action vocabulary (docs/presentation-engine.md
// §10.2, §11): submit carries a decision to a target, select picks an option,
// open navigates to a safe URL.
var actionTypes = map[string]bool{
	"submit": true,
	"select": true,
	"open":   true,
}

// safeURL accepts only http(s) absolute URLs and in-app relative paths
// starting with a single slash. Everything else — javascript:, data:, vbscript:,
// protocol-relative host tricks — is refused at validation time, so neither
// the web renderer nor the channel adapters can ever see a dangerous URL.
func safeURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// checkURL validates an optional URL field; empty is allowed when optional.
func checkURL(v *validation, field, raw string, required bool) {
	if raw == "" {
		if required {
			v.failf("%s is required", field)
		}
		return
	}
	if !safeURL(raw) {
		v.failf("%s: %q is not an allowed URL (http(s) or in-app path)", field, truncateLabel(raw))
	}
}

// checkLabel validates a human-facing string: bounded, no control characters.
// Labels are data — the renderers escape them, but raw control characters are
// refused outright because no honest report needs them.
func checkLabel(v *validation, field, raw string) {
	checkString(v, field, raw, v.limits.MaxLabelRunes, false)
}

// checkContent validates a component's text content: bounded by
// MaxContentBytes and free of control characters. Empty content is rejected —
// a text-bearing component with nothing to say is a document bug.
func checkContent(v *validation, field, raw string) {
	if strings.TrimSpace(raw) == "" {
		v.failf("%s is required", field)
		return
	}
	checkString(v, field, raw, v.limits.MaxContentBytes, true)
}

// checkString enforces the common string rules; max is in bytes for content
// fields and runes for labels.
func checkString(v *validation, field, raw string, max int, bytes bool) {
	size := utf8.RuneCountInString(raw)
	if bytes {
		size = len(raw)
	}
	if size > max {
		v.failf("%s exceeds %s limit (%d > %d)", field, limitKind(bytes), size, max)
		return
	}
	for _, r := range raw {
		if r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			v.failf("%s contains a control or invalid character", field)
			return
		}
	}
}

// checkShortString validates an identifier-like field (ids): short, printable.
func checkShortString(v *validation, field, raw string, max int) {
	if raw == "" {
		return
	}
	checkString(v, field, raw, max, false)
}

func limitKind(bytes bool) string {
	if bytes {
		return "byte"
	}
	return "rune"
}

func truncateLabel(raw string) string {
	runes := []rune(raw)
	if len(runes) > 32 {
		return string(runes[:32]) + "…"
	}
	return raw
}

// decodeItems decodes an items array of one component into out ([]T) with
// the count bound. A non-array payload is a validation error, not a
// render-time surprise. Decoding is strict (§14.3): unknown item fields are
// refused — custom per-item unmarshalers enforce the same rule inside.
func decodeItems(v *validation, c *Component, field string, out any, max int) int {
	if len(c.Items) == 0 {
		return 0
	}
	if !rawArray(c.Items) {
		v.failf("%s must be a JSON array", field)
		return 0
	}
	dec := json.NewDecoder(strings.NewReader(string(c.Items)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		v.failf("%s: %v", field, err)
		return 0
	}
	if dec.More() {
		v.failf("%s: unexpected trailing JSON content", field)
		return 0
	}
	items := reflectLen(out)
	if items > max {
		v.failf("%s holds %d entries, limit is %d", field, items, max)
		return items
	}
	return items
}

// decodeData decodes a component's data payload strictly.
func decodeData(v *validation, c *Component, out any) bool {
	if len(c.Data) == 0 {
		return false
	}
	if err := decodeStrict(c.Data, out); err != nil {
		v.failf("data: %v", err)
		return false
	}
	return true
}

// decodeProps decodes a component's props payload strictly.
func decodeProps(v *validation, c *Component, out any) bool {
	if len(c.Props) == 0 {
		return false
	}
	if err := decodeStrict(c.Props, out); err != nil {
		v.failf("props: %v", err)
		return false
	}
	return true
}

func reflectLen(v any) int {
	switch items := v.(type) {
	case *[]ListItem:
		return len(*items)
	case *[]KeyValueItem:
		return len(*items)
	case *[]KPIItem:
		return len(*items)
	case *[]ProgressItem:
		return len(*items)
	case *[]ComparisonItem:
		return len(*items)
	case *[]TimelineItem:
		return len(*items)
	case *[]AccordionItem:
		return len(*items)
	case *[]GalleryItem:
		return len(*items)
	case *[]string:
		return len(*items)
	default:
		return 0
	}
}

// jsonUnmarshalItems decodes an items array for the renderers, where a decode
// failure is impossible in practice (validation ran first) and must not break
// rendering anyway.
func jsonUnmarshalItems(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
