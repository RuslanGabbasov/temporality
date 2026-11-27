package llm

// Tool-call markup that leaked into the answer text (2026-11-27 incident):
// a provider whose serving stack does not parse the model's tool-call format
// returns the raw tags as plain content — `<tool_call><function=run_status></tool_call>`
// traveled all the way into a matrix chat as the run's final answer. The
// caller-facing contract of Completion is that ToolCalls carry the calls and
// Content carries user-visible prose, so this file restores that contract:
// markup blocks are pulled out of Content and returned as structured calls
// the same way the provider's own tool_calls field would have been. A call
// for a tool the harness does not know is rejected with an actionable tool
// message by the agent loop, so extraction degrades into recovery instead of
// into user-facing garbage.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

const (
	fenceMarker = "```"
	// maxTextToolCalls bounds a runaway response: parallel tool calls are
	// already discouraged on the wire (parallel_tool_calls=false), so more
	// than a handful of markup blocks in one completion is degenerate.
	maxTextToolCalls = 8
)

// rescueTextToolCalls rewrites a completion whose answer text carries
// tool-call markup: the blocks become structured ToolCalls and the text keeps
// only the prose. Completions with provider-side structured calls are left
// alone (extracting there could execute the same intent twice), as is text
// without any markup. Finish becomes "tool_calls" when the rescue produced
// the calls — the trace must not claim "stop" over a generation that asked
// for a tool.
func rescueTextToolCalls(completion Completion) Completion {
	if len(completion.ToolCalls) > 0 || !strings.Contains(completion.Content, "<") {
		return completion
	}
	clean, calls := extractTextToolCalls(completion.Content)
	if len(calls) == 0 && clean == completion.Content {
		return completion
	}
	completion.Content = clean
	completion.ToolCalls = calls
	if len(calls) > 0 && (completion.Finish == "" || completion.Finish == "stop") {
		completion.Finish = "tool_calls"
	}
	return completion
}

// extractTextToolCalls scans content for <tool_call>…</tool_call> and
// <function=name>…</function> blocks and returns the text with those blocks
// removed plus every call the blocks parsed into. Blocks inside fenced code
// (``` … ```) are documentation, not intent — they stay verbatim. A block
// that parses to no tool name keeps its inner text with the tags stripped:
// garbage markup must not reach the user, but neither must real prose
// surrounding it be lost.
func extractTextToolCalls(content string) (string, []ToolCall) {
	var clean strings.Builder
	var calls []ToolCall
	lower := strings.ToLower(content)
	pos := 0
	fences := 0 // parity of ``` markers seen so far
	for pos < len(content) && len(calls) < maxTextToolCalls {
		markup := nextMarkupTag(lower, pos)
		if markup.start < 0 {
			clean.WriteString(content[pos:])
			return strings.TrimSpace(clean.String()), calls
		}
		segment := content[pos:markup.start]
		fences += strings.Count(segment, fenceMarker)
		clean.WriteString(segment)
		if fences%2 == 1 {
			// Inside a fenced code block: legitimate documentation, keep the
			// tag verbatim and keep scanning after it.
			clean.WriteString(content[markup.start : markup.start+markup.length])
			pos = markup.start + markup.length
			continue
		}
		var body string
		if closeRel := strings.Index(lower[markup.start+markup.length:], markup.close); closeRel >= 0 {
			body = content[markup.start+markup.length : markup.start+markup.length+closeRel]
			pos = markup.start + markup.length + closeRel + len(markup.close)
		} else {
			// Unterminated block (truncated generation): everything to the end.
			body = content[markup.start+markup.length:]
			pos = len(content)
		}
		fences += strings.Count(body, fenceMarker)
		name, argsRaw, argsErr := parseMarkupBlock(content, markup, body)
		if name == "" {
			// Not a tool call: strip the tags, keep the payload prose.
			clean.WriteString(strings.TrimSpace(body))
			continue
		}
		args := map[string]any{}
		if argsRaw != "" && argsErr == "" {
			_ = json.Unmarshal([]byte(argsRaw), &args) // argsRaw was validated by the parser
		}
		calls = append(calls, ToolCall{
			ID:        textCallID(name, argsRaw, len(calls)),
			Name:      name,
			Args:      args,
			ArgsRaw:   argsRaw,
			ArgsError: argsErr,
		})
	}
	clean.WriteString(content[min(pos, len(content)):])
	return strings.TrimSpace(clean.String()), calls
}

type markupTag struct {
	start  int
	length int
	close  string
	// functionForm marks a <function=name> opener, whose name rides in the
	// tag attribute instead of the body.
	functionForm bool
}

// nextMarkupTag finds the nearest tool-call markup opener at or after from:
// <tool_call> (case-insensitive) or <function=name>.
func nextMarkupTag(lower string, from int) markupTag {
	tc := strings.Index(lower[from:], "<tool_call>")
	fn := strings.Index(lower[from:], "<function=")
	switch {
	case tc < 0 && fn < 0:
		return markupTag{start: -1}
	case fn < 0 || (tc >= 0 && tc < fn):
		start := from + tc
		return markupTag{start: start, length: len("<tool_call>"), close: "</tool_call>"}
	default:
		start := from + fn
		// The name ends at the first '>' — an attribute-style tag
		// (<function=run_status>); without one the tag is malformed and the
		// name falls back to the body's first line.
		length := len("<function=")
		if gt := strings.Index(lower[start:], ">"); gt >= 0 {
			length = gt + 1
		}
		return markupTag{start: start, length: length, close: "</function>", functionForm: true}
	}
}

