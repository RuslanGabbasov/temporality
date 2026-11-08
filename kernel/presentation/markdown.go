package presentation

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Markdown compilation (docs/presentation-engine.md × docs/artifact-registry.md):
// an artifact keeps its readable source — markdown with presentation fences —
// and the compiled form (fences replaced by embedded PNG data-URI images) is
// generated deterministically wherever the document leaves the system: the
// export/download endpoint, send_file delivery to messengers. One source of
// truth, diffable versions, and a portable single file on the way out.
//
// A fence is a line starting exactly with ```presentation (optionally with a
// trailing info string) and closed by a bare ``` line. The payload is either
// a full Presentation IR document or a reference {"ref": "presentation-id"}
// resolved through the resolver — the way an agent includes a visual it
// already published mid-run.

// FenceResolver resolves a {"ref": id} fence payload into the referenced
// presentation's raw IR. The caller wires it to the presentation store (API
// path) or the workspace HTTP API (activity path); nil resolver makes every
// ref fence a validation error.
type FenceResolver func(id string) (json.RawMessage, error)

// markdownFence is one located presentation fence in a source document.
type markdownFence struct {
	// startLine/endLine are 0-based inclusive indexes of the ``` lines.
	startLine, endLine int
	// payload is the raw text between the fence lines.
	payload string
	// ref, when non-empty, marks a reference fence {"ref": id}.
	ref string
}

// scanMarkdownFences locates presentation fences in document order. Unterminated
// fences are an error: a half-written visual must not silently render as text.
func scanMarkdownFences(source []byte) ([]markdownFence, error) {
	lines := strings.Split(string(source), "\n")
	fences := make([]markdownFence, 0, 4)
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```presentation") {
			continue
		}
		start := i
		payloadStart := i + 1
		end := -1
		for j := payloadStart; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "```") {
				end = j
				break
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("presentation fence at line %d is not closed with ```", start+1)
		}
		payload := strings.TrimSpace(strings.Join(lines[payloadStart:end], "\n"))
		if payload != "" {
			ref := refOfFence(payload)
			fences = append(fences, markdownFence{startLine: start, endLine: end, payload: payload, ref: ref})
		}
		i = end
	}
	return fences, nil
}

// refOfFence reports the referenced presentation id when the payload is a
// reference object {"ref": id} rather than a full IR document. A full IR
// carries type/version; a reference carries ref (and nothing else required).
func refOfFence(payload string) string {
	var probe struct {
		Ref   string `json:"ref"`
		Type  string `json:"type"`
		Items string `json:"-"`
	}
	if err := decodeStrict(json.RawMessage(payload), &probe); err != nil {
		return ""
	}
	return probe.Ref
}

// ValidateMarkdownFences validates every presentation fence of a markdown
// document without compiling: full-IR fences go through the validator, ref
// fences are checked structurally (resolution happens at compile time, where
// the store is reachable). The error names fence line numbers and IR paths —
// the same contract as Validate.
func ValidateMarkdownFences(source []byte, policy Policy) error {
	fences, err := scanMarkdownFences(source)
	if err != nil {
		return err
	}
	var problems []string
	for i, fence := range fences {
		if fence.ref != "" {
			continue // structural check only; the ref resolves where the store lives
		}
		if _, err := Validate([]byte(fence.payload), policy); err != nil {
			problems = append(problems, fmt.Sprintf("fence #%d (line %d): %v", i+1, fence.startLine+1, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("presentation fences are invalid:\n%s", strings.Join(problems, "\n"))
	}
	return nil
}

// CompileMarkdown replaces every presentation fence with an embedded PNG
// data-URI image, producing a self-contained markdown file that standalone
// viewers (VS Code, Obsidian, GitHub) render with the visuals in place.
// Ref fences resolve through the resolver before rendering. The returned
// count is the number of embedded images (0 = the source passed through
// unchanged).
func CompileMarkdown(source []byte, policy Policy, resolve FenceResolver) ([]byte, int, error) {
	fences, err := scanMarkdownFences(source)
	if err != nil {
		return nil, 0, err
	}
	if len(fences) == 0 {
		return source, 0, nil
	}

	// Collect problems first: one pass reports every broken fence instead of
	// the first, so the author fixes the document in one round trip.
	var problems []string
	type rendered struct {
		line  int
		image []byte
		title string
	}
	renders := make([]rendered, 0, len(fences))
	for i, fence := range fences {
		raw := json.RawMessage(fence.payload)
		if fence.ref != "" {
			if resolve == nil {
				problems = append(problems, fmt.Sprintf("fence #%d (line %d): ref %q cannot resolve here (no resolver)", i+1, fence.startLine+1, fence.ref))
				continue
			}
			resolved, rerr := resolve(fence.ref)
			if rerr != nil {
				problems = append(problems, fmt.Sprintf("fence #%d (line %d): ref %q: %v", i+1, fence.startLine+1, fence.ref, rerr))
				continue
			}
			raw = resolved
		}
		doc, verr := Validate(raw, policy)
		if verr != nil {
			problems = append(problems, fmt.Sprintf("fence #%d (line %d): %v", i+1, fence.startLine+1, verr))
			continue
		}
		drawing := LayoutDocument(doc, LayoutOptions{IncludeTitle: true, Width: 640})
		png, perr := RenderPNG(drawing)
		if perr != nil {
			problems = append(problems, fmt.Sprintf("fence #%d (line %d): render failed: %v", i+1, fence.startLine+1, perr))
			continue
		}
		title := doc.Title
		if title == "" {
			title = "Presentation"
		}
		renders = append(renders, rendered{line: fence.startLine, image: png, title: title})
	}
	if len(problems) > 0 {
		return nil, 0, fmt.Errorf("presentation fences are invalid:\n%s", strings.Join(problems, "\n"))
	}

	// Replace fence spans bottom-up so earlier offsets stay valid.
	lines := strings.Split(string(source), "\n")
	for i := len(renders) - 1; i >= 0; i-- {
		r := renders[i]
		fence := fences[i]
		encoded := base64.StdEncoding.EncodeToString(r.image)
		lines[fence.startLine] = fmt.Sprintf("![%s](data:image/png;base64,%s)", escapeMarkdownAlt(r.title), encoded)
		for j := fence.startLine + 1; j <= fence.endLine; j++ {
			lines[j] = "\x00delete"
		}
	}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "\x00delete" {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n")), len(renders), nil
}

// escapeMarkdownAlt keeps the alt text inside ![...]: brackets and parens
// would break the inline image syntax, so they are stripped outright.
func escapeMarkdownAlt(title string) string {
	replacer := strings.NewReplacer("[", "", "]", "", "(", "", ")", "", "\n", " ")
	return replacer.Replace(title)
}

// CompiledSizeCap bounds a compiled document: honest reports stay well under
// it, a runaway document (hundreds of charts) must fail at compile time
// rather than choke a channel upload.
const CompiledSizeCap = 8 << 20

// CompileMarkdownCapped is CompileMarkdown with the compiled-size guard —
// the entry point channel deliveries and downloads use.
func CompileMarkdownCapped(source []byte, policy Policy, resolve FenceResolver) ([]byte, int, error) {
	compiled, images, err := CompileMarkdown(source, policy, resolve)
	if err != nil {
		return nil, 0, err
	}
	if images > 0 && len(compiled) > CompiledSizeCap {
		return nil, 0, fmt.Errorf("compiled document exceeds %d bytes (%d embedded images)", CompiledSizeCap, images)
	}
	return compiled, images, nil
}
