package agent

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Telegram outbound formatting (docs/channels.md §13 — the per-driver
// renderer that doc left as an open question): channel-neutral markdown
// (agent answers, notification bodies) becomes the Telegram Bot API HTML
// subset (parse_mode=HTML), and bodies beyond the 4096-character message
// limit split into deliverable chunks at paragraph boundaries.
//
// The goldmark pipeline is shared with the matrix driver (same GFM
// extensions, same escaping — model output never injects raw markup); only
// the tag mapping differs, because Telegram accepts a much smaller tag set
// than a browser: <b> <i> <s> <u> <code> <pre><code class="language-x">
// <a href> <blockquote>. Tables do not exist in text messages — they
// collapse to text rows. Everything else the GFM renderer emits is dropped
// or reduced to text, never passed through blind.

// telegramHTMLLimit is the safe per-message character budget: below the
// 4096 API limit, leaving room for the markup tags the renderer adds.
const telegramHTMLLimit = 3800

var telegramTagStrip = regexp.MustCompile(`<[^>]*>`)

// telegramHTML renders markdown into one Telegram-HTML fragment. An empty
// result (nothing to render) means the caller should ship the plain text.
func telegramHTML(markdown string) string {
	source := matrixFormattedHTML(markdown)
	if source == "" {
		return ""
	}
	source = telegramFlattenTables(source)
	var b strings.Builder
	listDepth, ordered := 0, 0
	for i := 0; i < len(source); {
		if source[i] != '<' {
			b.WriteByte(source[i])
			i++
			continue
		}
		end := strings.IndexByte(source[i:], '>')
		if end < 0 {
			b.WriteString(source[i:])
			break
		}
		tag := source[i : i+end+1]
		i += end + 1
		name, closing := telegramTagName(tag)
		switch name {
		case "p":
			if closing {
				b.WriteString("\n")
			}
		case "br":
			b.WriteString("\n")
		case "h1", "h2", "h3", "h4", "h5", "h6", "strong":
			telegramMappedTag(&b, closing, "b")
		case "em":
			telegramMappedTag(&b, closing, "i")
		case "del", "s", "strike":
			telegramMappedTag(&b, closing, "s")
		case "ul":
			// bullets carry no wrapper in Telegram HTML
		case "ol":
			if closing {
				listDepth--
			} else {
				listDepth++
				ordered = 0
			}
		case "li":
			if !closing {
				if listDepth > 0 {
					ordered++
					b.WriteString(strconv.Itoa(ordered) + ". ")
				} else {
					b.WriteString("• ")
				}
			}
		case "input":
			if strings.Contains(tag, "checked") {
				b.WriteString("☑ ")
			} else {
				b.WriteString("☐ ")
			}
		case "hr":
			b.WriteString("—")
		case "code", "pre", "a", "blockquote", "b", "u":
			b.WriteString(tag) // supported verbatim (goldmark emits only href/class attrs)
		default:
			// unknown tags (span, figure…) drop; their text already flowed
		}
	}
	return strings.TrimSpace(telegramCollapseBlankLines(b.String()))
}

// telegramMappedTag writes an opening/closing pair mapped onto the Telegram
// equivalent tag (the source tag itself is discarded — Telegram only accepts
// its own set, so <strong> becomes <b> and <h2> a bold line).
func telegramMappedTag(b *strings.Builder, closing bool, telegramTag string) {
	if closing {
		b.WriteString("</" + telegramTag + ">")
		return
	}
	b.WriteString("<" + telegramTag + ">")
}

// telegramTagName parses one raw HTML tag into its lowercase name and whether
// it is a closing tag.
func telegramTagName(tag string) (string, bool) {
	body := strings.Trim(tag, "<>")
	closing := strings.HasPrefix(body, "/")
	if closing {
		body = body[1:]
	}
	if idx := strings.IndexAny(body, " \t\n\r/>"); idx >= 0 {
		body = body[:idx]
	}
	return strings.ToLower(body), closing
}

// telegramFlattenTables replaces every <table>…</table> block with text rows:
// header cells bolded, cells joined with " | ", one row per line. Goldmark's
// output for our own renderer is well-formed, so the string scan is safe.
func telegramFlattenTables(source string) string {
	if !strings.Contains(source, "<table") {
		return source
	}
	var out strings.Builder
	for {
		start := strings.Index(source, "<table")
		if start < 0 {
			out.WriteString(source)
			break
		}
		end := strings.Index(source[start:], "</table>")
		if end < 0 {
			out.WriteString(source) // malformed: leave as-is, the scanner drops the tag
			break
		}
		end += start + len("</table>")
		out.WriteString(source[:start])
		out.WriteString(telegramTableRows(source[start:end]))
		source = source[end:]
	}
	return out.String()
}

// telegramTableRows renders one full <table> block into text rows.
func telegramTableRows(block string) string {
	var rows []string
	for _, rowSrc := range strings.Split(block, "<tr>")[1:] {
		isHeader := strings.Contains(rowSrc, "<th")
		sep := "</td>"
		if isHeader {
			sep = "</th>"
		}
		parts := strings.Split(rowSrc, sep)
		if len(parts) > 0 {
			parts = parts[:len(parts)-1] // the tail after the last closing cell
		}
		var cells []string
		for _, cell := range parts {
			text := strings.TrimSpace(telegramCollapseBlankLines(telegramTagStrip.ReplaceAllString(cell, "")))
			if text == "" {
				continue
			}
			cells = append(cells, strings.ReplaceAll(text, "\n", " "))
		}
		if len(cells) == 0 {
			continue
		}
		line := strings.Join(cells, " | ")
		if isHeader {
			line = "<b>" + line + "</b>"
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

// telegramCollapseBlankLines caps consecutive blank lines at one — Telegram
// renders three newlines as a wall of whitespace.
func telegramCollapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// telegramChunks splits markdown into chunks that each render within one
// Telegram message, cutting at paragraph boundaries and never inside a fenced
// code block (a split ``` block renders as a broken <pre>). An oversized
// single paragraph (a huge fence, a wall of text) falls back to line
// boundaries, then to a hard rune-boundary cut.
func telegramChunks(markdown string) []string {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	var chunks []string
	current := ""
	flush := func() {
		if strings.TrimSpace(current) != "" {
			chunks = append(chunks, strings.TrimSpace(current))
		}
		current = ""
	}
	join := func(para string) string {
		if current == "" {
			return para
		}
		return current + "\n\n" + para
	}
	inFence := false
	for _, para := range strings.Split(markdown, "\n\n") {
		if strings.TrimSpace(para) == "" {
			continue
		}
		opens := telegramFenceDelta(para)
		if inFence || opens > 0 {
			// Fence content must travel with its opener: buffer greedily,
			// flush when the joined chunk would overflow.
			for inFence || opens > 0 {
				if len([]rune(telegramHTML(join(para)))) > telegramHTMLLimit && current != "" {
					flush()
					continue
				}
				break
			}
			if len([]rune(telegramHTML(join(para)))) <= telegramHTMLLimit {
				current = join(para)
			} else {
				// The fence alone exceeds one message: line-split it.
				for _, piece := range telegramSplitByLines(para) {
					if len([]rune(telegramHTML(join(piece)))) > telegramHTMLLimit {
						flush()
					}
					current = join(piece)
				}
			}
			inFence = telegramFenceState(inFence, para)
			continue
		}
		if len([]rune(telegramHTML(join(para)))) <= telegramHTMLLimit {
			current = join(para)
			continue
		}
		flush()
		if len([]rune(telegramHTML(para))) <= telegramHTMLLimit {
			current = para
			continue
		}
		for _, piece := range telegramSplitByLines(para) {
			if len([]rune(telegramHTML(join(piece)))) > telegramHTMLLimit {
				flush()
			}
			current = join(piece)
		}
	}
	flush()
	return chunks
}

// telegramSplitByLines breaks one oversized paragraph into renderable line
// groups; a single line longer than the budget hard-cuts on a rune boundary.
func telegramSplitByLines(para string) []string {
	var pieces []string
	current := ""
	join := func(line string) string {
		if current == "" {
			return line
		}
		return current + "\n" + line
	}
	for _, line := range strings.Split(para, "\n") {
		if len([]rune(telegramHTML(join(line)))) <= telegramHTMLLimit {
			current = join(line)
			continue
		}
		if current != "" {
			pieces = append(pieces, current)
			current = ""
		}
		for utf8.RuneCountInString(line) > telegramHTMLLimit {
			cut := telegramRuneCut(line, telegramHTMLLimit)
			pieces = append(pieces, strings.TrimSpace(line[:cut]))
			line = strings.TrimSpace(line[cut:])
		}
		current = line
	}
	if strings.TrimSpace(current) != "" {
		pieces = append(pieces, strings.TrimSpace(current))
	}
	return pieces
}

// telegramRuneCut returns the byte offset of the rune limit that also prefers
// a whitespace boundary.
func telegramRuneCut(s string, limit int) int {
	runes := []rune(s)
	if len(runes) <= limit {
		return len(s)
	}
	cut := len(string(runes[:limit]))
	if idx := strings.LastIndexByte(s[:cut], '\n'); idx > 0 {
		return idx + 1
	}
	if idx := strings.LastIndexByte(s[:cut], ' '); idx > 0 {
		return idx + 1
	}
	return cut
}

// telegramFenceDelta counts fence markers in one paragraph (a ``` block
// toggles state once per marker line).
func telegramFenceDelta(para string) int {
	delta := 0
	for _, line := range strings.Split(para, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			delta++
		}
	}
	return delta
}

// telegramFenceState reports the fence state after one paragraph.
func telegramFenceState(inFence bool, para string) bool {
	return inFence != (telegramFenceDelta(para)%2 == 1)
}