// parseMarkupBlock extracts name/arguments from one markup block. A
// <tool_call> body may nest a <function=name> block (the exact incident
// shape), hold the OpenAI JSON form, the name-over-JSON line form, or a bare
// name; a <function=name> opener carries the name in the tag attribute and
// the arguments in the body.
func parseMarkupBlock(content string, markup markupTag, body string) (name, argsRaw, argsErr string) {
	if markup.functionForm {
		tag := content[markup.start : markup.start+markup.length]
		tagName := ""
		if strings.HasSuffix(tag, ">") {
			tagName = strings.TrimSpace(tag[len("<function=") : len(tag)-1])
		}
		if tagName == "" {
			// Malformed opener (no '>'): the body's first line is the name.
			return parseToolCallPayload(body)
		}
		name = strings.TrimSuffix(tagName, "/")
		if !isToolName(name) {
			return "", "", ""
		}
		argsRaw, argsErr = argsFromJSONBody(body)
		return name, argsRaw, argsErr
	}
	if fnRel := strings.Index(strings.ToLower(body), "<function="); fnRel >= 0 {
		return parseNestedFunction(body[fnRel:])
	}
	return parseToolCallPayload(body)
}

// parseNestedFunction handles a <function=name>…</function> block that sat
// inside a <tool_call> body. The name is the tag attribute; the body up to
// </function> (or the end — the incident shape closed only the outer tag)
// holds the arguments.
func parseNestedFunction(block string) (name, argsRaw, argsErr string) {
	gt := strings.Index(block, ">")
	if gt < 0 {
		// `<function=name` with neither '>' nor close: the rest is the name.
		name = strings.TrimSpace(block[len("<function="):])
		if isToolName(name) {
			return name, "", ""
		}
		return "", "", ""
	}
	name = strings.TrimSpace(strings.TrimSuffix(block[len("<function="):gt], "/"))
	if !isToolName(name) {
		return "", "", ""
	}
	rest := block[gt+1:]
	if closeRel := strings.Index(strings.ToLower(rest), "</function>"); closeRel >= 0 {
		rest = rest[:closeRel]
	}
	argsRaw, argsErr = argsFromJSONBody(rest)
	return name, argsRaw, argsErr
}

// parseToolCallPayload accepts the textual shapes models actually emit inside
// <tool_call> blocks: a JSON object ({"name":…,"arguments":…}, the OpenAI
// wire shape {"function":{"name":…,"arguments":"…"}}), a name over a JSON
// body («run_status\n{"limit":5}»), or a bare name.
func parseToolCallPayload(body string) (name, argsRaw, argsErr string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", ""
	}
	if strings.HasPrefix(body, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(body), &obj); err != nil || obj == nil {
			return "", "", ""
		}
		if n, ok := obj["name"].(string); ok && n != "" {
			name = n
		} else {
			switch fn := obj["function"].(type) {
			case map[string]any:
				if n, ok := fn["name"].(string); ok {
					name = n
					obj = fn
				}
			case string:
				name = fn
			}
		}
		if name == "" {
			return "", "", ""
		}
		return nameWithArguments(name, obj)
	}
	// Line form: the name up to the first newline, optional JSON body after.
	line := body
	rest := ""
	if nl := strings.IndexAny(body, "\n\r"); nl >= 0 {
		line, rest = body[:nl], strings.TrimSpace(body[nl+1:])
	}
	line = strings.TrimSpace(line)
	if !isToolName(line) {
		return "", "", ""
	}
	if rest == "" {
		return line, "", ""
	}
	argsRaw, argsErr = argsFromJSONBody(rest)
	return line, argsRaw, argsErr
}

// argsFromJSONBody validates a block's argument payload: a JSON object (or
// nothing) travels as ArgsRaw the history replay sends back to the provider;
// anything else is carried with an ArgsError so the agent loop rejects the
// call and asks the model to re-issue it.
func argsFromJSONBody(body string) (argsRaw, argsErr string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	if !strings.HasPrefix(body, "{") {
		return body, "arguments are not a JSON object"
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(body), &probe); err != nil || probe == nil {
		return body, "arguments are not a JSON object"
	}
	return body, ""
}

// nameWithArguments pulls the arguments payload out of a decoded JSON object:
// the first recognized key wins. Object arguments re-marshal to the compact
// canonical form; string arguments must themselves be JSON objects.
func nameWithArguments(name string, obj map[string]any) (string, string, string) {
	for _, key := range []string{"arguments", "args", "parameters", "input"} {
		value, ok := obj[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case nil:
			return name, "", ""
		case map[string]any:
			raw, err := json.Marshal(typed)
			if err != nil {
				return name, "", "arguments failed to re-encode"
			}
			return name, string(raw), ""
		case string:
			var probe map[string]any
			if err := json.Unmarshal([]byte(typed), &probe); err != nil || probe == nil {
				return name, typed, "arguments are not a JSON object"
			}
			return name, typed, ""
		default:
			return name, "", "arguments must be a JSON object"
		}
	}
	return name, "", ""
}

// isToolName matches the identifier shape builtin and MCP tool names use
// (run_status, mcp__penpot__export_shape).
func isToolName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// textCallID mints a stable id for an extracted call: providers id their
// structured calls, text markup carries none, and the agent loop links tool
// replies to calls by id.
func textCallID(name, argsRaw string, sequence int) string {
	digest := sha256.Sum256([]byte(strconv.Itoa(sequence) + "\x00" + name + "\x00" + argsRaw))
	return "textcall_" + hex.EncodeToString(digest[:8])
}
